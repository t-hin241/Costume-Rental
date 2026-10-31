package rentalsapp

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/catalog/catalogpg"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/identity/identitypg"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/inventory/inventorypg"
	"webee_rentor/internal/payments"
	"webee_rentor/internal/payments/paymentspg"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalspg"
)

// Lists load one ledger per row, so these reads stay proportional to the
// page, whose size the transport bounds.

// Summary is one row of a booking list.
type Summary struct {
	Booking      *rentals.Booking
	PaymentState payments.PaymentState
	Customer     identity.Contact
	Image        *catalog.Image
	Overdue      bool
}

// BookingItemState says where one unit of a booking is.
type BookingItemState string

const (
	ItemAllocated      BookingItemState = "ALLOCATED"
	ItemWithCustomer   BookingItemState = "WITH_CUSTOMER"
	ItemReturned       BookingItemState = "RETURNED"
	ItemMissingPending BookingItemState = "MISSING_PENDING"
	ItemLost           BookingItemState = "LOST"
)

// ItemView is one physical unit held or handed over under the booking.
type ItemView struct {
	AssetID     inventory.AssetID
	AssetCode   string
	AssetStatus inventory.AssetStatus
	LineID      rentals.LineID
	Variant     catalogpg.VariantInfo
	State       BookingItemState
	Preexisting string
	ReturnedAt  *time.Time
	Conditions  []rentals.Condition
	Notes       string
}

// FeeView is a fee with the asset it concerns and what the reader may do.
type FeeView struct {
	rentals.FeeAssessment
	AssetCode string
	Actions   []rentals.FeeAction
}

// RefundAction names what a user can do with a refund.
type RefundAction string

const (
	RefundDoApprove RefundAction = "approve"
	RefundDoReject  RefundAction = "reject"
	RefundDoPayout  RefundAction = "payout"
)

// TransitionView is one history entry as its reader may see it.
type TransitionView struct {
	Action string
	To     rentals.Status
	At     time.Time
	Actor  string
	Note   string
}

// Detail is a full booking as one reader may see it.
type Detail struct {
	Summary
	// Ledger is nil for a reader with no access to its money.
	Ledger        *payments.Ledger
	Variants      map[catalog.VariantID]catalogpg.VariantInfo
	LineImages    map[rentals.LineID]*catalog.Image
	Items         []ItemView
	Fees          []FeeView
	RefundActions map[payments.RefundID][]RefundAction
	Transitions   []TransitionView
	Actions       []rentals.BookingAction
}

func (s *Service) contacts(ctx context.Context, q postgres.Querier, bs []*rentals.Booking) (map[identity.UserID]identity.Contact, error) {
	ids := make([]identity.UserID, len(bs))
	for i, b := range bs {
		ids[i] = b.CustomerID
	}
	return identitypg.Contacts(ctx, q, ids)
}

func imageOf(images map[string]catalog.Image, b *rentals.Booking) *catalog.Image {
	if len(b.Lines) == 0 {
		return nil
	}
	if img, ok := images[b.Lines[0].OfferingID]; ok {
		return &img
	}
	return nil
}

// summaries loads list rows in the order of ids.
func (s *Service) summaries(ctx context.Context, q postgres.Querier, ids []rentals.BookingID) ([]Summary, error) {
	bs := make([]*rentals.Booking, 0, len(ids))
	states := make([]payments.PaymentState, 0, len(ids))
	var offerings []string
	for _, id := range ids {
		b, _, err := rentalspg.LoadBooking(ctx, q, id, false)
		if err != nil {
			return nil, err
		}
		l, _, err := paymentspg.LoadLedger(ctx, q, string(id), false)
		if err != nil {
			return nil, err
		}
		bs, states = append(bs, b), append(states, l.Summary().State)
		if len(b.Lines) > 0 {
			offerings = append(offerings, b.Lines[0].OfferingID)
		}
	}
	people, err := s.contacts(ctx, q, bs)
	if err != nil {
		return nil, err
	}
	images, err := catalogpg.OfferingImages(ctx, q, offerings)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	out := make([]Summary, len(bs))
	for i, b := range bs {
		out[i] = Summary{Booking: b, PaymentState: states[i], Customer: people[b.CustomerID], Image: imageOf(images, b), Overdue: b.IsOverdue(now)}
	}
	return out, nil
}

// Location is the business timezone.
func (s *Service) Location() *time.Location { return s.loc }

