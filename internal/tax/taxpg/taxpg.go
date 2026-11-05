// Package taxpg stores the tax profile history and the revenue ledger in
// PostgreSQL (decision D41). Both are append-only: a profile version only
// gains the end date a newer one gives it, and revenue entries are never
// updated or deleted.
package taxpg

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/tax"
)

// ErrProfileTaken reports a version or period another request just stored.
var ErrProfileTaken = domainerr.New(domainerr.KindConflict, "tax.profile_conflict", "another tax profile version was just saved; reload and try again")

// profileDoc is the stored form of a profile.
type profileDoc struct {
	Version                string `json:"version"`
	TaxpayerType           string `json:"taxpayer_type"`
	AnnualRevenueThreshold int64  `json:"annual_revenue_threshold"`
	Currency               string `json:"currency"`
	ThresholdEffectiveFrom string `json:"threshold_effective_from"`
	WarningPercents        []int  `json:"warning_percents"`
}

// LoadHistory reads every profile version, oldest first. With lock the rows
// are read FOR UPDATE so two new versions queue.
func LoadHistory(ctx context.Context, q postgres.Querier, lock bool) (tax.History, error) {
	query := `SELECT document, effective_from, effective_until FROM tax_profiles ORDER BY effective_from`
	if lock {
		query += ` FOR UPDATE`
	}
	rows, err := q.Query(ctx, query)
	if err != nil {
		return tax.History{}, fmt.Errorf("read tax profiles: %w", err)
	}
	defer rows.Close()
	var h tax.History
	for rows.Next() {
		var raw []byte
		var p tax.Profile
		if err := rows.Scan(&raw, &p.EffectiveFrom, &p.EffectiveUntil); err != nil {
			return tax.History{}, fmt.Errorf("read tax profiles: %w", err)
		}
		var d profileDoc
		if err := json.Unmarshal(raw, &d); err != nil {
			return tax.History{}, fmt.Errorf("decode tax profile: %w", err)
		}
		c, err := money.ParseCurrency(d.Currency)
		if err != nil {
			return tax.History{}, fmt.Errorf("tax profile %s currency: %w", d.Version, err)
		}
		if p.AnnualRevenueThreshold, err = money.New(d.AnnualRevenueThreshold, c); err != nil {
			return tax.History{}, fmt.Errorf("tax profile %s threshold: %w", d.Version, err)
		}
		if p.ThresholdEffectiveFrom, err = businesstime.ParseDate(d.ThresholdEffectiveFrom); err != nil {
			return tax.History{}, fmt.Errorf("tax profile %s threshold date: %w", d.Version, err)
		}
		p.Version, p.TaxpayerType, p.WarningPercents = d.Version, tax.TaxpayerType(d.TaxpayerType), d.WarningPercents
		p.EffectiveFrom, p.EffectiveUntil = p.EffectiveFrom.UTC(), postgres.UTCPtr(p.EffectiveUntil)
		h.Profiles = append(h.Profiles, p)
	}
	if err := rows.Err(); err != nil {
		return tax.History{}, fmt.Errorf("read tax profiles: %w", err)
	}
	return h, nil
}

// InsertProfile stores a new version.
func InsertProfile(ctx context.Context, q postgres.Querier, p tax.Profile, now time.Time) error {
	raw, err := json.Marshal(profileDoc{
		Version: p.Version, TaxpayerType: string(p.TaxpayerType), AnnualRevenueThreshold: p.AnnualRevenueThreshold.Amount(),
		Currency: string(p.AnnualRevenueThreshold.Currency()), ThresholdEffectiveFrom: p.ThresholdEffectiveFrom.String(),
		WarningPercents: p.WarningPercents,
	})
	if err != nil {
		return fmt.Errorf("encode tax profile %s: %w", p.Version, err)
	}
	_, err = q.Exec(ctx, `INSERT INTO tax_profiles (version, document, effective_from, effective_until, created_at)
		VALUES ($1, $2, $3, $4, $5)`, p.Version, raw, p.EffectiveFrom.UTC(), postgres.UTCPtr(p.EffectiveUntil), now.UTC())
	switch {
	case postgres.IsUniqueViolation(err, "tax_profiles_pkey"), postgres.IsExclusionViolation(err, "tax_profiles_tstzrange_excl"):
		return fmt.Errorf("tax profile %s: %w", p.Version, ErrProfileTaken)
	case err != nil:
		return fmt.Errorf("insert tax profile %s: %w", p.Version, err)
	}
	return nil
}

