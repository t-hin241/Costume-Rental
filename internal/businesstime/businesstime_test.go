package businesstime_test

import (
	"errors"
	"testing"
	"time"

	"webee_rentor/internal/businesstime"
)

func TestVietnamLocalTimeToUTC(t *testing.T) {
	loc, err := businesstime.LoadLocation()
	if err != nil {
		t.Fatal(err)
	}
	d, err := businesstime.ParseDate("2026-11-10")
	if err != nil {
		t.Fatal(err)
	}
	tod, err := businesstime.ParseTimeOfDay("10:00")
	if err != nil {
		t.Fatal(err)
	}
	got := businesstime.At(d, tod, loc)
	want := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
	if !got.Equal(want) || got.Location() != time.UTC {
		t.Fatalf("At = %s, want %s", got, want)
	}
	if back := businesstime.DateOf(time.Date(2026, 11, 10, 17, 30, 0, 0, time.UTC), loc); back.String() != "2026-11-11" {
		t.Fatalf("17:30 UTC is next local day, got %s", back)
	}
}

func TestParseDateRejectsNonCalendarDates(t *testing.T) {
	for _, s := range []string{"2026-02-30", "2026-13-01", "10/11/2026", ""} {
		if _, err := businesstime.ParseDate(s); !errors.Is(err, businesstime.ErrInvalidDate) {
			t.Errorf("ParseDate(%q) err = %v", s, err)
		}
	}
}

func TestParseTimeOfDay(t *testing.T) {
	for _, s := range []string{"24:00", "9:00", "+9:00", "09:60", "ab:cd"} {
		if _, err := businesstime.ParseTimeOfDay(s); !errors.Is(err, businesstime.ErrInvalidTimeOfDay) {
			t.Errorf("ParseTimeOfDay(%q) err = %v", s, err)
		}
	}
	tod, err := businesstime.ParseTimeOfDay("23:59")
	if err != nil || tod.String() != "23:59" {
		t.Fatalf("ParseTimeOfDay(23:59) = %v, %v", tod, err)
	}
}
