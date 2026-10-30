package rentalspg

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/rentals"
)

// SlotKind separates counter appointments from courier windows.
type SlotKind string

const (
	SlotCounter  SlotKind = "COUNTER"
	SlotDelivery SlotKind = "DELIVERY"
)

// ReplaceSlots replaces the weekly slots of one kind.
func ReplaceSlots(ctx context.Context, q postgres.Querier, kind SlotKind, weekly map[time.Weekday][]rentals.Slot) error {
	if _, err := q.Exec(ctx, `DELETE FROM store_slots WHERE kind = $1`, kind); err != nil {
		return fmt.Errorf("replace %s slots: %w", kind, err)
	}
	for day, slots := range weekly {
		for _, s := range slots {
			if _, err := q.Exec(ctx, `INSERT INTO store_slots (kind, weekday, slot_id, starts_at, ends_at) VALUES ($1, $2, $3, $4::text::time, $5::text::time)`,
				kind, int(day), s.ID, s.Start.String(), s.End.String()); err != nil {
				return fmt.Errorf("replace %s slot %s: %w", kind, s.ID, err)
			}
		}
	}
	return nil
}

// AddClosure marks a local date as closed.
func AddClosure(ctx context.Context, q postgres.Querier, d businesstime.Date, reason string) error {
	if _, err := q.Exec(ctx, `INSERT INTO store_closures (closed_on, reason) VALUES ($1::text::date, $2) ON CONFLICT (closed_on) DO NOTHING`,
		d.String(), reason); err != nil {
		return fmt.Errorf("add closure %s: %w", d, err)
	}
	return nil
}

// LoadCalendar builds the store calendar from slots and closures. Dates and
// times are local to loc; the calendar validates overlaps on load.
func LoadCalendar(ctx context.Context, q postgres.Querier, loc *time.Location) (*rentals.StoreCalendar, error) {
	rows, err := q.Query(ctx, `SELECT kind, weekday, slot_id, to_char(starts_at, 'HH24:MI'), to_char(ends_at, 'HH24:MI')
		FROM store_slots ORDER BY kind, weekday, starts_at`)
	if err != nil {
		return nil, fmt.Errorf("load slots: %w", err)
	}
	weekly := map[SlotKind]map[time.Weekday][]rentals.Slot{SlotCounter: {}, SlotDelivery: {}}
	for rows.Next() {
		var kind SlotKind
		var day int
		var id, start, end string
		if err := rows.Scan(&kind, &day, &id, &start, &end); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan slot: %w", err)
		}
		s := rentals.Slot{ID: id}
		if s.Start, err = businesstime.ParseTimeOfDay(start); err != nil {
			rows.Close()
			return nil, err
		}
		if s.End, err = businesstime.ParseTimeOfDay(end); err != nil {
			rows.Close()
			return nil, err
		}
		weekly[kind][time.Weekday(day)] = append(weekly[kind][time.Weekday(day)], s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load slots: %w", err)
	}

	rows, err = q.Query(ctx, `SELECT to_char(closed_on, 'YYYY-MM-DD') FROM store_closures ORDER BY closed_on`)
	if err != nil {
		return nil, fmt.Errorf("load closures: %w", err)
	}
	days, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("load closures: %w", err)
	}
	closed := make([]businesstime.Date, 0, len(days))
	for _, d := range days {
		date, err := businesstime.ParseDate(d)
		if err != nil {
			return nil, err
		}
		closed = append(closed, date)
	}
	cal, err := rentals.NewStoreCalendar(loc, weekly[SlotCounter], closed)
	if err != nil {
		return nil, err
	}
	if err := cal.SetDeliveryWindows(weekly[SlotDelivery]); err != nil {
		return nil, err
	}
	return cal, nil
}