// CloseProfile sets the end date a newer version gives an open one.
func CloseProfile(ctx context.Context, q postgres.Querier, p tax.Profile) error {
	tag, err := q.Exec(ctx, `UPDATE tax_profiles SET effective_until = $2 WHERE version = $1 AND effective_until IS NULL`,
		p.Version, postgres.UTCPtr(p.EffectiveUntil))
	if err != nil {
		return fmt.Errorf("close tax profile %s: %w", p.Version, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("tax profile %s: %w", p.Version, ErrProfileTaken)
	}
	return nil
}

const entryColumns = `e.id, e.source, coalesce(e.booking_id, ''), coalesce(e.source_ref, ''), e.category, e.currency,
	e.list_amount, e.discount, e.net, e.recognized_at, e.profile_version, coalesce(e.reverses, ''), e.reason,
	e.channel, e.note, e.document_ref, e.recorded_by_kind, e.recorded_by_id`

func scanEntry(row pgx.Row, extra ...any) (tax.RevenueEntry, error) {
	var e tax.RevenueEntry
	var currency string
	var list, discount, net int64
	var byKind *identity.ActorKind
	var byID *string
	dst := append([]any{&e.ID, &e.Source, &e.BookingRef, &e.SourceRef, &e.Category, &currency, &list, &discount, &net,
		&e.RecognizedAt, &e.ProfileVersion, &e.Reverses, &e.Reason, &e.Channel, &e.Note, &e.DocumentRef, &byKind, &byID}, extra...)
	if err := row.Scan(dst...); err != nil {
		return tax.RevenueEntry{}, err
	}
	c, err := money.ParseCurrency(currency)
	if err != nil {
		return tax.RevenueEntry{}, err
	}
	if e.List, err = money.New(list, c); err != nil {
		return tax.RevenueEntry{}, err
	}
	if e.Discount, err = money.New(discount, c); err != nil {
		return tax.RevenueEntry{}, err
	}
	if e.Net, err = money.New(net, c); err != nil {
		return tax.RevenueEntry{}, err
	}
	if byKind != nil && byID != nil {
		e.RecordedBy = &identity.Actor{Kind: *byKind, ID: *byID}
	}
	e.RecognizedAt = e.RecognizedAt.UTC()
	return e, nil
}

func entries(ctx context.Context, q postgres.Querier, what, sql string, args ...any) ([]tax.RevenueEntry, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", what, err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (tax.RevenueEntry, error) { return scanEntry(r) })
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", what, err)
	}
	return out, nil
}

