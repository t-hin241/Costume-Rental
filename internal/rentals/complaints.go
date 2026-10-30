package rentals

import (
	"fmt"
	"math/bits"
	"slices"
	"strings"
	"time"

	"webee_rentor/internal/delivery"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/money"
	"webee_rentor/internal/period"
)

type ComplaintID string

// ComplaintKind is what was wrong with a delivered parcel.
type ComplaintKind string

const (
	ComplaintMissingItem      ComplaintKind = "MISSING_ITEM"
	ComplaintWrongItem        ComplaintKind = "WRONG_ITEM" // wrong size, variant or component
	ComplaintDamagedOnArrival ComplaintKind = "DAMAGED_ON_ARRIVAL"
)

// ComplaintStatus is the complaint lifecycle.
type ComplaintStatus string

const (
	ComplaintOpen     ComplaintStatus = "OPEN"
	ComplaintRejected ComplaintStatus = "REJECTED"
	ComplaintAccepted ComplaintStatus = "ACCEPTED"
	// Resolved by delivering the right items, or by refunding the rent of
	// the affected lines when that is not possible.
	ComplaintReplaced ComplaintStatus = "RESOLVED_REPLACEMENT"
	ComplaintRefunded ComplaintStatus = "RESOLVED_REFUND"
)

var (
	ErrComplaintWindowClosed = domainerr.New(domainerr.KindInvalidState, "rentals.complaint_window_closed", "the delivery complaint window has ended")
	ErrComplaintNotAllowed   = domainerr.New(domainerr.KindInvalid, "rentals.complaint_not_allowed", "delivery complaints apply to delivered courier bookings only")
	ErrComplaintDuplicate    = domainerr.New(domainerr.KindConflict, "rentals.complaint_duplicate", "an open or accepted complaint already covers this item")
)

// DeliveryComplaint reports a problem found when a courier delivery arrives
// (decision D20). Store-counter pickups are checked in front of the customer
// and have no complaint window.
type DeliveryComplaint struct {
	ID          ComplaintID
	BookingID   BookingID
	Kind        ComplaintKind
	Assets      []inventory.AssetID
	Description string
	Evidence    []string
	RaisedBy    identity.Actor
	RaisedAt    time.Time
	Status      ComplaintStatus
	// Fault is STORE or CARRIER once accepted.
	Fault        delivery.Fault
	DecidedBy    *identity.Actor
	DecidedAt    *time.Time
	DecisionNote string
	// Credit is the rent refunded when resolved by refund.
	Credit *ServiceCredit
}

// ServiceCredit is rent given back for an accepted delivery complaint.
type ServiceCredit struct {
	ComplaintID ComplaintID
	Lines       []LineCredit
	Amount      money.Money
	At          time.Time
}

// LineCredit is the part of a service credit for one booking line.
type LineCredit struct {
	LineID LineID
	Assets []inventory.AssetID
	Amount money.Money
}

// ComplaintRequest raises a complaint.
type ComplaintRequest struct {
	ID          ComplaintID
	Actor       identity.Actor
	Kind        ComplaintKind
	Assets      []inventory.AssetID
	Description string
	Evidence    []string
	Now         time.Time
}

