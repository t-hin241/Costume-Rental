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
)

type ModificationID string

// ModificationStatus is the change-request lifecycle (modification policy §6).
type ModificationStatus string

const (
	ModRequested       ModificationStatus = "REQUESTED"
	ModQuoted          ModificationStatus = "QUOTED"
	ModAwaitingPayment ModificationStatus = "AWAITING_PAYMENT"
	ModConfirmed       ModificationStatus = "CONFIRMED"
	ModRejected        ModificationStatus = "REJECTED"
	ModExpired         ModificationStatus = "EXPIRED"
	ModCancelled       ModificationStatus = "CANCELLED"
)

func (s ModificationStatus) open() bool {
	return s == ModRequested || s == ModQuoted || s == ModAwaitingPayment
}

// ChangeKind classifies what a modification changes.
type ChangeKind string

const (
	// ChangeSizeExchange picks other variants of the same item or bundle.
	ChangeSizeExchange ChangeKind = "SIZE_EXCHANGE"
	// ChangeBundle switches a bundle line to another bundle.
	ChangeBundle ChangeKind = "BUNDLE_CHANGE"
	// ChangePackage changes the rental package (duration) before handover.
	ChangePackage ChangeKind = "PACKAGE_CHANGE"
	// ChangeExtension lengthens the package after handover.
	ChangeExtension ChangeKind = "EXTENSION"
)

var (
	ErrNothingChanges       = domainerr.New(domainerr.KindInvalid, "rentals.nothing_changes", "the requested change is identical to the booking")
	ErrChangeNotSupported   = domainerr.New(domainerr.KindInvalid, "rentals.change_not_supported", "this change is not supported; items can change variant, bundles can change options or bundle, and packages apply to every line")
	ErrAfterHandover        = domainerr.New(domainerr.KindInvalid, "rentals.after_handover_change", "after handover only an extension can be requested here; exchanges follow the post-handover workflow")
	ErrSelfServiceClosed    = domainerr.New(domainerr.KindForbidden, "rentals.self_service_closed", "online changes close before pickup; the store will handle this request")
	ErrSelfServiceExhausted = domainerr.New(domainerr.KindForbidden, "rentals.self_service_size_changes_used", "the online size change allowance is used; the store will handle this request")
)

// LineChange replaces one booking line with a quote the server computed from
// the current catalog (the price list for modifications).
type LineChange struct {
	LineID LineID
	Quote  catalog.LineQuote
}

// ModificationRequest asks to change a booking.
type ModificationRequest struct {
	ID    ModificationID
	Actor identity.Actor
	Scope identity.Scope
	// Reason is shown to staff and kept for audit.
	Reason string
	// StoreFault: the store cannot provide what it committed to; changed
	// lines never cost more than their original snapshot. Staff only.
	StoreFault bool
	Changes    []LineChange
	// NewPackage changes the package of every line; all lines must then be
	// in Changes, quoted with this package.
	NewPackage *catalog.RentalPackage
	Now        time.Time
}

// BookingTerms is a before/after snapshot of what a modification changes.
type BookingTerms struct {
	Schedule Schedule
	Package  PackageSnapshot
	Lines    []Line
	Gross    money.Money
	Discount money.Money
	Rent     money.Money
	Security money.Money
	// Fulfillment carries the courier windows implied by the schedule.
	Fulfillment Fulfillment
}

// Modification is a change request with its quote. It never edits the
// booking until it is confirmed, and the original terms stay in Before.
type Modification struct {
	ID           ModificationID
	BookingID    BookingID
	Status       ModificationStatus
	RequestedBy  identity.Actor
	RequestScope identity.Scope
	Reason       string
	StoreFault   bool
	Changes      []LineChange
	NewPackage   *catalog.RentalPackage
	Kinds        []ChangeKind
	CreatedAt    time.Time

	Before BookingTerms
	After  *BookingTerms
	// Deltas are After minus Before; RequiredPayment is the increase that must
	// be paid (payments tagged with this modification) before confirmation.
	RentDelta       money.Money
	SecurityDelta   money.Money
	RequiredPayment money.Money
	Plan            inventory.ChangePlan
	SelfService     bool
	QuotedBy        *identity.Actor
	QuotedAt        *time.Time
	HoldExpiresAt   *time.Time

	DecidedBy    *identity.Actor
	DecidedAt    *time.Time
	DecisionNote string
}

