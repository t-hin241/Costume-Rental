package rentalspg

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/rentals"
)

// BookingFilter narrows a booking list; zero fields do not filter.
type BookingFilter struct {
	Term     string
	Status   rentals.Status
	Customer identity.UserID
	// Finished, when set, keeps only finished (true) or live (false) bookings.
	Finished *bool
	From, To *time.Time
	Page     int
	PageSize int
}

// EscapeLike makes user text match literally inside a LIKE pattern.
func EscapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// BookingPage lists booking ids, newest first, with the total matching.
func BookingPage(ctx context.Context, q postgres.Querier, f BookingFilter) ([]rentals.BookingID, int, error) {
	rows, err := q.Query(ctx, `
		SELECT b.id, count(*) OVER ()
		FROM bookings b
		JOIN users u ON u.id = b.customer_id
		LEFT JOIN customer_profiles p ON p.user_id = b.customer_id
		WHERE ($1 = '' OR b.status = $1)
			AND ($2 = '' OR b.customer_id = $2)
			AND ($3::boolean IS NULL OR (b.status IN ('COMPLETED', 'CANCELLED', 'EXPIRED')) = $3)
			AND ($4 = '' OR b.code ILIKE '%' || $4 || '%' OR u.email ILIKE '%' || $4 || '%'
				OR p.full_name ILIKE '%' || $4 || '%' OR p.phone ILIKE '%' || $4 || '%')
			AND ($5::timestamptz IS NULL OR b.pickup_at >= $5)
			AND ($6::timestamptz IS NULL OR b.pickup_at < $6)
		ORDER BY b.created_at DESC, b.id
		LIMIT $7 OFFSET $8`,
		string(f.Status), string(f.Customer), f.Finished, EscapeLike(f.Term), f.From, f.To, f.PageSize, (f.Page-1)*f.PageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("list bookings: %w", err)
	}
	defer rows.Close()
	var ids []rentals.BookingID
	total := 0
	for rows.Next() {
		var id rentals.BookingID
		if err := rows.Scan(&id, &total); err != nil {
			return nil, 0, fmt.Errorf("list bookings: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list bookings: %w", err)
	}
	return ids, total, nil
}

// TransitionRow is one entry of a booking's history.
type TransitionRow struct {
	Action string
	To     rentals.Status
	Actor  identity.Actor
	At     time.Time
	Reason string
}

// Transitions reads a booking's history, latest first.
func Transitions(ctx context.Context, q postgres.Querier, id rentals.BookingID) ([]TransitionRow, error) {
	rows, err := q.Query(ctx, `SELECT action, to_status, actor_kind, actor_id, at, reason
		FROM booking_transitions WHERE booking_id = $1 ORDER BY at DESC, id DESC`, id)
	if err != nil {
		return nil, fmt.Errorf("booking %s history: %w", id, err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (TransitionRow, error) {
		var t TransitionRow
		err := r.Scan(&t.Action, &t.To, &t.Actor.Kind, &t.Actor.ID, &t.At, &t.Reason)
		t.At = t.At.UTC()
		return t, err
	})
	if err != nil {
		return nil, fmt.Errorf("booking %s history: %w", id, err)
	}
	return out, nil
}

// OpsCounts are the numbers on the daily operations board.
type OpsCounts struct {
	PendingConfirmation int
	ToPrepare           int
	HandoversToday      int
	ReturnsToday        int
	InspectionsOpen     int
	AssetsBlocked       int
}

// CountOps counts today's work for a business day [dayStart, dayEnd).
func CountOps(ctx context.Context, q postgres.Querier, dayStart, dayEnd time.Time) (OpsCounts, error) {
	var c OpsCounts
	err := q.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status = 'PENDING_CONFIRMATION'),
			count(*) FILTER (WHERE status = 'CONFIRMED'),
			count(*) FILTER (WHERE status IN ('CONFIRMED', 'PREPARING') AND pickup_at >= $1 AND pickup_at < $2),
			count(*) FILTER (WHERE status = 'RENTED' AND due_at >= $1 AND due_at < $2),
			count(*) FILTER (WHERE status = 'RETURN_INSPECTION'),
			(SELECT count(*) FROM assets WHERE status NOT IN ('READY', 'RENTED', 'RETIRED'))
		FROM bookings WHERE status NOT IN ('COMPLETED', 'CANCELLED', 'EXPIRED')`, dayStart.UTC(), dayEnd.UTC()).
		Scan(&c.PendingConfirmation, &c.ToPrepare, &c.HandoversToday, &c.ReturnsToday, &c.InspectionsOpen, &c.AssetsBlocked)
	if err != nil {
		return OpsCounts{}, fmt.Errorf("count operations: %w", err)
	}
	return c, nil
}

// WorkQueue lists bookings waiting for a staff step, soonest first.
func WorkQueue(ctx context.Context, q postgres.Querier, limit int) ([]rentals.BookingID, error) {
	rows, err := q.Query(ctx, `SELECT id FROM bookings
		WHERE status IN ('PENDING_CONFIRMATION', 'CONFIRMED', 'PREPARING', 'RETURN_INSPECTION')
		ORDER BY pickup_at, id LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("work queue: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[rentals.BookingID])
	if err != nil {
		return nil, fmt.Errorf("work queue: %w", err)
	}
	return ids, nil
}

// ScheduleRow is a handover or a return due in a period.
type ScheduleRow struct {
	ID       rentals.BookingID
	Handover bool
	At       time.Time
}

// Schedule lists handovers (booked, not yet handed over) and returns (rented)
// falling in [from, to), earliest first.
func Schedule(ctx context.Context, q postgres.Querier, from, to time.Time, limit int) ([]ScheduleRow, error) {
	rows, err := q.Query(ctx, `
		SELECT id, true, pickup_at FROM bookings
			WHERE status IN ('PENDING_CONFIRMATION', 'CONFIRMED', 'PREPARING') AND pickup_at >= $1 AND pickup_at < $2
		UNION ALL
		SELECT id, false, due_at FROM bookings WHERE status = 'RENTED' AND due_at >= $1 AND due_at < $2
		ORDER BY 3, 1 LIMIT $3`, from.UTC(), to.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("schedule: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ScheduleRow, error) {
		var s ScheduleRow
		err := r.Scan(&s.ID, &s.Handover, &s.At)
		s.At = s.At.UTC()
		return s, err
	})
	if err != nil {
		return nil, fmt.Errorf("schedule: %w", err)
	}
	return out, nil
}

// ClosureReasons returns why the store is closed on particular dates, keyed by
// YYYY-MM-DD for one-off closures and MM-DD for yearly ones.
func ClosureReasons(ctx context.Context, q postgres.Querier) (map[string]string, error) {
	rows, err := q.Query(ctx, `
		SELECT to_char(closed_on, 'YYYY-MM-DD'), reason FROM store_closures
		UNION ALL SELECT lpad(month::text, 2, '0') || '-' || lpad(day::text, 2, '0'), reason FROM store_recurring_closures`)
	if err != nil {
		return nil, fmt.Errorf("closure reasons: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("closure reasons: %w", err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("closure reasons: %w", err)
	}
	return out, nil
}
