package identityapp

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/audit"
	"webee_rentor/internal/audit/auditpg"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/identity/identitypg"
	"webee_rentor/internal/platform/postgres"
)

// User returns an account with its roles, for administrators.
func (s *Service) User(ctx context.Context, p identity.Principal, id identity.UserID) (identity.User, error) {
	if err := p.Require(identity.PermUserRoleManage); err != nil {
		return identity.User{}, err
	}
	return identitypg.LoadUser(ctx, s.pool, id, false)
}

// GrantRole adds role to a user.
func (s *Service) GrantRole(ctx context.Context, p identity.Principal, id identity.UserID, role identity.Role, reason string) error {
	return s.changeAccess(ctx, p, id, func(u *identity.User, actor identity.Principal, now time.Time) (identity.AccountChange, error) {
		return u.GrantRole(actor, role, reason, now)
	})
}

// RevokeRole removes role from a user.
func (s *Service) RevokeRole(ctx context.Context, p identity.Principal, id identity.UserID, role identity.Role, reason string) error {
	return s.changeAccess(ctx, p, id, func(u *identity.User, actor identity.Principal, now time.Time) (identity.AccountChange, error) {
		return u.RevokeRole(actor, role, reason, now)
	})
}

// LockUser disables an account and ends its sessions.
func (s *Service) LockUser(ctx context.Context, p identity.Principal, id identity.UserID, reason string) error {
	return s.changeAccess(ctx, p, id, func(u *identity.User, actor identity.Principal, now time.Time) (identity.AccountChange, error) {
		return u.Lock(actor, reason, now)
	})
}

// UnlockUser re-enables an account.
func (s *Service) UnlockUser(ctx context.Context, p identity.Principal, id identity.UserID, reason string) error {
	return s.changeAccess(ctx, p, id, func(u *identity.User, actor identity.Principal, now time.Time) (identity.AccountChange, error) {
		return u.Unlock(actor, reason, now)
	})
}

// ResetMFA removes another user's second factor (lost device and recovery
// codes) and ends their sessions. The user enrols again at the next login
// when their roles require it. Removing an absent enrolment is a no-op.
func (s *Service) ResetMFA(ctx context.Context, p identity.Principal, id identity.UserID, reason string) error {
	if p.UserID == "" {
		return identity.ErrNotPrincipal
	}
	err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		m, _, err := identitypg.ActiveMatrix(ctx, tx)
		if err != nil {
			return err
		}
		// Re-resolve the actor: its roles may have changed since the
		// request started.
		actor, err := identitypg.LoadPrincipal(ctx, tx, p.UserID, m)
		if err != nil {
			return err
		}
		if err := identity.AuthorizeMFAReset(actor, id, reason); err != nil {
			return err
		}
		if _, err := identitypg.LoadUser(ctx, tx, id, true); err != nil {
			return err
		}
		deleted, err := identitypg.DeleteMFA(ctx, tx, id)
		if err != nil || !deleted {
			return err
		}
		now := s.now().UTC()
		revoked, err := identitypg.RevokeUserSessions(ctx, tx, id, "", identity.RevokeMFAChange, now)
		if err != nil {
			return err
		}
		return auditpg.Append(ctx, tx, audit.Event{
			ID: s.newID(), OccurredAt: now, Actor: actor.Actor(), Action: "user.mfa_reset",
			ResourceType: "user", ResourceID: string(id), Result: audit.ResultSuccess, Reason: reason,
			References: map[string]string{"sessions_revoked": fmt.Sprint(revoked)},
		})
	})
	if err != nil {
		return fmt.Errorf("reset mfa of %s: %w", id, err)
	}
	return nil
}

type accessChange func(u *identity.User, actor identity.Principal, now time.Time) (identity.AccountChange, error)

// changeAccess applies one access change. Inside the transaction it takes
// the admin-set lock, re-resolves the actor (whose roles may have changed
// since the request started) and locks the target user; afterwards at
// least one active admin must remain. The change, the session revocation
// and the audit event commit together.
func (s *Service) changeAccess(ctx context.Context, p identity.Principal, id identity.UserID, apply accessChange) error {
	// Fail fast on the request principal before touching the database.
	if p.UserID == "" {
		return identity.ErrNotPrincipal
	}
	err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := identitypg.LockAdminSet(ctx, tx); err != nil {
			return err
		}
		m, _, err := identitypg.ActiveMatrix(ctx, tx)
		if err != nil {
			return err
		}
		actor, err := identitypg.LoadPrincipal(ctx, tx, p.UserID, m)
		if err != nil {
			return err
		}
		before, err := identitypg.LoadUser(ctx, tx, id, true)
		if err != nil {
			return err
		}
		after := before
		after.Roles = append([]identity.Role(nil), before.Roles...)
		now := s.now().UTC()
		change, err := apply(&after, actor, now)
		if err != nil {
			return err
		}
		if !change.Changed {
			return nil
		}
		if err := identitypg.SaveAccess(ctx, tx, before, after, actor.Actor(), now); err != nil {
			return err
		}
		n, err := identitypg.CountActiveAdmins(ctx, tx)
		if err != nil {
			return err
		}
		if n == 0 {
			return identity.ErrLastAdmin
		}
		var revoked int64
		if change.RevokeSessions {
			if revoked, err = identitypg.RevokeUserSessions(ctx, tx, id, "", identity.RevokeAccessChange, now); err != nil {
				return err
			}
		}
		return auditpg.Append(ctx, tx, audit.Event{
			ID: s.newID(), OccurredAt: now, Actor: change.Actor, Action: change.Action,
			ResourceType: "user", ResourceID: string(id), Result: audit.ResultSuccess, Reason: change.Reason,
			References: map[string]string{"sessions_revoked": fmt.Sprint(revoked)},
			Changes:    accessDiff(before, after),
		})
	})
	if err != nil {
		return fmt.Errorf("change access of %s: %w", id, err)
	}
	return nil
}

func accessDiff(before, after identity.User) map[string]audit.Change {
	out := map[string]audit.Change{}
	if before.Status != after.Status {
		out["status"] = audit.Change{From: string(before.Status), To: string(after.Status)}
	}
	if b, a := joinRoles(before.Roles), joinRoles(after.Roles); b != a {
		out["roles"] = audit.Change{From: b, To: a}
	}
	return out
}

func joinRoles(roles []identity.Role) string {
	s := make([]string, len(roles))
	for i, r := range roles {
		s[i] = string(r)
	}
	slices.Sort(s)
	return strings.Join(s, ",")
}
