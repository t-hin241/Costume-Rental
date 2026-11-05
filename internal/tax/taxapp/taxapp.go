// Package taxapp runs the revenue and reconciliation use cases of decision
// D41: the tax profile (threshold and warning marks), the revenue ledger
// filled from ended bookings and from other channels, the threshold watch,
// and the revenue, cash-flow and reconciliation reports of tax-policy §10.
// None of it computes tax.
package taxapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"webee_rentor/internal/audit"
	"webee_rentor/internal/audit/auditpg"
	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
	"webee_rentor/internal/payments"
	"webee_rentor/internal/payments/paymentspg"
	"webee_rentor/internal/platform/ids"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalspg"
	"webee_rentor/internal/tax"
	"webee_rentor/internal/tax/taxpg"
)

const (
	// maxDayRange and maxRange bound a report period; a report by day spans
	// at most a year.
	maxDayRange = 366
	maxRange    = 3660
	// maxExport is the most entries one CSV export holds.
	maxExport = 50_000
	// listed is how many rows a reconciliation section shows; its total is
	// always given.
	listed = 50
)

// Service holds the dependencies of the revenue use cases.
type Service struct {
	pool  *pgxpool.Pool
	loc   *time.Location
	now   func() time.Time
	newID func() string
}

// Option configures the service.
type Option func(*Service)

// WithClock replaces time.Now, for tests.
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// New builds the service. loc is the business timezone.
func New(pool *pgxpool.Pool, loc *time.Location, opts ...Option) *Service {
	s := &Service{pool: pool, loc: loc, now: time.Now, newID: ids.New}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Location is the business timezone reports are read in.
func (s *Service) Location() *time.Location { return s.loc }

// SyncBooking brings the revenue of an ended booking in line with its ledger,
// inside the caller's transaction, which holds the booking lock. A booking
// still open has no revenue yet. It returns how many entries were added.
func SyncBooking(ctx context.Context, q postgres.Querier, b *rentals.Booking, now time.Time) (int, error) {
	ended := endedAt(b)
	if ended == nil {
		return 0, nil
	}
	l, _, err := paymentspg.LoadLedger(ctx, q, string(b.ID), false)
	if err != nil {
		return 0, err
	}
	h, err := taxpg.LoadHistory(ctx, q, false)
	if err != nil {
		return 0, err
	}
	existing, err := taxpg.EntriesOfBooking(ctx, q, b.ID)
	if err != nil {
		return 0, err
	}
	es, err := tax.SyncBooking(tax.BookingRevenue{Ledger: l, EndedAt: *ended, RentList: b.GrossRentTotal, Existing: existing}, h)
	if err != nil {
		return 0, fmt.Errorf("revenue of booking %s: %w", b.ID, err)
	}
	return taxpg.InsertEntries(ctx, q, es, now)
}

func endedAt(b *rentals.Booking) *time.Time {
	switch b.Status {
	case rentals.StatusCompleted:
		return b.CompletedAt
	case rentals.StatusCancelled:
		return b.CancelledAt
	case rentals.StatusExpired:
		return b.ExpiredAt
	}
	return nil
}

// RecognizePending catches up up to batch ended bookings whose revenue is
// behind their ledger: those that ended before the revenue ledger existed or
// before a tax profile covered them, and any change a transaction missed.
// Each booking is synced in its own transaction under its lock. It returns
// how many bookings gained entries.
func (s *Service) RecognizePending(ctx context.Context, batch int) (int, error) {
	due, _, err := taxpg.PendingBookings(ctx, s.pool, true, batch)
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, d := range due {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		added := 0
		err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			b, _, err := rentalspg.LoadBooking(ctx, tx, d.ID, true)
			if err != nil {
				return err
			}
			added, err = SyncBooking(ctx, tx, b, s.now().UTC())
			return err
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("recognise revenue of booking %s: %w", d.ID, err))
			continue
		}
		if added > 0 {
			n++
		}
	}
	return n, errors.Join(errs...)
}