// RequestModification records a change request. Customers may always ask;
// whether they can also get an instant quote depends on the self-service
// window (see Quote). Only one change may be open per booking.
func (b *Booking) RequestModification(req ModificationRequest) (*Modification, Transition, error) {
	if !req.Actor.IsUser() {
		return nil, Transition{}, ErrActorRequired
	}
	if req.ID == "" {
		return nil, Transition{}, domainerr.Invalid("id", "required")
	}
	if err := b.modifiable(req.Scope, req.Now); err != nil {
		return nil, Transition{}, err
	}
	if b.OpenModification != "" {
		return nil, Transition{}, fmt.Errorf("booking %s: %w", b.ID, ErrModificationOpen)
	}
	if req.StoreFault && (req.Scope != identity.ScopeAny || req.NewPackage != nil) {
		return nil, Transition{}, domainerr.Invalid("store_fault", "only staff can record a store fault, and not with a package change")
	}
	kinds, err := b.classify(req.Changes, req.NewPackage)
	if err != nil {
		return nil, Transition{}, err
	}
	changes := make([]LineChange, len(req.Changes))
	for i, c := range req.Changes {
		changes[i] = LineChange{LineID: c.LineID, Quote: c.Quote.Clone()}
	}
	m := &Modification{
		ID: req.ID, BookingID: b.ID, Status: ModRequested,
		RequestedBy: req.Actor, RequestScope: req.Scope, Reason: req.Reason, StoreFault: req.StoreFault,
		Changes: changes, NewPackage: req.NewPackage, Kinds: kinds, CreatedAt: req.Now.UTC(),
		Before: BookingTerms{Schedule: b.Schedule, Package: b.Package, Lines: cloneLines(b.Lines),
			Gross: b.GrossRentTotal, Discount: b.DiscountTotal, Rent: b.RentTotal, Security: b.SecurityDepositTotal, Fulfillment: b.Fulfillment},
	}
	b.OpenModification = m.ID
	return m, b.modTransition(m, req.Actor, req.Now, "requested"), nil
}

// modifiable checks the booking can take a change from this scope.
func (b *Booking) modifiable(scope identity.Scope, now time.Time) error {
	switch scope {
	case identity.ScopeOwn:
		if err := b.requireStatus(ActionModify, StatusPendingConfirmation, StatusConfirmed); err != nil {
			return err
		}
	case identity.ScopeAny:
		if err := b.requireStatus(ActionModify, StatusPendingConfirmation, StatusConfirmed, StatusPreparing, StatusRented); err != nil {
			return err
		}
	default:
		return domainerr.Invalid("scope", "required")
	}
	if b.Status == StatusPendingConfirmation && !now.Before(b.HoldExpiresAt) {
		return fmt.Errorf("booking %s: %w", b.ID, ErrHoldExpired)
	}
	return nil
}

