package rentals

import (
	"fmt"
	"strings"
	"time"

	"webee_rentor/internal/delivery"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/money"
)

// These methods apply delivery facts to the booking. The use case calls them
// from delivery task changes; a carrier status alone never completes,
// cancels or releases anything here.

// RecordDelivered records when the courier delivered the outbound parcel.
// Delivery after the rental start credits the delay against lateness.
func (b *Booking) RecordDelivered(actor identity.Actor, at time.Time) (Transition, error) {
	if !actor.Valid() {
		return Transition{}, ErrActorRequired
	}
	if err := b.requireStatus(ActionDelivery, StatusRented); err != nil {
		return Transition{}, err
	}
	if b.Fulfillment.Outbound.orStore() != LegCourier {
		return Transition{}, fmt.Errorf("booking %s outbound is not by courier: %w", b.ID, ErrInvalidTransition)
	}
	if b.Fulfillment.DeliveredAt != nil {
		return b.deliveryTransition(actor, at, "already delivered"), nil
	}
	b.Fulfillment.DeliveredAt = ptr(at)
	return b.deliveryTransition(actor, at, "delivered"), nil
}

// RecordCourierPickup records items handed to the courier for return; the
// customer's return obligation (and late fees) end here.
func (b *Booking) RecordCourierPickup(actor identity.Actor, assets []inventory.AssetID, at time.Time) (Transition, error) {
	if err := b.courierReturn(actor); err != nil {
		return Transition{}, err
	}
	if len(assets) == 0 {
		return Transition{}, fmt.Errorf("booking %s: %w", b.ID, ErrAssetMismatch)
	}
	for _, a := range assets {
		it, err := b.Item(a)
		if err != nil {
			return Transition{}, err
		}
		if it.ReturnedAt != nil || it.LostConfirmedAt != nil {
			return Transition{}, fmt.Errorf("booking %s asset %s already returned: %w", b.ID, a, ErrAssetMismatch)
		}
	}
	for _, a := range assets {
		it, _ := b.Item(a)
		it.ReturnedAt = ptr(at)
	}
	return b.deliveryTransition(actor, at, "picked up by courier: "+strings.Join(assetStrings(assets), ",")), nil
}

// RecordReturnPickupFailure applies a failed courier pickup. When the store
// or carrier caused it, the customer was ready and lateness stops at the
// first such attempt; a customer-caused failure changes nothing here (the
// re-attempt fee is a separate assessment).
func (b *Booking) RecordReturnPickupFailure(actor identity.Actor, attempt delivery.Attempt) (Transition, error) {
	if err := b.courierReturn(actor); err != nil {
		return Transition{}, err
	}
	switch attempt.Fault {
	case delivery.FaultStore, delivery.FaultCarrier:
		if b.Fulfillment.ReturnReadyAt == nil {
			b.Fulfillment.ReturnReadyAt = ptr(attempt.At)
		}
	case delivery.FaultCustomer:
	default:
		return Transition{}, domainerr.Invalid("fault", "classify the failed attempt first")
	}
	return b.deliveryTransition(actor, attempt.At, fmt.Sprintf("return pickup failed (%s): %s", attempt.Fault, attempt.Reason)), nil
}

// RecordReceivedAtStore confirms courier-returned items arrived. When every
// item is back or lost the booking moves to RETURN_INSPECTION; assets are
// still not READY.
func (b *Booking) RecordReceivedAtStore(actor identity.Actor, assets []inventory.AssetID, at time.Time) (Transition, error) {
	if !actor.IsUser() {
		return Transition{}, ErrActorRequired
	}
	if err := b.requireStatus(ActionReturn, StatusRented); err != nil {
		return Transition{}, err
	}
	if len(assets) == 0 {
		return Transition{}, fmt.Errorf("booking %s: %w", b.ID, ErrAssetMismatch)
	}
	for _, a := range assets {
		it, err := b.Item(a)
		if err != nil {
			return Transition{}, err
		}
		if it.ReturnedAt == nil || it.Resolved() {
			return Transition{}, fmt.Errorf("booking %s asset %s is not in courier transit: %w", b.ID, a, ErrAssetMismatch)
		}
	}
	for _, a := range assets {
		it, _ := b.Item(a)
		it.ReceivedAt = ptr(at)
	}
	return b.afterItemResolved(ActionReturn, actor, at, "received at store: "+strings.Join(assetStrings(assets), ",")), nil
}

