// Package pgtest runs integration tests against a throwaway PostgreSQL
// database. Set TEST_DATABASE_URL (environment or the repository's .env,
// which is git-ignored) to a postgres:// URL of a role allowed to CREATE
// DATABASE. Each test package gets its own database, migrated from scratch
// and dropped afterwards; without the variable the tests are skipped.
package pgtest

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"webee_rentor/internal/platform/postgres"
)

const envVar = "TEST_DATABASE_URL"

var (
	pool    *pgxpool.Pool
	dbURL   string
	skipMsg string
)

// Run wraps testing.M: it creates and migrates the package database, runs
// the tests and drops the database. seed, when not nil, runs once after
// migration (e.g. to install the active policy).
func Run(m *testing.M, seed func(ctx context.Context, p *pgxpool.Pool) error) int {
	base := lookupDSN()
	if base == "" {
		skipMsg = envVar + " is not set; integration tests skipped"
		return m.Run()
	}
	ctx := context.Background()
	name, drop, err := createDatabase(ctx, base)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pgtest:", err)
		return 1
	}
	defer drop()
	u, _ := url.Parse(base)
	u.Path = "/" + name
	dbURL = u.String()
	if err := postgres.Migrate(dbURL); err != nil {
		fmt.Fprintln(os.Stderr, "pgtest:", err)
		return 1
	}
	pool, err = postgres.Open(ctx, postgres.Config{DSN: dbURL, MaxConns: 32, StatementTimeout: 30 * time.Second})
	if err != nil {
		fmt.Fprintln(os.Stderr, "pgtest:", err)
		return 1
	}
	defer pool.Close()
	if seed != nil {
		if err := seed(ctx, pool); err != nil {
			fmt.Fprintln(os.Stderr, "pgtest: seed:", err)
			return 1
		}
	}
	return m.Run()
}

// Main is TestMain for integration test packages: os.Exit(Run(m, seed)).
func Main(m *testing.M, seed func(ctx context.Context, p *pgxpool.Pool) error) {
	os.Exit(Run(m, seed))
}

// Pool returns the package database, or skips the test without one.
func Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	if pool == nil {
		t.Skip(skipMsg)
	}
	return pool
}

// URL returns the package database URL, or skips the test without one.
func URL(t testing.TB) string {
	t.Helper()
	if pool == nil {
		t.Skip(skipMsg)
	}
	return dbURL
}

// NewDatabase creates an extra empty database for a test and returns its
// URL; it is dropped when the test ends.
func NewDatabase(t testing.TB) string {
	t.Helper()
	base := lookupDSN()
	if base == "" {
		t.Skip(envVar + " is not set; integration tests skipped")
	}
	name, drop, err := createDatabase(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(drop)
	u, _ := url.Parse(base)
	u.Path = "/" + name
	return u.String()
}

func createDatabase(ctx context.Context, base string) (string, func(), error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return "", nil, fmt.Errorf("%s must be a postgres:// URL", envVar)
	}
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		return "", nil, fmt.Errorf("connect with %s: %w", envVar, err)
	}
	name := "webee_test_" + randomHex(6)
	// name is generated from hex digits only, so it is safe to interpolate.
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close(ctx)
		return "", nil, fmt.Errorf("create test database (the role needs CREATEDB): %w", err)
	}
	drop := func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			fmt.Fprintln(os.Stderr, "pgtest: drop", name, err)
		}
		admin.Close(context.Background())
	}
	return name, drop, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// lookupDSN reads TEST_DATABASE_URL from the environment, falling back to
// the .env file at the module root.
func lookupDSN() string {
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return readDotEnv(filepath.Join(dir, ".env"))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func readDotEnv(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(k) == envVar {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// Clock is a settable clock shared by the services under test.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

// NewClock starts at t.
func NewClock(t time.Time) *Clock { return &Clock{t: t.UTC()} }

// Now returns the current fake time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Set moves the clock to t.
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t.UTC()
}
