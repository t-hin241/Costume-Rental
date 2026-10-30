package identity_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
)

func TestCredentialPolicy(t *testing.T) {
	if err := (identity.CredentialPolicy{MinPasswordLength: 7}).Validate(); !errors.Is(err, identity.ErrInvalidAuthConfig) {
		t.Fatalf("a minimum below the floor must be refused: %v", err)
	}
	c := identity.CredentialPolicy{MinPasswordLength: 12}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		pw string
		ok bool
	}{
		{"short", false},
		{"elevenchars", false},
		{"twelve chars", true},
		{"mật khẩu dài đủ", true}, // counted in characters, not bytes
		{"  padded pw  ", true},   // used as typed, never trimmed
		{strings.Repeat("a", identity.MaxPasswordLength), true},
		{strings.Repeat("a", identity.MaxPasswordLength+1), false},
		{"invalid utf8 \xff\xfe", false},
	} {
		err := c.CheckPassword("password", tc.pw)
		if (err == nil) != tc.ok {
			t.Errorf("CheckPassword(%q) = %v, want ok=%v", tc.pw, err, tc.ok)
		}
		if err != nil && !errors.Is(err, domainerr.ErrValidation) {
			t.Errorf("CheckPassword(%q) must be a field error, got %v", tc.pw, err)
		}
	}
}

func sessionPolicy() identity.SessionPolicy {
	return identity.SessionPolicy{
		Customer: identity.SessionLimits{TTL: 7 * 24 * time.Hour, Idle: 24 * time.Hour},
		Staff:    identity.SessionLimits{TTL: 12 * time.Hour, Idle: 30 * time.Minute},
	}
}

func TestSessionPolicy(t *testing.T) {
	p := sessionPolicy()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := p.For([]identity.Role{identity.RoleCustomer}); got != p.Customer {
		t.Errorf("customer limits = %v", got)
	}
	if got := p.For([]identity.Role{identity.RoleCustomer, identity.RoleFinance}); got != p.Staff {
		t.Errorf("any staff role must get staff limits, got %v", got)
	}
	bad := p
	bad.Staff.TTL = 30 * 24 * time.Hour
	if err := bad.Validate(); !errors.Is(err, identity.ErrInvalidAuthConfig) {
		t.Errorf("staff outliving customers err = %v", err)
	}
	bad = p
	bad.Customer.Idle = 0
	if err := bad.Validate(); !errors.Is(err, identity.ErrInvalidAuthConfig) {
		t.Errorf("zero idle err = %v", err)
	}
}

func TestSessionCheck(t *testing.T) {
	p := sessionPolicy()
	staff := []identity.Role{identity.RoleRentalStaff}
	s := identity.NewSession("s1", "u1", staff, p, false, now)
	if !s.ExpiresAt.Equal(now.Add(12*time.Hour)) || s.MFAAt != nil {
		t.Fatalf("staff session = %+v", s)
	}
	if err := s.Check(now.Add(29*time.Minute), staff, p); err != nil {
		t.Fatalf("live session: %v", err)
	}
	if err := s.Check(now.Add(30*time.Minute), staff, p); !errors.Is(err, identity.ErrNotPrincipal) {
		t.Errorf("idle session err = %v", err)
	}
	s.LastSeenAt = now.Add(12*time.Hour - time.Minute)
	if err := s.Check(now.Add(12*time.Hour), staff, p); !errors.Is(err, identity.ErrNotPrincipal) {
		t.Errorf("expired session err = %v", err)
	}
	revoked := now
	s = identity.NewSession("s2", "u1", nil, p, false, now)
	s.RevokedAt = &revoked
	if err := s.Check(now.Add(time.Second), nil, p); !errors.Is(err, identity.ErrNotPrincipal) {
		t.Errorf("revoked session err = %v", err)
	}
}

// A role that requires MFA, granted to a user whose session started with a
// password only, must not ride on that session.
func TestSessionNeedsTheSecondFactorItsRolesRequire(t *testing.T) {
	p := sessionPolicy()
	finance := []identity.Role{identity.RoleFinance}
	plain := identity.NewSession("s1", "u1", nil, p, false, now)
	if err := plain.Check(now.Add(time.Minute), finance, p); !errors.Is(err, identity.ErrNotPrincipal) {
		t.Fatalf("finance on a password-only session err = %v", err)
	}
	strong := identity.NewSession("s2", "u1", finance, p, true, now)
	if strong.MFAAt == nil || strong.Check(now.Add(time.Minute), finance, p) != nil {
		t.Fatal("an MFA session must satisfy finance")
	}
}

func TestMFAPolicy(t *testing.T) {
	for roles, want := range map[string]bool{"customer": false, "rental_staff": false, "inventory_manager": false, "finance": true, "admin": true} {
		if got := identity.RequiresMFA([]identity.Role{identity.RoleCustomer, identity.Role(roles)}); got != want {
			t.Errorf("RequiresMFA(%s) = %v", roles, got)
		}
	}
	admin := principal(t, "admin", identity.RoleAdmin)
	finance := principal(t, "finance", identity.RoleFinance)
	if err := identity.AuthorizeMFAReset(finance, "u1", "lost phone"); !errors.Is(err, identity.ErrForbidden) {
		t.Errorf("finance resetting MFA err = %v", err)
	}
	if err := identity.AuthorizeMFAReset(admin, "admin", "lost phone"); !errors.Is(err, identity.ErrSelfMFAReset) {
		t.Errorf("self reset err = %v", err)
	}
	if err := identity.AuthorizeMFAReset(admin, "u1", " "); !errors.Is(err, identity.ErrReasonRequired) {
		t.Errorf("reset without reason err = %v", err)
	}
	if err := identity.AuthorizeMFAReset(admin, "u1", "lost phone, identity checked"); err != nil {
		t.Errorf("admin reset err = %v", err)
	}
}

func TestUnlock(t *testing.T) {
	admin := principal(t, "admin", identity.RoleAdmin)
	staff := principal(t, "staff", identity.RoleRentalStaff)
	u := identity.User{ID: "u1", Status: identity.UserLocked, Roles: []identity.Role{identity.RoleCustomer}}
	if _, err := u.Unlock(staff, "x", now); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("staff unlock err = %v", err)
	}
	if _, err := u.Unlock(admin, " ", now); !errors.Is(err, identity.ErrReasonRequired) {
		t.Fatalf("unlock without reason err = %v", err)
	}
	change, err := u.Unlock(admin, "verified identity", now)
	if err != nil || !change.Changed || change.RevokeSessions || u.Status != identity.UserActive {
		t.Fatalf("unlock = %+v, %v", change, err)
	}
	if change, err := u.Unlock(admin, "again", now); err != nil || change.Changed {
		t.Fatalf("unlocking an active user must be a no-op: %+v, %v", change, err)
	}
	self := identity.User{ID: "admin", Status: identity.UserLocked}
	if _, err := self.Unlock(admin, "x", now); !errors.Is(err, identity.ErrSelfLock) {
		t.Fatalf("self unlock err = %v", err)
	}
}

func TestPrincipalPermissionsAreSorted(t *testing.T) {
	perms := principal(t, "c", identity.RoleCustomer).Permissions()
	if len(perms) != len(identity.ReferencePermissionMatrix()[identity.RoleCustomer]) {
		t.Fatalf("customer permissions = %v", perms)
	}
	for i := 1; i < len(perms); i++ {
		if perms[i-1] >= perms[i] {
			t.Fatalf("permissions not sorted: %v", perms)
		}
	}
}