// Profiles reads the tax profile history.
func (s *Service) Profiles(ctx context.Context, p identity.Principal) (tax.History, error) {
	if !p.Can(identity.PermTaxManage) {
		if err := p.Require(identity.PermRevenueRead); err != nil {
			return tax.History{}, err
		}
	}
	return taxpg.LoadHistory(ctx, s.pool, false)
}

// ProfileInput is a new tax profile version.
type ProfileInput struct {
	Version                string
	AnnualRevenueThreshold money.Money
	ThresholdEffectiveFrom businesstime.Date
	WarningPercents        []int
	// EffectiveFrom is the business day the version applies from.
	EffectiveFrom businesstime.Date
}

// AddProfile stores a new tax profile version (tax.manage) and closes the one
// it replaces, with an audit record.
func (s *Service) AddProfile(ctx context.Context, p identity.Principal, in ProfileInput) (tax.Profile, error) {
	if err := p.Require(identity.PermTaxManage); err != nil {
		return tax.Profile{}, err
	}
	if !in.EffectiveFrom.Valid() {
		return tax.Profile{}, domainerr.Invalid("effective_from", "required")
	}
	prof := tax.Profile{
		Version: in.Version, TaxpayerType: tax.IndividualBusiness, AnnualRevenueThreshold: in.AnnualRevenueThreshold,
		ThresholdEffectiveFrom: in.ThresholdEffectiveFrom, WarningPercents: in.WarningPercents,
		EffectiveFrom: businesstime.At(in.EffectiveFrom, businesstime.TimeOfDay{}, s.loc),
	}
	err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		h, err := taxpg.LoadHistory(ctx, tx, true)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		// A version may start today: its day began before now.
		closed, err := h.Add(p, prof, startOfDay(now, s.loc))
		if err != nil {
			return err
		}
		if closed != nil {
			if err := taxpg.CloseProfile(ctx, tx, *closed); err != nil {
				return err
			}
		}
		if err := taxpg.InsertProfile(ctx, tx, prof, now); err != nil {
			return err
		}
		refs := map[string]string{
			"effective_from": in.EffectiveFrom.String(), "threshold": prof.AnnualRevenueThreshold.String(),
			"threshold_effective_from": in.ThresholdEffectiveFrom.String(), "warning_percents": fmt.Sprint(prof.WarningPercents),
		}
		if closed != nil {
			refs["closes"] = closed.Version
		}
		return auditpg.Append(ctx, tx, audit.Event{
			ID: s.newID(), OccurredAt: now, Actor: p.Actor(), Action: "tax.profile_added", ResourceType: "tax_profile",
			ResourceID: prof.Version, Result: audit.ResultSuccess, References: refs,
		})
	})
	if err != nil {
		return tax.Profile{}, fmt.Errorf("add tax profile %s: %w", in.Version, err)
	}
	return prof, nil
}

func startOfDay(t time.Time, loc *time.Location) time.Time {
	return businesstime.At(businesstime.DateOf(t, loc), businesstime.TimeOfDay{}, loc)
}

// YearView is the threshold watch of a calendar year.
type YearView struct {
	Year int
	// Summary is nil while no profile covers the year.
	Summary *tax.YearSummary
	// Pending counts ended bookings whose revenue is not recognised yet,
	// whatever their year.
	Pending int
}