// classify validates the requested changes and names them.
func (b *Booking) classify(changes []LineChange, pkg *catalog.RentalPackage) ([]ChangeKind, error) {
	if len(changes) == 0 {
		return nil, ErrNothingChanges
	}
	targetPkg := b.Package.ID
	if pkg != nil {
		if err := pkg.Validate(); err != nil {
			return nil, err
		}
		if pkg.Status != catalog.StatusActive {
			return nil, catalog.ErrPackageUnavailable
		}
		if pkg.ID == b.Package.ID {
			return nil, ErrNothingChanges
		}
		if len(changes) != len(b.Lines) {
			return nil, fmt.Errorf("a package change re-prices every line: %w", ErrChangeNotSupported)
		}
		targetPkg = pkg.ID
	}
	seen := map[LineID]bool{}
	kinds := map[ChangeKind]bool{}
	for _, c := range changes {
		i := slices.IndexFunc(b.Lines, func(l Line) bool { return l.ID == c.LineID })
		if i < 0 || seen[c.LineID] {
			return nil, domainerr.Invalid("changes", "line ids must exist and be unique")
		}
		seen[c.LineID] = true
		old, q := b.Lines[i], c.Quote
		switch {
		case q.Package.ID != targetPkg:
			return nil, ErrMixedPackages
		case q.Quantity != old.Quantity:
			return nil, fmt.Errorf("quantity changes are not supported: %w", ErrChangeNotSupported)
		case len(q.Components) == 0 || q.RentAmount.Currency() != b.Currency:
			return nil, domainerr.Invalid("changes", "quote must have components in the booking currency")
		case old.Kind != q.Kind:
			// A bundle is never broken into individually priced items.
			return nil, fmt.Errorf("line %s %s → %s: %w", old.ID, old.Kind, q.Kind, ErrChangeNotSupported)
		case old.Kind == catalog.KindItem && old.OfferingID != q.OfferingID:
			return nil, fmt.Errorf("line %s can only change size: %w", old.ID, ErrChangeNotSupported)
		}
		switch {
		case old.OfferingID != q.OfferingID:
			kinds[ChangeBundle] = true
		case !sameComponents(old.Components, q.Components):
			kinds[ChangeSizeExchange] = true
		case pkg == nil:
			return nil, fmt.Errorf("line %s: %w", old.ID, ErrNothingChanges)
		}
	}
	if pkg != nil {
		if b.Status == StatusRented {
			kinds[ChangeExtension] = true
		} else {
			kinds[ChangePackage] = true
		}
	}
	if b.Status == StatusRented && (!kinds[ChangeExtension] || len(kinds) != 1 || pkg.Duration <= b.Package.Duration) {
		return nil, ErrAfterHandover
	}
	out := make([]ChangeKind, 0, len(kinds))
	for _, k := range []ChangeKind{ChangeSizeExchange, ChangeBundle, ChangePackage, ChangeExtension} {
		if kinds[k] {
			out = append(out, k)
		}
	}
	return out, nil
}

func sameComponents(a, b []catalog.ComponentDemand) bool {
	key := func(cs []catalog.ComponentDemand) []string {
		out := make([]string, len(cs))
		for i, c := range cs {
			out[i] = fmt.Sprintf("%s|%s|%d", c.Key, c.VariantID, c.Quantity)
		}
		slices.Sort(out)
		return out
	}
	return slices.Equal(key(a), key(b))
}

// QuoteInput carries what quoting needs from other aggregates.
type QuoteInput struct {
	Actor     identity.Actor
	Scope     identity.Scope
	Calendar  *StoreCalendar
	Inventory InventoryView
	Now       time.Time
}

