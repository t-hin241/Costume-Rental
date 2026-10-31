package rentals

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/money"
	"webee_rentor/internal/promotions"
)

var (
	ErrMixedPackages   = domainerr.New(domainerr.KindInvalid, "rentals.mixed_packages", "all lines of a booking must use the same rental package")
	ErrPricingMismatch = domainerr.New(domainerr.KindInvalid, "rentals.pricing_mismatch", "pricing result does not match the booking lines")
)

// mustSub subtracts amounts already validated in the same currency.
func mustSub(a, b money.Money) money.Money {
	d, _ := a.Sub(b)
	return d
}

// LineRequest pairs a line ID with a quote freshly computed by the server
// from the current catalog (never a client-supplied price).
type LineRequest struct {
	ID    LineID
	Quote catalog.LineQuote
}

// InventoryView is transactional inventory state for the variants involved,
// read under lock by the use case.
type InventoryView struct {
	Assets      []inventory.Asset
	Allocations []inventory.Allocation
	Policy      inventory.AllocationPolicy
}

// PlaceRequest is everything needed to create a booking and its allocations.
type PlaceRequest struct {
	BookingID  BookingID
	Code       string
	CustomerID identity.UserID
	Actor      identity.Actor
	Schedule   Schedule
	Calendar   *StoreCalendar
	Lines      []LineRequest
	// Fulfillment is the outbound/return choice; zero means store both ways.
	Fulfillment FulfillmentRequest
	// Pricing is the promotions.Price result for these lines (subtotals =
	// quote rent); nil means no discount applies.
	Pricing   *promotions.Result
	Policy    Policy
	Inventory InventoryView
	Now       time.Time
}

// Placement is the result to persist atomically: the booking with its
// snapshot plus every allocation. If any component is short, nothing is
// returned and nothing may be written.
type Placement struct {
	Booking     *Booking
	Allocations []inventory.PlannedAllocation
	Transition  Transition
}

// PlaceBooking validates the request against the policy, snapshots prices
// and composition, and plans allocations for every component, all or nothing.
func PlaceBooking(req PlaceRequest) (Placement, error) {
	b, err := Quote(req)
	if err != nil {
		return Placement{}, err
	}
	demands, err := b.Demands(b.Schedule, b.Fulfillment)
	if err != nil {
		return Placement{}, err
	}
	planned, err := inventory.Plan(inventory.PlanRequest{
		BookingRef: string(b.ID), Demands: demands,
		Assets: req.Inventory.Assets, Allocations: req.Inventory.Allocations,
		Now: req.Now, Policy: req.Inventory.Policy,
	})
	if err != nil {
		return Placement{}, fmt.Errorf("place booking %s: %w", b.ID, err)
	}
	t := Transition{BookingID: b.ID, Action: ActionCreate, To: b.Status, Actor: req.Actor, At: req.Now.UTC()}
	return Placement{Booking: b, Allocations: planned, Transition: t}, nil
}