// Year summarises the revenue of a calendar year (zero: the current one)
// against the threshold of the profile effective now, or at the end of a past
// year.
func (s *Service) Year(ctx context.Context, p identity.Principal, year int) (YearView, error) {
	if err := p.Require(identity.PermRevenueRead); err != nil {
		return YearView{}, err
	}
	if year == 0 {
		year = businesstime.DateOf(s.now(), s.loc).Year
	}
	if year < 2000 || year > 2100 {
		return YearView{}, domainerr.Invalid("year", "must be between 2000 and 2100")
	}
	start := businesstime.At(businesstime.Date{Year: year, Month: 1, Day: 1}, businesstime.TimeOfDay{}, s.loc)
	end := businesstime.At(businesstime.Date{Year: year + 1, Month: 1, Day: 1}, businesstime.TimeOfDay{}, s.loc)
	_, pending, err := taxpg.PendingBookings(ctx, s.pool, false, 1)
	if err != nil {
		return YearView{}, err
	}
	view := YearView{Year: year, Pending: pending}
	h, err := taxpg.LoadHistory(ctx, s.pool, false)
	if err != nil {
		return YearView{}, err
	}
	at := s.now().UTC()
	if !at.Before(end) {
		at = end.Add(-time.Nanosecond)
	}
	prof, err := h.At(at)
	if errors.Is(err, tax.ErrNoProfile) {
		return view, nil
	}
	if err != nil {
		return YearView{}, err
	}
	es, err := taxpg.EntriesBetween(ctx, s.pool, start, end)
	if err != nil {
		return YearView{}, err
	}
	sum := tax.Summarize(es, prof, year, s.loc)
	view.Summary = &sum
	return view, nil
}

// Period is a span of business days, both included.
type Period struct {
	From, To businesstime.Date
}

func (s *Service) bounds(per Period, maxDays int) (time.Time, time.Time, error) {
	if !per.From.Valid() || !per.To.Valid() {
		return time.Time{}, time.Time{}, domainerr.Invalid("from", "from and to are required dates")
	}
	start := businesstime.At(per.From, businesstime.TimeOfDay{}, s.loc)
	end := businesstime.At(per.To, businesstime.TimeOfDay{}, s.loc).AddDate(0, 0, 1)
	switch {
	case !end.After(start):
		return time.Time{}, time.Time{}, domainerr.Invalid("to", "must not be before from")
	case end.Sub(start) > time.Duration(maxDays+1)*24*time.Hour:
		return time.Time{}, time.Time{}, domainerr.Invalid("to", fmt.Sprintf("the period spans at most %d days", maxDays))
	}
	return start, end, nil
}

// Report sums recognised revenue by period and category, before and after
// discount (tax-policy §10.1).
func (s *Service) Report(ctx context.Context, p identity.Principal, per Period, g taxpg.Group) ([]taxpg.ReportRow, error) {
	if err := p.Require(identity.PermRevenueRead); err != nil {
		return nil, err
	}
	limit := maxRange
	if g == taxpg.GroupDay {
		limit = maxDayRange
	}
	start, end, err := s.bounds(per, limit)
	if err != nil {
		return nil, err
	}
	return taxpg.Report(ctx, s.pool, start, end, g, s.loc)
}

// EntryQuery selects revenue entries.
type EntryQuery struct {
	Period
	Category tax.Category
	Source   tax.Source
	Booking  string
}

func (s *Service) entryFilter(q EntryQuery) (taxpg.EntryFilter, error) {
	start, end, err := s.bounds(q.Period, maxRange)
	if err != nil {
		return taxpg.EntryFilter{}, err
	}
	switch q.Source {
	case "", tax.SourceBooking, tax.SourceManual:
	default:
		return taxpg.EntryFilter{}, domainerr.Invalid("source", "must be BOOKING or MANUAL")
	}
	return taxpg.EntryFilter{From: start, To: end, Category: q.Category, Source: q.Source, Booking: q.Booking}, nil
}

// Entries lists revenue entries, newest first: revenue by booking and the
// manual entries.
func (s *Service) Entries(ctx context.Context, p identity.Principal, q EntryQuery, page, size int) ([]taxpg.EntryRow, int, error) {
	if err := p.Require(identity.PermRevenueRead); err != nil {
		return nil, 0, err
	}
	f, err := s.entryFilter(q)
	if err != nil {
		return nil, 0, err
	}
	return taxpg.EntryPage(ctx, s.pool, f, page, size)
}