// VariantNames labels variants for messages shown to customers.
func (s *Service) VariantNames(ctx context.Context, ids []catalog.VariantID) map[catalog.VariantID]string {
	out := make(map[catalog.VariantID]string, len(ids))
	if len(ids) == 0 {
		return out
	}
	infos, err := catalogpg.Variants(ctx, s.pool, ids)
	if err != nil {
		return out // the numbers are what matters; a missing label changes nothing
	}
	for id, v := range infos {
		out[id] = v.ItemName
		if v.Size != "" {
			out[id] += " (" + v.Size + ")"
		}
	}
	return out
}

// Summaries reads list rows for a caller that already checked access.
func (s *Service) Summaries(ctx context.Context, ids []rentals.BookingID) ([]Summary, error) {
	return s.summaries(ctx, s.pool, ids)
}

// CountOverdue counts rentals past their return time, for reports.
func (s *Service) CountOverdue(ctx context.Context) (int, error) {
	now := s.now().UTC()
	due, err := rentalspg.RentedDueBy(ctx, s.pool, now, maxOverdueList)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range due {
		b, _, err := rentalspg.LoadBooking(ctx, s.pool, id, false)
		if err != nil {
			return 0, err
		}
		if b.IsOverdue(now) {
			n++
		}
	}
	return n, nil
}

// ListInput filters a booking list.
type ListInput struct {
	Term     string
	Status   rentals.Status
	Finished *bool
	From, To *time.Time
	Page     int
	PageSize int
}

// Bookings lists every booking for staff.
func (s *Service) Bookings(ctx context.Context, p identity.Principal, in ListInput) ([]Summary, int, error) {
	if err := p.Require(identity.PermBookingReadAll); err != nil {
		return nil, 0, err
	}
	return s.list(ctx, rentalspg.BookingFilter{Term: in.Term, Status: in.Status, From: in.From, To: in.To, Page: in.Page, PageSize: in.PageSize})
}

// MyBookings lists the principal's own bookings.
func (s *Service) MyBookings(ctx context.Context, p identity.Principal, finished bool, page, size int) ([]Summary, int, error) {
	if err := p.Require(identity.PermBookingReadOwn); err != nil {
		return nil, 0, err
	}
	return s.list(ctx, rentalspg.BookingFilter{Customer: p.UserID, Finished: &finished, Page: page, PageSize: size})
}

func (s *Service) list(ctx context.Context, f rentalspg.BookingFilter) ([]Summary, int, error) {
	ids, total, err := rentalspg.BookingPage(ctx, s.pool, f)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.summaries(ctx, s.pool, ids)
	return rows, total, err
}

// View reads a booking for someone allowed to see it, with the actions they
// may take. A customer sees staff as the store and no internal notes.
func (s *Service) View(ctx context.Context, p identity.Principal, id rentals.BookingID) (Detail, error) {
	b, _, err := rentalspg.LoadBooking(ctx, s.pool, id, false)
	if err != nil {
		return Detail{}, err
	}
	scope, err := rentals.Authorize(p, rentals.ActionRead, b)
	if err != nil {
		return Detail{}, err
	}
	l, _, err := paymentspg.LoadLedger(ctx, s.pool, string(id), false)
	if err != nil {
		return Detail{}, err
	}
	rows, err := s.summaries(ctx, s.pool, []rentals.BookingID{id})
	if err != nil {
		return Detail{}, err
	}
	d := Detail{Summary: rows[0], Variants: map[catalog.VariantID]catalogpg.VariantInfo{}, LineImages: map[rentals.LineID]*catalog.Image{}}
	if scope == identity.ScopeOwn || p.Can(identity.PermPaymentRead) || p.Can(identity.PermPaymentRecord) {
		d.Ledger = l
	}
	if err := s.fillLines(ctx, &d); err != nil {
		return Detail{}, err
	}
	if err := s.fillItems(ctx, &d); err != nil {
		return Detail{}, err
	}
	fees, err := rentalspg.AssessmentsOfBooking(ctx, s.pool, id)
	if err != nil {
		return Detail{}, err
	}
	if err := s.fillFees(ctx, &d, fees, p); err != nil {
		return Detail{}, err
	}
	if d.Transitions, err = s.history(ctx, b, p, scope, d.Items); err != nil {
		return Detail{}, err
	}
	d.RefundActions = refundActions(l, p)
	d.Actions = b.Actions(p, factsOf(l, fees), s.now().UTC())
	return d, nil
}

func refundActions(l *payments.Ledger, p identity.Principal) map[payments.RefundID][]RefundAction {
	out := map[payments.RefundID][]RefundAction{}
	for _, r := range l.Refunds {
		if r.Status != payments.RefundRequested && r.Status != payments.RefundApproved {
			continue
		}
		var a []RefundAction
		if r.Status == payments.RefundRequested && p.Can(identity.PermRefundApprove) {
			a = append(a, RefundDoApprove)
		}
		if p.Can(identity.PermRefundApprove) {
			a = append(a, RefundDoReject)
		}
		if p.Can(identity.PermPaymentRecord) {
			a = append(a, RefundDoPayout)
		}
		out[r.ID] = a
	}
	return out
}