// Quote validates the request and builds the booking it would create, with
// every amount, without touching inventory. Nothing is held: PlaceBooking
// checks stock under lock.
func Quote(req PlaceRequest) (*Booking, error) {
	if err := req.Policy.Validate(); err != nil {
		return nil, err
	}
	switch {
	case req.BookingID == "":
		return nil, domainerr.Invalid("booking_id", "required")
	case strings.TrimSpace(req.Code) == "":
		return nil, domainerr.Invalid("code", "required")
	case req.CustomerID == "":
		return nil, domainerr.Invalid("customer_id", "required")
	case !req.Actor.Valid():
		return nil, ErrActorRequired
	case len(req.Lines) == 0:
		return nil, domainerr.Invalid("lines", "at least one line is required")
	case len(req.Lines) > req.Policy.MaxLinesPerBooking:
		return nil, domainerr.Invalid("lines", fmt.Sprintf("at most %d lines", req.Policy.MaxLinesPerBooking))
	}
	if req.Calendar == nil {
		return nil, domainerr.Invalid("calendar", "required")
	}
	if err := checkScheduleWindow(req.Schedule, req.Policy, req.Now); err != nil {
		return nil, err
	}

	pkg := req.Lines[0].Quote.Package
	if req.Schedule.Duration() != pkg.Duration {
		return nil, fmt.Errorf("schedule %s vs package %s: %w", req.Schedule.Duration(), pkg.Duration, ErrDurationMismatch)
	}
	currency := req.Lines[0].Quote.RentAmount.Currency()
	lines := make([]Line, 0, len(req.Lines))
	seen := map[LineID]bool{}
	if req.Pricing != nil && len(req.Pricing.Lines) != len(req.Lines) {
		return nil, fmt.Errorf("pricing has %d lines for %d: %w", len(req.Pricing.Lines), len(req.Lines), ErrPricingMismatch)
	}
	var lists, deposits []money.Money
	for _, lr := range req.Lines {
		q := lr.Quote.Clone()
		switch {
		case lr.ID == "" || seen[lr.ID]:
			return nil, domainerr.Invalid("lines", "line ids must be present and unique")
		case q.Package.ID != pkg.ID:
			return nil, ErrMixedPackages
		case q.Quantity < 1 || q.Quantity > req.Policy.MaxQuantityPerLine:
			return nil, domainerr.Invalid("quantity", fmt.Sprintf("must be between 1 and %d", req.Policy.MaxQuantityPerLine))
		case len(q.Components) == 0:
			return nil, domainerr.Invalid("lines", "quote has no components")
		}
		seen[lr.ID] = true
		line, err := lineFromQuote(lr.ID, q)
		if err != nil {
			return nil, err
		}
		if req.Pricing != nil {
			pr, ok := req.Pricing.Line(string(lr.ID))
			if !ok || !pr.Subtotal.Equal(q.RentAmount) {
				return nil, fmt.Errorf("line %s: %w", lr.ID, ErrPricingMismatch)
			}
			line.Discount, line.RentAmount = pr.Discount, pr.Net
		}
		lists = append(lists, q.RentAmount)
		deposits = append(deposits, q.DepositAmount)
		lines = append(lines, line)
	}
	grossTotal, err := money.Sum(currency, lists...)
	if err != nil {
		return nil, fmt.Errorf("rent total: %w", err)
	}
	rentTotal, _, err := totals(lines, currency)
	if err != nil {
		return nil, fmt.Errorf("rent total: %w", err)
	}
	depositTotal, err := money.Sum(currency, deposits...)
	if err != nil {
		return nil, fmt.Errorf("deposit total: %w", err)
	}
	fulfillment, err := newFulfillment(req.Fulfillment, req.Schedule, req.Calendar, currency)
	if err != nil {
		return nil, err
	}
	reservation := money.Zero(currency)
	if req.Policy.Reservation.Kind != ReservationNone {
		if reservation, err = req.Policy.Reservation.amountFor(rentTotal); err != nil {
			return nil, fmt.Errorf("reservation deposit: %w", err)
		}
	}

	b := &Booking{
		ID: req.BookingID, Code: req.Code, CustomerID: req.CustomerID,
		Status:   StatusPendingConfirmation,
		Schedule: req.Schedule,
		Package:  PackageSnapshot{ID: pkg.ID, Code: pkg.Code, Label: pkg.Label, Duration: pkg.Duration},
		Lines:    lines, Currency: currency, Fulfillment: fulfillment,
		GrossRentTotal: grossTotal, DiscountTotal: mustSub(grossTotal, rentTotal),
		RentTotal: rentTotal, SecurityDepositTotal: depositTotal, ReservationDeposit: reservation,
		Policy:        req.Policy.clone(),
		CreatedAt:     req.Now.UTC(),
		HoldExpiresAt: req.Now.Add(req.Policy.HoldTTL).UTC(),
	}
	if req.Pricing != nil {
		b.Discounts = slices.Clone(req.Pricing.Applied)
		b.ManualAdjustments = slices.Clone(req.Pricing.Manual)
	}
	return b, nil
}

// Demands lists the inventory needed for schedule s and fulfillment f, using
// the snapshot policy buffers per component buffer class plus courier
// transit.
func (b *Booking) Demands(s Schedule, f Fulfillment) ([]inventory.Demand, error) {
	var out []inventory.Demand
	for _, l := range b.Lines {
		ds, err := b.demandsFor(l, s, f)
		if err != nil {
			return nil, err
		}
		out = append(out, ds...)
	}
	return out, nil
}

