package rentals

import (
	"fmt"
	"slices"
	"time"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/delivery"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/money"
	"webee_rentor/internal/period"
)

// LegMode is how one direction of a rental is fulfilled (decision D19: the
// customer chooses each direction separately).
type LegMode string

const (
	// LegStore: pickup or return at the store counter.
	LegStore LegMode = "STORE"
	// LegCourier: delivered to, or collected from, the customer address.
	LegCourier LegMode = "COURIER"
)

func (m LegMode) orStore() LegMode {
	if m == "" {
		return LegStore
	}
	return m
}

func (m LegMode) valid() bool { return m == LegStore || m == LegCourier }

var (
	ErrNoDeliveryWindow   = domainerr.New(domainerr.KindInvalid, "rentals.no_delivery_window", "the chosen time does not match a delivery window on an open day")
	ErrDeliveryNotOffered = domainerr.New(domainerr.KindInvalid, "rentals.delivery_not_offered", "a courier leg needs a service-area zone")
)

// Fulfillment is the booking's delivery snapshot. The address itself lives
// on the delivery tasks, not on the booking.
type Fulfillment struct {
	Outbound LegMode
	Return   LegMode
	// OutboundWindow is the courier delivery window; the rental starts at its
	// end. ReturnWindow is the courier pickup window; it ends at due_at.
	OutboundWindow *period.Range
	ReturnWindow   *period.Range
	ZoneID         string
	AreaVersion    string
	// Customer fees from the zone table at booking time.
	OutboundFee   money.Money
	ReturnFee     money.Money
	RedeliveryFee money.Money
	// DeliveredAt is when the carrier delivered the outbound parcel.
	DeliveredAt *time.Time
	// ReturnReadyAt is when the customer was ready for a courier pickup that
	// failed because of the store or the carrier; lateness stops there.
	ReturnReadyAt *time.Time
}

// courier reports whether any leg uses the courier.
func (f Fulfillment) courier() bool {
	return f.Outbound.orStore() == LegCourier || f.Return.orStore() == LegCourier
}

// DeliveryFees is the sum of both legs.
func (f Fulfillment) DeliveryFees(c money.Currency) money.Money {
	sum, err := money.Sum(c, zeroIfUnset(f.OutboundFee, c), zeroIfUnset(f.ReturnFee, c))
	if err != nil {
		return money.Zero(c)
	}
	return sum
}

// ServiceDelay is how late the outbound parcel arrived after the rental
// start; it is credited against late-return time (decision D19).
func (f Fulfillment) ServiceDelay(pickupAt time.Time) time.Duration {
	if f.Outbound.orStore() != LegCourier || f.DeliveredAt == nil {
		return 0
	}
	return max(f.DeliveredAt.Sub(pickupAt), 0)
}

func zeroIfUnset(m money.Money, c money.Currency) money.Money {
	if !m.Currency().Valid() {
		return money.Zero(c)
	}
	return m
}

// FulfillmentRequest is the customer's choice at checkout. Zone is resolved
// server-side from the address (delivery.ServiceArea.Resolve).
type FulfillmentRequest struct {
	Outbound    LegMode
	Return      LegMode
	Zone        *delivery.Zone
	AreaVersion string
}

// SetDeliveryWindows configures the courier delivery/pickup windows.
func (c *StoreCalendar) SetDeliveryWindows(weekly map[time.Weekday][]Slot) error {
	w, err := sortedSlots("calendar.delivery_windows", weekly)
	if err != nil {
		return err
	}
	c.delivery = w
	return nil
}

// ResolveFulfillment turns the customer's date, slot or window and modes into
// a schedule: store pickup starts at the slot start; courier delivery starts
// the rental at the window end. due_at = start + duration, and must be a
// store slot start (store return) or a delivery window end (courier pickup).
func (c *StoreCalendar) ResolveFulfillment(date businesstime.Date, outbound LegMode, slotID string, duration time.Duration, ret LegMode) (Schedule, error) {
	if !date.Valid() {
		return Schedule{}, businesstime.ErrInvalidDate
	}
	if c.closed[date] {
		return Schedule{}, fmt.Errorf("pickup %s: %w", date, ErrStoreClosed)
	}
	var start time.Time
	switch outbound.orStore() {
	case LegStore:
		return c.ResolveSchedule(date, slotID, duration)
	case LegCourier:
		i := slices.IndexFunc(c.delivery[date.Weekday()], func(s Slot) bool { return s.ID == slotID })
		if i < 0 {
			return Schedule{}, fmt.Errorf("delivery %s window %q: %w", date, slotID, ErrNoDeliveryWindow)
		}
		start = businesstime.At(date, c.delivery[date.Weekday()][i].End, c.loc)
	default:
		return Schedule{}, domainerr.Invalid("outbound", "unknown mode")
	}
	s, err := NewSchedule(start, start.Add(duration))
	if err != nil {
		return Schedule{}, err
	}
	if _, _, err := c.ValidateFulfillment(s, outbound, ret); err != nil {
		return Schedule{}, err
	}
	return s, nil
}

