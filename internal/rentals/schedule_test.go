package rentals_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/rentals"
)

func TestResolveScheduleUsesBusinessTimezone(t *testing.T) {
	s := schedule(t, "2026-11-10", "morning", 3*day)
	wantPickup := time.Date(2026, 11, 10, 2, 0, 0, 0, time.UTC) // 09:00 +07
	wantDue := time.Date(2026, 11, 13, 2, 0, 0, 0, time.UTC)
	if !s.PickupAt.Equal(wantPickup) || !s.DueAt.Equal(wantDue) {
		t.Fatalf("schedule = %+v", s)
	}
	block, err := s.Block(rentals.Buffer{Preparation: 2 * time.Hour, Turnaround: day})
	if err != nil {
		t.Fatal(err)
	}
	if !block.Start().Equal(wantPickup.Add(-2*time.Hour)) || !block.End().Equal(wantDue.Add(day)) {
		t.Fatalf("block = %s", block)
	}
}

func TestResolveScheduleRejectsClosedOrInvalidChoices(t *testing.T) {
	cal := calendar(t)
	cases := []struct {
		name string
		date string
		slot string
		d    time.Duration
		want error
	}{
		{"holiday pickup", "2026-11-20", "morning", day, rentals.ErrStoreClosed},
		{"sunday pickup", "2026-11-15", "morning", day, rentals.ErrSlotNotFound},
		{"unknown slot", "2026-11-10", "night", day, rentals.ErrSlotNotFound},
		{"due on sunday", "2026-11-12", "morning", 3 * day, rentals.ErrReturnOutsideSlots},
		{"due on holiday", "2026-11-19", "afternoon", day, rentals.ErrReturnOutsideSlots},
		{"due between slots", "2026-11-10", "morning", 3 * time.Hour, rentals.ErrReturnOutsideSlots},
	}
	for _, tc := range cases {
		if _, err := cal.ResolveSchedule(date(t, tc.date), tc.slot, tc.d); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if err := cal.ValidateSchedule(rentals.Schedule{
		PickupAt: time.Date(2026, 11, 10, 2, 0, 30, 0, time.UTC), DueAt: time.Date(2026, 11, 11, 2, 0, 0, 0, time.UTC),
	}); !errors.Is(err, rentals.ErrSlotNotFound) {
		t.Errorf("sub-minute pickup err = %v", err)
	}
}

// With a DST zone, adding an exact package duration shifts the local wall
// time; the calendar rejects it instead of silently drifting. Asia/Ho_Chi_Minh
// has no DST, so this guards configuration mistakes.
func TestDSTShiftIsRejectedNotDrifted(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	slot := []rentals.Slot{{ID: "morning", Start: tod(t, "09:00"), End: tod(t, "11:00")}}
	weekly := map[time.Weekday][]rentals.Slot{}
	for d := time.Sunday; d <= time.Saturday; d++ {
		weekly[d] = slot
	}
	cal, err := rentals.NewStoreCalendar(loc, weekly, nil)
	if err != nil {
		t.Fatal(err)
	}
	// DST ends 2026-10-25 in Berlin; 24h after 09:00 on the 24th is 08:00.
	if _, err := cal.ResolveSchedule(businesstime.Date{Year: 2026, Month: 10, Day: 24}, "morning", day); !errors.Is(err, rentals.ErrReturnOutsideSlots) {
		t.Fatalf("err = %v", err)
	}
}

func TestStoreCalendarValidation(t *testing.T) {
	loc, _ := businesstime.LoadLocation()
	overlapping := map[time.Weekday][]rentals.Slot{time.Monday: {
		{ID: "a", Start: tod(t, "09:00"), End: tod(t, "11:00")},
		{ID: "b", Start: tod(t, "10:00"), End: tod(t, "12:00")},
	}}
	if _, err := rentals.NewStoreCalendar(loc, overlapping, nil); err == nil {
		t.Fatal("overlapping slots must be rejected")
	}
	if _, err := rentals.NewStoreCalendar(nil, nil, nil); err == nil {
		t.Fatal("missing location must be rejected")
	}
}

func TestPolicyHasNoImplicitDefaults(t *testing.T) {
	if err := (rentals.Policy{}).Validate(); err == nil {
		t.Fatal("an empty policy must be rejected")
	}
	p := testPolicy()
	p.Confirmation = rentals.ConfirmationRule{Mode: rentals.ConfirmAuto, Funds: rentals.FundsRequirement{Kind: rentals.FundsNone}}
	if err := p.Validate(); err == nil {
		t.Fatal("auto confirmation without a funds requirement must be rejected")
	}

	invalid := map[string]func(*rentals.Policy){
		"percent missing":   func(p *rentals.Policy) { p.Reservation.RentPercent = 0 },
		"unknown treatment": func(p *rentals.Policy) { p.Reservation.Treatment = "" },
		"no zero-notice tier": func(p *rentals.Policy) {
			p.Cancellation.Tiers = p.Cancellation.Tiers[:3]
		},
		"tier percent above 100": func(p *rentals.Policy) { p.Cancellation.Tiers[0].FeePercent = 101 },
		"no-show percent negative": func(p *rentals.Policy) {
			p.Cancellation.NoShowFeePercent = -1
		},
		"requirement without reservation": func(p *rentals.Policy) { p.Reservation = rentals.ReservationPolicy{Kind: rentals.ReservationNone} },
	}
	for name, mutate := range invalid {
		p := testPolicy()
		p.Cancellation.Tiers = slices.Clone(p.Cancellation.Tiers)
		mutate(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("%s: policy must be rejected", name)
		}
	}
}
