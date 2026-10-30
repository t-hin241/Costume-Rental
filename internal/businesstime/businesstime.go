// Package businesstime is the single place where local business dates and
// wall-clock times are interpreted. Everything else compares UTC instants.
package businesstime

import (
	"fmt"
	"time"

	// Embed the tz database so Asia/Ho_Chi_Minh resolves even in minimal
	// containers without /usr/share/zoneinfo.
	_ "time/tzdata"

	"webee_rentor/internal/domainerr"
)

// LocationName is the business timezone of the MVP store.
const LocationName = "Asia/Ho_Chi_Minh"

var (
	ErrInvalidDate      = domainerr.New(domainerr.KindInvalid, "businesstime.invalid_date", "date must be a real calendar date in YYYY-MM-DD form")
	ErrInvalidTimeOfDay = domainerr.New(domainerr.KindInvalid, "businesstime.invalid_time_of_day", "time of day must be HH:MM between 00:00 and 23:59")
)

// LoadLocation loads the business timezone.
func LoadLocation() (*time.Location, error) {
	loc, err := time.LoadLocation(LocationName)
	if err != nil {
		return nil, fmt.Errorf("load business location %s: %w", LocationName, err)
	}
	return loc, nil
}

// Date is a civil calendar date without a time or zone.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// ParseDate parses YYYY-MM-DD and rejects normalised dates such as 2026-02-30.
func ParseDate(s string) (Date, error) {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return Date{}, fmt.Errorf("parse date %q: %w", s, ErrInvalidDate)
	}
	return Date{Year: t.Year(), Month: t.Month(), Day: t.Day()}, nil
}

// Valid reports whether d names a real calendar day.
func (d Date) Valid() bool {
	t := time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC)
	return t.Year() == d.Year && t.Month() == d.Month && t.Day() == d.Day
}

// Weekday returns the day of week of d.
func (d Date) Weekday() time.Weekday {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC).Weekday()
}

func (d Date) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
}

// TimeOfDay is a wall-clock time with minute precision.
type TimeOfDay struct {
	minutes int
}

// NewTimeOfDay builds HH:MM.
func NewTimeOfDay(hour, minute int) (TimeOfDay, error) {
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return TimeOfDay{}, fmt.Errorf("time of day %02d:%02d: %w", hour, minute, ErrInvalidTimeOfDay)
	}
	return TimeOfDay{minutes: hour*60 + minute}, nil
}

// ParseTimeOfDay parses HH:MM.
func ParseTimeOfDay(s string) (TimeOfDay, error) {
	if len(s) != 5 || s[2] != ':' {
		return TimeOfDay{}, fmt.Errorf("parse time of day %q: %w", s, ErrInvalidTimeOfDay)
	}
	for _, i := range []int{0, 1, 3, 4} {
		if s[i] < '0' || s[i] > '9' {
			return TimeOfDay{}, fmt.Errorf("parse time of day %q: %w", s, ErrInvalidTimeOfDay)
		}
	}
	h := int(s[0]-'0')*10 + int(s[1]-'0')
	m := int(s[3]-'0')*10 + int(s[4]-'0')
	return NewTimeOfDay(h, m)
}

// Minutes returns minutes since midnight.
func (t TimeOfDay) Minutes() int { return t.minutes }

func (t TimeOfDay) String() string {
	return fmt.Sprintf("%02d:%02d", t.minutes/60, t.minutes%60)
}

// At converts a local date and time in loc to a UTC instant.
func At(d Date, t TimeOfDay, loc *time.Location) time.Time {
	return time.Date(d.Year, d.Month, d.Day, t.minutes/60, t.minutes%60, 0, 0, loc).UTC()
}

// DateOf returns the local date of instant t in loc.
func DateOf(t time.Time, loc *time.Location) Date {
	l := t.In(loc)
	return Date{Year: l.Year(), Month: l.Month(), Day: l.Day()}
}

// TimeOfDayOf returns the local wall-clock time of t in loc, truncated to the
// minute, and whether t had no sub-minute component.
func TimeOfDayOf(t time.Time, loc *time.Location) (TimeOfDay, bool) {
	l := t.In(loc)
	exact := l.Second() == 0 && l.Nanosecond() == 0
	return TimeOfDay{minutes: l.Hour()*60 + l.Minute()}, exact
}
