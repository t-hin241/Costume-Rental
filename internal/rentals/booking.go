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

type (
	BookingID string
	LineID    string
)

// Status is the booking lifecycle. Payment state and asset condition are
// separate lifecycles owned by payments and inventory.
type Status string

const (
	StatusPendingConfirmation Status = "PENDING_CONFIRMATION"
	StatusConfirmed           Status = "CONFIRMED"
	StatusPreparing           Status = "PREPARING"
	StatusRented              Status = "RENTED"
	StatusReturnInspection    Status = "RETURN_INSPECTION"
	StatusCompleted           Status = "COMPLETED"
	StatusCancelled           Status = "CANCELLED"
	StatusExpired             Status = "EXPIRED"
)

// IsTerminal reports whether no further transition is possible.
func (s Status) IsTerminal() bool {
	return s == StatusCompleted || s == StatusCancelled || s == StatusExpired
}

// Action names an intent-specific booking operation.
type Action string

const (
	ActionCreate           Action = "booking.create"
	ActionRead             Action = "booking.read"
	ActionConfirm          Action = "booking.confirm"
	ActionStartPreparation Action = "booking.start_preparation"
	ActionHandover         Action = "booking.handover"
	ActionReturn           Action = "booking.return"
	ActionInspectAsset     Action = "booking.inspect_asset"
	ActionComplete         Action = "booking.complete"
	ActionCancel           Action = "booking.cancel"
	ActionExpire           Action = "booking.expire"
	ActionReschedule       Action = "booking.reschedule"
	ActionReportMissing    Action = "booking.report_missing"
	ActionConfirmLost      Action = "booking.confirm_lost"
	ActionReplaceAsset     Action = "booking.replace_asset"
	ActionModify           Action = "booking.modify"
	ActionDelivery         Action = "booking.delivery"
	ActionComplain         Action = "booking.complain"
)

// CancelReason classifies a cancellation.
type CancelReason string

const (
	CancelCustomerRequest CancelReason = "CUSTOMER_REQUEST"
	CancelNoShow          CancelReason = "NO_SHOW"
	CancelStaffDecision   CancelReason = "STAFF_DECISION"
	CancelPaymentFailed   CancelReason = "PAYMENT_FAILED"
)

var (
	ErrInvalidTransition       = domainerr.New(domainerr.KindInvalidState, "rentals.invalid_transition", "booking cannot perform this action in its current status")
	ErrHoldExpired             = domainerr.New(domainerr.KindInvalidState, "rentals.hold_expired", "the booking hold has expired")
	ErrHoldNotExpired          = domainerr.New(domainerr.KindInvalidState, "rentals.hold_not_expired", "the booking hold has not expired yet")
	ErrStaffConfirmationNeeded = domainerr.New(domainerr.KindForbidden, "rentals.staff_confirmation_required", "policy requires staff to confirm this booking")
	ErrInsufficientFunds       = domainerr.New(domainerr.KindInvalidState, "rentals.insufficient_funds", "required payment has not been received")
	ErrChecklistIncomplete     = domainerr.New(domainerr.KindInvalid, "rentals.checklist_incomplete", "handover checklist must be completed")
	ErrAssetMismatch           = domainerr.New(domainerr.KindInvalid, "rentals.asset_mismatch", "assets do not match the booking allocation")
	ErrHandoverTooLate         = domainerr.New(domainerr.KindInvalidState, "rentals.handover_too_late", "the rental period has already ended")
	ErrCancelWindowClosed      = domainerr.New(domainerr.KindInvalidState, "rentals.cancel_window_closed", "the no-show deadline has passed; the booking can only be recorded as a no-show")
	ErrRescheduleCutoffPassed  = domainerr.New(domainerr.KindInvalidState, "rentals.reschedule_cutoff_passed", "self-service rescheduling is no longer allowed; contact the store")
	ErrNoShowTooEarly          = domainerr.New(domainerr.KindInvalidState, "rentals.no_show_too_early", "no-show can only be recorded after the no-show deadline following pickup")
	ErrInspectionIncomplete    = domainerr.New(domainerr.KindInvalidState, "rentals.inspection_incomplete", "all returned assets must be inspected")
	ErrOpenFeeAssessments      = domainerr.New(domainerr.KindInvalidState, "rentals.open_fee_assessments", "proposed or disputed fees must be decided before completion")
	ErrModificationOpen        = domainerr.New(domainerr.KindInvalidState, "rentals.modification_open", "a booking change is pending; confirm, reject or cancel it first")
	ErrVerificationWindowOpen  = domainerr.New(domainerr.KindInvalidState, "rentals.verification_window_open", "the missing-item verification window has not ended")
	ErrNotSettled              = domainerr.New(domainerr.KindInvalidState, "rentals.not_settled", "payments, deposit and refunds must be reconciled before completion")
	ErrActorRequired           = domainerr.New(domainerr.KindInvalid, "rentals.actor_required", "a valid actor is required")
	ErrReasonRequired          = domainerr.New(domainerr.KindInvalid, "rentals.reason_required", "a reason is required")
)

