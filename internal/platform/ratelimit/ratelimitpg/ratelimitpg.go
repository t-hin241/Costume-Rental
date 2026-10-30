// Package ratelimitpg keeps rate-limit windows in PostgreSQL so every API
// instance shares the same counters. Keys are stored as SHA-256 hashes
// because they contain emails and IP addresses.
package ratelimitpg

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"webee_rentor/internal/platform/ratelimit"
)

// Limiter is a shared ratelimit.Limiter.
type Limiter struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// New builds the limiter; now is time.Now when nil.
func New(pool *pgxpool.Pool, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{pool: pool, now: now}
}

// Allow implements ratelimit.Limiter with one atomic upsert: a window
// older than r.Window restarts at now with one hit, otherwise the hit
// count grows. Concurrent requests serialise on the row.
func (l *Limiter) Allow(ctx context.Context, key string, r ratelimit.Rate) (bool, time.Duration, error) {
	h := sha256.Sum256([]byte(key))
	now := l.now().UTC()
	var start time.Time
	var hits int
	err := l.pool.QueryRow(ctx, `
		INSERT INTO rate_limit_windows (key_hash, window_start, hits) VALUES ($1, $2, 1)
		ON CONFLICT (key_hash) DO UPDATE SET
			window_start = CASE WHEN rate_limit_windows.window_start <= $3 THEN EXCLUDED.window_start
				ELSE rate_limit_windows.window_start END,
			hits = CASE WHEN rate_limit_windows.window_start <= $3 THEN 1
				ELSE least(rate_limit_windows.hits + 1, 2147483647) END
		RETURNING window_start, hits`, h[:], now, now.Add(-r.Window)).Scan(&start, &hits)
	if err != nil {
		return false, 0, fmt.Errorf("rate limit: %w", err)
	}
	if hits > r.N {
		return false, start.UTC().Add(r.Window).Sub(now), nil
	}
	return true, 0, nil
}

// Sweep deletes windows that started before olderThan; run it
// periodically with olderThan = now − the longest window in use.
func (l *Limiter) Sweep(ctx context.Context, olderThan time.Time) (int64, error) {
	tag, err := l.pool.Exec(ctx, `DELETE FROM rate_limit_windows WHERE window_start < $1`, olderThan.UTC())
	if err != nil {
		return 0, fmt.Errorf("sweep rate limit windows: %w", err)
	}
	return tag.RowsAffected(), nil
}
