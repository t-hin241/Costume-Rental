package identitypg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/platform/postgres"
)

// TokenKind selects a single-use emailed token table.
type TokenKind string

const (
	TokenPasswordReset TokenKind = "password_reset_tokens"
	TokenEmailVerify   TokenKind = "email_verification_tokens"
)

// table returns the table name; kinds are constants, so the name is safe
// to put in SQL.
func (k TokenKind) table() (string, error) {
	switch k {
	case TokenPasswordReset, TokenEmailVerify:
		return string(k), nil
	}
	return "", fmt.Errorf("unknown token kind %q", k)
}

// InsertToken stores a token under its hash.
func InsertToken(ctx context.Context, q postgres.Querier, kind TokenKind, tokenHash []byte, id identity.UserID, now, expiresAt time.Time) error {
	table, err := kind.table()
	if err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `INSERT INTO `+table+` (token_hash, user_id, created_at, expires_at) VALUES ($1, $2, $3, $4)`,
		tokenHash, id, now.UTC(), expiresAt.UTC()); err != nil {
		return fmt.Errorf("insert %s for %s: %w", kind, id, err)
	}
	return nil
}

// OneTimeToken is a stored emailed token.
type OneTimeToken struct {
	UserID    identity.UserID
	ExpiresAt time.Time
	Used      bool
}

// TokenForUpdate locks a token so it can be used only once. Unknown tokens
// are postgres.ErrNotFound.
func TokenForUpdate(ctx context.Context, q postgres.Querier, kind TokenKind, tokenHash []byte) (OneTimeToken, error) {
	table, err := kind.table()
	if err != nil {
		return OneTimeToken{}, err
	}
	var t OneTimeToken
	err = q.QueryRow(ctx, `SELECT user_id, expires_at, used_at IS NOT NULL FROM `+table+` WHERE token_hash = $1 FOR UPDATE`, tokenHash).
		Scan(&t.UserID, &t.ExpiresAt, &t.Used)
	if errors.Is(err, pgx.ErrNoRows) {
		return OneTimeToken{}, fmt.Errorf("load %s: %w", kind, postgres.ErrNotFound)
	}
	if err != nil {
		return OneTimeToken{}, fmt.Errorf("load %s: %w", kind, err)
	}
	t.ExpiresAt = t.ExpiresAt.UTC()
	return t, nil
}

// ConsumeTokens marks every unused token of a kind for a user as used, so
// using one link invalidates older ones.
func ConsumeTokens(ctx context.Context, q postgres.Querier, kind TokenKind, id identity.UserID, now time.Time) error {
	table, err := kind.table()
	if err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `UPDATE `+table+` SET used_at = $2 WHERE user_id = $1 AND used_at IS NULL`, id, now.UTC()); err != nil {
		return fmt.Errorf("consume %s of %s: %w", kind, id, err)
	}
	return nil
}

// MarkEmailVerified records that the user proved control of the email; it
// keeps the first verification time.
func MarkEmailVerified(ctx context.Context, q postgres.Querier, id identity.UserID, now time.Time) error {
	if _, err := q.Exec(ctx, `UPDATE users SET email_verified_at = coalesce(email_verified_at, $2), updated_at = $2 WHERE id = $1`,
		id, now.UTC()); err != nil {
		return fmt.Errorf("mark email of %s verified: %w", id, err)
	}
	return nil
}

// MFA is a user's TOTP enrolment.
type MFA struct {
	SecretCiphertext []byte
	Enabled          bool
	LastUsedStep     int64
}

// LoadMFA reads the enrolment; none is postgres.ErrNotFound. forUpdate
// locks the row so a code is accepted once.
func LoadMFA(ctx context.Context, q postgres.Querier, id identity.UserID, forUpdate bool) (MFA, error) {
	query := `SELECT secret_ciphertext, enabled_at IS NOT NULL, last_used_step FROM user_mfa WHERE user_id = $1`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var m MFA
	if err := q.QueryRow(ctx, query, id).Scan(&m.SecretCiphertext, &m.Enabled, &m.LastUsedStep); err != nil {
		return MFA{}, postgres.NotFound(err, fmt.Sprintf("load mfa of %s", id))
	}
	return m, nil
}

// StartMFAEnrolment stores a new, not yet confirmed secret, replacing an
// earlier unconfirmed one. An enabled enrolment is never replaced.
func StartMFAEnrolment(ctx context.Context, q postgres.Querier, id identity.UserID, ciphertext []byte, now time.Time) error {
	tag, err := q.Exec(ctx, `INSERT INTO user_mfa (user_id, secret_ciphertext, created_at) VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET secret_ciphertext = EXCLUDED.secret_ciphertext, created_at = EXCLUDED.created_at,
			last_used_step = 0
		WHERE user_mfa.enabled_at IS NULL`, id, ciphertext, now.UTC())
	if err != nil {
		return fmt.Errorf("start mfa enrolment of %s: %w", id, err)
	}
	if tag.RowsAffected() != 1 {
		return identity.ErrMFAAlreadyEnabled
	}
	return nil
}