// RaiseDeliveryComplaint records a complaint within Policy.Delivery.
// ComplaintWindow after the carrier delivered the outbound parcel. Wrong or
// damaged items need photo evidence.
func (b *Booking) RaiseDeliveryComplaint(existing []DeliveryComplaint, req ComplaintRequest) (*DeliveryComplaint, Transition, error) {
	if !req.Actor.IsUser() {
		return nil, Transition{}, ErrActorRequired
	}
	if err := b.requireStatus(ActionComplain, StatusRented); err != nil {
		return nil, Transition{}, err
	}
	if b.Fulfillment.Outbound.orStore() != LegCourier || b.Fulfillment.DeliveredAt == nil {
		return nil, Transition{}, fmt.Errorf("booking %s: %w", b.ID, ErrComplaintNotAllowed)
	}
	if !req.Now.Before(b.Fulfillment.DeliveredAt.Add(b.Policy.Delivery.ComplaintWindow)) {
		return nil, Transition{}, fmt.Errorf("booking %s: %w", b.ID, ErrComplaintWindowClosed)
	}
	switch req.Kind {
	case ComplaintMissingItem:
	case ComplaintWrongItem, ComplaintDamagedOnArrival:
		if len(req.Evidence) == 0 {
			return nil, Transition{}, ErrEvidenceRequired
		}
	default:
		return nil, Transition{}, domainerr.Invalid("kind", "unknown complaint kind")
	}
	if req.ID == "" || strings.TrimSpace(req.Description) == "" || len(req.Assets) == 0 {
		return nil, Transition{}, domainerr.Invalid("complaint", "id, description and affected items are required")
	}
	for _, a := range req.Assets {
		if _, err := b.Item(a); err != nil {
			return nil, Transition{}, err
		}
		for _, e := range existing {
			if (e.Status == ComplaintOpen || e.Status == ComplaintAccepted) && slices.Contains(e.Assets, a) {
				return nil, Transition{}, fmt.Errorf("asset %s: %w", a, ErrComplaintDuplicate)
			}
		}
	}
	c := &DeliveryComplaint{
		ID: req.ID, BookingID: b.ID, Kind: req.Kind, Assets: slices.Clone(req.Assets), Description: req.Description,
		Evidence: slices.Clone(req.Evidence), RaisedBy: req.Actor, RaisedAt: req.Now.UTC(), Status: ComplaintOpen,
	}
	return c, b.complaintTransition(c, req.Actor, req.Now, "raised"), nil
}

// Reject closes a complaint staff found unfounded or caused by the customer.
func (c *DeliveryComplaint) Reject(b *Booking, p identity.Principal, note string, now time.Time) (Transition, error) {
	if err := c.decide(b, p, note, ComplaintOpen); err != nil {
		return Transition{}, err
	}
	c.Status = ComplaintRejected
	c.stamp(p, note, now)
	return b.complaintTransition(c, p.Actor(), now, "rejected"), nil
}

// Accept records a verified store or carrier fault and protects the
// customer: missing items are not expected back nor charged, wrong or
// damaged items are collected by the store, and damage fees on them need
// evidence of new damage.
func (c *DeliveryComplaint) Accept(b *Booking, p identity.Principal, fault delivery.Fault, note string, now time.Time) (Transition, error) {
	if err := c.decide(b, p, note, ComplaintOpen); err != nil {
		return Transition{}, err
	}
	if fault != delivery.FaultStore && fault != delivery.FaultCarrier {
		return Transition{}, domainerr.Invalid("fault", "accepted complaints are store or carrier faults; reject customer-caused ones")
	}
	for _, a := range c.Assets {
		it, err := b.Item(a)
		if err != nil {
			return Transition{}, err
		}
		if c.Kind == ComplaintMissingItem {
			it.NotReceived = true
		} else {
			it.ArrivalIssue, it.CollectByStore = true, true
		}
	}
	c.Status, c.Fault = ComplaintAccepted, fault
	c.stamp(p, note, now)
	return b.complaintTransition(c, p.Actor(), now, "accepted"), nil
}

// ReplacementDemands lists the inventory a replacement needs: the booked
// variants of the affected items, from now until each component's current
// block end. The use case plans them with inventory.Plan.
func (c *DeliveryComplaint) ReplacementDemands(b *Booking, now time.Time) ([]inventory.Demand, error) {
	if c.Status != ComplaintAccepted {
		return nil, fmt.Errorf("complaint %s from %s: %w", c.ID, c.Status, ErrInvalidTransition)
	}
	var out []inventory.Demand
	for _, a := range c.Assets {
		it, err := b.Item(a)
		if err != nil {
			return nil, err
		}
		comp, err := b.component(it.LineID, it.ComponentKey)
		if err != nil {
			return nil, err
		}
		full, err := b.blockFor(b.Schedule, b.Fulfillment, b.Policy.Buffers.For(comp.BufferClass))
		if err != nil {
			return nil, err
		}
		block, err := period.New(now, full.End())
		if err != nil {
			return nil, fmt.Errorf("complaint %s: the rental is already over: %w", c.ID, err)
		}
		out = append(out, inventory.Demand{LineRef: string(it.LineID), ComponentKey: it.ComponentKey, VariantID: comp.VariantID, Quantity: 1, Block: block})
	}
	return out, nil
}

