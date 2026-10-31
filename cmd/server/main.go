// Command server runs the HTTP API. Configuration comes from the
// environment (see .env.example); migrations must already be applied with
// cmd/migrate.
//
//	DATABASE_URL=postgres://... go run ./cmd/server
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"webee_rentor/internal/backoffice"
	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/catalog/catalogapp"
	"webee_rentor/internal/httpapi"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/identity/argon2id"
	"webee_rentor/internal/identity/identityapp"
	"webee_rentor/internal/identity/identitypg"
	"webee_rentor/internal/inventory/inventoryapp"
	"webee_rentor/internal/notifications"
	"webee_rentor/internal/notifications/smtpmail"
	"webee_rentor/internal/platform/envconfig"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/platform/ratelimit/ratelimitpg"
	"webee_rentor/internal/platform/secretbox"
	"webee_rentor/internal/rentals/rentalsapp"
	"webee_rentor/internal/support/supportapp"
)

func main() {
	slog.SetDefault(slog.New(httpapi.LogHandler(slog.NewJSONHandler(os.Stdout, nil))))
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	env := envconfig.New()
	dsn := env.Required("DATABASE_URL")
	addr := env.String("HTTP_ADDR", ":8080")
	httpCfg := httpapi.Config{
		CookieSecure:   env.Bool("HTTP_COOKIE_SECURE", true),
		TrustedOrigins: env.List("HTTP_TRUSTED_ORIGINS"),
		TrustedProxies: env.Prefixes("HTTP_TRUSTED_PROXIES"),
		RequestTimeout: env.Duration("HTTP_REQUEST_TIMEOUT", 15*time.Second),
		RateLimits:     httpapi.DefaultRateLimits(),
	}
	authCfg := env.Auth()
	hashConcurrency := env.Int("AUTH_HASH_CONCURRENCY", runtime.NumCPU())
	mfaKey := env.Required("AUTH_MFA_ENCRYPTION_KEY")
	baseURL := env.Required("APP_BASE_URL")
	shopName := env.String("SHOP_NAME", "Webee Rental")
	store := httpapi.StoreInfo{
		Name: shopName, Address: env.String("SHOP_ADDRESS", ""), Phone: env.String("SHOP_PHONE", ""), Email: env.String("SHOP_EMAIL", ""),
		Hours: env.String("SHOP_HOURS", ""), PaymentInstructions: env.String("SHOP_PAYMENT_INSTRUCTIONS", ""),
	}
	smtpCfg := smtpmail.Config{
		Host: env.Required("SMTP_HOST"), Port: env.Int("SMTP_PORT", 587),
		Username: env.String("SMTP_USERNAME", ""), Password: env.String("SMTP_PASSWORD", ""),
		From: env.Required("SMTP_FROM"), FromName: env.String("SMTP_FROM_NAME", shopName),
		TLS: smtpmail.TLSMode(env.String("SMTP_TLS", string(smtpmail.TLSStartTLS))),
	}
	if err := env.Err(); err != nil {
		return err
	}
	dev := !httpCfg.CookieSecure
	if dev {
		slog.Warn("HTTP_COOKIE_SECURE=false: session cookies are sent over plain HTTP; use only for local development")
	}
	if smtpCfg.TLS == smtpmail.TLSNone && !dev {
		return errors.New("SMTP_TLS=none is only allowed with HTTP_COOKIE_SECURE=false (local development)")
	}

	secrets, err := secretbox.FromBase64(mfaKey)
	if err != nil {
		return errors.New("AUTH_MFA_ENCRYPTION_KEY must be 32 random bytes, base64-encoded (openssl rand -base64 32)")
	}
	smtpSender, err := smtpmail.New(smtpCfg)
	if err != nil {
		return err
	}
	queue, err := notifications.NewQueue(smtpSender, notifications.DefaultQueueConfig())
	if err != nil {
		return err
	}
	mailer, err := notifications.NewAccountMail(queue, baseURL, shopName, dev)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.Open(ctx, postgres.Config{DSN: dsn, ConnectTimeout: 5 * time.Second, StatementTimeout: 10 * time.Second})
	if err != nil {
		return err
	}
	defer pool.Close()

	hasher, err := argon2id.New(argon2id.OWASP, hashConcurrency)
	if err != nil {
		return err
	}
	svc := identityapp.New(pool)
	auth, err := identityapp.NewAuth(svc, authCfg, hasher, mailer, secrets)
	if err != nil {
		return err
	}
	loc, err := businesstime.LoadLocation()
	if err != nil {
		return err
	}
	bookingMail, err := notifications.NewBookingMail(queue, shopName, func(ctx context.Context, id identity.UserID) (string, error) {
		people, err := identitypg.Contacts(ctx, pool, []identity.UserID{id})
		if err != nil {
			return "", err
		}
		return people[id].Email, nil
	})
	if err != nil {
		return err
	}
	inventorySvc := inventoryapp.New(pool)
	rentalSvc := rentalsapp.New(pool, loc, rentalsapp.WithNotifier(bookingMail))
	httpCfg.Services = &httpapi.Services{
		Catalog: catalogapp.New(pool), Rentals: rentalSvc, Inventory: inventorySvc, Support: supportapp.New(pool),
		Backoffice: backoffice.New(pool, loc, rentalSvc, inventorySvc), Store: store,
	}
	limiter := ratelimitpg.New(pool, nil)
	httpCfg.Limiter = limiter
	handler, err := httpapi.New(httpCfg, svc, auth, func(ctx context.Context) error { return pool.Ping(ctx) })
	if err != nil {
		return err
	}
	go sweepRateLimits(ctx, limiter, httpCfg.RateLimits.LongestWindow())
	go expireHolds(ctx, rentalSvc)

	srv := &http.Server{
		Addr: addr, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10,
	}
	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", addr)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	// Requests are done; send what they queued.
	if err := queue.Close(shutdownCtx); err != nil {
		slog.Error("email queue not drained", "error", err)
	}
	return nil
}

// expireHolds releases the holds that ran out, once a minute. Each pass is
// bounded and safe to repeat, so a slow one only delays the next.
func expireHolds(ctx context.Context, rentals *rentalsapp.Service) {
	const batch = 100
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			passCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			if n, err := rentals.ExpireHolds(passCtx, batch); err != nil {
				slog.Error("booking hold expiry failed", "expired", n, "error", err)
			}
			if n, err := rentals.ExpireModificationHolds(passCtx, batch); err != nil {
				slog.Error("modification hold expiry failed", "expired", n, "error", err)
			}
			cancel()
		}
	}
}

// sweepRateLimits deletes expired rate-limit windows every 10 minutes.
func sweepRateLimits(ctx context.Context, l *ratelimitpg.Limiter, keep time.Duration) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			sweepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if _, err := l.Sweep(sweepCtx, now.Add(-keep)); err != nil {
				slog.Warn("rate limit sweep failed", "error", err)
			}
			cancel()
		}
	}
}