// RecordMFAUse stores the accepted time step and, when enable is set,
// confirms the enrolment.
func RecordMFAUse(ctx context.Context, q postgres.Querier, id identity.UserID, step int64, enable bool, now time.Time) error {
	if _, err := q.Exec(ctx, `UPDATE user_mfa SET last_used_step = greatest(last_used_step, $2),
		enabled_at = CASE WHEN $3 THEN coalesce(enabled_at, $4) ELSE enabled_at END WHERE user_id = $1`,
		id, step, enable, now.UTC()); err != nil {
		return fmt.Errorf("record mfa use of %s: %w", id, err)
	}
	return nil
}

// DeleteMFA removes the enrolment and recovery codes.
func DeleteMFA(ctx context.Context, q postgres.Querier, id identity.UserID) (bool, error) {
	if _, err := q.Exec(ctx, `DELETE FROM mfa_recovery_codes WHERE user_id = $1`, id); err != nil {
		return false, fmt.Errorf("delete recovery codes of %s: %w", id, err)
	}
	tag, err := q.Exec(ctx, `DELETE FROM user_mfa WHERE user_id = $1`, id)
	if err != nil {
		return false, fmt.Errorf("delete mfa of %s: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// ReplaceRecoveryCodes stores fresh recovery code hashes, dropping old ones.
func ReplaceRecoveryCodes(ctx context.Context, q postgres.Querier, id identity.UserID, hashes [][]byte, now time.Time) error {
	if _, err := q.Exec(ctx, `DELETE FROM mfa_recovery_codes WHERE user_id = $1`, id); err != nil {
		return fmt.Errorf("drop recovery codes of %s: %w", id, err)
	}
	for _, h := range hashes {
		if _, err := q.Exec(ctx, `INSERT INTO mfa_recovery_codes (user_id, code_hash, created_at) VALUES ($1, $2, $3)`,
			id, h, now.UTC()); err != nil {
			return fmt.Errorf("store recovery code of %s: %w", id, err)
		}
	}
	return nil
}

// UseRecoveryCode marks an unused code as used and reports whether it was.
func UseRecoveryCode(ctx context.Context, q postgres.Querier, id identity.UserID, hash []byte, now time.Time) (bool, error) {
	tag, err := q.Exec(ctx, `UPDATE mfa_recovery_codes SET used_at = $3 WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL`,
		id, hash, now.UTC())
	if err != nil {
		return false, fmt.Errorf("use recovery code of %s: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// Challenge is the pending second step of a login.
type Challenge struct {
	UserID    identity.UserID
	ExpiresAt time.Time
	Enroll    bool
	Attempts  int
	Used      bool
}

// InsertChallenge stores a login challenge under its token hash.
func InsertChallenge(ctx context.Context, q postgres.Querier, tokenHash []byte, id identity.UserID, enroll bool, now, expiresAt time.Time) error {
	if _, err := q.Exec(ctx, `INSERT INTO login_challenges (token_hash, user_id, created_at, expires_at, enroll)
		VALUES ($1, $2, $3, $4, $5)`, tokenHash, id, now.UTC(), expiresAt.UTC(), enroll); err != nil {
		return fmt.Errorf("insert login challenge for %s: %w", id, err)
	}
	return nil
}

// ChallengeForUpdate locks a challenge; unknown is postgres.ErrNotFound.
func ChallengeForUpdate(ctx context.Context, q postgres.Querier, tokenHash []byte) (Challenge, error) {
	var c Challenge
	err := q.QueryRow(ctx, `SELECT user_id, expires_at, enroll, attempts, used_at IS NOT NULL FROM login_challenges
		WHERE token_hash = $1 FOR UPDATE`, tokenHash).Scan(&c.UserID, &c.ExpiresAt, &c.Enroll, &c.Attempts, &c.Used)
	if err != nil {
		return Challenge{}, postgres.NotFound(err, "load login challenge")
	}
	c.ExpiresAt = c.ExpiresAt.UTC()
	return c, nil
}

// CountChallengeAttempt adds one attempt, and marks the challenge used when
// done is set.
func CountChallengeAttempt(ctx context.Context, q postgres.Querier, tokenHash []byte, done bool, now time.Time) error {
	if _, err := q.Exec(ctx, `UPDATE login_challenges SET attempts = attempts + 1,
		used_at = CASE WHEN $2 THEN $3 ELSE used_at END WHERE token_hash = $1`, tokenHash, done, now.UTC()); err != nil {
		return fmt.Errorf("count login challenge attempt: %w", err)
	}
	return nil
}

// SetSessionMFA records that a live session passed a second factor.
func SetSessionMFA(ctx context.Context, q postgres.Querier, id string, now time.Time) error {
	if _, err := q.Exec(ctx, `UPDATE sessions SET mfa_at = $2 WHERE id = $1 AND revoked_at IS NULL`, id, now.UTC()); err != nil {
		return fmt.Errorf("set mfa on session %s: %w", id, err)
	}
	return nil
}
