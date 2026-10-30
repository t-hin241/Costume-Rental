package ratelimitpg_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"webee_rentor/internal/platform/postgres/pgtest"
	"webee_rentor/internal/platform/ratelimit"
	"webee_rentor/internal/platform/ratelimit/ratelimitpg"
)

func TestMain(m *testing.M) { pgtest.Main(m, nil) }

func TestWindowsAreSharedAndReset(t *testing.T) {
	ctx := context.Background()
	f := pgtest.New(t)
	clock := pgtest.NewClock(pgtest.Now)
	// Two limiters on one database behave like two API instances.
	a, b := ratelimitpg.New(f.Pool, clock.Now), ratelimitpg.New(f.Pool, clock.Now)
	r := ratelimit.Rate{N: 3, Window: time.Minute}
	key := f.Prefix + ":login:a@example.test"
	for i, l := range []*ratelimitpg.Limiter{a, b, a} {
		if ok, _, err := l.Allow(ctx, key, r); !ok || err != nil {
			t.Fatalf("hit %d: %v, %v", i+1, ok, err)
		}
	}
	ok, retry, err := b.Allow(ctx, key, r)
	if ok || err != nil || retry != time.Minute {
		t.Fatalf("4th hit = %v, retry %v, %v", ok, retry, err)
	}
	if ok, _, _ := a.Allow(ctx, f.Prefix+":other", r); !ok {
		t.Fatal("keys must not share a window")
	}
	clock.Set(pgtest.Now.Add(time.Minute))
	if ok, _, err := a.Allow(ctx, key, r); !ok || err != nil {
		t.Fatalf("a new window must allow again: %v", err)
	}

	clock.Set(pgtest.Now.Add(3 * time.Hour))
	if n, err := a.Sweep(ctx, clock.Now().Add(-time.Hour)); err != nil || n < 2 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
}

func TestConcurrentHitsAreCountedExactly(t *testing.T) {
	ctx := context.Background()
	f := pgtest.New(t)
	l := ratelimitpg.New(f.Pool, nil)
	r := ratelimit.Rate{N: 10, Window: time.Hour}
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _, err := l.Allow(ctx, f.Prefix+":burst", r); err != nil {
				t.Error(err)
			} else if ok {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 10 {
		t.Fatalf("allowed %d of 30, want exactly 10", allowed.Load())
	}
}
