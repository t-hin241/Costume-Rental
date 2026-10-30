package delivery

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
)

// CarrierEvent is a carrier status update already authenticated and mapped
// to an internal status by the adapter. Key is the provider event id, or
// shipment code + type + time + payload hash when the provider has none.
type CarrierEvent struct {
	Provider       string
	ShipmentCode   string
	Key            string
	Status         Status
	ProviderStatus string
	OccurredAt     time.Time
	ReceivedAt     time.Time
	Source         EventSource
	Reason         string
	Evidence       string
	// Fault, when the carrier says who caused a failed attempt; otherwise
	// staff classify it.
	Fault Fault
}

// Outcome reports what an event did.
type Outcome string

const (
	OutcomeApplied   Outcome = "APPLIED"
	OutcomeDuplicate Outcome = "DUPLICATE"
	// OutcomeStale: an older or out-of-order event; recorded, no change.
	OutcomeStale Outcome = "STALE"
	// OutcomeReview: contradicts a final status; the task goes to
	// MANUAL_REVIEW and nothing is released automatically.
	OutcomeReview Outcome = "MANUAL_REVIEW"
)

var carrierStatuses = map[Direction][]Status{
	Outbound: {StatusPickedUp, StatusInTransit, StatusDelivered, StatusPickupFailed, StatusDeliveryFailed, StatusLost, StatusDamaged, StatusCancelled},
	Return:   {StatusPickupConfirmed, StatusInTransit, StatusDelivered, StatusPickupFailed, StatusReturnFailed, StatusLost, StatusDamaged, StatusCancelled},
}

// ApplyCarrierEvent records an event and moves the task forward when it
// makes sense. Events never move the task backwards; duplicates are ignored;
// events for another provider or shipment are rejected.
func (t *Task) ApplyCarrierEvent(e CarrierEvent) (Outcome, error) {
	if e.Provider != t.ProviderCode || e.ShipmentCode == "" || e.ShipmentCode != t.ProviderShipmentCode {
		return "", fmt.Errorf("task %s event for %s/%s: %w", t.ID, e.Provider, e.ShipmentCode, ErrShipmentMismatch)
	}
	if strings.TrimSpace(e.Key) == "" || e.OccurredAt.IsZero() {
		return "", domainerr.Invalid("event", "key and occurrence time are required")
	}
	if !slices.Contains(carrierStatuses[t.Direction], e.Status) {
		return "", domainerr.Invalid("status", fmt.Sprintf("%s is not a carrier status for %s", e.Status, t.Direction))
	}
	if slices.ContainsFunc(t.Events, func(x Event) bool { return x.Key == e.Key }) {
		return OutcomeDuplicate, nil
	}
	if e.Source == "" {
		e.Source = SourceCarrierWebhook
	}
	outcome := t.decide(e)
	t.Events = append(t.Events, Event{
		Key: e.Key, Source: e.Source, Status: e.Status, ProviderStatus: e.ProviderStatus,
		OccurredAt: e.OccurredAt.UTC(), ReceivedAt: e.ReceivedAt.UTC(), Reason: e.Reason, Evidence: e.Evidence,
		Applied: outcome == OutcomeApplied || outcome == OutcomeReview,
	})
	return outcome, nil
}