// Replacement pairs an affected item with the asset dispatched instead.
type Replacement struct {
	Old inventory.AssetID
	New inventory.AssetID
}

// ResolveWithReplacement records the replacement items handed to the courier
// (delivery free of charge). The new items join the booking with the same
// line snapshot; the old ones stay until collected or resolved.
func (c *DeliveryComplaint) ResolveWithReplacement(b *Booking, p identity.Principal, replacements []Replacement, note string, now time.Time) (Transition, error) {
	if err := c.decide(b, p, note, ComplaintAccepted); err != nil {
		return Transition{}, err
	}
	if len(replacements) != len(c.Assets) {
		return Transition{}, fmt.Errorf("complaint %s needs one replacement per affected item: %w", c.ID, ErrAssetMismatch)
	}
	added := make([]RentedItem, 0, len(replacements))
	for _, r := range replacements {
		if !slices.Contains(c.Assets, r.Old) || r.New == "" || slices.ContainsFunc(b.Items, func(i RentedItem) bool { return i.AssetID == r.New }) {
			return Transition{}, fmt.Errorf("replacement %s → %s: %w", r.Old, r.New, ErrAssetMismatch)
		}
		old, _ := b.Item(r.Old)
		n := *old
		n.AssetID, n.ReplacementFor, n.ReplacedBy = r.New, r.Old, ""
		n.ReturnedAt, n.ReceivedAt, n.MissingReportedAt, n.LostConfirmedAt, n.Finding = nil, nil, nil, nil, nil
		n.NotReceived, n.ArrivalIssue, n.CollectByStore, n.CarrierLiable, n.CarrierDamageReported = false, false, false, false, false
		n.PreexistingIssues = ""
		added = append(added, n)
	}
	for i, r := range replacements {
		old, _ := b.Item(r.Old)
		old.ReplacedBy = r.New
		b.Items = append(b.Items, added[i])
	}
	c.Status = ComplaintReplaced
	c.stamp(p, note, now)
	return b.complaintTransition(c, p.Actor(), now, "replacement dispatched"), nil
}

// ResolveWithRefund gives back rent for the affected items when no
// replacement is possible (decision D20). Each item is refunded its share of
// the line rent, weighted by the snapshot daily rates of the line items:
// line rent x affected daily rates / all daily rates of the line, rounded up
// to the đồng in the customer's favour. An item is refunded once, and a line
// never more than its rent. RentTotal is reduced; the use case reduces the
// ledger rent (payments.Ledger.ReduceRent) and requests the refund through
// the approval flow.
func (c *DeliveryComplaint) ResolveWithRefund(b *Booking, p identity.Principal, note string, now time.Time) (ServiceCredit, Transition, error) {
	if err := c.decide(b, p, note, ComplaintAccepted); err != nil {
		return ServiceCredit{}, Transition{}, err
	}
	affected := map[LineID][]inventory.AssetID{}
	var order []LineID
	for _, a := range c.Assets {
		it, err := b.Item(a)
		if err != nil {
			return ServiceCredit{}, Transition{}, err
		}
		if b.credited(a) {
			continue
		}
		if _, ok := affected[it.LineID]; !ok {
			order = append(order, it.LineID)
		}
		affected[it.LineID] = append(affected[it.LineID], a)
	}
	credit := ServiceCredit{ComplaintID: c.ID, At: now.UTC()}
	var total int64
	for _, id := range order {
		amount, err := b.lineShare(id, affected[id])
		if err != nil {
			return ServiceCredit{}, Transition{}, err
		}
		credit.Lines = append(credit.Lines, LineCredit{LineID: id, Assets: affected[id], Amount: mustVND(amount, b.Currency)})
		total += amount
	}
	if total == 0 {
		return ServiceCredit{}, Transition{}, fmt.Errorf("complaint %s: the affected items were already refunded: %w", c.ID, ErrInvalidTransition)
	}
	credit.Amount = mustVND(total, b.Currency)
	b.RentTotal = mustSub(b.RentTotal, credit.Amount)
	b.ServiceCredits = append(b.ServiceCredits, credit)
	c.Status, c.Credit = ComplaintRefunded, &credit
	c.stamp(p, note, now)
	return credit, b.complaintTransition(c, p.Actor(), now, "refunded "+credit.Amount.String()), nil
}

