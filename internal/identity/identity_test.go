package identity_test

import (
	"errors"
	"testing"
	"time"

	"webee_rentor/internal/identity"
)

var now = time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)

func principal(t *testing.T, id identity.UserID, roles ...identity.Role) identity.Principal {
	t.Helper()
	p, err := identity.NewPrincipal(id, roles, identity.ReferencePermissionMatrix())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDenyByDefault(t *testing.T) {
	customer := principal(t, "u1", identity.RoleCustomer)
	for _, perm := range []identity.Permission{
		identity.PermBookingReadAll, identity.PermRefundApprove, identity.PermUserRoleManage,
		identity.PermInventoryManage, identity.PermAuditRead,
	} {
		if err := customer.Require(perm); !errors.Is(err, identity.ErrForbidden) {
			t.Errorf("customer %s err = %v", perm, err)
		}
	}
	staff := principal(t, "s1", identity.RoleRentalStaff)
	for _, perm := range []identity.Permission{identity.PermRefundApprove, identity.PermUserRoleManage, identity.PermInventoryManage} {
		if staff.Can(perm) {
			t.Errorf("rental staff must not have %s by default", perm)
		}
	}
	finance := principal(t, "f1", identity.RoleFinance)
	if finance.Can(identity.PermInventoryManage) || finance.Can(identity.PermUserRoleManage) {
		t.Error("finance must not manage inventory or roles by default")
	}
	if _, err := identity.NewPrincipal("x", []identity.Role{"superuser"}, identity.ReferencePermissionMatrix()); !errors.Is(err, identity.ErrUnknownRole) {
		t.Errorf("unknown role err = %v", err)
	}
}

func TestOwnershipScope(t *testing.T) {
	a := principal(t, "a", identity.RoleCustomer)
	if s, err := a.RequireOwnOrAny(identity.PermBookingReadOwn, identity.PermBookingReadAll, "a"); err != nil || s != identity.ScopeOwn {
		t.Fatalf("own access = %v, %v", s, err)
	}
	if _, err := a.RequireOwnOrAny(identity.PermBookingReadOwn, identity.PermBookingReadAll, "b"); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("customer A reading B err = %v", err)
	}
	staff := principal(t, "s", identity.RoleRentalStaff)
	if s, err := staff.RequireOwnOrAny(identity.PermBookingReadOwn, identity.PermBookingReadAll, "b"); err != nil || s != identity.ScopeAny {
		t.Fatalf("staff access = %v, %v", s, err)
	}
}

func TestRoleChangesPreventSelfEscalation(t *testing.T) {
	admin := principal(t, "admin1", identity.RoleAdmin)
	user, err := identity.NewCustomer("u1", "  Khach@Example.COM ", now)
	if err != nil {
		t.Fatal(err)
	}
	if user.Email != "khach@example.com" {
		t.Fatalf("email not normalized: %q", user.Email)
	}

	change, err := user.GrantRole(admin, identity.RoleRentalStaff, "hired", now)
	if err != nil || !change.Changed || !change.RevokeSessions {
		t.Fatalf("grant = %+v, %v", change, err)
	}
	again, err := user.GrantRole(admin, identity.RoleRentalStaff, "hired", now)
	if err != nil || again.Changed || again.RevokeSessions {
		t.Fatalf("idempotent grant = %+v, %v", again, err)
	}

	self := admin
	adminUser := identity.User{ID: "admin1", Roles: []identity.Role{identity.RoleAdmin}}
	if _, err := adminUser.RevokeRole(self, identity.RoleAdmin, "oops", now); !errors.Is(err, identity.ErrSelfRoleChange) {
		t.Fatalf("self role change err = %v", err)
	}

	// A non-admin with role management (if ever configured) still cannot mint admins.
	matrix := identity.ReferencePermissionMatrix()
	matrix[identity.RoleFinance] = append(matrix[identity.RoleFinance], identity.PermUserRoleManage)
	manager, err := identity.NewPrincipal("m1", []identity.Role{identity.RoleFinance}, matrix)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := user.GrantRole(manager, identity.RoleAdmin, "promote", now); !errors.Is(err, identity.ErrAdminGrantNeeded) {
		t.Fatalf("non-admin granting admin err = %v", err)
	}

	staff := principal(t, "s1", identity.RoleRentalStaff)
	if _, err := user.GrantRole(staff, identity.RoleFinance, "x", now); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("staff granting role err = %v", err)
	}
	if _, err := user.GrantRole(admin, identity.RoleFinance, " ", now); !errors.Is(err, identity.ErrReasonRequired) {
		t.Fatalf("grant without reason err = %v", err)
	}
}