// Line is the snapshot of one booked offering. It never changes after the
// booking is created, whatever happens to the catalog.
type Line struct {
	ID              LineID
	Kind            catalog.OfferingKind
	OfferingID      string
	OfferingName    string
	OfferingVersion int
	Quantity        int
	UnitRent        money.Money
	// ListRent is the catalog rent (unit x quantity) before discounts.
	ListRent money.Money
	// Discount is this line share of applied discounts (promotions).
	Discount money.Money
	// RentAmount is the rent charged for the line: ListRent - Discount.
	RentAmount    money.Money
	UnitDeposit   money.Money
	DepositAmount money.Money
	Components    []catalog.ComponentDemand
}

// PackageSnapshot freezes the rental package at booking time.
type PackageSnapshot struct {
	ID       catalog.PackageID
	Code     string
	Label    string
	Duration time.Duration
}

// Booking is the rental aggregate. Fields are exported for persistence and
// reads; every state change must go through a method so lifecycle rules,
// timestamps and transitions stay consistent. Never map client input onto it.
type Booking struct {
	ID         BookingID
	Code       string // human reference, not an access secret
	CustomerID identity.UserID
	Status     Status
	Schedule   Schedule
	Package    PackageSnapshot
	Lines      []Line
	Currency   money.Currency
	// GrossRentTotal and DiscountTotal explain RentTotal, the commercial
	// rent after discounts that the ledger charges.
	GrossRentTotal money.Money
	DiscountTotal  money.Money
	RentTotal      money.Money
	// Discounts and ManualAdjustments snapshot what was applied at booking.
	Discounts         []promotions.Applied
	ManualAdjustments []promotions.ManualApplied
	// SecurityDepositTotal is the refundable security deposit (tiền bảo đảm).
	SecurityDepositTotal money.Money
	// ReservationDeposit is the amount paid to hold the date (tiền giữ lịch);
	// zero when the policy has none. Its fate is decided by Policy.Reservation.
	ReservationDeposit money.Money
	// Fulfillment is how each direction is fulfilled, with courier windows
	// and delivery fees.
	Fulfillment Fulfillment
	// Policy is the snapshot of the rules in force when the booking was made.
	Policy Policy

	CreatedAt             time.Time
	HoldExpiresAt         time.Time
	ConfirmedAt           *time.Time
	PreparationStartedAt  *time.Time
	HandedOverAt          *time.Time
	ReturnedAt            *time.Time
	InspectionCompletedAt *time.Time
	CompletedAt           *time.Time
	CancelledAt           *time.Time
	ExpiredAt             *time.Time
	CancelReason          CancelReason
	CancelNote            string

	// Items are the physical assets handed over, each with its own return,
	// loss and inspection record.
	Items []RentedItem

	// OpenModification is the change request currently holding tentative
	// allocations; handover and reschedule wait until it ends.
	OpenModification ModificationID
	// Modifications lists confirmed changes in order; each keeps its own
	// before/after snapshot.
	Modifications []ModificationID
	// SelfServiceSizeChanges counts size exchanges the customer made online.
	SelfServiceSizeChanges int
	// ServiceCredits are rent refunds for accepted delivery complaints;
	// RentTotal is already reduced by them.
	ServiceCredits []ServiceCredit
}

// Transition is the audit record of a booking action.
type Transition struct {
	BookingID BookingID
	Action    Action
	From      Status
	To        Status
	Actor     identity.Actor
	At        time.Time
	Reason    string
	// ReleaseAllocations tells the use case to release every allocation of
	// the booking in the same transaction (idempotent).
	ReleaseAllocations bool
	// ReleaseModificationHolds names a modification whose tentative holds
	// must be released (it was rejected, cancelled or expired).
	ReleaseModificationHolds ModificationID
}

func (b *Booking) transition(action Action, to Status, actor identity.Actor, at time.Time, reason string) Transition {
	t := Transition{BookingID: b.ID, Action: action, From: b.Status, To: to, Actor: actor, At: at.UTC(), Reason: reason}
	b.Status = to
	return t
}

func (b *Booking) requireStatus(action Action, allowed ...Status) error {
	if !slices.Contains(allowed, b.Status) {
		return fmt.Errorf("booking %s %s from %s: %w", b.ID, action, b.Status, ErrInvalidTransition)
	}
	return nil
}

