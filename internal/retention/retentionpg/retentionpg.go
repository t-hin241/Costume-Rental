// Package retentionpg reads what decides how long personal data is kept and
// deletes it once the rules allow (decision D39).
package retentionpg

import (
	"context"
	"fmt"
	"time"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/rentals"
)

// profileFacts reads, for each customer profile, its last activity, what is
// still open and the finished bookings whose money the use case checks.
// GREATEST skips the NULLs of accounts that never booked or wrote. Staff
// profiles are not customer profiles and never come up.
const profileFacts = `
	SELECT p.user_id, u.email, p.full_name, p.phone,
		GREATEST(u.created_at, p.updated_at,
			(SELECT max(coalesce(s.last_seen_at, s.created_at)) FROM sessions s WHERE s.user_id = p.user_id),
			(SELECT max(coalesce(b.completed_at, b.cancelled_at, b.expired_at, b.created_at)) FROM bookings b WHERE b.customer_id = p.user_id),
			(SELECT max(m.at) FROM support_messages m WHERE m.author_id = p.user_id)) AS last_activity,
		(SELECT count(*) FROM bookings b WHERE b.customer_id = p.user_id
			AND b.status NOT IN ('COMPLETED', 'CANCELLED', 'EXPIRED')) AS open_bookings,
		(SELECT count(*) FROM support_requests r WHERE r.customer_id = p.user_id AND r.status <> 'RESOLVED') AS open_support,
		coalesce((SELECT array_agg(b.id ORDER BY b.id) FROM bookings b WHERE b.customer_id = p.user_id
			AND b.status IN ('COMPLETED', 'CANCELLED', 'EXPIRED')), '{}') AS finished
	FROM customer_profiles p
	JOIN users u ON u.id = p.user_id
	WHERE NOT EXISTS (SELECT 1 FROM user_roles r WHERE r.user_id = p.user_id AND r.role <> 'customer')`

// Profile is a customer profile with the facts read from the database.
type Profile struct {
	UserID       identity.UserID
	Contact      identity.Contact
	LastActivity time.Time
	OpenBookings int
	OpenSupport  int
	// Finished lists bookings that ended; the use case checks their money
	// and fees.
	Finished []rentals.BookingID
}

func scanProfiles(ctx context.Context, q postgres.Querier, sql string, args ...any) ([]Profile, int, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("read profiles for retention: %w", err)
	}
	defer rows.Close()
	var out []Profile
	total := 0
	for rows.Next() {
		var p Profile
		var finished []string
		if err := rows.Scan(&p.UserID, &p.Contact.Email, &p.Contact.Name, &p.Contact.Phone, &p.LastActivity,
			&p.OpenBookings, &p.OpenSupport, &finished, &total); err != nil {
			return nil, 0, fmt.Errorf("read profiles for retention: %w", err)
		}
		p.LastActivity = p.LastActivity.UTC()
		for _, id := range finished {
			p.Finished = append(p.Finished, rentals.BookingID(id))
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("read profiles for retention: %w", err)
	}
	return out, total, nil
}

// InactiveProfiles lists customer profiles whose account was last used
// before the given time, longest unused first.
func InactiveProfiles(ctx context.Context, q postgres.Querier, before time.Time, page, size int) ([]Profile, int, error) {
	return scanProfiles(ctx, q, `
		SELECT f.*, count(*) OVER () FROM (`+profileFacts+`) f
		WHERE f.last_activity < $1
		ORDER BY f.last_activity, f.user_id
		LIMIT $2 OFFSET $3`, before.UTC(), size, (page-1)*size)
}

// ProfileOf reads one customer profile; postgres.ErrNotFound when the user
// has none, or is staff.
func ProfileOf(ctx context.Context, q postgres.Querier, user identity.UserID) (Profile, error) {
	all, _, err := scanProfiles(ctx, q, `SELECT f.*, 1 FROM (`+profileFacts+` AND p.user_id = $1) f`, string(user))
	if err != nil {
		return Profile{}, err
	}
	if len(all) == 0 {
		return Profile{}, fmt.Errorf("customer profile of %s: %w", user, postgres.ErrNotFound)
	}
	return all[0], nil
}

// DeleteProfile removes a customer's name and phone. The account, its email
// and its bookings stay.
func DeleteProfile(ctx context.Context, q postgres.Querier, user identity.UserID) error {
	if _, err := q.Exec(ctx, `DELETE FROM customer_profiles WHERE user_id = $1`, string(user)); err != nil {
		return fmt.Errorf("delete customer profile of %s: %w", user, err)
	}
	return nil
}

// ResolvedRequest is a support conversation due for deletion.
type ResolvedRequest struct {
	ID, Code   string
	ResolvedAt time.Time
}

// ResolvedBefore locks up to limit conversations resolved before the given
// time, skipping those another pass holds.
func ResolvedBefore(ctx context.Context, q postgres.Querier, before time.Time, limit int) ([]ResolvedRequest, error) {
	rows, err := q.Query(ctx, `SELECT id, code, resolved_at FROM support_requests
		WHERE status = 'RESOLVED' AND resolved_at < $1
		ORDER BY resolved_at, id LIMIT $2 FOR UPDATE SKIP LOCKED`, before.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list resolved support requests: %w", err)
	}
	defer rows.Close()
	var out []ResolvedRequest
	for rows.Next() {
		var r ResolvedRequest
		if err := rows.Scan(&r.ID, &r.Code, &r.ResolvedAt); err != nil {
			return nil, fmt.Errorf("list resolved support requests: %w", err)
		}
		r.ResolvedAt = r.ResolvedAt.UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list resolved support requests: %w", err)
	}
	return out, nil
}

// DeleteSupportRequest removes a conversation with its messages.
func DeleteSupportRequest(ctx context.Context, q postgres.Querier, id string) error {
	if _, err := q.Exec(ctx, `DELETE FROM support_messages WHERE request_id = $1`, id); err != nil {
		return fmt.Errorf("delete messages of support request %s: %w", id, err)
	}
	if _, err := q.Exec(ctx, `DELETE FROM support_requests WHERE id = $1`, id); err != nil {
		return fmt.Errorf("delete support request %s: %w", id, err)
	}
	return nil
}