func factsOf(l *payments.Ledger, fees []rentals.FeeAssessment) rentals.ActionFacts {
	sum := l.Summary()
	return rentals.ActionFacts{
		MoneyOwed: l.Collectable().IsPositive(), MoneyToClose: l.HasMoneyToClose(), Settled: sum.IsSettled(),
		OpenFees: rentals.CountOpenAssessments(fees),
	}
}

func (s *Service) fillLines(ctx context.Context, d *Detail) error {
	var variants []catalog.VariantID
	offerings := make([]string, 0, len(d.Booking.Lines))
	for _, l := range d.Booking.Lines {
		offerings = append(offerings, l.OfferingID)
		for _, c := range l.Components {
			variants = append(variants, c.VariantID)
		}
	}
	for _, it := range d.Booking.Items {
		variants = append(variants, it.VariantID)
	}
	var err error
	if d.Variants, err = catalogpg.Variants(ctx, s.pool, variants); err != nil {
		return err
	}
	images, err := catalogpg.OfferingImages(ctx, s.pool, offerings)
	if err != nil {
		return err
	}
	for _, l := range d.Booking.Lines {
		if img, ok := images[l.OfferingID]; ok {
			d.LineImages[l.ID] = &img
		}
	}
	return nil
}

func (s *Service) fillItems(ctx context.Context, d *Detail) error {
	b := d.Booking
	allocs, err := inventorypg.AllocationsOfBooking(ctx, s.pool, string(b.ID), true)
	if err != nil {
		return err
	}
	allocs = slices.DeleteFunc(allocs, func(a inventory.Allocation) bool { return a.Tentative() })
	ids := make([]inventory.AssetID, 0, len(allocs)+len(b.Items))
	for _, a := range allocs {
		ids = append(ids, a.AssetID)
	}
	for _, it := range b.Items {
		ids = append(ids, it.AssetID)
	}
	assets, err := inventorypg.AssetsByID(ctx, s.pool, ids)
	if err != nil {
		return err
	}
	for _, it := range b.Items {
		a := assets[it.AssetID]
		v := ItemView{
			AssetID: it.AssetID, AssetCode: a.Code, AssetStatus: a.Status, LineID: it.LineID, Variant: d.Variants[it.VariantID],
			State: itemState(it), Preexisting: it.PreexistingIssues, ReturnedAt: it.ReturnedAt,
		}
		if it.Finding != nil {
			v.Conditions, v.Notes = it.Finding.Conditions, it.Finding.Notes
		}
		d.Items = append(d.Items, v)
	}
	if len(b.Items) > 0 {
		return nil
	}
	for _, al := range allocs {
		a := assets[al.AssetID]
		info, ok := d.Variants[al.VariantID]
		if !ok {
			continue
		}
		d.Items = append(d.Items, ItemView{
			AssetID: al.AssetID, AssetCode: a.Code, AssetStatus: a.Status, LineID: rentals.LineID(al.LineRef), Variant: info, State: ItemAllocated,
		})
	}
	return nil
}

func itemState(i rentals.RentedItem) BookingItemState {
	switch {
	case i.LostConfirmedAt != nil:
		return ItemLost
	case i.ReturnedAt != nil:
		return ItemReturned
	case i.MissingPending():
		return ItemMissingPending
	}
	return ItemWithCustomer
}

func (s *Service) fillFees(ctx context.Context, d *Detail, fees []rentals.FeeAssessment, p identity.Principal) error {
	ids := make([]inventory.AssetID, 0, len(fees))
	for _, f := range fees {
		ids = append(ids, f.AssetID)
	}
	assets, err := inventorypg.AssetsByID(ctx, s.pool, ids)
	if err != nil {
		return err
	}
	for _, f := range fees {
		d.Fees = append(d.Fees, FeeView{FeeAssessment: f, AssetCode: assets[f.AssetID].Code,
			Actions: f.Actions(p, d.Booking.Policy.FeeApproval.ManagerApprovalAbove)})
	}
	return nil
}

