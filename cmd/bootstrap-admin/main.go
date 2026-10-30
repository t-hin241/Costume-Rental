// Command bootstrap-admin creates the first administrator account. It
// refuses while any active administrator exists; later admins are granted
// through the admin API. The email is treated as verified, and TOTP is
// enrolled at the admin's first login. The password is read from the first
// line of standard input so it never appears in arguments or shell history:
//
//	DATABASE_URL=postgres://... go run ./cmd/bootstrap-admin -email owner@example.vn < password.txt
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"webee_rentor/internal/identity/argon2id"
	"webee_rentor/internal/identity/identityapp"
	"webee_rentor/internal/platform/envconfig"
	"webee_rentor/internal/platform/postgres"
)

func main() {
	email := flag.String("email", "", "administrator email")
	flag.Parse()
	id, err := run(*email)
	if err != nil {
		slog.Error("bootstrap failed", "error", err)
		os.Exit(1)
	}
	slog.Info("administrator created", "user_id", id)
}

func run(email string) (string, error) {
	if email == "" {
		return "", errors.New("-email is required")
	}
	env := envconfig.New()
	dsn := env.Required("DATABASE_URL")
	authCfg := env.Auth()
	if err := env.Err(); err != nil {
		return "", err
	}
	fmt.Fprintln(os.Stderr, "password (first line of stdin):")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read password: %w", err)
	}
	password := strings.TrimRight(line, "\r\n")

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := postgres.Open(ctx, postgres.Config{DSN: dsn, ConnectTimeout: 5 * time.Second, StatementTimeout: 10 * time.Second})
	if err != nil {
		return "", err
	}
	defer pool.Close()
	hasher, err := argon2id.New(argon2id.OWASP, 1)
	if err != nil {
		return "", err
	}
	// No secret box: bootstrapping never touches TOTP; the admin enrols
	// at first login.
	auth, err := identityapp.NewAuth(identityapp.New(pool), authCfg, hasher, noMail{}, nil)
	if err != nil {
		return "", err
	}
	id, err := auth.BootstrapAdmin(ctx, email, password)
	return string(id), err
}

// noMail: bootstrapping sends no email.
type noMail struct{}

func (noMail) VerifyEmail(context.Context, string, string, time.Time) error   { return nil }
func (noMail) PasswordReset(context.Context, string, string, time.Time) error { return nil }
func (noMail) AccountExists(context.Context, string) error                    { return nil }
