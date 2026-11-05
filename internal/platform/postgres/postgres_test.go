package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/platform/postgres/pgtest"
)

func TestMain(m *testing.M) { pgtest.Main(m, nil) }

func TestMigrationsRoundTrip(t *testing.T) {
	url := pgtest.NewDatabase(t)
	if err := postgres.Migrate(url); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := postgres.Migrate(url); err != nil {
		t.Fatalf("second up must be a no-op: %v", err)
	}
	if err := postgres.MigrateDown(url); err != nil {
		t.Fatalf("down: %v", err)
	}
	if err := postgres.Migrate(url); err != nil {
		t.Fatalf("up after down: %v", err)
	}
}

// Migration 000018 (decision D39) dates the conversations already resolved
// and grants the erasure permission to admin only; rolling back restores the
// previous matrix.
func TestDataRetentionMigrationKeepsExistingData(t *testing.T) {
	ctx := context.Background()
	url := pgtest.NewDatabase(t)
	if err := postgres.MigrateTo(url, 17); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.Open(ctx, postgres.Config{DSN: url})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	resolved := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	for _, stmt := range []string{
		`INSERT INTO users (id, email, status, created_at, updated_at) VALUES ('u1', 'u1@example.test', 'ACTIVE', now(), now())`,
		`INSERT INTO support_requests (id, code, customer_id, topic, subject, status, created_at, updated_at)
			VALUES ('done', 'HT-1', 'u1', 'OTHER', 'x', 'RESOLVED', $1, $1), ('open', 'HT-2', 'u1', 'OTHER', 'x', 'OPEN', $1, $1)`,
	} {
		args := []any{}
		if strings.Contains(stmt, "$1") {
			args = append(args, resolved)
		}
		if _, err := pool.Exec(ctx, stmt, args...); err != nil {
			t.Fatal(err)
		}
	}
	matrix := func() (string, bool, bool) {
		t.Helper()
		var version string
		var admin, staff bool
		if err := pool.QueryRow(ctx, `SELECT version, document->'admin' ? 'customer.profile_erase', document->'rental_staff' ? 'customer.profile_erase'
			FROM policy_documents WHERE kind = 'PERMISSION_MATRIX' AND active`).Scan(&version, &admin, &staff); err != nil {
			t.Fatal(err)
		}
		return version, admin, staff
	}

	if err := postgres.MigrateTo(url, 18); err != nil {
		t.Fatal(err)
	}
	var done, open *time.Time
	if err := pool.QueryRow(ctx, `SELECT (SELECT resolved_at FROM support_requests WHERE id = 'done'),
		(SELECT resolved_at FROM support_requests WHERE id = 'open')`).Scan(&done, &open); err != nil {
		t.Fatal(err)
	}
	if done == nil || !done.Equal(resolved) || open != nil {
		t.Fatalf("resolved_at = %v / %v", done, open)
	}
	if v, admin, staff := matrix(); v != "reference-2026-11-05" || !admin || staff {
		t.Fatalf("matrix after 000018 = %s admin %v staff %v", v, admin, staff)
	}
	if err := postgres.MigrateTo(url, 17); err != nil {
		t.Fatal(err)
	}
	if v, admin, _ := matrix(); v != "reference-2026-11-02" || admin {
		t.Fatalf("matrix after rollback = %s admin %v", v, admin)
	}
	if err := postgres.MigrateTo(url, 18); err != nil {
		t.Fatal(err)
	}
}

// Migration 000019 (decision D41) grants revenue.record and
// payment.reconcile to finance and admin only; rolling back restores the
// previous matrix.
func TestRevenueLedgerMigrationGrantsFinance(t *testing.T) {
	ctx := context.Background()
	url := pgtest.NewDatabase(t)
	if err := postgres.MigrateTo(url, 19); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.Open(ctx, postgres.Config{DSN: url})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	grants := func() (version string, finance, admin, staff bool) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT version,
				document->'finance' ? 'revenue.record' AND document->'finance' ? 'payment.reconcile',
				document->'admin' ? 'revenue.record' AND document->'admin' ? 'payment.reconcile',
				document->'rental_staff' ? 'revenue.record' OR document->'rental_staff' ? 'payment.reconcile'
			FROM policy_documents WHERE kind = 'PERMISSION_MATRIX' AND active`).Scan(&version, &finance, &admin, &staff); err != nil {
			t.Fatal(err)
		}
		return
	}
	if v, finance, admin, staff := grants(); v != "reference-2026-11-05.2" || !finance || !admin || staff {
		t.Fatalf("matrix after 000019 = %s finance %v admin %v staff %v", v, finance, admin, staff)
	}
	if err := postgres.MigrateTo(url, 18); err != nil {
		t.Fatal(err)
	}
	if v, finance, _, _ := grants(); v != "reference-2026-11-05" || finance {
		t.Fatalf("matrix after rollback = %s finance %v", v, finance)
	}
}

func TestHistoryTablesAreAppendOnly(t *testing.T) {
	ctx := context.Background()
	p := pgtest.Pool(t)
	_, err := p.Exec(ctx, `INSERT INTO audit_events (id, occurred_at, actor_kind, actor_id, action, resource_type, resource_id, result)
		VALUES ('ev-append-only', now(), 'system', 'test', 'test.action', 'test', 'r1', 'SUCCESS')`)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`UPDATE audit_events SET reason = 'edited' WHERE id = 'ev-append-only'`,
		`DELETE FROM audit_events WHERE id = 'ev-append-only'`,
	} {
		if _, err := p.Exec(ctx, stmt); !isCode(err, "42501") {
			t.Errorf("%s: err = %v, want insufficient_privilege", stmt, err)
		}
	}
}

func TestPolicyVersionsAreImmutable(t *testing.T) {
	ctx := context.Background()
	p := pgtest.Pool(t)
	actor := identity.SystemActor("test")
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	err := postgres.InTx(ctx, p, func(tx pgx.Tx) error {
		return postgres.SaveDocument(ctx, tx, postgres.DocAllocationPolicy, "immutable-v1", map[string]any{"a": 1}, actor, now, false)
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Exec(ctx, `UPDATE policy_documents SET document = '{"a": 2}' WHERE kind = 'INVENTORY_ALLOCATION' AND version = 'immutable-v1'`)
	if !isCode(err, "42501") {
		t.Fatalf("editing a stored policy version: err = %v", err)
	}
}

func isCode(err error, code string) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == code
}