// credited reports whether an item was already refunded.
func (b *Booking) credited(asset inventory.AssetID) bool {
	for _, sc := range b.ServiceCredits {
		for _, lc := range sc.Lines {
			if slices.Contains(lc.Assets, asset) {
				return true
			}
		}
	}
	return false
}

// lineShare is the refund for some items of a line, capped so the line is
// never refunded more than its rent in total.
func (b *Booking) lineShare(id LineID, assets []inventory.AssetID) (int64, error) {
	i := slices.IndexFunc(b.Lines, func(l Line) bool { return l.ID == id })
	if i < 0 {
		return 0, fmt.Errorf("line %s: %w", id, ErrAssetMismatch)
	}
	l := b.Lines[i]
	var all, part int64
	for _, c := range l.Components {
		all += c.UnitDailyRate.Amount() * int64(c.Quantity)
	}
	for _, a := range assets {
		it, err := b.Item(a)
		if err != nil {
			return 0, err
		}
		part += it.DailyRate.Amount()
	}
	if all <= 0 || part <= 0 {
		return 0, fmt.Errorf("line %s has no daily rates to share the rent: %w", id, ErrInvalidTransition)
	}
	share := ceilMulDiv(l.RentAmount.Amount(), min(part, all), all)
	var already int64
	for _, sc := range b.ServiceCredits {
		for _, lc := range sc.Lines {
			if lc.LineID == id {
				already += lc.Amount.Amount()
			}
		}
	}
	return min(share, l.RentAmount.Amount()-already), nil
}

// ceilMulDiv returns ceil(a*b/c) for non-negative values with b <= c,
// without overflowing.
func ceilMulDiv(a, b, c int64) int64 {
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	q, r := bits.Div64(hi, lo, uint64(c))
	if r > 0 {
		q++
	}
	return int64(q)
}

// RecordUndeliveredItemBack resolves an item the customer never received
// once staff locate it (it never left the store, or the carrier brought it
// back). No late or loss fee applies.
func (b *Booking) RecordUndeliveredItemBack(actor identity.Actor, asset inventory.AssetID, at time.Time) (Transition, error) {
	if !actor.IsUser() {
		return Transition{}, ErrActorRequired
	}
	if err := b.requireStatus(ActionDelivery, StatusRented); err != nil {
		return Transition{}, err
	}
	it, err := b.Item(asset)
	if err != nil {
		return Transition{}, err
	}
	if !it.NotReceived || it.Resolved() {
		return Transition{}, fmt.Errorf("booking %s asset %s: %w", b.ID, asset, ErrInvalidTransition)
	}
	it.ReturnedAt, it.ReceivedAt = ptr(at), ptr(at)
	return b.afterItemResolved(ActionDelivery, actor, at, string(asset)+" undelivered item back in store"), nil
}

func (c *DeliveryComplaint) decide(b *Booking, p identity.Principal, note string, from ComplaintStatus) error {
	if err := p.Require(identity.PermDeliveryManage); err != nil {
		return err
	}
	if strings.TrimSpace(note) == "" {
		return ErrReasonRequired
	}
	if c.BookingID != b.ID || c.Status != from {
		return fmt.Errorf("complaint %s from %s: %w", c.ID, c.Status, ErrInvalidTransition)
	}
	return nil
}

func (c *DeliveryComplaint) stamp(p identity.Principal, note string, now time.Time) {
	actor := p.Actor()
	c.DecidedBy, c.DecidedAt, c.DecisionNote = &actor, ptr(now), note
}

func (b *Booking) complaintTransition(c *DeliveryComplaint, actor identity.Actor, now time.Time, what string) Transition {
	return Transition{BookingID: b.ID, Action: ActionComplain, From: b.Status, To: b.Status, Actor: actor, At: now.UTC(),
		Reason: fmt.Sprintf("complaint %s (%s) %s", c.ID, c.Kind, what)}
}

func mustVND(v int64, c money.Currency) money.Money {
	m, _ := money.New(v, c) // bounded by the booking line amounts
	return m
}