// Quote prices the change and holds the new allocations. Customers can
// quote their own request only inside the self-service window and size
// allowance; otherwise staff quote it. Unchanged lines keep their snapshot;
// changed lines use the new quotes, capped at the original amounts when the
// store is at fault.
func (m *Modification) Quote(b *Booking, in QuoteInput) (Transition, error) {
	if !in.Actor.IsUser() {
		return Transition{}, ErrActorRequired
	}
	if err := m.require(ModRequested); err != nil {
		return Transition{}, err
	}
	if b.ID != m.BookingID || b.OpenModification != m.ID {
		return Transition{}, fmt.Errorf("modification %s: %w", m.ID, ErrInvalidTransition)
	}
	if err := b.modifiable(in.Scope, in.Now); err != nil {
		return Transition{}, err
	}
	if in.Scope == identity.ScopeOwn {
		if !in.Now.Before(b.Schedule.PickupAt.Add(-b.Policy.Modification.SelfServiceCutoff)) {
			return Transition{}, fmt.Errorf("booking %s: %w", b.ID, ErrSelfServiceClosed)
		}
		if slices.Contains(m.Kinds, ChangeSizeExchange) && b.SelfServiceSizeChanges >= b.Policy.Modification.SelfServiceSizeChanges {
			return Transition{}, fmt.Errorf("booking %s: %w", b.ID, ErrSelfServiceExhausted)
		}
	}

	pkg := b.Package
	schedule := b.Schedule
	fulfillment := b.Fulfillment
	if m.NewPackage != nil {
		pkg = PackageSnapshot{ID: m.NewPackage.ID, Code: m.NewPackage.Code, Label: m.NewPackage.Label, Duration: m.NewPackage.Duration}
		s, err := NewSchedule(b.Schedule.PickupAt, b.Schedule.PickupAt.Add(pkg.Duration))
		if err != nil {
			return Transition{}, err
		}
		if in.Calendar == nil {
			return Transition{}, domainerr.Invalid("calendar", "required")
		}
		if fulfillment.OutboundWindow, fulfillment.ReturnWindow, err = in.Calendar.ValidateFulfillment(s, b.Fulfillment.Outbound, b.Fulfillment.Return); err != nil {
			return Transition{}, err
		}
		if b.Status == StatusRented {
			fulfillment.OutboundWindow = b.Fulfillment.OutboundWindow // already delivered
		}
		schedule = s
	}

	lines := cloneLines(b.Lines)
	changed := map[string]bool{}
	for _, c := range m.Changes {
		i := slices.IndexFunc(lines, func(l Line) bool { return l.ID == c.LineID })
		nl, err := lineFromQuote(c.LineID, c.Quote)
		if err != nil {
			return Transition{}, err
		}
		nl = carryDiscount(nl, lines[i], m.StoreFault)
		lines[i] = nl
		changed[string(c.LineID)] = true
	}
	rent, security, err := totals(lines, b.Currency)
	if err != nil {
		return Transition{}, err
	}
	gross, err := money.Sum(b.Currency, listRents(lines)...)
	if err != nil {
		return Transition{}, err
	}

	var demands []inventory.Demand
	for _, l := range lines {
		if !changed[string(l.ID)] {
			continue
		}
		ds, err := b.demandsFor(l, schedule, fulfillment)
		if err != nil {
			return Transition{}, err
		}
		demands = append(demands, ds...)
	}
	var own []inventory.Allocation
	for _, a := range in.Inventory.Allocations {
		if a.BookingRef == string(b.ID) && changed[a.LineRef] && a.Status == inventory.AllocationActive && !a.Tentative() {
			own = append(own, a)
		}
	}
	plan, err := inventory.PlanChange(inventory.ChangeRequest{
		BookingRef: string(b.ID), ModificationRef: string(m.ID), Demands: demands, Own: own,
		Assets: in.Inventory.Assets, Allocations: in.Inventory.Allocations, Now: in.Now, Policy: in.Inventory.Policy,
	})
	if err != nil {
		return Transition{}, fmt.Errorf("modification %s: %w", m.ID, err)
	}

	rentDelta, err := rent.Sub(b.RentTotal)
	if err != nil {
		return Transition{}, err
	}
	secDelta, err := security.Sub(b.SecurityDepositTotal)
	if err != nil {
		return Transition{}, err
	}
	required, err := money.New(max(rentDelta.Amount(), 0)+max(secDelta.Amount(), 0), b.Currency)
	if err != nil {
		return Transition{}, err
	}
	m.After = &BookingTerms{Schedule: schedule, Package: pkg, Lines: lines, Gross: gross, Discount: mustSub(gross, rent), Rent: rent, Security: security, Fulfillment: fulfillment}
	m.RentDelta, m.SecurityDelta, m.RequiredPayment, m.Plan = rentDelta, secDelta, required, plan
	m.SelfService = in.Scope == identity.ScopeOwn
	actor := in.Actor
	m.QuotedBy, m.QuotedAt, m.HoldExpiresAt = &actor, ptr(in.Now), ptr(in.Now.Add(b.Policy.Modification.HoldTTL))
	m.Status = ModQuoted
	if required.IsPositive() {
		m.Status = ModAwaitingPayment
	}
	return b.modTransition(m, in.Actor, in.Now, "quoted"), nil
}

