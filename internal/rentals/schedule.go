package rentals

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/period"
)

var (
	ErrStoreClosed        = domainerr.New(domainerr.KindInvalid, "rentals.store_closed", "the store is closed on that date")
	ErrSlotNotFound       = domainerr.New(domainerr.KindInvalid, "rentals.slot_not_found", "the chosen time slot is not offered on that date")
	ErrReturnOutsideSlots = domainerr.New(domainerr.KindInvalid, "rentals.return_outside_slots", "the due time does not fall on an open return slot")
	ErrDurationMismatch   = domainerr.New(domainerr.KindInvalid, "rentals.duration_mismatch", "rental duration must equal the package duration exactly")
	ErrPickupTooSoon      = domainerr.New(domainerr.KindInvalid, "rentals.pickup_too_soon", "pickup is earlier than the minimum lead time")
	ErrPickupTooFarAhead  = domainerr.New(domainerr.KindInvalid, "rentals.pickup_too_far_ahead", "pickup is beyond the maximum booking horizon")
)

// Schedule is the customer's rental period: pickup_at to due_at. Actual
// handover/return times are recorded separately on the booking.
type Schedule struct {
	PickupAt time.Time
	DueAt    time.Time
}

// NewSchedule validates and normalises to UTC.
func NewSchedule(pickupAt, dueAt time.Time) (Schedule, error) {
	r, err := period.New(pickupAt, dueAt)
	if err != nil {
		return Schedule{}, fmt.Errorf("schedule: %w", err)
	}
	return Schedule{PickupAt: r.Start(), DueAt: r.End()}, nil
}

// Duration is the agreed rental length.
func (s Schedule) Duration() time.Duration { return s.DueAt.Sub(s.PickupAt) }

// Block is the inventory block [pickup - preparation, due + turnaround).
func (s Schedule) Block(b Buffer) (period.Range, error) {
	r, err := period.New(s.PickupAt, s.DueAt)
	if err != nil {
		return period.Range{}, err
	}
	return r.Expand(b.Preparation, b.Turnaround)
}

// Slot is a pickup/return appointment window. The appointment instant is
// Start; End bounds when the customer may arrive. Pickup and return use the
// same slot set (owner decision D7).
type Slot struct {
	ID    string
	Start businesstime.TimeOfDay
	End   businesstime.TimeOfDay
}

// StoreCalendar turns the customer's date + slot choice into UTC instants
// and checks returns fall on open slots. It is the only place that reads
// local store hours.
type StoreCalendar struct {
	loc    *time.Location
	weekly map[time.Weekday][]Slot
	closed map[businesstime.Date]bool
	// recurring holds the dates closed every year (decision D25).
	recurring map[businesstime.MonthDay]bool
	// delivery holds the courier delivery/pickup windows (decision D19).
	delivery map[time.Weekday][]Slot
}

// NewStoreCalendar validates store hours. closed lists holidays and other
// closure dates in local time.
func NewStoreCalendar(loc *time.Location, weekly map[time.Weekday][]Slot, closed []businesstime.Date) (*StoreCalendar, error) {
	if loc == nil {
		return nil, domainerr.Invalid("calendar.location", "required")
	}
	c := &StoreCalendar{loc: loc, closed: map[businesstime.Date]bool{}, delivery: map[time.Weekday][]Slot{}}
	var err error
	if c.weekly, err = sortedSlots("calendar.slots", weekly); err != nil {
		return nil, err
	}
	for _, d := range closed {
		if !d.Valid() {
			return nil, fmt.Errorf("closed date %s: %w", d, businesstime.ErrInvalidDate)
		}
		c.closed[d] = true
	}
	return c, nil
}

func sortedSlots(field string, weekly map[time.Weekday][]Slot) (map[time.Weekday][]Slot, error) {
	out := map[time.Weekday][]Slot{}
	for day, slots := range weekly {
		ids := map[string]bool{}
		sorted := slices.Clone(slots)
		slices.SortFunc(sorted, func(a, b Slot) int { return a.Start.Minutes() - b.Start.Minutes() })
		for i, s := range sorted {
			if strings.TrimSpace(s.ID) == "" || ids[s.ID] {
				return nil, domainerr.Invalid(field, fmt.Sprintf("%s: slot ids must be present and unique", day))
			}
			if s.End.Minutes() <= s.Start.Minutes() {
				return nil, domainerr.Invalid(field, fmt.Sprintf("%s slot %s must end after it starts", day, s.ID))
			}
			if i > 0 && sorted[i-1].End.Minutes() > s.Start.Minutes() {
				return nil, domainerr.Invalid(field, fmt.Sprintf("%s slots overlap", day))
			}
			ids[s.ID] = true
		}
		out[day] = sorted
	}
	return out, nil
}

// Location returns the business timezone.
func (c *StoreCalendar) Location() *time.Location { return c.loc }

// SetRecurringClosures configures the dates the store closes every year.
// Lunar holidays move between years and are listed as closed dates instead.
func (c *StoreCalendar) SetRecurringClosures(days []businesstime.MonthDay) error {
	recurring := make(map[businesstime.MonthDay]bool, len(days))
	for _, d := range days {
		if !d.Valid() {
			return fmt.Errorf("recurring closure %s: %w", d, businesstime.ErrInvalidMonthDay)
		}
		recurring[d] = true
	}
	c.recurring = recurring
	return nil
}

// isClosed reports whether the store is closed on a local date.
func (c *StoreCalendar) isClosed(d businesstime.Date) bool {
	return c.closed[d] || c.recurring[d.MonthDay()]
}

// ResolveSchedule converts a local pickup date and slot plus the package
// duration into a schedule. due_at = pickup_at + duration exactly; it must
// land on a return slot on an open day, otherwise the choice is rejected.
func (c *StoreCalendar) ResolveSchedule(pickupDate businesstime.Date, slotID string, duration time.Duration) (Schedule, error) {
	if !pickupDate.Valid() {
		return Schedule{}, businesstime.ErrInvalidDate
	}
	if c.isClosed(pickupDate) {
		return Schedule{}, fmt.Errorf("pickup %s: %w", pickupDate, ErrStoreClosed)
	}
	i := slices.IndexFunc(c.weekly[pickupDate.Weekday()], func(s Slot) bool { return s.ID == slotID })
	if i < 0 {
		return Schedule{}, fmt.Errorf("pickup %s slot %q: %w", pickupDate, slotID, ErrSlotNotFound)
	}
	pickup := businesstime.At(pickupDate, c.weekly[pickupDate.Weekday()][i].Start, c.loc)
	s, err := NewSchedule(pickup, pickup.Add(duration))
	if err != nil {
		return Schedule{}, err
	}
	if err := c.ValidateSchedule(s); err != nil {
		return Schedule{}, err
	}
	return s, nil
}

// ValidateSchedule checks a store-pickup, store-return schedule: pickup and
// due both fall exactly on a slot start on an open day.
func (c *StoreCalendar) ValidateSchedule(s Schedule) error {
	_, _, err := c.ValidateFulfillment(s, LegStore, LegStore)
	return err
}

func (c *StoreCalendar) onSlotStart(t time.Time) bool {
	d := businesstime.DateOf(t, c.loc)
	if c.isClosed(d) {
		return false
	}
	tod, exact := businesstime.TimeOfDayOf(t, c.loc)
	if !exact {
		return false
	}
	return slices.ContainsFunc(c.weekly[d.Weekday()], func(s Slot) bool { return s.Start == tod })
}
