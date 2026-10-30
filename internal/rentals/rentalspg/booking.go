// Package rentalspg stores bookings, their transitions, modifications and
// the store calendar in PostgreSQL.
//
// A booking is written as a whole: header columns, lines, components and
// handed-over items are relational; immutable snapshots (policy,
// fulfillment, discounts, service credits) are JSONB encoded from the domain
// types. Every write names the version it read, so a concurrent change is
// reported as postgres.ErrStale instead of being overwritten.
package rentalspg

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/money"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/rentals"
)

// Version is the optimistic concurrency token of a stored booking; zero
// means not stored yet.
type Version int

const requestKeyIndex = "bookings_request_key"

const insertBooking = `
			INSERT INTO bookings (id, code, customer_id, request_key, status, currency, pickup_at, due_at,
				package_id, package_code, package_label, package_duration_hours,
				gross_rent_total, discount_total, rent_total, security_deposit_total, reservation_deposit,
				policy_version, policy, fulfillment, discounts, manual_adjustments, service_credits,
				open_modification_id, confirmed_modification_ids, self_service_size_changes,
				created_at, hold_expires_at, confirmed_at, preparation_started_at, handed_over_at, returned_at,
				inspection_completed_at, completed_at, cancelled_at, expired_at, cancel_reason, cancel_note,
				updated_at, version)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20,
				$21, $22, $23, $24, $25, $26, $27, $28, $29, $30, $31, $32, $33, $34, $35, $36, $37, $38, $39, 1)`

// SaveBooking inserts (version 0) or updates the booking and its children,
// returning the new version. requestKey is only used on insert.
func SaveBooking(ctx context.Context, q postgres.Querier, b *rentals.Booking, v Version, requestKey string, now time.Time) (Version, error) {
	snap, err := encodeSnapshots(b)
	if err != nil {
		return 0, err
	}
	var key *string
	if requestKey != "" {
		key = &requestKey
	}
	var cancelReason *rentals.CancelReason
	if b.CancelReason != "" {
		cancelReason = &b.CancelReason
	}
	var openMod *rentals.ModificationID
	if b.OpenModification != "" {
		openMod = &b.OpenModification
	}
	if b.Package.Duration%time.Hour != 0 {
		return 0, fmt.Errorf("booking %s package duration %s is not whole hours", b.ID, b.Package.Duration)
	}
	if v == 0 {
		_, err = q.Exec(ctx, insertBooking,
			b.ID, b.Code, b.CustomerID, key, b.Status, b.Currency, b.Schedule.PickupAt, b.Schedule.DueAt,
			b.Package.ID, b.Package.Code, b.Package.Label, int(b.Package.Duration/time.Hour),
			b.GrossRentTotal.Amount(), b.DiscountTotal.Amount(), b.RentTotal.Amount(),
			b.SecurityDepositTotal.Amount(), b.ReservationDeposit.Amount(),
			b.Policy.Version, snap.policy, snap.fulfillment, snap.discounts, snap.manual, snap.credits,
			openMod, modIDs(b.Modifications), b.SelfServiceSizeChanges,
			b.CreatedAt, b.HoldExpiresAt, b.ConfirmedAt, b.PreparationStartedAt, b.HandedOverAt, b.ReturnedAt,
			b.InspectionCompletedAt, b.CompletedAt, b.CancelledAt, b.ExpiredAt, cancelReason, b.CancelNote,
			now.UTC())
		if postgres.IsUniqueViolation(err, requestKeyIndex) {
			return 0, fmt.Errorf("booking request key: %w", postgres.ErrDuplicate)
		}
		if err != nil {
			return 0, fmt.Errorf("insert booking %s: %w", b.ID, err)
		}
	} else {
		tag, err := q.Exec(ctx, `
			UPDATE bookings SET status = $2, pickup_at = $3, due_at = $4,
				package_id = $5, package_code = $6, package_label = $7, package_duration_hours = $8,
				gross_rent_total = $9, discount_total = $10, rent_total = $11, security_deposit_total = $12,
				reservation_deposit = $13, policy_version = $14, policy = $15, fulfillment = $16, discounts = $17,
				manual_adjustments = $18, service_credits = $19, open_modification_id = $20,
				confirmed_modification_ids = $21, self_service_size_changes = $22, hold_expires_at = $23,
				confirmed_at = $24, preparation_started_at = $25, handed_over_at = $26, returned_at = $27,
				inspection_completed_at = $28, completed_at = $29, cancelled_at = $30, expired_at = $31,
				cancel_reason = $32, cancel_note = $33, updated_at = $34, version = version + 1
			WHERE id = $1 AND version = $35`,
			b.ID, b.Status, b.Schedule.PickupAt, b.Schedule.DueAt,
			b.Package.ID, b.Package.Code, b.Package.Label, int(b.Package.Duration/time.Hour),
			b.GrossRentTotal.Amount(), b.DiscountTotal.Amount(), b.RentTotal.Amount(),
			b.SecurityDepositTotal.Amount(), b.ReservationDeposit.Amount(),
			b.Policy.Version, snap.policy, snap.fulfillment, snap.discounts, snap.manual, snap.credits,
			openMod, modIDs(b.Modifications), b.SelfServiceSizeChanges, b.HoldExpiresAt,
			b.ConfirmedAt, b.PreparationStartedAt, b.HandedOverAt, b.ReturnedAt,
			b.InspectionCompletedAt, b.CompletedAt, b.CancelledAt, b.ExpiredAt, cancelReason, b.CancelNote,
			now.UTC(), int(v))
		if err != nil {
			return 0, fmt.Errorf("update booking %s: %w", b.ID, err)
		}
		if tag.RowsAffected() == 0 {
			return 0, fmt.Errorf("booking %s version %d: %w", b.ID, v, postgres.ErrStale)
		}
	}
	if err := saveLines(ctx, q, b); err != nil {
		return 0, err
	}
	if err := saveItems(ctx, q, b); err != nil {
		return 0, err
	}
	return v + 1, nil
}