func (t *Task) decide(e CarrierEvent) Outcome {
	// A return shipment "delivered" to the store is the carrier's claim;
	// staff confirm receipt with ReceiveAtStore.
	if t.Direction == Return && e.Status == StatusDelivered {
		if t.Status == StatusReceivedAtStore {
			return OutcomeStale
		}
		t.DeliveredAt = ptr(e.OccurredAt)
		return OutcomeApplied
	}
	if t.Status == StatusManualReview {
		// Only staff resolve a review; carrier events are kept as history.
		if e.Source != SourceAdmin {
			return OutcomeStale
		}
		t.set(e.Status)
		return OutcomeApplied
	}
	if terminal(t.Status) {
		if e.Status == t.Status || (t.Status != StatusCancelled && rank(t.Direction, e.Status) >= 0) {
			return OutcomeStale
		}
		t.Status = StatusManualReview
		t.FailureReason = fmt.Sprintf("carrier reported %s after %s", e.Status, t.Progress)
		return OutcomeReview
	}
	switch e.Status {
	case StatusCancelled:
		if t.Status != StatusCancelRequested {
			t.Status, t.FailureReason = StatusManualReview, "carrier cancelled without a request"
			return OutcomeReview
		}
		t.Status = StatusCancelled
		return OutcomeApplied
	case StatusPickupFailed, StatusDeliveryFailed, StatusReturnFailed:
		t.Status, t.FailureReason = e.Status, e.Reason
		fault := e.Fault
		if fault == "" {
			fault = FaultUnknown
		}
		t.Attempts = append(t.Attempts, Attempt{ID: e.Key, Status: e.Status, At: e.OccurredAt.UTC(), Fault: fault, Reason: e.Reason, Evidence: e.Evidence})
		return OutcomeApplied
	case StatusLost, StatusDamaged:
		t.Status, t.FailureReason = e.Status, e.Reason
		return OutcomeApplied
	}
	// Normal progress: only forward. After a failed attempt a re-attempt may
	// report the same or a later stage again.
	r, cur := rank(t.Direction, e.Status), rank(t.Direction, t.Progress)
	failed := t.Status == StatusPickupFailed || t.Status == StatusDeliveryFailed || t.Status == StatusReturnFailed
	if r < cur || (r == cur && !failed) {
		return OutcomeStale
	}
	switch e.Status {
	case StatusPickedUp, StatusPickupConfirmed:
		t.PickedUpAt = ptr(e.OccurredAt)
	case StatusDelivered:
		t.DeliveredAt = ptr(e.OccurredAt)
	}
	t.set(e.Status)
	return OutcomeApplied
}

// ManualEvent lets authorised staff record a status the carrier did not
// report (manual coordination). Reason and actor are mandatory and the event
// is kept for audit; cancellation and store receipt use their own methods.
func (t *Task) ManualEvent(p identity.Principal, status Status, reason, evidence string, at time.Time) (Outcome, error) {
	if err := p.Require(identity.PermDeliveryManage); err != nil {
		return "", err
	}
	if strings.TrimSpace(reason) == "" {
		return "", ErrReasonRequired
	}
	if status == StatusCancelled || status == StatusReceivedAtStore || !slices.Contains(carrierStatuses[t.Direction], status) {
		return "", domainerr.Invalid("status", "not a manual status")
	}
	actor := p.Actor()
	key := fmt.Sprintf("manual:%s:%d", status, at.UnixNano())
	e := CarrierEvent{Provider: t.ProviderCode, ShipmentCode: t.ProviderShipmentCode, Key: key, Status: status,
		OccurredAt: at, ReceivedAt: at, Source: SourceAdmin, Reason: reason, Evidence: evidence}
	if t.ProviderShipmentCode == "" {
		e.ShipmentCode = "manual"
		t.ProviderShipmentCode = "manual"
	}
	out, err := t.ApplyCarrierEvent(e)
	if err == nil {
		t.Events[len(t.Events)-1].Actor = &actor
	}
	return out, err
}

// FeeReconciliation compares carrier cost with what the customer paid.
type FeeReconciliation struct {
	CustomerFee money.Money
	Estimated   *money.Money
	Final       money.Money
	// Variance is Final - Estimated (zero when nothing was estimated).
	Variance money.Money
	// WithinTolerance: the store absorbs the difference; otherwise staff
	// review it. The customer fee never changes automatically.
	WithinTolerance bool
}

// RecordFinalFee stores the carrier's final cost and reconciles it against
// the estimate with the store's tolerance.
func (t *Task) RecordFinalFee(final, tolerance money.Money) (FeeReconciliation, error) {
	if !final.Currency().Valid() || final.IsNegative() || tolerance.Currency() != final.Currency() || tolerance.IsNegative() {
		return FeeReconciliation{}, domainerr.Invalid("fee", "final fee and tolerance must be zero or positive")
	}
	f := final
	t.CarrierFeeFinal = &f
	r := FeeReconciliation{CustomerFee: t.CustomerFee, Estimated: t.CarrierFeeEstimated, Final: final, Variance: money.Zero(final.Currency())}
	if t.CarrierFeeEstimated != nil {
		v, err := final.Sub(*t.CarrierFeeEstimated)
		if err != nil {
			return FeeReconciliation{}, err
		}
		r.Variance = v
	}
	abs := r.Variance.Amount()
	if abs < 0 {
		abs = -abs
	}
	r.WithinTolerance = abs <= tolerance.Amount()
	return r, nil
}

// AuthorizeRead lets customers see the tasks of their own bookings and staff
// see all.
func AuthorizeRead(p identity.Principal, t *Task) error {
	_, err := p.RequireOwnOrAny(identity.PermDeliveryReadOwn, identity.PermDeliveryReadAll, t.CustomerID)
	return err
}