// ValidateFulfillment checks a schedule against the modes and returns the
// courier windows it implies.
func (c *StoreCalendar) ValidateFulfillment(s Schedule, outbound, ret LegMode) (outWin, retWin *period.Range, err error) {
	switch outbound.orStore() {
	case LegStore:
		if !c.onSlotStart(s.PickupAt) {
			return nil, nil, fmt.Errorf("pickup %s: %w", s.PickupAt, ErrSlotNotFound)
		}
	case LegCourier:
		w, ok := c.windowEndingAt(s.PickupAt)
		if !ok {
			return nil, nil, fmt.Errorf("delivery ending %s: %w", s.PickupAt, ErrNoDeliveryWindow)
		}
		outWin = &w
	default:
		return nil, nil, domainerr.Invalid("outbound", "unknown mode")
	}
	switch ret.orStore() {
	case LegStore:
		if !c.onSlotStart(s.DueAt) {
			return nil, nil, fmt.Errorf("due %s: %w", s.DueAt, ErrReturnOutsideSlots)
		}
	case LegCourier:
		w, ok := c.windowEndingAt(s.DueAt)
		if !ok {
			return nil, nil, fmt.Errorf("pickup window ending %s: %w", s.DueAt, ErrNoDeliveryWindow)
		}
		retWin = &w
	default:
		return nil, nil, domainerr.Invalid("return", "unknown mode")
	}
	return outWin, retWin, nil
}

func (c *StoreCalendar) windowEndingAt(t time.Time) (period.Range, bool) {
	d := businesstime.DateOf(t, c.loc)
	if c.closed[d] {
		return period.Range{}, false
	}
	tod, exact := businesstime.TimeOfDayOf(t, c.loc)
	if !exact {
		return period.Range{}, false
	}
	for _, w := range c.delivery[d.Weekday()] {
		if w.End == tod {
			r, err := period.New(businesstime.At(d, w.Start, c.loc), t)
			return r, err == nil
		}
	}
	return period.Range{}, false
}

// newFulfillment validates the checkout choice and snapshots fees.
func newFulfillment(req FulfillmentRequest, s Schedule, cal *StoreCalendar, currency money.Currency) (Fulfillment, error) {
	f := Fulfillment{Outbound: req.Outbound.orStore(), Return: req.Return.orStore(), AreaVersion: req.AreaVersion,
		OutboundFee: money.Zero(currency), ReturnFee: money.Zero(currency), RedeliveryFee: money.Zero(currency)}
	if !f.Outbound.valid() || !f.Return.valid() {
		return Fulfillment{}, domainerr.Invalid("fulfillment", "unknown mode")
	}
	outWin, retWin, err := cal.ValidateFulfillment(s, f.Outbound, f.Return)
	if err != nil {
		return Fulfillment{}, err
	}
	f.OutboundWindow, f.ReturnWindow = outWin, retWin
	if !f.courier() {
		return f, nil
	}
	if req.Zone == nil || !req.Zone.Active || req.AreaVersion == "" {
		return Fulfillment{}, ErrDeliveryNotOffered
	}
	f.ZoneID, f.RedeliveryFee = req.Zone.ID, req.Zone.RedeliveryFee
	if f.Outbound == LegCourier {
		f.OutboundFee = req.Zone.OutboundFee
	}
	if f.Return == LegCourier {
		f.ReturnFee = req.Zone.ReturnFee
	}
	for _, m := range []money.Money{f.OutboundFee, f.ReturnFee, f.RedeliveryFee} {
		if m.Currency() != currency {
			return Fulfillment{}, fmt.Errorf("zone %s fees: %w", f.ZoneID, money.ErrCurrencyMismatch)
		}
	}
	return f, nil
}

// blockFor is the inventory block for a component: preparation before the
// item leaves the store (courier: delivery window start minus outbound lead)
// through turnaround after it is back (courier: due plus return transit).
func (b *Booking) blockFor(s Schedule, f Fulfillment, buf Buffer) (period.Range, error) {
	start := s.PickupAt
	if f.Outbound.orStore() == LegCourier && f.OutboundWindow != nil {
		start = f.OutboundWindow.Start().Add(-b.Policy.Delivery.OutboundLead)
	}
	end := s.DueAt
	if f.Return.orStore() == LegCourier {
		end = end.Add(b.Policy.Delivery.ReturnTransit)
	}
	return period.New(start.Add(-buf.Preparation), end.Add(buf.Turnaround))
}