type snapshots struct {
	policy, fulfillment, discounts, manual, credits string
}

func encodeSnapshots(b *rentals.Booking) (snapshots, error) {
	var s snapshots
	for _, f := range []struct {
		dst *string
		v   any
	}{{&s.policy, b.Policy}, {&s.fulfillment, b.Fulfillment}, {&s.discounts, b.Discounts},
		{&s.manual, b.ManualAdjustments}, {&s.credits, b.ServiceCredits}} {
		raw, err := json.Marshal(f.v)
		if err != nil {
			return snapshots{}, fmt.Errorf("encode booking %s snapshot: %w", b.ID, err)
		}
		*f.dst = string(raw)
	}
	return s, nil
}

func modIDs(ids []rentals.ModificationID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}

// saveLines replaces the line snapshot. Lines change only through a
// confirmed modification, whose own row keeps the before/after terms.
func saveLines(ctx context.Context, q postgres.Querier, b *rentals.Booking) error {
	ids := make([]string, len(b.Lines))
	for i, l := range b.Lines {
		ids[i] = string(l.ID)
	}
	if _, err := q.Exec(ctx, `DELETE FROM booking_lines WHERE booking_id = $1 AND NOT (line_id = ANY($2))`, b.ID, ids); err != nil {
		return fmt.Errorf("booking %s lines: %w", b.ID, err)
	}
	if _, err := q.Exec(ctx, `DELETE FROM booking_line_components WHERE booking_id = $1`, b.ID); err != nil {
		return fmt.Errorf("booking %s components: %w", b.ID, err)
	}
	for i, l := range b.Lines {
		if _, err := q.Exec(ctx, `
			INSERT INTO booking_lines (booking_id, line_id, position, kind, offering_id, offering_name, offering_version,
				quantity, unit_rent, list_rent, discount, rent_amount, unit_deposit, deposit_amount)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			ON CONFLICT (booking_id, line_id) DO UPDATE SET position = EXCLUDED.position, kind = EXCLUDED.kind,
				offering_id = EXCLUDED.offering_id, offering_name = EXCLUDED.offering_name,
				offering_version = EXCLUDED.offering_version, quantity = EXCLUDED.quantity,
				unit_rent = EXCLUDED.unit_rent, list_rent = EXCLUDED.list_rent, discount = EXCLUDED.discount,
				rent_amount = EXCLUDED.rent_amount, unit_deposit = EXCLUDED.unit_deposit,
				deposit_amount = EXCLUDED.deposit_amount`,
			b.ID, l.ID, i, l.Kind, l.OfferingID, l.OfferingName, l.OfferingVersion, l.Quantity,
			l.UnitRent.Amount(), l.ListRent.Amount(), l.Discount.Amount(), l.RentAmount.Amount(),
			l.UnitDeposit.Amount(), l.DepositAmount.Amount()); err != nil {
			return fmt.Errorf("booking %s line %s: %w", b.ID, l.ID, err)
		}
		for j, c := range l.Components {
			if _, err := q.Exec(ctx, `
				INSERT INTO booking_line_components (booking_id, line_id, component_key, position, item_id, variant_id,
					sku, quantity, buffer_class, unit_daily_rate)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
				b.ID, l.ID, c.Key, j, c.ItemID, c.VariantID, c.SKU, c.Quantity, c.BufferClass, c.UnitDailyRate.Amount()); err != nil {
				return fmt.Errorf("booking %s line %s component %s: %w", b.ID, l.ID, c.Key, err)
			}
		}
	}
	return nil
}

// saveItems upserts handed-over items; items are never removed (a
// replacement adds a new item and marks the old one).
func saveItems(ctx context.Context, q postgres.Querier, b *rentals.Booking) error {
	for i, it := range b.Items {
		var finding *string
		if it.Finding != nil {
			raw, err := json.Marshal(it.Finding)
			if err != nil {
				return fmt.Errorf("booking %s item %s finding: %w", b.ID, it.AssetID, err)
			}
			f := string(raw)
			finding = &f
		}
		if _, err := q.Exec(ctx, `
			INSERT INTO booking_items (booking_id, asset_id, position, line_id, component_key, variant_id, daily_rate,
				preexisting_issues, returned_at, received_at, missing_reported_at, lost_confirmed_at, carrier_liable,
				carrier_damage_reported, not_received, arrival_issue, collect_by_store, replacement_for, replaced_by, finding)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17,
				nullif($18, ''), nullif($19, ''), $20)
			ON CONFLICT (booking_id, asset_id) DO UPDATE SET position = EXCLUDED.position,
				preexisting_issues = EXCLUDED.preexisting_issues, returned_at = EXCLUDED.returned_at,
				received_at = EXCLUDED.received_at, missing_reported_at = EXCLUDED.missing_reported_at,
				lost_confirmed_at = EXCLUDED.lost_confirmed_at, carrier_liable = EXCLUDED.carrier_liable,
				carrier_damage_reported = EXCLUDED.carrier_damage_reported, not_received = EXCLUDED.not_received,
				arrival_issue = EXCLUDED.arrival_issue, collect_by_store = EXCLUDED.collect_by_store,
				replacement_for = EXCLUDED.replacement_for, replaced_by = EXCLUDED.replaced_by,
				finding = EXCLUDED.finding`,
			b.ID, it.AssetID, i, it.LineID, it.ComponentKey, it.VariantID, it.DailyRate.Amount(),
			it.PreexistingIssues, it.ReturnedAt, it.ReceivedAt, it.MissingReportedAt, it.LostConfirmedAt,
			it.CarrierLiable, it.CarrierDamageReported, it.NotReceived, it.ArrivalIssue, it.CollectByStore,
			it.ReplacementFor, it.ReplacedBy, finding); err != nil {
			return fmt.Errorf("booking %s item %s: %w", b.ID, it.AssetID, err)
		}
	}
	return nil
}

// LoadBooking reads a booking. With lock it is read FOR UPDATE, which every
// use case that changes the booking must do.
func LoadBooking(ctx context.Context, q postgres.Querier, id rentals.BookingID, lock bool) (*rentals.Booking, Version, error) {
	query := `SELECT id, code, customer_id, status, currency, pickup_at, due_at,
		package_id, package_code, package_label, package_duration_hours,
		gross_rent_total, discount_total, rent_total, security_deposit_total, reservation_deposit,
		policy, fulfillment, discounts, manual_adjustments, service_credits,
		coalesce(open_modification_id, ''), confirmed_modification_ids, self_service_size_changes,
		created_at, hold_expires_at, confirmed_at, preparation_started_at, handed_over_at, returned_at,
		inspection_completed_at, completed_at, cancelled_at, expired_at, coalesce(cancel_reason, ''), cancel_note, version
		FROM bookings WHERE id = $1`
	if lock {
		query += ` FOR UPDATE`
	}
	b := &rentals.Booking{}
	var hours, v int
	var gross, discount, rent, security, reservation int64
	var policy, fulfillment, discounts, manual, credits []byte
	var mods []string
	err := q.QueryRow(ctx, query, id).Scan(&b.ID, &b.Code, &b.CustomerID, &b.Status, &b.Currency,
		&b.Schedule.PickupAt, &b.Schedule.DueAt, &b.Package.ID, &b.Package.Code, &b.Package.Label, &hours,
		&gross, &discount, &rent, &security, &reservation,
		&policy, &fulfillment, &discounts, &manual, &credits,
		&b.OpenModification, &mods, &b.SelfServiceSizeChanges,
		&b.CreatedAt, &b.HoldExpiresAt, &b.ConfirmedAt, &b.PreparationStartedAt, &b.HandedOverAt, &b.ReturnedAt,
		&b.InspectionCompletedAt, &b.CompletedAt, &b.CancelledAt, &b.ExpiredAt, &b.CancelReason, &b.CancelNote, &v)
	if err != nil {
		return nil, 0, postgres.NotFound(err, fmt.Sprintf("booking %s", id))
	}
	b.Package.Duration = time.Duration(hours) * time.Hour
	b.Schedule.PickupAt, b.Schedule.DueAt = b.Schedule.PickupAt.UTC(), b.Schedule.DueAt.UTC()
	b.CreatedAt, b.HoldExpiresAt = b.CreatedAt.UTC(), b.HoldExpiresAt.UTC()
	for _, p := range []**time.Time{&b.ConfirmedAt, &b.PreparationStartedAt, &b.HandedOverAt, &b.ReturnedAt,
		&b.InspectionCompletedAt, &b.CompletedAt, &b.CancelledAt, &b.ExpiredAt} {
		*p = postgres.UTCPtr(*p)
	}
	amounts := []struct {
		dst *money.Money
		v   int64
	}{{&b.GrossRentTotal, gross}, {&b.DiscountTotal, discount}, {&b.RentTotal, rent},
		{&b.SecurityDepositTotal, security}, {&b.ReservationDeposit, reservation}}
	for _, a := range amounts {
		if *a.dst, err = money.New(a.v, b.Currency); err != nil {
			return nil, 0, fmt.Errorf("booking %s amounts: %w", id, err)
		}
	}
	for _, s := range []struct {
		raw []byte
		dst any
	}{{policy, &b.Policy}, {fulfillment, &b.Fulfillment}, {discounts, &b.Discounts}, {manual, &b.ManualAdjustments}, {credits, &b.ServiceCredits}} {
		if err := json.Unmarshal(s.raw, s.dst); err != nil {
			return nil, 0, fmt.Errorf("booking %s snapshot: %w", id, err)
		}
	}
	for _, m := range mods {
		b.Modifications = append(b.Modifications, rentals.ModificationID(m))
	}
	if b.Lines, err = loadLines(ctx, q, b.ID, b.Currency); err != nil {
		return nil, 0, err
	}
	if b.Items, err = loadItems(ctx, q, b.ID, b.Currency); err != nil {
		return nil, 0, err
	}
	return b, Version(v), nil
}

func loadLines(ctx context.Context, q postgres.Querier, id rentals.BookingID, cur money.Currency) ([]rentals.Line, error) {
	rows, err := q.Query(ctx, `SELECT line_id, kind, offering_id, offering_name, offering_version, quantity,
		unit_rent, list_rent, discount, rent_amount, unit_deposit, deposit_amount
		FROM booking_lines WHERE booking_id = $1 ORDER BY position`, id)
	if err != nil {
		return nil, fmt.Errorf("booking %s lines: %w", id, err)
	}
	lines, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (rentals.Line, error) {
		var l rentals.Line
		var unitRent, list, discount, rent, unitDeposit, deposit int64
		if err := r.Scan(&l.ID, &l.Kind, &l.OfferingID, &l.OfferingName, &l.OfferingVersion, &l.Quantity,
			&unitRent, &list, &discount, &rent, &unitDeposit, &deposit); err != nil {
			return l, err
		}
		return l, setAmounts(cur, []*money.Money{&l.UnitRent, &l.ListRent, &l.Discount, &l.RentAmount, &l.UnitDeposit, &l.DepositAmount},
			unitRent, list, discount, rent, unitDeposit, deposit)
	})
	if err != nil {
		return nil, fmt.Errorf("booking %s lines: %w", id, err)
	}

	rows, err = q.Query(ctx, `SELECT line_id, component_key, item_id, variant_id, sku, quantity, buffer_class, unit_daily_rate
		FROM booking_line_components WHERE booking_id = $1 ORDER BY line_id, position`, id)
	if err != nil {
		return nil, fmt.Errorf("booking %s components: %w", id, err)
	}
	type comp struct {
		line rentals.LineID
		c    catalog.ComponentDemand
	}
	comps, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (comp, error) {
		var x comp
		var rate int64
		if err := r.Scan(&x.line, &x.c.Key, &x.c.ItemID, &x.c.VariantID, &x.c.SKU, &x.c.Quantity, &x.c.BufferClass, &rate); err != nil {
			return x, err
		}
		var err error
		x.c.UnitDailyRate, err = money.New(rate, cur)
		return x, err
	})
	if err != nil {
		return nil, fmt.Errorf("booking %s components: %w", id, err)
	}
	for _, x := range comps {
		i := slices.IndexFunc(lines, func(l rentals.Line) bool { return l.ID == x.line })
		if i < 0 {
			return nil, fmt.Errorf("booking %s component of unknown line %s", id, x.line)
		}
		lines[i].Components = append(lines[i].Components, x.c)
	}
	return lines, nil
}

func loadItems(ctx context.Context, q postgres.Querier, id rentals.BookingID, cur money.Currency) ([]rentals.RentedItem, error) {
	rows, err := q.Query(ctx, `SELECT asset_id, line_id, component_key, variant_id, daily_rate, preexisting_issues,
		returned_at, received_at, missing_reported_at, lost_confirmed_at, carrier_liable, carrier_damage_reported,
		not_received, arrival_issue, collect_by_store, coalesce(replacement_for, ''), coalesce(replaced_by, ''), finding
		FROM booking_items WHERE booking_id = $1 ORDER BY position`, id)
	if err != nil {
		return nil, fmt.Errorf("booking %s items: %w", id, err)
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (rentals.RentedItem, error) {
		var it rentals.RentedItem
		var rate int64
		var finding []byte
		var replFor, replBy string
		if err := r.Scan(&it.AssetID, &it.LineID, &it.ComponentKey, &it.VariantID, &rate, &it.PreexistingIssues,
			&it.ReturnedAt, &it.ReceivedAt, &it.MissingReportedAt, &it.LostConfirmedAt, &it.CarrierLiable,
			&it.CarrierDamageReported, &it.NotReceived, &it.ArrivalIssue, &it.CollectByStore, &replFor, &replBy, &finding); err != nil {
			return it, err
		}
		it.ReplacementFor, it.ReplacedBy = inventory.AssetID(replFor), inventory.AssetID(replBy)
		it.ReturnedAt, it.ReceivedAt = postgres.UTCPtr(it.ReturnedAt), postgres.UTCPtr(it.ReceivedAt)
		it.MissingReportedAt, it.LostConfirmedAt = postgres.UTCPtr(it.MissingReportedAt), postgres.UTCPtr(it.LostConfirmedAt)
		if finding != nil {
			it.Finding = &rentals.InspectionFinding{}
			if err := json.Unmarshal(finding, it.Finding); err != nil {
				return it, err
			}
		}
		var err error
		it.DailyRate, err = money.New(rate, cur)
		return it, err
	})
	if err != nil {
		return nil, fmt.Errorf("booking %s items: %w", id, err)
	}
	if len(items) == 0 {
		return nil, nil // nothing handed over yet, as in a new booking
	}
	return items, nil
}

func setAmounts(cur money.Currency, dst []*money.Money, vals ...int64) error {
	for i, v := range vals {
		m, err := money.New(v, cur)
		if err != nil {
			return err
		}
		*dst[i] = m
	}
	return nil
}

// BookingIDByRequestKey finds the booking a customer already placed with a
// retry key.
func BookingIDByRequestKey(ctx context.Context, q postgres.Querier, customerID, requestKey string) (rentals.BookingID, bool, error) {
	var id rentals.BookingID
	err := q.QueryRow(ctx, `SELECT id FROM bookings WHERE customer_id = $1 AND request_key = $2`, customerID, requestKey).Scan(&id)
	if err == pgx.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("booking by request key: %w", err)
	}
	return id, true, nil
}

// ExpiredHolds lists pending bookings whose hold ended at or before now,
// oldest first. The caller expires each in its own transaction.
func ExpiredHolds(ctx context.Context, q postgres.Querier, now time.Time, limit int) ([]rentals.BookingID, error) {
	rows, err := q.Query(ctx, `SELECT id FROM bookings WHERE status = 'PENDING_CONFIRMATION' AND hold_expires_at <= $1
		ORDER BY hold_expires_at, id LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("expired holds: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[rentals.BookingID])
	if err != nil {
		return nil, fmt.Errorf("expired holds: %w", err)
	}
	return ids, nil
}

// AppendTransition writes a booking transition to the append-only history.
func AppendTransition(ctx context.Context, q postgres.Querier, t rentals.Transition) error {
	var from *rentals.Status
	if t.From != "" {
		from = &t.From
	}
	_, err := q.Exec(ctx, `
		INSERT INTO booking_transitions (booking_id, action, from_status, to_status, actor_kind, actor_id, at, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		t.BookingID, t.Action, from, t.To, t.Actor.Kind, t.Actor.ID, t.At.UTC(), t.Reason)
	if err != nil {
		return fmt.Errorf("booking %s transition %s: %w", t.BookingID, t.Action, err)
	}
	return nil
}