// RecordLostInTransit records an item the carrier lost: on the way to the
// customer (not yet delivered) or on the way back (after the customer handed
// it over). The customer is not charged for it.
func (b *Booking) RecordLostInTransit(actor identity.Actor, asset inventory.AssetID, reason string, at time.Time) (Transition, error) {
	if !actor.IsUser() {
		return Transition{}, ErrActorRequired
	}
	if err := b.requireStatus(ActionDelivery, StatusRented); err != nil {
		return Transition{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return Transition{}, ErrReasonRequired
	}
	it, err := b.Item(asset)
	if err != nil {
		return Transition{}, err
	}
	outbound := b.Fulfillment.Outbound.orStore() == LegCourier && it.ReturnedAt == nil && (b.Fulfillment.DeliveredAt == nil || it.NotReceived)
	inbound := b.Fulfillment.Return.orStore() == LegCourier && it.ReturnedAt != nil && it.ReceivedAt == nil
	if it.Resolved() || (!outbound && !inbound) {
		return Transition{}, fmt.Errorf("booking %s asset %s is not with the carrier: %w", b.ID, asset, ErrInvalidTransition)
	}
	it.LostConfirmedAt, it.CarrierLiable = ptr(at), true
	return b.afterItemResolved(ActionDelivery, actor, at, string(asset)+" lost by carrier: "+reason), nil
}

// RecordCarrierDamage flags an item the carrier reported damaged; damage fees
// then need evidence that the customer caused it.
func (b *Booking) RecordCarrierDamage(actor identity.Actor, asset inventory.AssetID, at time.Time) (Transition, error) {
	if !actor.Valid() {
		return Transition{}, ErrActorRequired
	}
	if err := b.requireStatus(ActionDelivery, StatusRented, StatusReturnInspection); err != nil {
		return Transition{}, err
	}
	it, err := b.Item(asset)
	if err != nil {
		return Transition{}, err
	}
	it.CarrierDamageReported = true
	return b.deliveryTransition(actor, at, string(asset)+" damage reported by carrier"), nil
}

// RecordDeliveryFailure applies a classified failed outbound attempt. From
// the first one the customer caused, delivery delay no longer counts against
// lateness (decision D42); a store or carrier failure changes nothing, so the
// delay keeps counting until the parcel arrives.
func (b *Booking) RecordDeliveryFailure(actor identity.Actor, attempt delivery.Attempt) (Transition, error) {
	if !actor.Valid() {
		return Transition{}, ErrActorRequired
	}
	if err := b.requireStatus(ActionDelivery, StatusRented); err != nil {
		return Transition{}, err
	}
	if b.Fulfillment.Outbound.orStore() != LegCourier || b.Fulfillment.DeliveredAt != nil {
		return Transition{}, fmt.Errorf("booking %s outbound is not awaiting delivery: %w", b.ID, ErrInvalidTransition)
	}
	switch attempt.Fault {
	case delivery.FaultCustomer:
		if m := b.Fulfillment.CustomerMissedDeliveryAt; m == nil || attempt.At.Before(*m) {
			b.Fulfillment.CustomerMissedDeliveryAt = ptr(attempt.At)
		}
	case delivery.FaultStore, delivery.FaultCarrier:
	default:
		return Transition{}, domainerr.Invalid("fault", "classify the failed attempt first")
	}
	return b.deliveryTransition(actor, attempt.At, fmt.Sprintf("delivery failed (%s): %s", attempt.Fault, attempt.Reason)), nil
}

// RecordCollectedAtStore records the customer collecting at the counter a
// parcel the carrier could not deliver (follow-up AT_STORE). It counts as the
// delivery: the delay before it is credited as for a late courier, and no
// delivery complaint applies because the items are checked in front of the
// customer.
func (b *Booking) RecordCollectedAtStore(actor identity.Actor, task *delivery.Task, at time.Time) (Transition, error) {
	if !actor.IsUser() {
		return Transition{}, ErrActorRequired
	}
	if err := b.requireStatus(ActionDelivery, StatusRented); err != nil {
		return Transition{}, err
	}
	if b.Fulfillment.DeliveredAt != nil {
		return Transition{}, fmt.Errorf("booking %s already delivered: %w", b.ID, ErrInvalidTransition)
	}
	if err := b.closedOutbound(task, delivery.FollowUpAtStore); err != nil {
		return Transition{}, err
	}
	if at.Before(*task.ReceivedAt) {
		return Transition{}, domainerr.Invalid("collected_at", "cannot be before the parcel was back in the store")
	}
	b.Fulfillment.DeliveredAt, b.Fulfillment.CollectedAtStore = ptr(at), true
	return b.deliveryTransition(actor, at, "collected at store after failed delivery"), nil
}

// UndeliveredEnd is the snapshot of a rental ended because the outbound
// parcel could not be delivered (decision D42).
type UndeliveredEnd struct {
	TaskID    delivery.TaskID
	AttemptID string
	Fault     delivery.Fault
	// FeePercent of RentBefore is kept: the no-show rate (D8) when the
	// customer caused the failure, nothing when the store or carrier did.
	FeePercent int
	RentBefore money.Money
	Rent       money.Money
	// WaiveOutbound and WaiveReturn name the delivery fees to reverse: the
	// return leg never went to the carrier, and the outbound fee is waived
	// when the store or carrier caused the failure.
	WaiveOutbound bool
	WaiveReturn   bool
	Basis         string
	By            identity.Actor
	At            time.Time
}

// UndeliveredRequest ends a rental whose outbound shipment the carrier gave
// up on.
type UndeliveredRequest struct {
	Actor identity.Actor
	// Task is the booking's outbound shipment closed with FollowUpEndBooking.
	Task *delivery.Task
	Now  time.Time
}

// EndUndelivered ends a rental the customer never received. The items are
// back in the store (the closed shipment says when) and go to inspection like
// any return; no late, loss, cleaning or damage fee applies to them. RentTotal
// becomes the kept share, rounded down to the đồng; the use case reduces the
// ledger rent (payments.Ledger.ReduceRent) when it is lower, reverses the
// waived delivery fees and refunds the rest through SettleRental. Fees
// already approved, such as redelivery fees, are not touched here.
func (b *Booking) EndUndelivered(req UndeliveredRequest) (UndeliveredEnd, Transition, error) {
	if !req.Actor.IsUser() {
		return UndeliveredEnd{}, Transition{}, ErrActorRequired
	}
	if err := b.requireStatus(ActionDelivery, StatusRented); err != nil {
		return UndeliveredEnd{}, Transition{}, err
	}
	if b.Fulfillment.DeliveredAt != nil {
		return UndeliveredEnd{}, Transition{}, fmt.Errorf("booking %s already delivered: %w", b.ID, ErrInvalidTransition)
	}
	if err := b.closedOutbound(req.Task, delivery.FollowUpEndBooking); err != nil {
		return UndeliveredEnd{}, Transition{}, err
	}
	attempt, _ := req.Task.FinalAttempt()
	end := UndeliveredEnd{TaskID: req.Task.ID, AttemptID: attempt.ID, Fault: attempt.Fault, RentBefore: b.RentTotal,
		WaiveReturn: b.Fulfillment.Return.orStore() == LegCourier, By: req.Actor, At: req.Now.UTC()}
	switch attempt.Fault {
	case delivery.FaultCustomer:
		end.FeePercent, end.Basis = b.Policy.Cancellation.NoShowFeePercent, "customer caused the failed delivery: no-show rate"
	case delivery.FaultStore, delivery.FaultCarrier:
		end.WaiveOutbound, end.Basis = true, "store or carrier caused the failed delivery: nothing kept"
	default:
		return UndeliveredEnd{}, Transition{}, fmt.Errorf("task %s: %w", req.Task.ID, delivery.ErrAttemptUnclassified)
	}
	rent, err := money.New(b.RentTotal.Amount()*int64(end.FeePercent)/100, b.Currency)
	if err != nil {
		return UndeliveredEnd{}, Transition{}, err
	}
	end.Rent = rent
	back := *req.Task.ReceivedAt
	for i := range b.Items {
		it := &b.Items[i]
		if it.Resolved() {
			continue // lost in transit
		}
		it.ReturnedAt, it.ReceivedAt, it.NotReceived = ptr(back), ptr(back), true
	}
	b.RentTotal, b.Fulfillment.Undelivered = rent, &end
	return end, b.afterItemResolved(ActionDelivery, req.Actor, req.Now, fmt.Sprintf("ended undelivered (%s): %d%% of rent kept", end.Fault, end.FeePercent)), nil
}

// closedOutbound checks the task is this booking's outbound shipment, closed
// with the given follow-up once the parcel was back in the store.
func (b *Booking) closedOutbound(task *delivery.Task, follow delivery.FollowUp) error {
	if b.Fulfillment.Outbound.orStore() != LegCourier {
		return fmt.Errorf("booking %s outbound is not by courier: %w", b.ID, ErrInvalidTransition)
	}
	if task == nil || task.BookingRef != string(b.ID) || task.Direction != delivery.Outbound {
		return domainerr.Invalid("task", "not an outbound shipment of this booking")
	}
	if task.Status != delivery.StatusClosedUnfulfilled || task.FollowUp != follow || task.ReceivedAt == nil {
		return fmt.Errorf("task %s %s/%s, want closed with %s: %w", task.ID, task.Status, task.FollowUp, follow, ErrInvalidTransition)
	}
	return nil
}

// DelayedBlockEnd is where an item's allocation must end once a late courier
// delivery moved its due time (decision D42); ok is false without a delay.
// The use case extends the allocation with inventory.Allocation.Extend, which
// leaves it unchanged and lists the conflicts when the extra time is taken.
func (b *Booking) DelayedBlockEnd(asset inventory.AssetID) (time.Time, bool, error) {
	delay := b.Fulfillment.ServiceDelay(b.Schedule.PickupAt)
	if delay == 0 {
		return time.Time{}, false, nil
	}
	it, err := b.Item(asset)
	if err != nil {
		return time.Time{}, false, err
	}
	comp, err := b.component(it.LineID, it.ComponentKey)
	if err != nil {
		return time.Time{}, false, err
	}
	s := Schedule{PickupAt: b.Schedule.PickupAt, DueAt: b.Schedule.DueAt.Add(delay)}
	block, err := b.blockFor(s, b.Fulfillment, b.Policy.Buffers.For(comp.BufferClass))
	if err != nil {
		return time.Time{}, false, err
	}
	return block.End(), true, nil
}

func (b *Booking) courierReturn(actor identity.Actor) error {
	if !actor.Valid() {
		return ErrActorRequired
	}
	if err := b.requireStatus(ActionDelivery, StatusRented); err != nil {
		return err
	}
	if b.Fulfillment.Return.orStore() != LegCourier {
		return fmt.Errorf("booking %s return is not by courier: %w", b.ID, ErrInvalidTransition)
	}
	return nil
}

func (b *Booking) deliveryTransition(actor identity.Actor, at time.Time, reason string) Transition {
	return Transition{BookingID: b.ID, Action: ActionDelivery, From: b.Status, To: b.Status, Actor: actor, At: at.UTC(), Reason: reason}
}