func TestLockRevokesSessions(t *testing.T) {
	admin := principal(t, "admin1", identity.RoleAdmin)
	user, _ := identity.NewCustomer("u1", "a@example.com", now)
	change, err := user.Lock(admin, "fraud review", now)
	if err != nil || !change.RevokeSessions || user.Status != identity.UserLocked {
		t.Fatalf("lock = %+v, %v", change, err)
	}
	adminUser := identity.User{ID: "admin1"}
	if _, err := adminUser.Lock(admin, "x", now); !errors.Is(err, identity.ErrSelfLock) {
		t.Fatalf("self lock err = %v", err)
	}
}

func TestNormalizeEmailRejectsInvalid(t *testing.T) {
	for _, e := range []string{"", "no-at", "Name <a@b.com>", "a@b.com, c@d.com"} {
		if _, err := identity.NormalizeEmail(e); !errors.Is(err, identity.ErrInvalidEmail) {
			t.Errorf("NormalizeEmail(%q) err = %v", e, err)
		}
	}
}

func TestReferenceMatrixPassesGuardrails(t *testing.T) {
	if err := identity.ReferencePermissionMatrix().Validate(); err != nil {
		t.Fatal(err)
	}
	finance := principal(t, "f1", identity.RoleFinance)
	staff := principal(t, "s1", identity.RoleRentalStaff)
	if !finance.Can(identity.PermAuditRead) || staff.Can(identity.PermAuditRead) {
		t.Error("audit log is for admin and finance only (D22)")
	}
	inventory := principal(t, "i1", identity.RoleInventoryManager)
	for _, p := range []identity.Principal{staff, inventory} {
		if !p.Can(identity.PermRentalReturnInspect) {
			t.Errorf("%v must be able to pass inspection and cleaning (D22)", p.Roles)
		}
	}
}

func TestMatrixGuardrails(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(identity.PermissionMatrix)
	}{
		{"customer with staff read", func(m identity.PermissionMatrix) {
			m[identity.RoleCustomer] = append(m[identity.RoleCustomer], identity.PermBookingReadAll)
		}},
		{"staff approving refunds", func(m identity.PermissionMatrix) {
			m[identity.RoleRentalStaff] = append(m[identity.RoleRentalStaff], identity.PermRefundApprove)
		}},
		{"inventory manager waiving fees", func(m identity.PermissionMatrix) {
			m[identity.RoleInventoryManager] = append(m[identity.RoleInventoryManager], identity.PermFeeWaive)
		}},
		{"finance managing roles", func(m identity.PermissionMatrix) {
			m[identity.RoleFinance] = append(m[identity.RoleFinance], identity.PermUserRoleManage)
		}},
		{"admin locked out of policy", func(m identity.PermissionMatrix) {
			m[identity.RoleAdmin] = []identity.Permission{identity.PermUserRoleManage, identity.PermUserLock, identity.PermTaxManage}
		}},
		{"unknown permission", func(m identity.PermissionMatrix) {
			m[identity.RoleFinance] = append(m[identity.RoleFinance], "payment.steal")
		}},
		{"unknown role", func(m identity.PermissionMatrix) { m["superuser"] = nil }},
		{"duplicate", func(m identity.PermissionMatrix) {
			m[identity.RoleFinance] = append(m[identity.RoleFinance], identity.PermPaymentRead)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := identity.ReferencePermissionMatrix()
			tt.mutate(m)
			if err := m.Validate(); !errors.Is(err, identity.ErrInvalidMatrix) {
				t.Fatalf("err = %v, want ErrInvalidMatrix", err)
			}
		})
	}
}
