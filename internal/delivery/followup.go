package delivery

import (
	"fmt"
	"strings"
	"time"

	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
)

// FollowUp is what staff do once the carrier stopped attempting a shipment
// (decision D42). The carrier's own re-attempts on the same shipment need
// none of this.
type FollowUp string

const (
	// FollowUpNewShipment: a new shipment for the same leg, free of charge.
	FollowUpNewShipment FollowUp = "NEW_SHIPMENT"
	// FollowUpAtStore: the customer collects (outbound) or returns (return)
	// at the counter instead.
	FollowUpAtStore FollowUp = "AT_STORE"
	// FollowUpEndBooking, outbound only: the rental ends undelivered.
	FollowUpEndBooking FollowUp = "END_BOOKING"
)

var ErrAttemptUnclassified = domainerr.New(domainerr.KindInvalidState, "delivery.attempt_unclassified", "classify who caused the failed attempt first")

// AwaitingFollowUp reports whether the carrier stopped attempting and staff
// have not closed the task yet.
func (t *Task) AwaitingFollowUp() bool {
	return t.Status == StatusManualReview && t.GaveUpAt != nil
}

// FinalAttempt is the failed attempt the carrier stopped on.
func (t *Task) FinalAttempt() (Attempt, bool) {
	if t.GaveUpAt == nil {
		return Attempt{}, false
	}
	return t.Attempt(t.GaveUpAttempt)
}

// CloseUnfulfilled ends a shipment the carrier stopped attempting, with the
// follow-up staff chose. The final attempt must be classified first, because
// who caused it decides the money. Closing an outbound shipment confirms the
// parcel is back in the store: every follow-up starts from there.
func (t *Task) CloseUnfulfilled(p identity.Principal, follow FollowUp, reason string, at time.Time) error {
	if err := p.Require(identity.PermDeliveryManage); err != nil {
		return err
	}
	if strings.TrimSpace(reason) == "" {
		return ErrReasonRequired
	}
	switch follow {
	case FollowUpNewShipment, FollowUpAtStore:
	case FollowUpEndBooking:
		if t.Direction != Outbound {
			return domainerr.Invalid("follow_up", "only an undelivered outbound shipment can end the booking")
		}
	default:
		return domainerr.Invalid("follow_up", "unknown follow-up")
	}
	if !t.AwaitingFollowUp() {
		return fmt.Errorf("task %s from %s: %w", t.ID, t.Status, ErrInvalidTransition)
	}
	a, ok := t.FinalAttempt()
	if !ok || a.Fault == FaultUnknown || a.Fault == "" {
		return fmt.Errorf("task %s attempt %s: %w", t.ID, t.GaveUpAttempt, ErrAttemptUnclassified)
	}
	actor := p.Actor()
	if t.Direction == Outbound {
		t.ReceivedAt = ptr(at)
	}
	t.Status, t.FollowUp, t.ClosedBy, t.ClosedReason = StatusClosedUnfulfilled, follow, &actor, reason
	return nil
}

// FollowUpTask creates the new shipment for a leg closed with
// FollowUpNewShipment. It gets its own client reference, carries no customer
// fee (the leg was paid once; a customer-caused failure is charged as a
// redelivery fee instead) and can be created once.
func (t *Task) FollowUpTask(req NewTaskRequest) (*Task, error) {
	if t.Status != StatusClosedUnfulfilled || t.FollowUp != FollowUpNewShipment || t.ReplacedBy != "" {
		return nil, fmt.Errorf("task %s from %s: %w", t.ID, t.Status, ErrInvalidTransition)
	}
	if req.ID == t.ID || req.BookingRef != t.BookingRef || req.CustomerID != t.CustomerID || req.Direction != t.Direction {
		return nil, fmt.Errorf("task %s follow-up: %w", t.ID, ErrShipmentMismatch)
	}
	if !req.CustomerFee.IsZero() {
		return nil, domainerr.Invalid("customer_fee", "a follow-up shipment is free of charge")
	}
	n, err := NewTask(req)
	if err != nil {
		return nil, err
	}
	n.Replaces, t.ReplacedBy = t.ID, n.ID
	return n, nil
}
