package identityapp_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/identity/identityapp"
	"webee_rentor/internal/platform/postgres/pgtest"
)

func TestMain(m *testing.M) { pgtest.Main(m, nil) }

func TestAdminPublishesMatrixAndItAppliesToTheNextRequest(t *testing.T) {
	ctx := context.Background()
	f := pgtest.New(t)
	clock := pgtest.NewClock(pgtest.Now)
	svc := identityapp.New(f.Pool, identityapp.WithClock(clock.Now))
	admin := f.User("admin", identity.RoleAdmin)
	staff := f.User("staff", identity.RoleRentalStaff)
	t.Cleanup(func() {
		// Versions are immutable; re-publishing the seed restores it.
		if err := svc.PublishMatrix(ctx, admin, "reference-2026-10-30", identity.ReferencePermissionMatrix(), "test cleanup"); err != nil {
			t.Error(err)
		}
	})

	m := identity.ReferencePermissionMatrix()
	m[identity.RoleRentalStaff] = slices.DeleteFunc(m[identity.RoleRentalStaff], func(p identity.Permission) bool {
		return p == identity.PermBookingCancelAny
	})
	if err := svc.PublishMatrix(ctx, staff, "v2", m, "x"); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("staff publishing err = %v", err)
	}
	finance := f.User("finance", identity.RoleFinance)
	if err := svc.PublishMatrix(ctx, finance, "v2", m, "x"); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("finance publishing err = %v", err)
	}
	if err := svc.PublishMatrix(ctx, admin, f.Prefix+"-v2", m, "staff no longer cancel"); err != nil {
		t.Fatal(err)
	}
	p, err := svc.Principal(ctx, staff.UserID)
	if err != nil || p.Can(identity.PermBookingCancelAny) || !p.Can(identity.PermRentalHandover) {
		t.Fatalf("new matrix must apply at once: %v", err)
	}

	var changes string
	if err := f.Pool.QueryRow(ctx, `SELECT changes->'role:rental_staff'->>'To' FROM audit_events
		WHERE action = 'identity.permission_matrix_publish' AND resource_id = $1`, f.Prefix+"-v2").Scan(&changes); err != nil {
		t.Fatalf("publish must be audited: %v", err)
	}

	bad := identity.ReferencePermissionMatrix()
	bad[identity.RoleRentalStaff] = append(bad[identity.RoleRentalStaff], identity.PermRefundApprove)
	if err := svc.PublishMatrix(ctx, admin, f.Prefix+"-v3", bad, "x"); !errors.Is(err, identity.ErrInvalidMatrix) {
		t.Fatalf("guardrail err = %v", err)
	}
	_, version, err := svc.PermissionMatrix(ctx, admin)
	if err != nil || version != f.Prefix+"-v2" {
		t.Fatalf("a rejected matrix must not become active: %s, %v", version, err)
	}
}