func (s *Service) history(ctx context.Context, b *rentals.Booking, p identity.Principal, scope identity.Scope, items []ItemView) ([]TransitionView, error) {
	rows, err := rentalspg.Transitions(ctx, s.pool, b.ID)
	if err != nil {
		return nil, err
	}
	var users []identity.UserID
	for _, t := range rows {
		if t.Actor.IsUser() {
			users = append(users, identity.UserID(t.Actor.ID))
		}
	}
	people, err := identitypg.Contacts(ctx, s.pool, users)
	if err != nil {
		return nil, err
	}
	out := make([]TransitionView, 0, len(rows))
	for _, t := range rows {
		v := TransitionView{Action: t.Action, To: t.To, At: t.At}
		switch {
		case scope == identity.ScopeOwn && t.Actor.ID == string(p.UserID):
			v.Actor = "Bạn"
		case scope == identity.ScopeOwn || t.Actor.IsSystem():
			v.Actor = "Cửa hàng"
		default:
			v.Actor = people[identity.UserID(t.Actor.ID)].Label()
		}
		if scope != identity.ScopeOwn {
			v.Note = s.transitionNote(t, items)
		}
		out = append(out, v)
	}
	return out, nil
}

// transitionNote is the reason in staff terms. Item events name the asset by
// its id, and rescheduling keeps the old times for the audit trail.
func (s *Service) transitionNote(t rentalspg.TransitionRow, items []ItemView) string {
	switch rentals.Action(t.Action) {
	case rentals.ActionReturn, rentals.ActionInspectAsset, rentals.ActionReportMissing:
		for _, it := range items {
			if string(it.AssetID) == t.Reason {
				return it.AssetCode
			}
		}
	case rentals.ActionReschedule:
		pickup, due, ok := strings.Cut(strings.TrimPrefix(t.Reason, "from "), "–")
		from, err1 := time.Parse(time.RFC3339, pickup)
		to, err2 := time.Parse(time.RFC3339, due)
		if ok && err1 == nil && err2 == nil {
			const layout = "15:04 02/01/2006"
			return fmt.Sprintf("Lịch cũ: %s đến %s", from.In(s.loc).Format(layout), to.In(s.loc).Format(layout))
		}
	}
	return t.Reason
}

// CancellationPreview is what cancelling now would cost.
type CancellationPreview struct {
	FeePercent int
	Result     payments.CancellationResult
	Basis      string
}

// PreviewCancellation runs the real cancellation rules on the booking and
// ledger in memory and saves nothing, so the preview cannot drift from them.
func (s *Service) PreviewCancellation(ctx context.Context, p identity.Principal, id rentals.BookingID, reason rentals.CancelReason) (CancellationPreview, error) {
	b, _, err := rentalspg.LoadBooking(ctx, s.pool, id, false)
	if err != nil {
		return CancellationPreview{}, err
	}
	scope, err := rentals.Authorize(p, rentals.ActionCancel, b)
	if err != nil {
		return CancellationPreview{}, err
	}
	l, _, err := paymentspg.LoadLedger(ctx, s.pool, string(id), false)
	if err != nil {
		return CancellationPreview{}, err
	}
	now := s.now().UTC()
	if scope == identity.ScopeOwn {
		reason = rentals.CancelCustomerRequest
	} else if reason == "" {
		reason = rentals.CancelStaffDecision
	}
	if _, err := b.Cancel(rentals.CancelRequest{Actor: p.Actor(), Scope: scope, Reason: reason, Note: "preview", Now: now}); err != nil {
		return CancellationPreview{}, fmt.Errorf("preview cancellation of %s: %w", id, err)
	}
	fee, err := b.CancellationFee(l.Summary().TotalReceived.IsPositive())
	if err != nil {
		return CancellationPreview{}, err
	}
	if l.Staged != nil {
		l.DiscardRepricing(l.Staged.Ref)
	}
	l.ExpirePendingPayments(now)
	res, _, err := l.SettleCancellation(payments.CancellationRequest{
		Fee: fee.Fee, Basis: fee.Basis, FeeChargeID: "preview", ResolutionID: "preview", Actor: p.Actor(), Now: now,
	})
	if err != nil {
		return CancellationPreview{}, fmt.Errorf("preview cancellation of %s: %w", id, err)
	}
	return CancellationPreview{FeePercent: fee.FeePercent, Result: res, Basis: fee.Basis}, nil
}

// DecideBookingFee applies a decision to a fee of this booking; a fee of
// another booking is reported as not found.
func (s *Service) DecideBookingFee(ctx context.Context, p identity.Principal, id rentals.BookingID, fee rentals.AssessmentID, decision FeeDecision, note string) error {
	a, err := rentalspg.LoadAssessment(ctx, s.pool, fee)
	if err != nil {
		return err
	}
	if a.BookingID != id {
		return fmt.Errorf("fee %s of booking %s: %w", fee, id, postgres.ErrNotFound)
	}
	_, err = s.DecideFee(ctx, p, fee, decision, note)
	return err
}
