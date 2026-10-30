package identitypg

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/platform/postgres"
)

// InsertUser stores a new user and its roles. A taken email (compared
// case-insensitively) is postgres.ErrDuplicate.
func InsertUser(ctx context.Context, q postgres.Querier, u identity.User, passwordHash string, by identity.Actor) error {
	_, err := q.Exec(ctx, `INSERT INTO users (id, email, password_hash, status, email_verified_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $6)`, u.ID, u.Email, passwordHash, u.Status, u.EmailVerifiedAt, u.CreatedAt)
	if postgres.IsUniqueViolation(err, "users_email_key") {
		return fmt.Errorf("insert user: email taken: %w", postgres.ErrDuplicate)
	}
	if err != nil {
		return fmt.Errorf("insert user %s: %w", u.ID, err)
	}
	for _, r := range u.Roles {
		if err := insertRole(ctx, q, u.ID, r, by, u.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}

// Credentials is what a login needs. PasswordHash is empty for accounts
// that have no password yet.
type Credentials struct {
	UserID        identity.UserID
	Email         string
	Status        identity.UserStatus
	EmailVerified bool
	PasswordHash  string
}

// CredentialsByEmail looks a user up by normalized email.
func CredentialsByEmail(ctx context.Context, q postgres.Querier, email string) (Credentials, error) {
	return credentials(ctx, q, `lower(email) = lower($1)`, email)
}

// CredentialsByID looks a user up by id.
func CredentialsByID(ctx context.Context, q postgres.Querier, id identity.UserID) (Credentials, error) {
	return credentials(ctx, q, `id = $1`, id)
}

func credentials(ctx context.Context, q postgres.Querier, where string, arg any) (Credentials, error) {
	var c Credentials
	var hash *string
	err := q.QueryRow(ctx, `SELECT id, email, status, email_verified_at IS NOT NULL, password_hash FROM users WHERE `+where, arg).
		Scan(&c.UserID, &c.Email, &c.Status, &c.EmailVerified, &hash)
	if err != nil {
		return Credentials{}, postgres.NotFound(err, "load credentials")
	}
	if hash != nil {
		c.PasswordHash = *hash
	}
	return c, nil
}

// SetPasswordHash replaces a user's password hash.
func SetPasswordHash(ctx context.Context, q postgres.Querier, id identity.UserID, hash string, now time.Time) error {
	tag, err := q.Exec(ctx, `UPDATE users SET password_hash = $2, updated_at = $3 WHERE id = $1`, id, hash, now.UTC())
	if err != nil {
		return fmt.Errorf("set password of %s: %w", id, err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("set password of %s: %w", id, postgres.ErrNotFound)
	}
	return nil
}

// LoadUser reads a user and its roles; forUpdate locks the user row so
// concurrent access changes to the same user serialise.
func LoadUser(ctx context.Context, q postgres.Querier, id identity.UserID, forUpdate bool) (identity.User, error) {
	query := `SELECT id, email, status, email_verified_at, created_at FROM users WHERE id = $1`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var u identity.User
	if err := q.QueryRow(ctx, query, id).Scan(&u.ID, &u.Email, &u.Status, &u.EmailVerifiedAt, &u.CreatedAt); err != nil {
		return identity.User{}, postgres.NotFound(err, fmt.Sprintf("load user %s", id))
	}
	u.CreatedAt, u.EmailVerifiedAt = postgres.UTC(u.CreatedAt), postgres.UTCPtr(u.EmailVerifiedAt)
	rows, err := q.Query(ctx, `SELECT role FROM user_roles WHERE user_id = $1 ORDER BY role`, id)
	if err != nil {
		return identity.User{}, fmt.Errorf("load roles of %s: %w", id, err)
	}
	if u.Roles, err = pgx.CollectRows(rows, pgx.RowTo[identity.Role]); err != nil {
		return identity.User{}, fmt.Errorf("load roles of %s: %w", id, err)
	}
	return u, nil
}

// SaveAccess writes the status and role differences between before and
// after, recording by as the granter of new roles.
func SaveAccess(ctx context.Context, q postgres.Querier, before, after identity.User, by identity.Actor, now time.Time) error {
	if before.Status != after.Status {
		if _, err := q.Exec(ctx, `UPDATE users SET status = $2, updated_at = $3 WHERE id = $1`, after.ID, after.Status, now.UTC()); err != nil {
			return fmt.Errorf("save status of %s: %w", after.ID, err)
		}
	}
	for _, r := range after.Roles {
		if !slices.Contains(before.Roles, r) {
			if err := insertRole(ctx, q, after.ID, r, by, now); err != nil {
				return err
			}
		}
	}
	for _, r := range before.Roles {
		if !slices.Contains(after.Roles, r) {
			if _, err := q.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1 AND role = $2`, after.ID, r); err != nil {
				return fmt.Errorf("revoke role %s of %s: %w", r, after.ID, err)
			}
		}
	}
	return nil
}

func insertRole(ctx context.Context, q postgres.Querier, id identity.UserID, r identity.Role, by identity.Actor, now time.Time) error {
	if _, err := q.Exec(ctx, `INSERT INTO user_roles (user_id, role, granted_at, granted_by_kind, granted_by_id)
		VALUES ($1, $2, $3, $4, $5)`, id, r, now.UTC(), by.Kind, by.ID); err != nil {
		return fmt.Errorf("grant role %s to %s: %w", r, id, err)
	}
	return nil
}

// CountActiveAdmins counts unlocked users holding the admin role.
func CountActiveAdmins(ctx context.Context, q postgres.Querier) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM user_roles r JOIN users u ON u.id = r.user_id
		WHERE r.role = 'admin' AND u.status = 'ACTIVE'`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count admins: %w", err)
	}
	return n, nil
}

// LockAdminSet takes a transaction-scoped advisory lock that serialises
// every change able to remove or add an administrator, so two admins
// cannot demote each other at the same time and leave none.
func LockAdminSet(ctx context.Context, q postgres.Querier) error {
	if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('identity.admin_set'))`); err != nil {
		return fmt.Errorf("lock admin set: %w", err)
	}
	return nil
}

// InsertSession stores a new session under the hash of its token.
func InsertSession(ctx context.Context, q postgres.Querier, s identity.Session, tokenHash []byte) error {
	_, err := q.Exec(ctx, `INSERT INTO sessions (id, token_hash, user_id, created_at, expires_at, last_seen_at, mfa_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, s.ID, tokenHash, s.UserID, s.CreatedAt, s.ExpiresAt, s.LastSeenAt, s.MFAAt)
	if err != nil {
		return fmt.Errorf("insert session for %s: %w", s.UserID, err)
	}
	return nil
}

// SessionByTokenHash finds a session, live or not, by token hash.
func SessionByTokenHash(ctx context.Context, q postgres.Querier, tokenHash []byte) (identity.Session, error) {
	var s identity.Session
	var lastSeen *time.Time
	err := q.QueryRow(ctx, `SELECT id, user_id, created_at, expires_at, last_seen_at, revoked_at, mfa_at
		FROM sessions WHERE token_hash = $1`, tokenHash).
		Scan(&s.ID, &s.UserID, &s.CreatedAt, &s.ExpiresAt, &lastSeen, &s.RevokedAt, &s.MFAAt)
	if err != nil {
		return identity.Session{}, postgres.NotFound(err, "load session")
	}
	s.CreatedAt, s.ExpiresAt = postgres.UTC(s.CreatedAt), postgres.UTC(s.ExpiresAt)
	s.RevokedAt, s.MFAAt = postgres.UTCPtr(s.RevokedAt), postgres.UTCPtr(s.MFAAt)
	s.LastSeenAt = s.CreatedAt
	if lastSeen != nil {
		s.LastSeenAt = lastSeen.UTC()
	}
	return s, nil
}

// TouchSession records activity on a live session.
func TouchSession(ctx context.Context, q postgres.Querier, id string, now time.Time) error {
	if _, err := q.Exec(ctx, `UPDATE sessions SET last_seen_at = $2 WHERE id = $1 AND revoked_at IS NULL`, id, now.UTC()); err != nil {
		return fmt.Errorf("touch session %s: %w", id, err)
	}
	return nil
}

// RevokeSession ends one session and reports whether it was still live.
func RevokeSession(ctx context.Context, q postgres.Querier, id string, reason identity.RevokeReason, now time.Time) (bool, error) {
	tag, err := q.Exec(ctx, `UPDATE sessions SET revoked_at = $2, revoke_reason = $3 WHERE id = $1 AND revoked_at IS NULL`,
		id, now.UTC(), reason)
	if err != nil {
		return false, fmt.Errorf("revoke session %s: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// RevokeUserSessions ends every live session of a user except keep (which
// may be empty) and returns how many were ended.
func RevokeUserSessions(ctx context.Context, q postgres.Querier, id identity.UserID, keep string, reason identity.RevokeReason, now time.Time) (int64, error) {
	tag, err := q.Exec(ctx, `UPDATE sessions SET revoked_at = $3, revoke_reason = $4
		WHERE user_id = $1 AND id <> $2 AND revoked_at IS NULL`, id, keep, now.UTC(), reason)
	if err != nil {
		return 0, fmt.Errorf("revoke sessions of %s: %w", id, err)
	}
	return tag.RowsAffected(), nil
}