// ExportEntries hands every entry of the query to fn, oldest first, for an
// export. The export itself is recorded in the audit log.
func (s *Service) ExportEntries(ctx context.Context, p identity.Principal, q EntryQuery, fn func(taxpg.EntryRow) error) error {
	if err := p.Require(identity.PermRevenueRead); err != nil {
		return err
	}
	f, err := s.entryFilter(q)
	if err != nil {
		return err
	}
	if err := auditpg.Append(ctx, s.pool, audit.Event{
		ID: s.newID(), OccurredAt: s.now().UTC(), Actor: p.Actor(), Action: "revenue.exported", ResourceType: "revenue_ledger",
		ResourceID: q.From.String() + ".." + q.To.String(), Result: audit.ResultSuccess,
	}); err != nil {
		return err
	}
	return taxpg.EachEntry(ctx, s.pool, f, maxExport, fn)
}

// AddManualEntry records revenue earned outside the website (revenue.record).
func (s *Service) AddManualEntry(ctx context.Context, p identity.Principal, in tax.ManualInput) (tax.RevenueEntry, error) {
	if err := p.Require(identity.PermRevenueRecord); err != nil {
		return tax.RevenueEntry{}, err
	}
	var out tax.RevenueEntry
	err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		h, err := taxpg.LoadHistory(ctx, tx, false)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		in.ID = tax.EntryID(s.newID())
		e, err := tax.NewManualEntry(p, in, h, s.loc, now)
		if err != nil {
			return err
		}
		if _, err := taxpg.InsertEntries(ctx, tx, []tax.RevenueEntry{e}, now); err != nil {
			return err
		}
		out = e
		return auditpg.Append(ctx, tx, audit.Event{
			ID: s.newID(), OccurredAt: now, Actor: p.Actor(), Action: "revenue.manual_recorded", ResourceType: "revenue_entry",
			ResourceID: string(e.ID), Result: audit.ResultSuccess,
			References: map[string]string{"amount": e.Net.String(), "category": string(e.Category), "date": in.Date.String(), "channel": e.Channel},
		})
	})
	if err != nil {
		return tax.RevenueEntry{}, fmt.Errorf("record manual revenue: %w", err)
	}
	return out, nil
}

// ReverseEntry cancels a manual entry with a reason (revenue.record).
func (s *Service) ReverseEntry(ctx context.Context, p identity.Principal, id tax.EntryID, reason string) (tax.RevenueEntry, error) {
	if err := p.Require(identity.PermRevenueRecord); err != nil {
		return tax.RevenueEntry{}, err
	}
	var out tax.RevenueEntry
	err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		e, reversed, err := taxpg.Entry(ctx, tx, id)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		r, err := tax.ReverseManual(p, e, reversed, tax.EntryID(s.newID()), reason, now)
		if err != nil {
			return err
		}
		n, err := taxpg.InsertEntries(ctx, tx, []tax.RevenueEntry{r}, now)
		if err != nil {
			return err
		}
		if n == 0 {
			return tax.ErrAlreadyReversed
		}
		out = r
		return auditpg.Append(ctx, tx, audit.Event{
			ID: s.newID(), OccurredAt: now, Actor: p.Actor(), Action: "revenue.manual_reversed", ResourceType: "revenue_entry",
			ResourceID: string(e.ID), Result: audit.ResultSuccess, Reason: r.Reason,
			References: map[string]string{"amount": e.Net.String(), "reversal": string(r.ID)},
		})
	})
	if err != nil {
		return tax.RevenueEntry{}, fmt.Errorf("reverse revenue entry %s: %w", id, err)
	}
	return out, nil
}

