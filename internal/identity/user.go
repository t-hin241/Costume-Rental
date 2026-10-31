package identity

import (
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"time"

	"webee_rentor/internal/domainerr"
)

// UserStatus is the account state.
type UserStatus string

// Contact is how staff address a customer. Name and Phone are empty until the
// customer fills in their profile.
type Contact struct {
	Name  string
	Email string
	Phone string
}

// ValidPhone accepts what people write as a phone number: 8 to 15 digits with
// the usual separators.
func ValidPhone(s string) bool {
	digits := 0
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '+' && i == 0, r == ' ', r == '-', r == '.', r == '(', r == ')':
		default:
			return false
		}
	}
	return digits >= 8 && digits <= 15
}

// Label names the person for lists and history.
func (c Contact) Label() string {
	if c.Name != "" {
		return c.Name
	}
	return c.Email
}

const (
	UserActive UserStatus = "ACTIVE"
	UserLocked UserStatus = "LOCKED"
)

var (
	ErrInvalidEmail     = domainerr.New(domainerr.KindInvalid, "identity.invalid_email", "email address is invalid")
	ErrSelfRoleChange   = domainerr.New(domainerr.KindForbidden, "identity.self_role_change", "users cannot change their own roles")
	ErrAdminGrantNeeded = domainerr.New(domainerr.KindForbidden, "identity.admin_required", "only an admin can grant or revoke the admin role")
	ErrSelfLock         = domainerr.New(domainerr.KindForbidden, "identity.self_lock", "users cannot lock their own account")
	ErrReasonRequired   = domainerr.New(domainerr.KindInvalid, "identity.reason_required", "a reason is required")
)

const maxEmailLength = 254

// NormalizeEmail trims and lower-cases an address and validates its form.
// Uniqueness is enforced on the normalized value by the repository.
func NormalizeEmail(raw string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(raw))
	if e == "" || len(e) > maxEmailLength {
		return "", ErrInvalidEmail
	}
	addr, err := mail.ParseAddress(e)
	if err != nil || addr.Address != e || addr.Name != "" {
		return "", ErrInvalidEmail
	}
	return e, nil
}

// User is an account. Password hashing and sessions live in adapters; the
// domain only decides when sessions must be revoked.
type User struct {
	ID     UserID
	Email  string
	Status UserStatus
	Roles  []Role
	// EmailVerifiedAt is nil until the owner of Email confirms it; login
	// is refused until then (decision D23).
	EmailVerifiedAt *time.Time
	CreatedAt       time.Time
}

// NewCustomer registers a user with the customer role.
func NewCustomer(id UserID, email string, now time.Time) (User, error) {
	if id == "" {
		return User{}, domainerr.Invalid("id", "required")
	}
	normalized, err := NormalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	return User{
		ID:        id,
		Email:     normalized,
		Status:    UserActive,
		Roles:     []Role{RoleCustomer},
		CreatedAt: now.UTC(),
	}, nil
}

// AccountChange describes an access change for audit. When
// RevokeSessions is true the use case must invalidate the user's sessions.
type AccountChange struct {
	UserID         UserID
	Action         string
	Role           Role
	Actor          Actor
	At             time.Time
	Reason         string
	Changed        bool
	RevokeSessions bool
}

// GrantRole adds a role. Users cannot change their own roles, and only an
// admin can grant admin, which prevents self-escalation through normal APIs.
func (u *User) GrantRole(actor Principal, role Role, reason string, now time.Time) (AccountChange, error) {
	if err := u.authorizeRoleChange(actor, role, reason); err != nil {
		return AccountChange{}, err
	}
	change := AccountChange{UserID: u.ID, Action: "user.role_grant", Role: role, Actor: actor.Actor(), At: now.UTC(), Reason: reason}
	if slices.Contains(u.Roles, role) {
		return change, nil
	}
	u.Roles = append(u.Roles, role)
	change.Changed, change.RevokeSessions = true, true
	return change, nil
}

// RevokeRole removes a role. Keeping at least one admin is a cross-user
// invariant enforced by the use case.
func (u *User) RevokeRole(actor Principal, role Role, reason string, now time.Time) (AccountChange, error) {
	if err := u.authorizeRoleChange(actor, role, reason); err != nil {
		return AccountChange{}, err
	}
	change := AccountChange{UserID: u.ID, Action: "user.role_revoke", Role: role, Actor: actor.Actor(), At: now.UTC(), Reason: reason}
	idx := slices.Index(u.Roles, role)
	if idx < 0 {
		return change, nil
	}
	u.Roles = slices.Delete(u.Roles, idx, idx+1)
	change.Changed, change.RevokeSessions = true, true
	return change, nil
}

// Lock disables the account and requires session revocation.
func (u *User) Lock(actor Principal, reason string, now time.Time) (AccountChange, error) {
	if err := actor.Require(PermUserLock); err != nil {
		return AccountChange{}, err
	}
	if actor.UserID == u.ID {
		return AccountChange{}, ErrSelfLock
	}
	if strings.TrimSpace(reason) == "" {
		return AccountChange{}, ErrReasonRequired
	}
	change := AccountChange{UserID: u.ID, Action: "user.lock", Actor: actor.Actor(), At: now.UTC(), Reason: reason}
	if u.Status == UserLocked {
		return change, nil
	}
	u.Status = UserLocked
	change.Changed, change.RevokeSessions = true, true
	return change, nil
}

// Unlock re-enables a locked account. Sessions were revoked by the lock, so
// the user must log in again.
func (u *User) Unlock(actor Principal, reason string, now time.Time) (AccountChange, error) {
	if err := actor.Require(PermUserLock); err != nil {
		return AccountChange{}, err
	}
	if actor.UserID == u.ID {
		return AccountChange{}, ErrSelfLock
	}
	if strings.TrimSpace(reason) == "" {
		return AccountChange{}, ErrReasonRequired
	}
	change := AccountChange{UserID: u.ID, Action: "user.unlock", Actor: actor.Actor(), At: now.UTC(), Reason: reason}
	if u.Status == UserActive {
		return change, nil
	}
	u.Status = UserActive
	change.Changed = true
	return change, nil
}

func (u *User) authorizeRoleChange(actor Principal, role Role, reason string) error {
	if err := actor.Require(PermUserRoleManage); err != nil {
		return err
	}
	if !role.Valid() {
		return fmt.Errorf("role %q: %w", role, ErrUnknownRole)
	}
	if actor.UserID == u.ID {
		return ErrSelfRoleChange
	}
	if role == RoleAdmin && !actor.HasRole(RoleAdmin) {
		return ErrAdminGrantNeeded
	}
	if strings.TrimSpace(reason) == "" {
		return ErrReasonRequired
	}
	return nil
}