// Confirm applies a quoted change once any increase is paid. paid is the sum
// of succeeded payments tagged with this modification
// (payments.Ledger.PaidForModification). The use case applies m.Plan and
// commits the ledger re-pricing in the same transaction.
func (m *Modification) Confirm(b *Booking, actor identity.Actor, paid money.Money, now time.Time) (Transition, error) {
	if !actor.IsUser() {
		return Transition{}, ErrActorRequired
	}
	if err := m.require(ModQuoted, ModAwaitingPayment); err != nil {
		return Transition{}, err
	}
	if !now.Before(*m.HoldExpiresAt) {
		return Transition{}, fmt.Errorf("modification %s: %w", m.ID, ErrHoldExpired)
	}
	if b.ID != m.BookingID || b.OpenModification != m.ID {
		return Transition{}, fmt.Errorf("modification %s: %w", m.ID, ErrInvalidTransition)
	}
	if err := b.modifiable(identity.ScopeAny, now); err != nil {
		return Transition{}, err
	}
	if paid.Currency() != b.Currency || paid.Amount() < m.RequiredPayment.Amount() {
		return Transition{}, fmt.Errorf("modification %s needs %s: %w", m.ID, m.RequiredPayment, ErrInsufficientFunds)
	}
	a := m.After
	b.Schedule, b.Package, b.Lines = a.Schedule, a.Package, cloneLines(a.Lines)
	b.Fulfillment.OutboundWindow, b.Fulfillment.ReturnWindow = a.Fulfillment.OutboundWindow, a.Fulfillment.ReturnWindow
	b.GrossRentTotal, b.DiscountTotal, b.RentTotal, b.SecurityDepositTotal = a.Gross, a.Discount, a.Rent, a.Security
	b.OpenModification = ""
	b.Modifications = append(b.Modifications, m.ID)
	if m.SelfService && slices.Contains(m.Kinds, ChangeSizeExchange) {
		b.SelfServiceSizeChanges++
	}
	m.decide(ModConfirmed, actor, "", now)
	return b.modTransition(m, actor, now, "confirmed"), nil
}

// Reject declines a change (staff). Holds are released; the booking keeps its
// original terms.
func (m *Modification) Reject(b *Booking, actor identity.Actor, note string, now time.Time) (Transition, error) {
	if strings.TrimSpace(note) == "" {
		return Transition{}, ErrReasonRequired
	}
	return m.end(b, ModRejected, actor, note, now)
}

// Cancel withdraws a change, e.g. by the customer or because the booking was
// cancelled.
func (m *Modification) Cancel(b *Booking, actor identity.Actor, note string, now time.Time) (Transition, error) {
	return m.end(b, ModCancelled, actor, note, now)
}

// Expire ends a quote whose hold ran out (payment not received in time).
// Running it again, or on a request that is no longer quoted, is a no-op.
func (m *Modification) Expire(b *Booking, actor identity.Actor, now time.Time) (Transition, bool, error) {
	if m.Status != ModQuoted && m.Status != ModAwaitingPayment {
		return Transition{}, false, nil
	}
	if now.Before(*m.HoldExpiresAt) {
		return Transition{}, false, fmt.Errorf("modification %s: %w", m.ID, ErrHoldNotExpired)
	}
	t, err := m.end(b, ModExpired, actor, "hold expired", now)
	return t, err == nil, err
}

