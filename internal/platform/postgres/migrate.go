package postgres

import (
	"embed"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	// Registers the pgx5:// database driver.
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// Migrations are versioned and append-only: once a file has run on a shared
// environment it is never edited; changes go in a new file.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrate applies every pending up migration. It takes the advisory lock of
// golang-migrate, so concurrent runs wait instead of racing.
func Migrate(dsn string) error {
	m, err := newMigrator(dsn)
	if err != nil {
		return err
	}
	defer m.Close()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("postgres: migrate up: %w", err)
	}
	return nil
}

// MigrateDown rolls back every migration. It exists for tests and local
// development; never run it against shared environments.
func MigrateDown(dsn string) error {
	m, err := newMigrator(dsn)
	if err != nil {
		return err
	}
	defer m.Close()
	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("postgres: migrate down: %w", err)
	}
	return nil
}

func newMigrator(dsn string) (*migrate.Migrate, error) {
	src, err := iofs.New(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("postgres: load migrations: %w", err)
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return nil, errors.New("postgres: DATABASE_URL must be a postgres:// URL")
	}
	u.Scheme = "pgx5"
	m, err := migrate.NewWithSourceInstance("iofs", src, u.String())
	if err != nil {
		return nil, fmt.Errorf("postgres: open migrator: %w", redact(err, u))
	}
	return m, nil
}

// redact keeps the password out of errors that echo the connection URL.
func redact(err error, u *url.URL) error {
	if p, ok := u.User.Password(); ok && p != "" {
		return errors.New(strings.ReplaceAll(err.Error(), p, "xxxxx"))
	}
	return err
}
