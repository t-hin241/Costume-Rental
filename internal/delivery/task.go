package delivery

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
	"webee_rentor/internal/period"
)

type TaskID string

// Direction of a task.
type Direction string

const (
	Outbound Direction = "OUTBOUND" // store → customer
	Return   Direction = "RETURN"   // customer → store
)

// Status is the internal task status (policy §8). Carrier codes are mapped to
// these by the adapter and never leak into the domain.
type Status string

const (
	// Outbound progress.
	StatusPendingCreation Status = "PENDING_CREATION"
	StatusCreated         Status = "CREATED"
	StatusReadyForPickup  Status = "READY_FOR_PICKUP"
	StatusPickedUp        Status = "PICKED_UP"
	StatusInTransit       Status = "IN_TRANSIT"
	StatusDelivered       Status = "DELIVERED"
	// Return progress (PENDING_CREATION and IN_TRANSIT are shared).
	StatusScheduled       Status = "SCHEDULED"
	StatusCourierAssigned Status = "COURIER_ASSIGNED"
	StatusPickupConfirmed Status = "PICKUP_CONFIRMED"
	StatusReceivedAtStore Status = "RECEIVED_AT_STORE"
	// Exceptions.
	StatusCreationFailed  Status = "CREATION_FAILED"
	StatusPickupFailed    Status = "PICKUP_FAILED"
	StatusDeliveryFailed  Status = "DELIVERY_FAILED"
	StatusReturnFailed    Status = "RETURN_FAILED"
	StatusCancelRequested Status = "CANCEL_REQUESTED"
	StatusCancelled       Status = "CANCELLED"
	StatusLost            Status = "LOST"
	StatusDamaged         Status = "DAMAGED"
	StatusManualReview    Status = "MANUAL_REVIEW"
)

var progress = map[Direction][]Status{
	Outbound: {StatusPendingCreation, StatusCreated, StatusReadyForPickup, StatusPickedUp, StatusInTransit, StatusDelivered},
	Return:   {StatusScheduled, StatusPendingCreation, StatusCourierAssigned, StatusPickupConfirmed, StatusInTransit, StatusReceivedAtStore},
}

func rank(d Direction, s Status) int { return slices.Index(progress[d], s) }

func terminal(s Status) bool {
	return s == StatusDelivered || s == StatusReceivedAtStore || s == StatusCancelled || s == StatusLost
}

// Fault says who caused a failed attempt (customer absent or wrong address,
// store not ready, carrier failure). Only customer faults may be charged.
type Fault string

const (
	FaultUnknown  Fault = "UNKNOWN"
	FaultCustomer Fault = "CUSTOMER"
	FaultStore    Fault = "STORE"
	FaultCarrier  Fault = "CARRIER"
)

var (
	ErrShipmentMismatch  = domainerr.New(domainerr.KindConflict, "delivery.shipment_mismatch", "event or result does not belong to this task")
	ErrInvalidTransition = domainerr.New(domainerr.KindInvalidState, "delivery.invalid_transition", "the task cannot do this in its current status")
	ErrReasonRequired    = domainerr.New(domainerr.KindInvalid, "delivery.reason_required", "a reason is required")
	ErrActorRequired     = domainerr.New(domainerr.KindInvalid, "delivery.actor_required", "a valid actor is required")
)

// Parcel describes the package handed to the carrier.
type Parcel struct {
	WeightGram    int
	LengthCM      int
	WidthCM       int
	HeightCM      int
	PackageCount  int
	DeclaredValue money.Money
}

// Attempt is one failed delivery or pickup attempt.
type Attempt struct {
	ID       string
	Status   Status // DELIVERY_FAILED or PICKUP_FAILED
	At       time.Time
	Fault    Fault
	Reason   string
	Evidence string
	// ClassifiedBy is set when staff decide the fault.
	ClassifiedBy *identity.Actor
}

// EventSource is where an event came from.
type EventSource string

const (
	SourceCarrierWebhook EventSource = "CARRIER_WEBHOOK"
	SourceCarrierAPI     EventSource = "CARRIER_API"
	SourceAdmin          EventSource = "ADMIN"
)

// Event is the append-only history of a task, including events that were
// stale or duplicates and therefore did not change the status.
type Event struct {
	Key            string
	Source         EventSource
	Status         Status
	ProviderStatus string
	OccurredAt     time.Time
	ReceivedAt     time.Time
	Actor          *identity.Actor
	Reason         string
	Evidence       string
	Applied        bool
}