func (m *Modification) end(b *Booking, to ModificationStatus, actor identity.Actor, note string, now time.Time) (Transition, error) {
	if !actor.Valid() {
		return Transition{}, ErrActorRequired
	}
	if !m.Status.open() {
		return Transition{}, fmt.Errorf("modification %s from %s: %w", m.ID, m.Status, ErrInvalidTransition)
	}
	if b.ID != m.BookingID {
		return Transition{}, fmt.Errorf("modification %s: %w", m.ID, ErrInvalidTransition)
	}
	if b.OpenModification == m.ID {
		b.OpenModification = ""
	}
	m.decide(to, actor, note, now)
	t := b.modTransition(m, actor, now, strings.ToLower(string(to)))
	t.ReleaseModificationHolds = m.ID
	return t, nil
}

func (m *Modification) require(allowed ...ModificationStatus) error {
	if !slices.Contains(allowed, m.Status) {
		return fmt.Errorf("modification %s from %s: %w", m.ID, m.Status, ErrInvalidTransition)
	}
	return nil
}

func (m *Modification) decide(to ModificationStatus, actor identity.Actor, note string, now time.Time) {
	m.Status, m.DecidedBy, m.DecidedAt, m.DecisionNote = to, &actor, ptr(now), note
}

func (b *Booking) modTransition(m *Modification, actor identity.Actor, now time.Time, what string) Transition {
	return Transition{BookingID: b.ID, Action: ActionModify, From: b.Status, To: b.Status, Actor: actor, At: now.UTC(),
		Reason: fmt.Sprintf("modification %s %s", m.ID, what)}
}

func lineFromQuote(id LineID, q catalog.LineQuote) (Line, error) {
	q = q.Clone()
	c := q.RentAmount.Currency()
	if !c.Valid() || q.DepositAmount.Currency() != c {
		return Line{}, fmt.Errorf("line %s: %w", id, money.ErrCurrencyMismatch)
	}
	return Line{
		ID: id, Kind: q.Kind, OfferingID: q.OfferingID, OfferingName: q.OfferingName,
		OfferingVersion: q.OfferingVersion, Quantity: q.Quantity,
		UnitRent: q.UnitRent, ListRent: q.RentAmount, Discount: money.Zero(c), RentAmount: q.RentAmount,
		UnitDeposit: q.UnitDeposit, DepositAmount: q.DepositAmount,
		Components: q.Components,
	}, nil
}

// carryDiscount keeps the changed line discount (never below zero) and does
// not re-evaluate coupons (owner decision D12), so a same-price size swap
// costs nothing extra. On a store fault the net rent and
// the deposit never exceed the original line.
func carryDiscount(n, old Line, storeFault bool) Line {
	d := min(old.Discount.Amount(), n.ListRent.Amount())
	if storeFault && n.ListRent.Amount()-d > old.RentAmount.Amount() {
		d = n.ListRent.Amount() - old.RentAmount.Amount()
	}
	n.Discount, _ = money.New(d, n.ListRent.Currency())
	n.RentAmount = mustSub(n.ListRent, n.Discount)
	if storeFault && n.DepositAmount.Amount() > old.DepositAmount.Amount() {
		n.UnitDeposit, n.DepositAmount = old.UnitDeposit, old.DepositAmount
	}
	return n
}

func listRents(lines []Line) []money.Money {
	out := make([]money.Money, len(lines))
	for i, l := range lines {
		out[i] = l.ListRent
	}
	return out
}

func totals(lines []Line, c money.Currency) (money.Money, money.Money, error) {
	rents := make([]money.Money, len(lines))
	deposits := make([]money.Money, len(lines))
	for i, l := range lines {
		rents[i], deposits[i] = l.RentAmount, l.DepositAmount
	}
	rent, err := money.Sum(c, rents...)
	if err != nil {
		return money.Money{}, money.Money{}, err
	}
	dep, err := money.Sum(c, deposits...)
	return rent, dep, err
}

func cloneLines(ls []Line) []Line {
	out := make([]Line, len(ls))
	for i, l := range ls {
		l.Components = slices.Clone(l.Components)
		out[i] = l
	}
	return out
}
