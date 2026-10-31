package identitypg

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/platform/postgres"
)

// Contacts returns how to address the given users.
func Contacts(ctx context.Context, q postgres.Querier, ids []identity.UserID) (map[identity.UserID]identity.Contact, error) {
	rows, err := q.Query(ctx, `SELECT u.id, u.email, coalesce(p.full_name, ''), coalesce(p.phone, '')
		FROM users u LEFT JOIN customer_profiles p ON p.user_id = u.id WHERE u.id = ANY($1)`, slices.Compact(slices.Sorted(slices.Values(ids))))
	if err != nil {
		return nil, fmt.Errorf("load contacts: %w", err)
	}
	defer rows.Close()
	out := make(map[identity.UserID]identity.Contact, len(ids))
	for rows.Next() {
		var id identity.UserID
		var c identity.Contact
		if err := rows.Scan(&id, &c.Email, &c.Name, &c.Phone); err != nil {
			return nil, fmt.Errorf("load contacts: %w", err)
		}
		out[id] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load contacts: %w", err)
	}
	return out, nil
}

// SaveProfile stores a user's name and phone.
func SaveProfile(ctx context.Context, q postgres.Querier, id identity.UserID, name, phone string, now time.Time) error {
	if _, err := q.Exec(ctx, `
		INSERT INTO customer_profiles (user_id, full_name, phone, updated_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id) DO UPDATE SET full_name = EXCLUDED.full_name, phone = EXCLUDED.phone, updated_at = EXCLUDED.updated_at`,
		id, name, phone, now.UTC()); err != nil {
		return fmt.Errorf("save profile of %s: %w", id, err)
	}
	return nil
}

// UserFilter narrows the user list; zero fields do not filter.
type UserFilter struct {
	Term     string
	Role     identity.Role
	Page     int
	PageSize int
}

// UserRow is one line of the user list.
type UserRow struct {
	User       identity.User
	Contact    identity.Contact
	MFAEnabled bool
	LastSeenAt *time.Time
}

// UserPage lists users, newest first, with the total matching.
func UserPage(ctx context.Context, q postgres.Querier, f UserFilter) ([]UserRow, int, error) {
	term := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Term)
	rows, err := q.Query(ctx, `
		SELECT u.id, u.email, u.status, u.created_at, coalesce(p.full_name, ''), coalesce(p.phone, ''),
			coalesce(m.enabled_at IS NOT NULL, false),
			(SELECT max(s.last_seen_at) FROM sessions s WHERE s.user_id = u.id),
			coalesce((SELECT array_agg(r.role ORDER BY r.role) FROM user_roles r WHERE r.user_id = u.id), '{}'),
			count(*) OVER ()
		FROM users u
		LEFT JOIN customer_profiles p ON p.user_id = u.id
		LEFT JOIN user_mfa m ON m.user_id = u.id
		WHERE ($1 = '' OR EXISTS (SELECT 1 FROM user_roles r WHERE r.user_id = u.id AND r.role = $1))
			AND ($2 = '' OR u.email ILIKE '%' || $2 || '%' OR p.full_name ILIKE '%' || $2 || '%' OR p.phone ILIKE '%' || $2 || '%')
		ORDER BY u.created_at DESC, u.id
		LIMIT $3 OFFSET $4`, string(f.Role), term, f.PageSize, (f.Page-1)*f.PageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	var out []UserRow
	total := 0
	for rows.Next() {
		var r UserRow
		if err := rows.Scan(&r.User.ID, &r.User.Email, &r.User.Status, &r.User.CreatedAt, &r.Contact.Name, &r.Contact.Phone,
			&r.MFAEnabled, &r.LastSeenAt, &r.User.Roles, &total); err != nil {
			return nil, 0, fmt.Errorf("list users: %w", err)
		}
		r.User.CreatedAt, r.LastSeenAt = postgres.UTC(r.User.CreatedAt), postgres.UTCPtr(r.LastSeenAt)
		r.Contact.Email = r.User.Email
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}
	return out, total, nil
}
