package period_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"webee_rentor/internal/period"
)

var t0 = time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC)

func rng(t *testing.T, from, to time.Duration) period.Range {
	t.Helper()
	r, err := period.New(t0.Add(from), t0.Add(to))
	if err != nil {
		t.Fatalf("period.New: %v", err)
	}
	return r
}

func TestHalfOpenOverlap(t *testing.T) {
	a := rng(t, 0, 24*time.Hour)
	tests := []struct {
		name string
		b    period.Range
		want bool
	}{
		{"adjacent after", rng(t, 24*time.Hour, 48*time.Hour), false},
		{"adjacent before", rng(t, -24*time.Hour, 0), false},
		{"one minute overlap", rng(t, 24*time.Hour-time.Minute, 48*time.Hour), true},
		{"contained", rng(t, time.Hour, 2*time.Hour), true},
		{"identical", a, true},
	}
	for _, tc := range tests {
		if got := a.Overlaps(tc.b); got != tc.want {
			t.Errorf("%s: Overlaps = %v, want %v", tc.name, got, tc.want)
		}
		if got := tc.b.Overlaps(a); got != tc.want {
			t.Errorf("%s (reversed): Overlaps = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestContainsIsHalfOpen(t *testing.T) {
	r := rng(t, 0, time.Hour)
	if !r.Contains(t0) || r.Contains(t0.Add(time.Hour)) {
		t.Fatal("Contains must include start and exclude end")
	}
}

func TestInvalidRanges(t *testing.T) {
	if _, err := period.New(t0, t0); !errors.Is(err, period.ErrInvalidRange) {
		t.Fatalf("empty range err = %v", err)
	}
	if _, err := rng(t, 0, time.Hour).Expand(-time.Minute, 0); !errors.Is(err, period.ErrInvalidRange) {
		t.Fatalf("negative expand err = %v", err)
	}
}

func TestMinus(t *testing.T) {
	a := rng(t, 0, 10*time.Hour)
	if got := a.Minus(rng(t, 2*time.Hour, 5*time.Hour)); len(got) != 2 || got[0] != rng(t, 0, 2*time.Hour) || got[1] != rng(t, 5*time.Hour, 10*time.Hour) {
		t.Fatalf("hole = %v", got)
	}
	if got := a.Minus(rng(t, 0, 10*time.Hour)); len(got) != 0 {
		t.Fatalf("covered = %v", got)
	}
	if got := a.Minus(rng(t, -time.Hour, 4*time.Hour)); len(got) != 1 || got[0] != rng(t, 4*time.Hour, 10*time.Hour) {
		t.Fatalf("extension = %v", got)
	}
	if got := a.Minus(rng(t, 20*time.Hour, 30*time.Hour)); len(got) != 1 || got[0] != a {
		t.Fatalf("disjoint = %v", got)
	}
}

func TestExpandAddsBuffers(t *testing.T) {
	r, err := rng(t, 2*time.Hour, 26*time.Hour).Expand(2*time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Start().Equal(t0) || !r.End().Equal(t0.Add(50*time.Hour)) {
		t.Fatalf("Expand = %s", r)
	}
}

func TestJSONRoundTripRevalidates(t *testing.T) {
	r := rng(t, 0, 2*time.Hour)
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var back period.Range
	if err := json.Unmarshal(b, &back); err != nil || back != r {
		t.Fatalf("back = %v, %v (json %s)", back, err, b)
	}
	if b, _ := json.Marshal(period.Range{}); string(b) != "null" {
		t.Fatalf("zero range json = %s", b)
	}
	err = json.Unmarshal([]byte(`{"start":"2026-11-10T02:00:00Z","end":"2026-11-10T01:00:00Z"}`), &back)
	if !errors.Is(err, period.ErrInvalidRange) {
		t.Fatalf("inverted range err = %v", err)
	}
}