// Task is one shipment leg of a booking.
type Task struct {
	ID         TaskID
	BookingRef string
	CustomerID identity.UserID
	Direction  Direction
	Status     Status
	// Progress is the furthest normal status reached; exceptions do not
	// overwrite it, so late progress events can be judged.
	Progress     Status
	ProviderCode string
	// ClientReference is the stable idempotency reference sent to the
	// carrier; it never changes on retry.
	ClientReference      string
	ProviderShipmentCode string
	ServiceCode          string
	From                 Address
	To                   Address
	Window               period.Range
	ExpectedAt           *time.Time
	PickedUpAt           *time.Time
	DeliveredAt          *time.Time
	ReceivedAt           *time.Time
	Parcel               Parcel
	// CustomerFee is the snapshot of what the customer pays for this leg;
	// carrier costs never change it.
	CustomerFee         money.Money
	CarrierFeeEstimated *money.Money
	CarrierFeeFinal     *money.Money
	FailureCode         string
	FailureReason       string
	Attempts            []Attempt
	Events              []Event
	statusBeforeCancel  Status
	CreatedAt           time.Time
}

// NewTaskRequest creates a task. For OUTBOUND, From is the store and To the
// customer; for RETURN it is the reverse (customer → store), which is a new
// shipment, not the carrier "return to sender" operation.
type NewTaskRequest struct {
	ID           TaskID
	BookingRef   string
	CustomerID   identity.UserID
	Direction    Direction
	ProviderCode string
	Store        Address
	Customer     Address
	Window       period.Range
	Parcel       Parcel
	CustomerFee  money.Money
	Now          time.Time
}

// NewTask creates an outbound task (PENDING_CREATION) or a return task
// (SCHEDULED).
func NewTask(req NewTaskRequest) (*Task, error) {
	switch {
	case req.ID == "" || req.BookingRef == "" || req.CustomerID == "" || strings.TrimSpace(req.ProviderCode) == "":
		return nil, domainerr.Invalid("task", "id, booking, customer and provider are required")
	case req.Direction != Outbound && req.Direction != Return:
		return nil, domainerr.Invalid("direction", "unknown direction")
	case req.Window.IsZero():
		return nil, domainerr.Invalid("window", "required")
	case !req.CustomerFee.Currency().Valid() || req.CustomerFee.IsNegative():
		return nil, domainerr.Invalid("customer_fee", "must be zero or positive")
	case req.Parcel.PackageCount < 1 || req.Parcel.WeightGram < 1:
		return nil, domainerr.Invalid("parcel", "weight and package count are required")
	}
	if err := req.Store.Validate(); err != nil {
		return nil, fmt.Errorf("store address: %w", err)
	}
	if err := req.Customer.Validate(); err != nil {
		return nil, fmt.Errorf("customer address: %w", err)
	}
	t := &Task{
		ID: req.ID, BookingRef: req.BookingRef, CustomerID: req.CustomerID, Direction: req.Direction,
		ProviderCode: req.ProviderCode, ClientReference: string(req.ID), Window: req.Window,
		Parcel: req.Parcel, CustomerFee: req.CustomerFee, CreatedAt: req.Now.UTC(),
	}
	if req.Direction == Outbound {
		t.Status, t.From, t.To = StatusPendingCreation, req.Store, req.Customer
	} else {
		t.Status, t.From, t.To = StatusScheduled, req.Customer, req.Store
	}
	t.Progress = t.Status
	return t, nil
}

func (t *Task) set(s Status) {
	t.Status = s
	if rank(t.Direction, s) > rank(t.Direction, t.Progress) {
		t.Progress = s
	}
}

func (t *Task) require(allowed ...Status) error {
	if !slices.Contains(allowed, t.Status) {
		return fmt.Errorf("task %s from %s: %w", t.ID, t.Status, ErrInvalidTransition)
	}
	return nil
}

// RequestCreation queues the return shipment for creation (SCHEDULED →
// PENDING_CREATION), e.g. a configured time before the due date.
func (t *Task) RequestCreation() error {
	if t.Direction != Return {
		return fmt.Errorf("task %s: %w", t.ID, ErrInvalidTransition)
	}
	if err := t.require(StatusScheduled); err != nil {
		return err
	}
	t.set(StatusPendingCreation)
	return nil
}

// ShipmentResult is what the carrier returned when creating a shipment.
type ShipmentResult struct {
	ProviderShipmentCode string
	ServiceCode          string
	ExpectedAt           *time.Time
	EstimatedFee         *money.Money
}

// RecordCreated stores the carrier shipment. Recording the same code again
// (a retried worker) is a no-op; a different code means a duplicate shipment
// and is refused.
func (t *Task) RecordCreated(r ShipmentResult) (bool, error) {
	if r.ProviderShipmentCode == "" {
		return false, domainerr.Invalid("provider_shipment_code", "required")
	}
	if t.ProviderShipmentCode != "" {
		if t.ProviderShipmentCode == r.ProviderShipmentCode {
			return false, nil
		}
		return false, fmt.Errorf("task %s already has shipment %s: %w", t.ID, t.ProviderShipmentCode, ErrShipmentMismatch)
	}
	if err := t.require(StatusPendingCreation, StatusCreationFailed); err != nil {
		return false, err
	}
	t.ProviderShipmentCode, t.ServiceCode, t.ExpectedAt = r.ProviderShipmentCode, r.ServiceCode, r.ExpectedAt
	if r.EstimatedFee != nil {
		fee := *r.EstimatedFee
		t.CarrierFeeEstimated = &fee
	}
	t.FailureCode, t.FailureReason = "", ""
	if t.Direction == Outbound {
		t.set(StatusCreated)
	} else {
		t.set(StatusCourierAssigned)
	}
	return true, nil
}