// Position is the money held or owed right now, over every ledger
// (tax-policy §10.2).
type Position struct {
	SecurityHeld    money.Money
	ReservationHeld money.Money
	// OwedEnded is rent and fees still unpaid on bookings that ended: what
	// customers owe. DueOpen is what open bookings have still to pay.
	OwedEnded       money.Money
	OwedEndedCount  int
	DueOpen         money.Money
	PendingRefunds  money.Money
	PendingPayments money.Money
	// Overpaid is money received above what bookings owe, to refund.
	Overpaid money.Money
}

// CashFlow is the money that moved in a period and the position now.
type CashFlow struct {
	Received []paymentspg.Movement
	Refunded []paymentspg.Movement
	Position Position
}

// CashFlow reads money received and refunded in a period by method, and the
// money held or owed now.
func (s *Service) CashFlow(ctx context.Context, p identity.Principal, per Period) (CashFlow, error) {
	if err := p.Require(identity.PermRevenueRead); err != nil {
		return CashFlow{}, err
	}
	start, end, err := s.bounds(per, maxRange)
	if err != nil {
		return CashFlow{}, err
	}
	var out CashFlow
	if out.Received, out.Refunded, err = paymentspg.CashMovements(ctx, s.pool, start, end); err != nil {
		return CashFlow{}, err
	}
	totals, err := paymentspg.Totals(ctx, s.pool, nil)
	if err != nil {
		return CashFlow{}, err
	}
	statuses, err := rentalspg.Statuses(ctx, s.pool)
	if err != nil {
		return CashFlow{}, err
	}
	var pos struct{ sec, res, owed, due, refunds, pending, over int64 }
	for id, t := range totals {
		sum := t.Summary(money.VND)
		pos.sec += sum.SecurityHeld.Amount()
		pos.res += sum.ReservationHeld.Amount()
		pos.refunds += sum.PendingRefunds.Amount()
		pos.pending += sum.PendingPayments.Amount()
		// A credited reservation will pay part of the rent: it is not owed twice.
		owed := sum.RentalOutstanding.Amount() - sum.ReservationCredit.Amount()
		switch {
		case owed < 0:
			pos.over -= owed
		case owed > 0 && statuses[rentals.BookingID(id)].IsTerminal():
			pos.owed += owed
			out.Position.OwedEndedCount++
		case owed > 0:
			pos.due += owed
		}
	}
	out.Position.SecurityHeld, out.Position.ReservationHeld = vnd(pos.sec), vnd(pos.res)
	out.Position.OwedEnded, out.Position.DueOpen = vnd(pos.owed), vnd(pos.due)
	out.Position.PendingRefunds, out.Position.PendingPayments, out.Position.Overpaid = vnd(pos.refunds), vnd(pos.pending), vnd(pos.over)
	return out, nil
}

// Mismatch says how an ended booking's revenue and money disagree.
type Mismatch string

const (
	// MismatchNotRecognised: recognised revenue differs from the active
	// charges (waiting for a profile or the catch-up job).
	MismatchNotRecognised Mismatch = "NOT_RECOGNISED"
	// MismatchOwed: the customer has not paid everything charged.
	MismatchOwed Mismatch = "OWED"
	// MismatchOverpaid: more was kept than charged, to refund.
	MismatchOverpaid Mismatch = "OVERPAID"
)

// BookingCheck compares an ended booking's revenue with its money.
type BookingCheck struct {
	ID         rentals.BookingID
	Code       string
	Status     rentals.Status
	EndedAt    time.Time
	Charged    money.Money
	Recognised money.Money
	// Covered is what the customer paid or the deposit covered of it.
	Covered    money.Money
	Mismatches []Mismatch
}

// Section is the first rows of a list with how many there are in all.
type Section[T any] struct {
	Items []T
	Total int
}

