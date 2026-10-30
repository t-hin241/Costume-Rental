package identity

import (
	"slices"
	"strings"

	"webee_rentor/internal/domainerr"
)

var (
	ErrInvalidVerifyToken = domainerr.New(domainerr.KindInvalid, "identity.invalid_verification_token", "the verification link is invalid or has expired")
	ErrInvalidChallenge   = domainerr.New(domainerr.KindForbidden, "identity.invalid_login_challenge", "the login step has expired; sign in again")
	ErrInvalidMFACode     = domainerr.New(domainerr.KindInvalid, "identity.invalid_mfa_code", "the authentication code is incorrect")
	ErrMFAAlreadyEnabled  = domainerr.New(domainerr.KindInvalidState, "identity.mfa_already_enabled", "two-factor authentication is already enabled")
	ErrMFANotEnabled      = domainerr.New(domainerr.KindInvalidState, "identity.mfa_not_enabled", "two-factor authentication is not enabled")
	ErrMFARequired        = domainerr.New(domainerr.KindForbidden, "identity.mfa_required", "two-factor authentication is mandatory for this account")
	ErrMFAUnavailable     = domainerr.New(domainerr.KindInvalidState, "identity.mfa_unavailable", "two-factor authentication is not configured on this server")
)

var ErrSelfMFAReset = domainerr.New(domainerr.KindForbidden, "identity.self_mfa_reset", "users cannot reset their own two-factor authentication")

// AuthorizeMFAReset checks an administrator may remove another user's
// second factor (lost device and recovery codes). It is an account
// security action, so it needs the same permission as locking.
func AuthorizeMFAReset(actor Principal, target UserID, reason string) error {
	if err := actor.Require(PermUserLock); err != nil {
		return err
	}
	if actor.UserID == target {
		return ErrSelfMFAReset
	}
	if strings.TrimSpace(reason) == "" {
		return ErrReasonRequired
	}
	return nil
}

// mfaRequiredRoles must pass a second factor at every login (decision D23).
// Other roles may enrol voluntarily.
var mfaRequiredRoles = []Role{RoleAdmin, RoleFinance}

// RequiresMFA reports whether a user holding roles must use a second factor.
func RequiresMFA(roles []Role) bool {
	return slices.ContainsFunc(roles, func(r Role) bool { return slices.Contains(mfaRequiredRoles, r) })
}