func ptr(t time.Time) *time.Time {
	u := t.UTC()
	return &u
}

func (b *Booking) amounts() Amounts {
	return Amounts{Rent: b.RentTotal, Security: b.SecurityDepositTotal, Reservation: b.ReservationDeposit,
		Delivery: b.Fulfillment.DeliveryFees(b.Currency)}
}

// ReservationCreditsRent reports whether the reservation deposit is deducted
// from the rent when the rental goes ahead. The use case passes this to the
// payment ledger when opening it.
func (b *Booking) ReservationCreditsRent() bool {
	return b.ReservationDeposit.IsPositive() && b.Policy.Reservation.Treatment == ReservationAppliedToRent
}

// CancellationFee is the fee owed for a cancelled or expired booking.
type CancellationFee struct {
	FeePercent int
	// Fee is FeePercent of RentTotal, rounded down to the đồng.
	Fee money.Money
	// Basis explains which rule applied, for the audit record.
	Basis string
}

// CancellationFee applies the snapshot cancellation policy (D8): a share of
// the rent by notice before pickup_at, the no-show rate, or the store-cancel
// rate. The security deposit is never part of the base. An expired hold or a
// cancellation for failed payment never secured the date and costs nothing
// (owner decision D16); nor does a booking cancelled before it was confirmed
// with no money received (owner decision D26).
func (b *Booking) CancellationFee(moneyReceived bool) (CancellationFee, error) {
	c := b.Policy.Cancellation
	var pct int
	var basis string
	switch {
	case b.Status == StatusExpired:
		pct, basis = 0, "hold expired"
	case b.Status != StatusCancelled:
		return CancellationFee{}, fmt.Errorf("booking %s cancellation fee from %s: %w", b.ID, b.Status, ErrInvalidTransition)
	case b.CancelReason == CancelPaymentFailed:
		pct, basis = 0, "payment failed"
	case b.ConfirmedAt == nil && !moneyReceived:
		pct, basis = 0, "cancelled unconfirmed and unpaid"
	case b.CancelReason == CancelNoShow:
		pct, basis = c.NoShowFeePercent, "no-show"
	case b.CancelReason == CancelStaffDecision:
		pct, basis = c.StoreCancelFeePercent, "store cancelled"
	case b.CancelReason == CancelCustomerRequest:
		notice := b.Schedule.PickupAt.Sub(*b.CancelledAt)
		tier := c.tierFor(notice)
		pct, basis = tier.FeePercent, fmt.Sprintf("customer cancelled %s before pickup (tier >= %s)", notice, tier.MinNotice)
	default:
		return CancellationFee{}, fmt.Errorf("booking %s cancel reason %q: %w", b.ID, b.CancelReason, ErrInvalidTransition)
	}
	fee, err := money.New(b.RentTotal.Amount()*int64(pct)/100, b.Currency)
	if err != nil {
		return CancellationFee{}, err
	}
	return CancellationFee{FeePercent: pct, Fee: fee, Basis: basis}, nil
}

// Confirm moves a pending booking to CONFIRMED when the hold is still valid
// and the snapshot policy's funds requirement is met.
func (b *Booking) Confirm(actor identity.Actor, funds Funds, now time.Time) (Transition, error) {
	if !actor.Valid() {
		return Transition{}, ErrActorRequired
	}
	if err := b.requireStatus(ActionConfirm, StatusPendingConfirmation); err != nil {
		return Transition{}, err
	}
	if !now.Before(b.HoldExpiresAt) {
		return Transition{}, fmt.Errorf("booking %s: %w", b.ID, ErrHoldExpired)
	}
	if actor.IsSystem() && b.Policy.Confirmation.Mode != ConfirmAuto {
		return Transition{}, fmt.Errorf("booking %s: %w", b.ID, ErrStaffConfirmationNeeded)
	}
	ok, err := b.Policy.Confirmation.Funds.SatisfiedBy(b.amounts(), funds)
	if err != nil {
		return Transition{}, fmt.Errorf("booking %s confirm: %w", b.ID, err)
	}
	if !ok {
		return Transition{}, fmt.Errorf("booking %s confirm: %w", b.ID, ErrInsufficientFunds)
	}
	b.ConfirmedAt = ptr(now)
	return b.transition(ActionConfirm, StatusConfirmed, actor, now, ""), nil
}

