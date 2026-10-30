package rentals

import (
	"fmt"
	"strings"
	"time"

	"webee_rentor/internal/delivery"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
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