// RecordCreationFailed records a permanent creation error (sanitised code and
// reason). Transient errors are retried by the worker without calling this.
func (t *Task) RecordCreationFailed(code, reason string) error {
	if err := t.require(StatusPendingCreation); err != nil {
		return err
	}
	if strings.TrimSpace(reason) == "" {
		return ErrReasonRequired
	}
	t.FailureCode, t.FailureReason = code, reason
	t.Status = StatusCreationFailed
	return nil
}

// RetryCreation lets staff retry a failed creation with the same client
// reference, so the carrier can detect a duplicate.
func (t *Task) RetryCreation(actor identity.Actor) error {
	if !actor.IsUser() {
		return ErrActorRequired
	}
	if err := t.require(StatusCreationFailed); err != nil {
		return err
	}
	t.Status = StatusPendingCreation
	return nil
}

// MarkReadyForPickup records that the parcel is packed and checked.
func (t *Task) MarkReadyForPickup(actor identity.Actor) error {
	if !actor.IsUser() {
		return ErrActorRequired
	}
	if t.Direction != Outbound {
		return fmt.Errorf("task %s: %w", t.ID, ErrInvalidTransition)
	}
	if err := t.require(StatusCreated); err != nil {
		return err
	}
	t.set(StatusReadyForPickup)
	return nil
}

// RequestCancel asks the carrier to cancel before the parcel is picked up.
// The task is CANCELLED only once the carrier confirms.
func (t *Task) RequestCancel(actor identity.Actor, reason string) error {
	if !actor.Valid() {
		return ErrActorRequired
	}
	if strings.TrimSpace(reason) == "" {
		return ErrReasonRequired
	}
	if terminal(t.Status) || t.Status == StatusCancelRequested || rank(t.Direction, t.Progress) >= rank(t.Direction, t.pickedUpStatus()) {
		return fmt.Errorf("task %s from %s: %w", t.ID, t.Status, ErrInvalidTransition)
	}
	t.statusBeforeCancel = t.Status
	t.Status = StatusCancelRequested
	t.FailureReason = reason
	return nil
}

// ConfirmCancelled records the carrier's confirmation (or that no shipment
// existed yet).
func (t *Task) ConfirmCancelled() error {
	if err := t.require(StatusCancelRequested); err != nil {
		return err
	}
	t.Status = StatusCancelled
	return nil
}

// CancelRejected restores the previous status when the carrier refuses.
func (t *Task) CancelRejected(reason string) error {
	if err := t.require(StatusCancelRequested); err != nil {
		return err
	}
	t.Status, t.FailureReason = t.statusBeforeCancel, reason
	return nil
}

func (t *Task) pickedUpStatus() Status {
	if t.Direction == Outbound {
		return StatusPickedUp
	}
	return StatusPickupConfirmed
}

// ReceiveAtStore is staff confirming the return parcel physically arrived.
// It never makes assets READY; inspection follows.
func (t *Task) ReceiveAtStore(actor identity.Actor, at time.Time) error {
	if !actor.IsUser() {
		return ErrActorRequired
	}
	if t.Direction != Return {
		return fmt.Errorf("task %s: %w", t.ID, ErrInvalidTransition)
	}
	if err := t.require(StatusPickupConfirmed, StatusInTransit, StatusDamaged, StatusManualReview); err != nil {
		return err
	}
	t.ReceivedAt = ptr(at)
	t.set(StatusReceivedAtStore)
	return nil
}

// ClassifyAttempt lets staff decide who caused a failed attempt.
func (t *Task) ClassifyAttempt(actor identity.Actor, attemptID string, fault Fault, reason string) error {
	if !actor.IsUser() {
		return ErrActorRequired
	}
	if strings.TrimSpace(reason) == "" {
		return ErrReasonRequired
	}
	if fault != FaultCustomer && fault != FaultStore && fault != FaultCarrier {
		return domainerr.Invalid("fault", "unknown fault")
	}
	i := slices.IndexFunc(t.Attempts, func(a Attempt) bool { return a.ID == attemptID })
	if i < 0 {
		return domainerr.Invalid("attempt", "not found")
	}
	t.Attempts[i].Fault, t.Attempts[i].Reason, t.Attempts[i].ClassifiedBy = fault, reason, &actor
	return nil
}

// Attempt finds a failed attempt.
func (t *Task) Attempt(id string) (Attempt, bool) {
	i := slices.IndexFunc(t.Attempts, func(a Attempt) bool { return a.ID == id })
	if i < 0 {
		return Attempt{}, false
	}
	return t.Attempts[i], true
}

func ptr(t time.Time) *time.Time {
	u := t.UTC()
	return &u
}
