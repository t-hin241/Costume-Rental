// Package period models half-open time ranges [start, end) used for rental
// schedules and inventory blocks.
package period

import (
	"encoding/json"
	"fmt"
	"time"

	"webee_rentor/internal/domainerr"
)

var ErrInvalidRange = domainerr.New(domainerr.KindInvalid, "period.invalid_range", "range end must be after start")

// Range is a half-open interval [Start, End) stored in UTC. Two ranges where
// one ends exactly when the other starts do not overlap.
type Range struct {
	start time.Time
	end   time.Time
}

// New builds a range; end must be strictly after start.
func New(start, end time.Time) (Range, error) {
	if start.IsZero() || end.IsZero() || !end.After(start) {
		return Range{}, fmt.Errorf("range [%s, %s): %w", start, end, ErrInvalidRange)
	}
	return Range{start: start.UTC(), end: end.UTC()}, nil
}

func (r Range) Start() time.Time        { return r.start }
func (r Range) End() time.Time          { return r.end }
func (r Range) Duration() time.Duration { return r.end.Sub(r.start) }
func (r Range) IsZero() bool            { return r.start.IsZero() && r.end.IsZero() }

// Overlaps reports whether the two ranges share any instant.
func (r Range) Overlaps(o Range) bool {
	return r.start.Before(o.end) && o.start.Before(r.end)
}

// Contains reports whether t is in [Start, End).
func (r Range) Contains(t time.Time) bool {
	return !t.Before(r.start) && t.Before(r.end)
}

// Expand widens the range by before at the start and after at the end.
// Negative values are rejected to keep buffers meaningful.
func (r Range) Expand(before, after time.Duration) (Range, error) {
	if before < 0 || after < 0 {
		return Range{}, fmt.Errorf("expand by %s/%s: %w", before, after, ErrInvalidRange)
	}
	return New(r.start.Add(-before), r.end.Add(after))
}

// Minus returns the parts of r not covered by o (zero, one or two ranges).
func (r Range) Minus(o Range) []Range {
	if !r.Overlaps(o) {
		return []Range{r}
	}
	var out []Range
	if r.start.Before(o.start) {
		out = append(out, Range{start: r.start, end: o.start})
	}
	if o.end.Before(r.end) {
		out = append(out, Range{start: o.end, end: r.end})
	}
	return out
}

func (r Range) String() string {
	return fmt.Sprintf("[%s, %s)", r.start.Format(time.RFC3339), r.end.Format(time.RFC3339))
}

type rangeJSON struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// MarshalJSON encodes {"start": RFC 3339, "end": RFC 3339} for stored
// snapshots. The zero range encodes as null.
func (r Range) MarshalJSON() ([]byte, error) {
	if r.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(rangeJSON{Start: r.start, End: r.end})
}

// UnmarshalJSON decodes and re-validates a range written by MarshalJSON.
func (r *Range) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*r = Range{}
		return nil
	}
	var v rangeJSON
	if err := json.Unmarshal(b, &v); err != nil {
		return fmt.Errorf("decode range: %w", err)
	}
	n, err := New(v.Start, v.End)
	if err != nil {
		return fmt.Errorf("decode range: %w", err)
	}
	*r = n
	return nil
}
