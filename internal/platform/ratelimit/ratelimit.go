// Package ratelimit counts requests per key in fixed windows. Memory keeps
// counters in one process (tests, single instance); ratelimitpg shares
// them through PostgreSQL across every API instance.
package ratelimit

import (
	"context"
	"sync"
	"time"
)

// Rate is a fixed-window limit: at most N requests per Window per key.
type Rate struct {
	N      int
	Window time.Duration
}

// Valid reports whether the rate is usable.
func (r Rate) Valid() bool { return r.N > 0 && r.Window > 0 }

// Limiter counts one hit for key under r. When the key is over the limit it
// returns false and how long until the window resets.
type Limiter interface {
	Allow(ctx context.Context, key string, r Rate) (bool, time.Duration, error)
}

type window struct {
	start time.Time
	n     int
}

// Memory is an in-process Limiter.
type Memory struct {
	now func() time.Time

	mu        sync.Mutex
	windows   map[string]*window
	lastSweep time.Time
}

// NewMemory builds an in-process limiter; now is time.Now when nil.
func NewMemory(now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{now: now, windows: map[string]*window{}}
}

// sweepEvery bounds how long expired windows stay in memory.
const sweepEvery = time.Hour

// Allow implements Limiter.
func (m *Memory) Allow(_ context.Context, key string, r Rate) (bool, time.Duration, error) {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if now.Sub(m.lastSweep) >= sweepEvery {
		for k, w := range m.windows {
			if now.Sub(w.start) >= sweepEvery {
				delete(m.windows, k)
			}
		}
		m.lastSweep = now
	}
	w := m.windows[key]
	if w == nil || now.Sub(w.start) >= r.Window {
		w = &window{start: now}
		m.windows[key] = w
	}
	if w.n >= r.N {
		return false, w.start.Add(r.Window).Sub(now), nil
	}
	w.n++
	return true, 0, nil
}