func (b *Booking) demandsFor(l Line, s Schedule, f Fulfillment) ([]inventory.Demand, error) {
	var out []inventory.Demand
	for _, c := range l.Components {
		block, err := b.blockFor(s, f, b.Policy.Buffers.For(c.BufferClass))
		if err != nil {
			return nil, fmt.Errorf("line %s component %s block: %w", l.ID, c.Key, err)
		}
		out = append(out, inventory.Demand{LineRef: string(l.ID), ComponentKey: c.Key, VariantID: c.VariantID, Quantity: c.Quantity, Block: block})
	}
	return out, nil
}

// RescheduleRequest moves the rental to a new schedule with the same package.
// Changing package, size or bundle is not supported until re-pricing rules
// are approved.
type RescheduleRequest struct {
	Actor     identity.Actor
	Scope     identity.Scope
	Schedule  Schedule
	Calendar  *StoreCalendar
	Inventory InventoryView
	Now       time.Time
}

// Rescheduling lists the allocation changes to persist with the booking in
// one transaction.
type Rescheduling struct {
	Transition  Transition
	Release     []inventory.AllocationID
	Allocations []inventory.PlannedAllocation
}

// Reschedule re-plans allocations for the new schedule, excluding the
// booking's own current allocations. If the new schedule is unavailable the
// booking is left untouched and keeps its existing reservation.
func (b *Booking) Reschedule(req RescheduleRequest) (Rescheduling, error) {
	if !req.Actor.IsUser() {
		return Rescheduling{}, ErrActorRequired
	}
	if err := b.requireStatus(ActionReschedule, StatusPendingConfirmation, StatusConfirmed); err != nil {
		return Rescheduling{}, err
	}
	if b.OpenModification != "" {
		return Rescheduling{}, fmt.Errorf("booking %s: %w", b.ID, ErrModificationOpen)
	}
	switch req.Scope {
	case identity.ScopeOwn:
		if !req.Now.Before(b.Schedule.PickupAt.Add(-b.Policy.CustomerRescheduleCutoff)) {
			return Rescheduling{}, fmt.Errorf("booking %s: %w", b.ID, ErrRescheduleCutoffPassed)
		}
	case identity.ScopeAny:
	default:
		return Rescheduling{}, domainerr.Invalid("scope", "required")
	}
	if b.Status == StatusPendingConfirmation && !req.Now.Before(b.HoldExpiresAt) {
		return Rescheduling{}, fmt.Errorf("booking %s: %w", b.ID, ErrHoldExpired)
	}
	if req.Schedule.Duration() != b.Package.Duration {
		return Rescheduling{}, fmt.Errorf("booking %s: %w", b.ID, ErrDurationMismatch)
	}
	if err := checkScheduleWindow(req.Schedule, b.Policy, req.Now); err != nil {
		return Rescheduling{}, err
	}
	if req.Calendar == nil {
		return Rescheduling{}, domainerr.Invalid("calendar", "required")
	}
	nf := b.Fulfillment
	var err error
	if nf.OutboundWindow, nf.ReturnWindow, err = req.Calendar.ValidateFulfillment(req.Schedule, nf.Outbound, nf.Return); err != nil {
		return Rescheduling{}, err
	}
	demands, err := b.Demands(req.Schedule, nf)
	if err != nil {
		return Rescheduling{}, err
	}
	planned, err := inventory.Plan(inventory.PlanRequest{
		BookingRef: string(b.ID), Demands: demands,
		Assets:      req.Inventory.Assets,
		Allocations: inventory.ExcludeBooking(req.Inventory.Allocations, string(b.ID)),
		Now:         req.Now, Policy: req.Inventory.Policy,
	})
	if err != nil {
		return Rescheduling{}, fmt.Errorf("reschedule booking %s: %w", b.ID, err)
	}
	var release []inventory.AllocationID
	for _, a := range req.Inventory.Allocations {
		if a.BookingRef == string(b.ID) && a.Status == inventory.AllocationActive {
			release = append(release, a.ID)
		}
	}
	old := b.Schedule
	b.Schedule, b.Fulfillment = req.Schedule, nf
	t := Transition{
		BookingID: b.ID, Action: ActionReschedule, From: b.Status, To: b.Status, Actor: req.Actor, At: req.Now.UTC(),
		Reason: fmt.Sprintf("from %s–%s", old.PickupAt.Format(time.RFC3339), old.DueAt.Format(time.RFC3339)),
	}
	return Rescheduling{Transition: t, Release: release, Allocations: planned}, nil
}