// ReconReport is the reconciliation report of a period (tax-policy §10.3).
type ReconReport struct {
	// Recognised is the revenue recognised in the period; Received and
	// Refunded are the money that moved in it, deposits included.
	Recognised money.Money
	Received   money.Money
	Refunded   money.Money
	// EndedBookings counts the bookings that ended in the period;
	// Mismatched lists those whose revenue and money disagree.
	EndedBookings int
	Mismatched    Section[BookingCheck]
	Unreconciled  Section[paymentspg.Txn]
	Attention     Section[paymentspg.Attention]
	// Unrecognised lists ended bookings of any period still to recognise.
	Unrecognised Section[taxpg.Pending]
	// OtherCharges lists OTHER_CHARGE entries, whose nature finance checks.
	OtherCharges Section[taxpg.EntryRow]
}

// Reconciliation builds the reconciliation report of a period.
func (s *Service) Reconciliation(ctx context.Context, p identity.Principal, per Period) (ReconReport, error) {
	if err := p.Require(identity.PermRevenueRead); err != nil {
		return ReconReport{}, err
	}
	start, end, err := s.bounds(per, maxDayRange)
	if err != nil {
		return ReconReport{}, err
	}
	var out ReconReport
	es, err := taxpg.EntriesBetween(ctx, s.pool, start, end)
	if err != nil {
		return ReconReport{}, err
	}
	var recognised int64
	for _, e := range es {
		recognised += e.Net.Amount()
	}
	out.Recognised = vnd(recognised)
	received, refunded, err := paymentspg.CashMovements(ctx, s.pool, start, end)
	if err != nil {
		return ReconReport{}, err
	}
	out.Received, out.Refunded = sumOf(received), sumOf(refunded)

	ended, err := rentalspg.EndedBetween(ctx, s.pool, start, end)
	if err != nil {
		return ReconReport{}, err
	}
	out.EndedBookings = len(ended)
	ids := make([]string, len(ended))
	for i, e := range ended {
		ids[i] = string(e.ID)
	}
	totals, err := paymentspg.Totals(ctx, s.pool, ids)
	if err != nil {
		return ReconReport{}, err
	}
	rec, err := taxpg.RecognisedOf(ctx, s.pool, ids)
	if err != nil {
		return ReconReport{}, err
	}
	for _, e := range ended {
		sum := totals[string(e.ID)].Summary(money.VND)
		c := BookingCheck{ID: e.ID, Code: e.Code, Status: e.Status, EndedAt: e.EndedAt,
			Charged: sum.RentalDue, Recognised: vnd(rec[string(e.ID)]), Covered: sum.RentalCovered}
		if c.Recognised != c.Charged {
			c.Mismatches = append(c.Mismatches, MismatchNotRecognised)
		}
		switch d := c.Covered.Amount() - c.Charged.Amount(); {
		case d < 0:
			c.Mismatches = append(c.Mismatches, MismatchOwed)
		case d > 0:
			c.Mismatches = append(c.Mismatches, MismatchOverpaid)
		}
		if len(c.Mismatches) > 0 {
			out.Mismatched.Total++
			if len(out.Mismatched.Items) < listed {
				out.Mismatched.Items = append(out.Mismatched.Items, c)
			}
		}
	}
	no := false
	if out.Unreconciled.Items, out.Unreconciled.Total, err = paymentspg.Txns(ctx, s.pool,
		paymentspg.TxnFilter{From: start, To: end, Reconciled: &no}, 1, listed); err != nil {
		return ReconReport{}, err
	}
	if out.Attention.Items, out.Attention.Total, err = paymentspg.NeedingAttention(ctx, s.pool, listed); err != nil {
		return ReconReport{}, err
	}
	if out.Unrecognised.Items, out.Unrecognised.Total, err = taxpg.PendingBookings(ctx, s.pool, false, listed); err != nil {
		return ReconReport{}, err
	}
	if out.OtherCharges.Items, out.OtherCharges.Total, err = taxpg.EntryPage(ctx, s.pool,
		taxpg.EntryFilter{From: start, To: end, Category: tax.CatOtherCharge}, 1, listed); err != nil {
		return ReconReport{}, err
	}
	return out, nil
}

