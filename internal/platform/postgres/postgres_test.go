package postgres_test

import (
	"context"
	"errors"
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
