package identitypg_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/identity/identitypg"
	"webee_rentor/internal/platform/postgres/pgtest"
)

func TestMain(m *testing.M) { pgtest.Main(m, nil) }

// Migration 000009 seeds exactly the reference matrix; this keeps the SQL
// seed and identity.ReferencePermissionMatrix from drifting apart.
func TestSeededMatrixIsTheReferenceMatrix(t *testing.T) {
	m, version, err := identitypg.ActiveMatrix(context.Background(), pgtest.Pool(t))
	if err != nil {
		t.Fatal(err)
	}
	if version != "reference-2026-10-30" || !reflect.DeepEqual(m.Normalized(), identity.ReferencePermissionMatrix().Normalized()) {
		t.Fatalf("seeded matrix %s differs from identity.ReferencePermissionMatrix", version)
	}
}

func TestLoadPrincipal(t *testing.T) {
	ctx := context.Background()
	f := pgtest.New(t)
	staff := f.User("staff", identity.RoleRentalStaff, identity.RoleInventoryManager)
	if !staff.Can(identity.PermInventoryManage) || !staff.Can(identity.PermRentalHandover) || staff.Can(identity.PermRefundApprove) {
		t.Fatal("roles from the database must combine under the matrix")
	}
	m, _, err := identitypg.ActiveMatrix(ctx, f.Pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identitypg.LoadPrincipal(ctx, f.Pool, "nobody", m); !errors.Is(err, identity.ErrNotPrincipal) {
		t.Fatalf("unknown user err = %v", err)
	}
	if _, err := f.Pool.Exec(ctx, `UPDATE users SET status = 'LOCKED' WHERE id = $1`, staff.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := identitypg.LoadPrincipal(ctx, f.Pool, staff.UserID, m); !errors.Is(err, identity.ErrAccountLocked) {
		t.Fatalf("locked user err = %v", err)
	}
}