// StartPreparation marks staff picking and checking the items.
func (b *Booking) StartPreparation(actor identity.Actor, now time.Time) (Transition, error) {
	if !actor.IsUser() {
		return Transition{}, ErrActorRequired
	}
	if err := b.requireStatus(ActionStartPreparation, StatusConfirmed); err != nil {
		return Transition{}, err
	}
	b.PreparationStartedAt = ptr(now)
	return b.transition(ActionStartPreparation, StatusPreparing, actor, now, ""), nil
}

// CancelRequest carries who cancels and in which capacity.
type CancelRequest struct {
	Actor identity.Actor
	// Scope comes from authorization: ScopeOwn applies customer rules,
	// ScopeAny applies staff rules. System actors may only cancel for
	// PAYMENT_FAILED.
	Scope  identity.Scope
	Reason CancelReason
	Note   string
	Now    time.Time
}

// Cancel cancels before handover and asks for allocations to be released.
// A customer-requested cancellation is accepted until pickup_at plus the
// policy's NoShowAfter; after that only a no-show can be recorded. The fee
// is then computed by CancellationFee and settled in payments.
func (b *Booking) Cancel(req CancelRequest) (Transition, error) {
	if !req.Actor.Valid() {
		return Transition{}, ErrActorRequired
	}
	noShowAt := b.Schedule.PickupAt.Add(b.Policy.Cancellation.NoShowAfter)
	switch {
	case req.Actor.IsSystem():
		if req.Reason != CancelPaymentFailed {
			return Transition{}, fmt.Errorf("booking %s system cancel %s: %w", b.ID, req.Reason, ErrInvalidTransition)
		}
		if err := b.requireStatus(ActionCancel, StatusPendingConfirmation); err != nil {
			return Transition{}, err
		}
	case req.Scope == identity.ScopeOwn:
		if req.Reason != CancelCustomerRequest {
			return Transition{}, domainerr.Invalid("reason", "customers cancel with CUSTOMER_REQUEST")
		}
		if err := b.requireStatus(ActionCancel, StatusPendingConfirmation, StatusConfirmed, StatusPreparing); err != nil {
			return Transition{}, err
		}
		if !req.Now.Before(noShowAt) {
			return Transition{}, fmt.Errorf("booking %s: %w", b.ID, ErrCancelWindowClosed)
		}
	case req.Scope == identity.ScopeAny:
		if err := b.requireStatus(ActionCancel, StatusPendingConfirmation, StatusConfirmed, StatusPreparing); err != nil {
			return Transition{}, err
		}
		switch req.Reason {
		case CancelStaffDecision:
		case CancelPaymentFailed:
			// Costs nothing because the date was never secured (D16), so it
			// cannot be used on a confirmed booking.
			if err := b.requireStatus(ActionCancel, StatusPendingConfirmation); err != nil {
				return Transition{}, err
			}
		case CancelCustomerRequest:
			if !req.Now.Before(noShowAt) {
				return Transition{}, fmt.Errorf("booking %s: %w", b.ID, ErrCancelWindowClosed)
			}
		case CancelNoShow:
			if req.Now.Before(noShowAt) {
				return Transition{}, fmt.Errorf("booking %s: %w", b.ID, ErrNoShowTooEarly)
			}
		default:
			return Transition{}, domainerr.Invalid("reason", "unknown cancel reason")
		}
		if strings.TrimSpace(req.Note) == "" {
			return Transition{}, ErrReasonRequired
		}
	default:
		return Transition{}, domainerr.Invalid("scope", "required")
	}
	b.CancelledAt, b.CancelReason, b.CancelNote = ptr(req.Now), req.Reason, req.Note
	t := b.transition(ActionCancel, StatusCancelled, req.Actor, req.Now, string(req.Reason))
	t.ReleaseAllocations = true
	return t, nil
}

// Expire ends an unconfirmed hold after HoldExpiresAt. It is safe to run
// repeatedly and to race with Confirm: anything no longer pending is left
// untouched and reported as unchanged.
func (b *Booking) Expire(actor identity.Actor, now time.Time) (Transition, bool, error) {
	if !actor.Valid() {
		return Transition{}, false, ErrActorRequired
	}
	if b.Status != StatusPendingConfirmation {
		return Transition{}, false, nil
	}
	if now.Before(b.HoldExpiresAt) {
		return Transition{}, false, fmt.Errorf("booking %s: %w", b.ID, ErrHoldNotExpired)
	}
	b.ExpiredAt = ptr(now)
	t := b.transition(ActionExpire, StatusExpired, actor, now, "hold expired")
	t.ReleaseAllocations = true
	return t, true, nil
}

func sameSet(a, b []inventory.AssetID) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	if len(slices.Compact(slices.Clone(x))) != len(x) {
		return false
	}
	return slices.Equal(x, y)
}