// InsertEntries appends entries. One already stored (same id, or the same
// charge or reversal recognised by a concurrent run) is skipped, which makes
// recognition safe to repeat. It returns how many were added.
func InsertEntries(ctx context.Context, q postgres.Querier, es []tax.RevenueEntry, now time.Time) (int, error) {
	n := 0
	for _, e := range es {
		var kind *identity.ActorKind
		var by *string
		if e.RecordedBy != nil {
			kind, by = &e.RecordedBy.Kind, &e.RecordedBy.ID
		}
		tag, err := q.Exec(ctx, `
			INSERT INTO revenue_entries (id, source, booking_id, source_ref, category, currency, list_amount, discount, net,
				recognized_at, profile_version, reverses, reason, channel, note, document_ref, recorded_by_kind, recorded_by_id, created_at)
			VALUES ($1, $2, nullif($3, ''), nullif($4, ''), $5, $6, $7, $8, $9, $10, $11, nullif($12, ''), $13, $14, $15, $16, $17, $18, $19)
			ON CONFLICT DO NOTHING`,
			e.ID, e.Source, e.BookingRef, e.SourceRef, e.Category, e.Net.Currency(), e.List.Amount(), e.Discount.Amount(), e.Net.Amount(),
			e.RecognizedAt.UTC(), e.ProfileVersion, e.Reverses, e.Reason, e.Channel, e.Note, e.DocumentRef, kind, by, now.UTC())
		if err != nil {
			return n, fmt.Errorf("insert revenue entry %s: %w", e.ID, err)
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}

// EntriesOfBooking reads a booking's entries.
func EntriesOfBooking(ctx context.Context, q postgres.Querier, id rentals.BookingID) ([]tax.RevenueEntry, error) {
	return entries(ctx, q, "revenue of booking "+string(id),
		`SELECT `+entryColumns+` FROM revenue_entries e WHERE e.booking_id = $1 ORDER BY e.recognized_at, e.id`, string(id))
}

// EntriesBetween reads the entries recognised in [from, to).
func EntriesBetween(ctx context.Context, q postgres.Querier, from, to time.Time) ([]tax.RevenueEntry, error) {
	return entries(ctx, q, "revenue entries",
		`SELECT `+entryColumns+` FROM revenue_entries e WHERE e.recognized_at >= $1 AND e.recognized_at < $2
		ORDER BY e.recognized_at, e.id`, from.UTC(), to.UTC())
}

// Entry reads one entry FOR UPDATE and whether it is already reversed.
func Entry(ctx context.Context, q postgres.Querier, id tax.EntryID) (tax.RevenueEntry, bool, error) {
	var reversed bool
	e, err := scanEntry(q.QueryRow(ctx, `SELECT `+entryColumns+`,
		EXISTS (SELECT 1 FROM revenue_entries r WHERE r.reverses = e.id)
		FROM revenue_entries e WHERE e.id = $1 FOR UPDATE`, string(id)), &reversed)
	if err != nil {
		return tax.RevenueEntry{}, false, postgres.NotFound(err, "revenue entry "+string(id))
	}
	return e, reversed, nil
}

// EntryFilter selects entries of the ledger list and export.
type EntryFilter struct {
	From, To time.Time
	Category tax.Category
	Source   tax.Source
	// Booking matches the booking code exactly.
	Booking string
}

// EntryRow is an entry with the code of its booking.
type EntryRow struct {
	tax.RevenueEntry
	BookingCode string
}

func (f EntryFilter) where() (string, []any) {
	return `e.recognized_at >= $1 AND e.recognized_at < $2 AND ($3 = '' OR e.category = $3)
		AND ($4 = '' OR e.source = $4) AND ($5 = '' OR b.code = $5)`,
		[]any{f.From.UTC(), f.To.UTC(), string(f.Category), string(f.Source), f.Booking}
}

// EntryPage lists entries, newest first, with the total count.
func EntryPage(ctx context.Context, q postgres.Querier, f EntryFilter, page, size int) ([]EntryRow, int, error) {
	where, args := f.where()
	rows, err := q.Query(ctx, `SELECT `+entryColumns+`, coalesce(b.code, ''), count(*) OVER ()
		FROM revenue_entries e LEFT JOIN bookings b ON b.id = e.booking_id
		WHERE `+where+` ORDER BY e.recognized_at DESC, e.id LIMIT $6 OFFSET $7`, append(args, size, (page-1)*size)...)
	if err != nil {
		return nil, 0, fmt.Errorf("read revenue entries: %w", err)
	}
	defer rows.Close()
	var out []EntryRow
	total := 0
	for rows.Next() {
		var r EntryRow
		e, err := scanEntry(rows, &r.BookingCode, &total)
		if err != nil {
			return nil, 0, fmt.Errorf("read revenue entries: %w", err)
		}
		r.RevenueEntry = e
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("read revenue entries: %w", err)
	}
	return out, total, nil
}

// ErrTooManyRows stops an export that would be too large to send at once.
var ErrTooManyRows = domainerr.New(domainerr.KindInvalid, "tax.export_too_large", "too many rows; export a shorter period")

// EachEntry calls fn for every entry of the filter, oldest first, up to max.
func EachEntry(ctx context.Context, q postgres.Querier, f EntryFilter, max int, fn func(EntryRow) error) error {
	where, args := f.where()
	var n int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM revenue_entries e LEFT JOIN bookings b ON b.id = e.booking_id WHERE `+where, args...).Scan(&n); err != nil {
		return fmt.Errorf("count revenue entries: %w", err)
	}
	if n > max {
		return ErrTooManyRows
	}
	rows, err := q.Query(ctx, `SELECT `+entryColumns+`, coalesce(b.code, '')
		FROM revenue_entries e LEFT JOIN bookings b ON b.id = e.booking_id
		WHERE `+where+` ORDER BY e.recognized_at, e.id`, args...)
	if err != nil {
		return fmt.Errorf("read revenue entries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r EntryRow
		e, err := scanEntry(rows, &r.BookingCode)
		if err != nil {
			return fmt.Errorf("read revenue entries: %w", err)
		}
		r.RevenueEntry = e
		if err := fn(r); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Group is the period of a revenue report row.
type Group string

const (
	GroupDay     Group = "day"
	GroupMonth   Group = "month"
	GroupQuarter Group = "quarter"
	GroupYear    Group = "year"
)

// ReportRow is the revenue of one period and category.
type ReportRow struct {
	// Period is the first business day of the period.
	Period   businesstime.Date
	Category tax.Category
	Entries  int
	List     money.Money
	Discount money.Money
	Net      money.Money
}

// Report sums the entries of [from, to) by business period and category.
func Report(ctx context.Context, q postgres.Querier, from, to time.Time, g Group, loc *time.Location) ([]ReportRow, error) {
	switch g {
	case GroupDay, GroupMonth, GroupQuarter, GroupYear:
	default:
		return nil, domainerr.Invalid("group", "must be day, month, quarter or year")
	}
	rows, err := q.Query(ctx, `
		SELECT to_char(date_trunc($3, e.recognized_at AT TIME ZONE $4), 'YYYY-MM-DD'), e.category,
			count(*), sum(e.list_amount), sum(e.discount), sum(e.net)
		FROM revenue_entries e
		WHERE e.recognized_at >= $1 AND e.recognized_at < $2
		GROUP BY 1, 2 ORDER BY 1, 2`, from.UTC(), to.UTC(), string(g), loc.String())
	if err != nil {
		return nil, fmt.Errorf("revenue report: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ReportRow, error) {
		var row ReportRow
		var day string
		var list, discount, net int64
		if err := r.Scan(&day, &row.Category, &row.Entries, &list, &discount, &net); err != nil {
			return ReportRow{}, err
		}
		var err error
		if row.Period, err = businesstime.ParseDate(day); err != nil {
			return ReportRow{}, err
		}
		row.List, row.Discount, row.Net = vnd(list), vnd(discount), vnd(net)
		return row, nil
	})
	if err != nil {
		return nil, fmt.Errorf("revenue report: %w", err)
	}
	return out, nil
}

// pendingCharges finds ended bookings whose ledger has a charge the revenue
// ledger does not follow yet: an active charge with no entry, or a charge
// reversed after the end whose entry is not reversed. It mirrors
// tax.SyncBooking and the ids of tax.EntryOf/ReversalOf. With covered, a
// missing entry counts only if a profile covers its time, so bookings that
// wait for a profile do not crowd out the others.
const pendingCharges = `
	WITH ended AS (
		SELECT b.id, b.code, coalesce(b.completed_at, b.cancelled_at, b.expired_at) AS at
		FROM bookings b WHERE b.status IN ('COMPLETED', 'CANCELLED', 'EXPIRED'))
	SELECT d.id, d.code, d.at FROM ended d
	WHERE EXISTS (
		SELECT 1 FROM ledger_charges c
		WHERE c.booking_id = d.id AND c.amount > 0 AND c.kind NOT IN ('SECURITY_DEPOSIT', 'RESERVATION_DEPOSIT')
			AND (c.reversed_at IS NULL OR c.reversed_at > d.at)
			AND ((NOT EXISTS (SELECT 1 FROM revenue_entries e WHERE e.id = 'rev:' || c.id)
					AND (NOT $1 OR EXISTS (SELECT 1 FROM tax_profiles p
						WHERE tstzrange(p.effective_from, p.effective_until, '[)') @> greatest(d.at, c.created_at))))
				OR (c.reversed_at IS NOT NULL AND EXISTS (SELECT 1 FROM revenue_entries e WHERE e.id = 'rev:' || c.id)
					AND NOT EXISTS (SELECT 1 FROM revenue_entries e WHERE e.id = 'rev:' || c.id || ':reversal'))))`

// Pending is an ended booking whose revenue is not up to date.
type Pending struct {
	ID      rentals.BookingID
	Code    string
	EndedAt time.Time
}

// PendingBookings lists up to limit ended bookings whose revenue is behind
// their ledger, oldest end first, with how many there are in all.
func PendingBookings(ctx context.Context, q postgres.Querier, covered bool, limit int) ([]Pending, int, error) {
	rows, err := q.Query(ctx, `SELECT p.*, count(*) OVER () FROM (`+pendingCharges+`) p ORDER BY p.at, p.id LIMIT $2`, covered, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("bookings with revenue to recognise: %w", err)
	}
	defer rows.Close()
	var out []Pending
	total := 0
	for rows.Next() {
		var p Pending
		if err := rows.Scan(&p.ID, &p.Code, &p.EndedAt, &total); err != nil {
			return nil, 0, fmt.Errorf("bookings with revenue to recognise: %w", err)
		}
		p.EndedAt = p.EndedAt.UTC()
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("bookings with revenue to recognise: %w", err)
	}
	return out, total, nil
}

// RecognisedOf sums the net revenue recognised for each booking.
func RecognisedOf(ctx context.Context, q postgres.Querier, ids []string) (map[string]int64, error) {
	rows, err := q.Query(ctx, `SELECT booking_id, sum(net) FROM revenue_entries WHERE booking_id = ANY($1) GROUP BY 1`, ids)
	if err != nil {
		return nil, fmt.Errorf("recognised revenue of bookings: %w", err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id string
		var net int64
		if err := rows.Scan(&id, &net); err != nil {
			return nil, fmt.Errorf("recognised revenue of bookings: %w", err)
		}
		out[id] = net
	}
	return out, rows.Err()
}

func vnd(v int64) money.Money {
	m, _ := money.New(v, money.VND) // sums of bounded ledger amounts
	return m
}
