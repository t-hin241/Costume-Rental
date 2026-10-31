package pgtest

import (
	"context"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestRunFailsUnderCIWithoutADatabase(t *testing.T) {
	t.Setenv("CI", "true")
	t.Setenv(envVar, "")
	t.Chdir(t.TempDir()) // no go.mod here, so no .env fallback either
	if got := Run(nil, nil); got != 1 {
		t.Fatalf("Run = %d, want 1", got)
	}
}

func TestDropEndsSessionsLeftOpen(t *testing.T) {
	base := lookupDSN()
	if base == "" {
		t.Skip(envVar + " is not set; integration tests skipped")
	}
	ctx := context.Background()
	name, drop, err := createDatabase(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(base)
	u.Path = "/" + name
	left, err := pgx.Connect(ctx, u.String())
	if err != nil {
		drop()
		t.Fatal(err)
	}
	defer left.Close(ctx)
	drop()

	check, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close(ctx)
	var exists bool
	if err := check.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Errorf("database %s is still there", name)
	}
}
