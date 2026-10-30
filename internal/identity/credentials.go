package identity

import (
	"fmt"
	"slices"
	"time"
	"unicode/utf8"

	"webee_rentor/internal/domainerr"
)

var (
	// ErrInvalidCredentials is the single answer to every failed login, so
	// a response never tells whether the email exists, the password was
	// wrong or the account is locked.
	ErrInvalidCredentials = domainerr.New(domainerr.KindForbidden, "identity.invalid_credentials", "email or password is incorrect")
	ErrInvalidResetToken  = domainerr.New(domainerr.KindInvalid, "identity.invalid_reset_token", "the reset link is invalid or has expired")
	ErrAdminExists        = domainerr.New(domainerr.KindConflict, "identity.admin_exists", "an administrator already exists")
	ErrLastAdmin          = domainerr.New(domainerr.KindInvalidState, "identity.last_admin", "at least one active administrator must remain")
	ErrInvalidAuthConfig  = domainerr.New(domainerr.KindInvalid, "identity.invalid_auth_config", "authentication configuration violates a security guardrail")
)

const (
	// minPasswordFloor and MaxPasswordLength are guardrails no
	// configuration may relax. The ceiling bounds the hashing cost of
	// hostile input while allowing passphrases.
	minPasswordFloor  = 8
	MaxPasswordLength = 128
)

// CredentialPolicy is the configurable part of the password rules
// (decision D23: 12 by default, configurable).
type CredentialPolicy struct {
	MinPasswordLength int
}

// Validate refuses a policy below the guardrail floor.
func (c CredentialPolicy) Validate() error {
	if c.MinPasswordLength < minPasswordFloor || c.MinPasswordLength > MaxPasswordLength {
		return fmt.Errorf("min password length %d outside [%d, %d]: %w", c.MinPasswordLength, minPasswordFloor, MaxPasswordLength, ErrInvalidAuthConfig)
	}
	return nil
}

// CheckPassword validates a new password's length in characters. The
// password is used exactly as typed: no trimming or normalisation, and no
// composition rules (NIST SP 800-63B).
func (c CredentialPolicy) CheckPassword(field, password string) error {
	if !utf8.ValidString(password) {
		return domainerr.Invalid(field, "must be valid UTF-8")
	}
	if n := utf8.RuneCountInString(password); n < c.MinPasswordLength || n > MaxPasswordLength {
		return domainerr.Invalid(field, fmt.Sprintf("must be %d to %d characters", c.MinPasswordLength, MaxPasswordLength))
	}
	return nil
}

// SessionLimits bound one session: an absolute lifetime from login and an
// idle timeout since the last request.
type SessionLimits struct {
	TTL  time.Duration
	Idle time.Duration
}

// SessionPolicy gives staff, who can move money and inventory, shorter
// sessions than customers (decision D23).
type SessionPolicy struct {
	Customer SessionLimits
	Staff    SessionLimits
}

// Validate requires positive limits with idle no longer than the lifetime,
// and staff sessions no longer than customer ones.
func (p SessionPolicy) Validate() error {
	for name, l := range map[string]SessionLimits{"customer": p.Customer, "staff": p.Staff} {
		if l.TTL <= 0 || l.Idle <= 0 || l.Idle > l.TTL {
			return fmt.Errorf("%s session limits %v/%v: %w", name, l.TTL, l.Idle, ErrInvalidAuthConfig)
		}
	}
	if p.Staff.TTL > p.Customer.TTL || p.Staff.Idle > p.Customer.Idle {
		return fmt.Errorf("staff sessions must not outlive customer sessions: %w", ErrInvalidAuthConfig)
	}
	return nil
}

// For returns the limits for a user holding roles: any role besides
// customer counts as staff.
func (p SessionPolicy) For(roles []Role) SessionLimits {
	if slices.ContainsFunc(roles, func(r Role) bool { return r != RoleCustomer }) {
		return p.Staff
	}
	return p.Customer
}

// RevokeReason records why a session ended.
type RevokeReason string

const (
	RevokeLogout         RevokeReason = "logout"
	RevokeLoginRotation  RevokeReason = "login_rotation" // a new login replaced the session on this device
	RevokeAccessChange   RevokeReason = "access_change"  // role change or account lock
	RevokePasswordChange RevokeReason = "password_change"
	RevokePasswordReset  RevokeReason = "password_reset"
	RevokeMFAChange      RevokeReason = "mfa_change" // second factor enabled or reset
)

// Session is a server-side login. Only a hash of its bearer token is
// stored, never the token itself.
type Session struct {
	ID         string
	UserID     UserID
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
	RevokedAt  *time.Time
	// MFAAt is when the login passed a second factor; nil = password only.
	MFAAt *time.Time
}

// NewSession starts a session for a user holding roles. mfa records whether
// the login passed a second factor.
func NewSession(id string, user UserID, roles []Role, policy SessionPolicy, mfa bool, now time.Time) Session {
	now = now.UTC()
	s := Session{ID: id, UserID: user, CreatedAt: now, ExpiresAt: now.Add(policy.For(roles).TTL), LastSeenAt: now}
	if mfa {
		s.MFAAt = &now
	}
	return s
}

// Check fails with ErrNotPrincipal unless the session is live at now for a
// user currently holding roles: not revoked, expired or idle (the idle
// timeout follows the current roles), and with a second factor when the
// roles require one, so a role granted mid-session cannot ride on a
// password-only login.
func (s Session) Check(now time.Time, roles []Role, policy SessionPolicy) error {
	if s.RevokedAt != nil || !now.Before(s.ExpiresAt) || !now.Before(s.LastSeenAt.Add(policy.For(roles).Idle)) {
		return fmt.Errorf("session %s is not live: %w", s.ID, ErrNotPrincipal)
	}
	if RequiresMFA(roles) && s.MFAAt == nil {
		return fmt.Errorf("session %s lacks the second factor its roles require: %w", s.ID, ErrNotPrincipal)
	}
	return nil
}