// Transactions lists succeeded payments and refunds of a period with their
// reconciliation.
func (s *Service) Transactions(ctx context.Context, p identity.Principal, per Period, kind payments.TxnKind, reconciled *bool, page, size int) ([]paymentspg.Txn, int, error) {
	if err := p.Require(identity.PermRevenueRead); err != nil {
		return nil, 0, err
	}
	switch kind {
	case "", payments.TxnPayment, payments.TxnRefund:
	default:
		return nil, 0, domainerr.Invalid("kind", "must be PAYMENT or REFUND")
	}
	start, end, err := s.bounds(per, maxRange)
	if err != nil {
		return nil, 0, err
	}
	return paymentspg.Txns(ctx, s.pool, paymentspg.TxnFilter{From: start, To: end, Kind: kind, Reconciled: reconciled}, page, size)
}

// Reconcile marks a succeeded payment or refund as matched with the bank
// statement or cash book (payment.reconcile).
func (s *Service) Reconcile(ctx context.Context, p identity.Principal, kind payments.TxnKind, txnID, statementRef, note string) (payments.Reconciliation, error) {
	if err := p.Require(identity.PermPaymentReconcile); err != nil {
		return payments.Reconciliation{}, err
	}
	var out payments.Reconciliation
	err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		booking, err := paymentspg.TxnBooking(ctx, tx, kind, txnID)
		if err != nil {
			return err
		}
		l, _, err := paymentspg.LoadLedger(ctx, tx, booking, false)
		if err != nil {
			return err
		}
		active, err := paymentspg.ReconciliationInForce(ctx, tx, kind, txnID)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		r, err := l.Reconcile(p, s.newID(), kind, txnID, active, statementRef, note, now)
		if err != nil {
			return err
		}
		if err := paymentspg.InsertReconciliation(ctx, tx, r); err != nil {
			return err
		}
		out = r
		return auditpg.Append(ctx, tx, audit.Event{
			ID: s.newID(), OccurredAt: now, Actor: p.Actor(), Action: "payment.reconciled", ResourceType: "reconciliation",
			ResourceID: r.ID, Result: audit.ResultSuccess,
			References: map[string]string{"booking_id": booking, "kind": string(kind), "transaction_id": txnID, "statement_ref": r.StatementRef},
		})
	})
	if err != nil {
		return payments.Reconciliation{}, fmt.Errorf("reconcile %s %s: %w", kind, txnID, err)
	}
	return out, nil
}

// RevokeReconciliation undoes a mistaken reconciliation (payment.reconcile).
func (s *Service) RevokeReconciliation(ctx context.Context, p identity.Principal, id, reason string) error {
	if err := p.Require(identity.PermPaymentReconcile); err != nil {
		return err
	}
	err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := paymentspg.LoadReconciliation(ctx, tx, id)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		if err := r.Revoke(p, reason, now); err != nil {
			return err
		}
		if err := paymentspg.RevokeReconciliation(ctx, tx, r); err != nil {
			return err
		}
		return auditpg.Append(ctx, tx, audit.Event{
			ID: s.newID(), OccurredAt: now, Actor: p.Actor(), Action: "payment.reconciliation_revoked", ResourceType: "reconciliation",
			ResourceID: r.ID, Result: audit.ResultSuccess, Reason: r.Revoked.Reason,
			References: map[string]string{"booking_id": r.BookingRef, "kind": string(r.Kind), "transaction_id": r.TxnID},
		})
	})
	if err != nil {
		return fmt.Errorf("revoke reconciliation %s: %w", id, err)
	}
	return nil
}

func sumOf(ms []paymentspg.Movement) money.Money {
	var v int64
	for _, m := range ms {
		v += m.Amount.Amount()
	}
	return vnd(v)
}

func vnd(v int64) money.Money {
	m, _ := money.New(v, money.VND) // sums of bounded ledger amounts
	return m
}