// ReplaceAssetRequest swaps an asset the booking holds for an equivalent one,
// e.g. it was damaged or lost before handover.
type ReplaceAssetRequest struct {
	Actor      identity.Actor
	Allocation inventory.AllocationID
	Reason     string
	Inventory  InventoryView
	Now        time.Time
}

// AssetReplacement is the allocation change to persist with the transition.
type AssetReplacement struct {
	Transition Transition
	Release    inventory.AllocationID
	Allocation inventory.PlannedAllocation
}

// ReplaceAsset plans another asset of the same variant for the same block.
// The old allocation keeps blocking its asset while planning, so it cannot be
// picked again; with no other asset free the booking keeps what it has and
// staff decide with the customer.
func (b *Booking) ReplaceAsset(req ReplaceAssetRequest) (AssetReplacement, error) {
	if !req.Actor.IsUser() {
		return AssetReplacement{}, ErrActorRequired
	}
	if err := b.requireStatus(ActionReplaceAsset, StatusPendingConfirmation, StatusConfirmed, StatusPreparing); err != nil {
		return AssetReplacement{}, err
	}
	if b.OpenModification != "" {
		return AssetReplacement{}, fmt.Errorf("booking %s: %w", b.ID, ErrModificationOpen)
	}
	if strings.TrimSpace(req.Reason) == "" {
		return AssetReplacement{}, ErrReasonRequired
	}
	i := slices.IndexFunc(req.Inventory.Allocations, func(a inventory.Allocation) bool {
		return a.ID == req.Allocation && a.BookingRef == string(b.ID) && a.Status == inventory.AllocationActive &&
			!a.Tentative() && a.HandedOverAt == nil
	})
	if i < 0 {
		return AssetReplacement{}, fmt.Errorf("booking %s allocation %s: %w", b.ID, req.Allocation, ErrAssetMismatch)
	}
	old := req.Inventory.Allocations[i]
	planned, err := inventory.Plan(inventory.PlanRequest{
		BookingRef: string(b.ID),
		Demands: []inventory.Demand{{LineRef: old.LineRef, ComponentKey: old.ComponentKey, VariantID: old.VariantID,
			Quantity: 1, Block: old.Block}},
		Assets: req.Inventory.Assets, Allocations: req.Inventory.Allocations, Now: req.Now, Policy: req.Inventory.Policy,
	})
	if err != nil {
		return AssetReplacement{}, fmt.Errorf("replace asset on booking %s: %w", b.ID, err)
	}
	t := Transition{
		BookingID: b.ID, Action: ActionReplaceAsset, From: b.Status, To: b.Status, Actor: req.Actor, At: req.Now.UTC(),
		Reason: fmt.Sprintf("%s → %s: %s", old.AssetID, planned[0].AssetID, req.Reason),
	}
	return AssetReplacement{Transition: t, Release: old.ID, Allocation: planned[0]}, nil
}

// checkScheduleWindow checks the booking horizon of the policy; slots and
// delivery windows are checked by StoreCalendar.ValidateFulfillment.
func checkScheduleWindow(s Schedule, p Policy, now time.Time) error {
	if s.PickupAt.IsZero() || !s.DueAt.After(s.PickupAt) {
		return domainerr.Invalid("schedule", "pickup and due are required and due must be after pickup")
	}
	if s.PickupAt.Before(now.Add(p.MinLeadTime)) {
		return ErrPickupTooSoon
	}
	if s.PickupAt.After(now.Add(p.MaxAdvance)) {
		return ErrPickupTooFarAhead
	}
	return nil
}
