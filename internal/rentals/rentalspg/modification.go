package rentalspg

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/rentals"
)

const openModificationIndex = "booking_modifications_one_open"

// SaveModification inserts (version 0) or updates a modification and
// returns the new version. The whole aggregate, including its before/after
// terms and allocation plan, is kept as a JSONB document.
func SaveModification(ctx context.Context, q postgres.Querier, m *rentals.Modification, v Version, now time.Time) (Version, error) {
	doc, err := json.Marshal(m)
	if err != nil {
		return 0, fmt.Errorf("encode modification %s: %w", m.ID, err)
	}
	if v == 0 {
		_, err = q.Exec(ctx, `
			INSERT INTO booking_modifications (id, booking_id, status, hold_expires_at, document, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			m.ID, m.BookingID, m.Status, m.HoldExpiresAt, string(doc), m.CreatedAt, now.UTC())
		if postgres.IsUniqueViolation(err, openModificationIndex) {
			return 0, fmt.Errorf("booking %s: %w", m.BookingID, rentals.ErrModificationOpen)
		}
		if err != nil {
			return 0, fmt.Errorf("insert modification %s: %w", m.ID, err)
		}
		return 1, nil
	}
	tag, err := q.Exec(ctx, `
		UPDATE booking_modifications SET status = $2, hold_expires_at = $3, document = $4, updated_at = $5,
			version = version + 1
		WHERE id = $1 AND version = $6`, m.ID, m.Status, m.HoldExpiresAt, string(doc), now.UTC(), int(v))
	if err != nil {
		return 0, fmt.Errorf("update modification %s: %w", m.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return 0, fmt.Errorf("modification %s version %d: %w", m.ID, v, postgres.ErrStale)
	}
	return v + 1, nil
}

// LoadModification reads a modification, FOR UPDATE when lock is set.
func LoadModification(ctx context.Context, q postgres.Querier, id rentals.ModificationID, lock bool) (*rentals.Modification, Version, error) {
	query := `SELECT document, version FROM booking_modifications WHERE id = $1`
	if lock {
		query += ` FOR UPDATE`
	}
	var doc []byte
	var v int
	if err := q.QueryRow(ctx, query, id).Scan(&doc, &v); err != nil {
		return nil, 0, postgres.NotFound(err, fmt.Sprintf("modification %s", id))
	}
	m := &rentals.Modification{}
	if err := json.Unmarshal(doc, m); err != nil {
		return nil, 0, fmt.Errorf("decode modification %s: %w", id, err)
	}
	return m, Version(v), nil
}

// ExpiredModificationHolds lists quoted modifications whose hold ended at or
// before now, with their booking.
func ExpiredModificationHolds(ctx context.Context, q postgres.Querier, now time.Time, limit int) ([]rentals.ModificationID, error) {
	rows, err := q.Query(ctx, `SELECT id FROM booking_modifications
		WHERE status IN ('QUOTED', 'AWAITING_PAYMENT') AND hold_expires_at <= $1
		ORDER BY hold_expires_at, id LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("expired modification holds: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[rentals.ModificationID])
	if err != nil {
		return nil, fmt.Errorf("expired modification holds: %w", err)
	}
	return ids, nil
}
