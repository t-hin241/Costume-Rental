// Package postgres is the PostgreSQL adapter shared by the module
// repositories: connection pool, transactions, migrations and the mapping
// of database errors to domain errors. Domain packages never import it.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"webee_rentor/internal/domainerr"
)

// Querier is satisfied by pgx.Tx and *pgxpool.Pool. Repositories take a
// Querier so the use case owns the transaction boundary.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var (
	ErrNotFound = domainerr.New(domainerr.KindNotFound, "storage.not_found", "record not found")
	// ErrStale means the row changed since it was read; reload and retry.
	ErrStale = domainerr.New(domainerr.KindConflict, "storage.stale_write", "the record was changed by another request")
	// ErrDuplicate means a unique key is already taken.
	ErrDuplicate = domainerr.New(domainerr.KindConflict, "storage.duplicate", "a record with the same key already exists")
)

// Config holds pool settings; the DSN comes from the environment and is
// never logged.
type Config struct {
	DSN              string
	MaxConns         int32
	ConnectTimeout   time.Duration
	StatementTimeout time.Duration
}

// Open creates a pool whose sessions run in UTC with a statement timeout,
// and checks connectivity.
func Open(ctx context.Context, c Config) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(c.DSN)
	if err != nil {
		// The parse error may echo the DSN, which can hold a password.
		return nil, errors.New("postgres: invalid DATABASE_URL")
	}
	if c.MaxConns > 0 {
		cfg.MaxConns = c.MaxConns
	}
	if c.ConnectTimeout > 0 {
		cfg.ConnConfig.ConnectTimeout = c.ConnectTimeout
	}
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	if c.StatementTimeout > 0 {
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = fmt.Sprint(c.StatementTimeout.Milliseconds())
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return pool, nil
}

// maxTxAttempts bounds retries of transactions aborted by a deadlock or a
// serialization failure. fn must only touch the database so a retry cannot
// duplicate outside effects.
const maxTxAttempts = 3

// InTx runs fn in a READ COMMITTED transaction, committing when fn returns
// nil. Deadlocks and serialization failures are retried a few times.
func InTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	var err error
	for attempt := 1; attempt <= maxTxAttempts; attempt++ {
		err = pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, fn)
		if !retryable(err) {
			return err
		}
	}
	return fmt.Errorf("postgres: transaction failed after %d attempts: %w", maxTxAttempts, err)
}

func retryable(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && (pg.Code == "40P01" || pg.Code == "40001")
}

// IsExclusionViolation reports whether err is a violation of the named
// exclusion constraint (SQLSTATE 23P01).
func IsExclusionViolation(err error, constraint string) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23P01" && pg.ConstraintName == constraint
}

// IsUniqueViolation reports whether err is a violation of the named unique
// constraint or index (SQLSTATE 23505).
func IsUniqueViolation(err error, constraint string) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == constraint
}

// NotFound maps pgx.ErrNoRows to ErrNotFound with context.
func NotFound(err error, what string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// UTC normalises a scanned timestamp; pgx returns timestamptz in time.Local.
func UTC(t time.Time) time.Time { return t.UTC() }

// UTCPtr normalises an optional scanned timestamp.
func UTCPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
