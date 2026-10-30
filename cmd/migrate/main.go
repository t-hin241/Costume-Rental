// Command migrate applies the database migrations embedded in the binary.
// It reads DATABASE_URL from the environment and must run, under control,
// before the application receives traffic.
//
//	DATABASE_URL=postgres://... go run ./cmd/migrate
package main

import (
	"log/slog"
	"os"

	"webee_rentor/internal/platform/postgres"
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		slog.Error("DATABASE_URL is not set")
		os.Exit(2)
	}
	if err := postgres.Migrate(dsn); err != nil {
		slog.Error("migration failed", "error", err)
		os.Exit(1)
	}
	slog.Info("migrations applied")
}
