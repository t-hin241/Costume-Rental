// Package payments owns the money side of a booking: charges, collected
// payments, refunds and security-deposit deductions. Every movement is its
// own record; history is never edited to "fix" numbers. Balances and the
// payment state are derived, never stored as flags like is_paid.
//
// The domain does not import any payment SDK. Signature verification and
// provider calls live in adapters, which hand verified events to the ledger.
package payments

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
)

type (
	ChargeID    string
	PaymentID   string
	RefundID    string
	DeductionID string
)

// Bucket separates money owed for using the costume (rent and fees), the
// refundable security deposit, and the reservation deposit that holds the
// date.
type Bucket string

const (
	BucketRental      Bucket = "RENTAL"
	BucketSecurity    Bucket = "SECURITY"
	BucketReservation Bucket = "RESERVATION"
)

func (b Bucket) valid() bool {
	return b == BucketRental || b == BucketSecurity || b == BucketReservation
}

// ChargeKind is what an amount is owed for.
type ChargeKind string

const (
	ChargeRent               ChargeKind = "RENT"
	ChargeSecurityDeposit    ChargeKind = "SECURITY_DEPOSIT"
	ChargeReservationDeposit ChargeKind = "RESERVATION_DEPOSIT"
	ChargeDeliveryFee        ChargeKind = "DELIVERY_FEE"
	// ChargeCancellationFee is a share of the rent charged on cancellation.
	// It is not deductible from the security deposit.
	ChargeCancellationFee ChargeKind = "CANCELLATION_FEE"
	ChargeLateFee         ChargeKind = "LATE_FEE"
	// ChargeRedeliveryFee is a customer-caused re-attempt fee (assessed).
	ChargeRedeliveryFee ChargeKind = "REDELIVERY_FEE"
	ChargeCleaningFee   ChargeKind = "CLEANING_FEE"
	ChargeDamageFee     ChargeKind = "DAMAGE_FEE"
	ChargeLossFee       ChargeKind = "LOSS_FEE"
	ChargeOtherFee      ChargeKind = "OTHER_FEE"
)

// Bucket returns which side of the ledger the charge belongs to.
func (k ChargeKind) Bucket() Bucket {
	switch k {
	case ChargeSecurityDeposit:
		return BucketSecurity
	case ChargeReservationDeposit:
		return BucketReservation
	}
	return BucketRental
}

// assessed fees arise after the fact and may be covered by the deposit.
func (k ChargeKind) assessed() bool {
	return k == ChargeLateFee || k == ChargeCleaningFee || k == ChargeDamageFee || k == ChargeLossFee ||
		k == ChargeOtherFee || k == ChargeRedeliveryFee
}

// DeliveryLeg names which courier leg a delivery fee pays for.
type DeliveryLeg string

const (
	LegOutbound DeliveryLeg = "OUTBOUND"
	LegReturn   DeliveryLeg = "RETURN"
)

var (
	ErrNotFound            = domainerr.New(domainerr.KindNotFound, "payments.not_found", "ledger record not found")
	ErrInvalidAmount       = domainerr.New(domainerr.KindInvalid, "payments.invalid_amount", "amount must be positive and in the ledger currency")
	ErrExceedsOutstanding  = domainerr.New(domainerr.KindConflict, "payments.exceeds_outstanding", "amount exceeds what is currently owed")
	ErrExceedsRefundable   = domainerr.New(domainerr.KindConflict, "payments.exceeds_refundable", "amount exceeds what can be refunded or deducted")
	ErrIdempotencyConflict = domainerr.New(domainerr.KindConflict, "payments.idempotency_conflict", "idempotency key was already used with different parameters")
	ErrInvalidTransition   = domainerr.New(domainerr.KindInvalidState, "payments.invalid_transition", "record cannot make this transition from its current status")
	ErrReasonRequired      = domainerr.New(domainerr.KindInvalid, "payments.reason_required", "a reason is required")
	ErrActorRequired       = domainerr.New(domainerr.KindInvalid, "payments.actor_required", "a user actor is required")
	ErrApproverMustDiffer  = domainerr.New(domainerr.KindForbidden, "payments.approver_must_differ", "this amount must be approved by someone other than the requester")
	ErrChargeHasDeductions = domainerr.New(domainerr.KindInvalidState, "payments.charge_has_deductions", "reject or keep the deposit deductions before reversing this charge")
	ErrNotDeductible       = domainerr.New(domainerr.KindInvalid, "payments.not_deductible", "only assessed late/damage/loss/other fees can be covered by the deposit")
	ErrProviderMismatch    = domainerr.New(domainerr.KindConflict, "payments.provider_mismatch", "event does not match the recorded provider, reference, amount or currency")
)

// Charge is an amount the customer owes. RENT and SECURITY_DEPOSIT come from
// the booking snapshot; fees are added later with a reason.
type Charge struct {
	ID        ChargeID
	Kind      ChargeKind
	Amount    money.Money
	Reason    string
	Evidence  []string // object-storage keys of photos/documents
	CreatedBy identity.Actor
	CreatedAt time.Time
	Reversal  *Reversal
	// AssessmentRef links a fee to the approved rentals.FeeAssessment it
	// records; ApprovedBy is that assessment's approver.
	AssessmentRef string
	ApprovedBy    *identity.Actor
	// DeliveryLeg is set on the booking-time delivery fees.
	DeliveryLeg DeliveryLeg
}

// Reversal waives a charge without deleting it.
type Reversal struct {
	Reason string
	By     identity.Actor
	At     time.Time
}

// ApprovalPolicy controls separation of duties for refunds and deductions.
// Store policy pending owner confirmation.
type ApprovalPolicy struct {
	// DistinctApproverAbove: amounts strictly above this need an approver
	// different from the requester. Zero means always.
	DistinctApproverAbove money.Money
}

func (p ApprovalPolicy) check(requester, approver identity.Actor, amount money.Money) error {
	if !approver.IsUser() {
		return ErrActorRequired
	}
	if approver == requester && amount.Amount() > p.DistinctApproverAbove.Amount() {
		return ErrApproverMustDiffer
	}
	return nil
}

// Ledger is the per-booking financial aggregate. The use case loads and
// saves it under a lock on the booking so concurrent operations serialize.
type Ledger struct {
	BookingRef string
	Currency   money.Currency
	Charges    []Charge
	Payments   []Payment
	Refunds    []Refund
	Deductions []Deduction
	// Resolutions record what became of the reservation deposit.
	Resolutions []ReservationResolution
	// CreditReservationToRent is fixed at opening from the booking snapshot:
	// the reservation deposit will be deducted from rent at handover rather
	// than refunded after return.
	CreditReservationToRent bool
	// Cancellation is set once the ledger is settled for a cancellation.
	Cancellation *Cancellation
	// Staged is the pending re-pricing of an open booking modification;
	// CommittedRepricings lists applied ones.
	Staged              *Repricing
	CommittedRepricings []string
	// RentReductions lists applied service-failure rent reductions.
	RentReductions []string
	// ProcessedEvents holds provider event keys already applied. Persistence
	// backs this with a unique constraint.
	ProcessedEvents map[string]bool
}

// OpenRequest carries the booking snapshot totals into a new ledger.
type OpenRequest struct {
	BookingRef  string
	Rent        money.Money
	Security    money.Money
	Reservation money.Money
	// Courier fees of each leg from the booking fulfillment snapshot.
	OutboundDeliveryFee money.Money
	ReturnDeliveryFee   money.Money
	// Charge IDs are required for every non-zero amount.
	RentChargeID             ChargeID
	SecurityChargeID         ChargeID
	ReservationChargeID      ChargeID
	OutboundDeliveryChargeID ChargeID
	ReturnDeliveryChargeID   ChargeID
	// CreditReservationToRent comes from rentals.Booking.ReservationCreditsRent.
	CreditReservationToRent bool
	Actor                   identity.Actor
	Now                     time.Time
}

// OpenLedger creates the ledger from the booking snapshot. Zero security or
// reservation amounts create no charge.
func OpenLedger(req OpenRequest) (*Ledger, error) {
	if req.BookingRef == "" {
		return nil, domainerr.Invalid("booking_ref", "required")
	}
	if !req.Actor.Valid() {
		return nil, ErrActorRequired
	}
	c := req.Rent.Currency()
	if !c.Valid() || !req.Rent.IsPositive() ||
		req.Security.Currency() != c || req.Security.IsNegative() ||
		req.Reservation.Currency() != c || req.Reservation.IsNegative() {
		return nil, ErrInvalidAmount
	}
	if req.CreditReservationToRent && req.Reservation.Amount() > req.Rent.Amount() {
		return nil, fmt.Errorf("reservation credited to rent exceeds rent: %w", ErrInvalidAmount)
	}
	l := &Ledger{
		BookingRef: req.BookingRef, Currency: c, ProcessedEvents: map[string]bool{},
		CreditReservationToRent: req.CreditReservationToRent && req.Reservation.IsPositive(),
	}
	add := func(id ChargeID, kind ChargeKind, amount money.Money) error {
		if !amount.Currency().Valid() && amount.IsZero() {
			return nil // optional amount left unset
		}
		if amount.Currency() != c || amount.IsNegative() {
			return ErrInvalidAmount
		}
		if !amount.IsPositive() {
			return nil
		}
		if id == "" || slices.ContainsFunc(l.Charges, func(ch Charge) bool { return ch.ID == id }) {
			return domainerr.Invalid("charge_id", "required and unique")
		}
		if err := l.checkAmount(amount); err != nil {
			return err
		}
		l.Charges = append(l.Charges, Charge{ID: id, Kind: kind, Amount: amount, Reason: "booking snapshot", CreatedBy: req.Actor, CreatedAt: req.Now.UTC()})
		return nil
	}
	if err := add(req.RentChargeID, ChargeRent, req.Rent); err != nil {
		return nil, err
	}
	if err := add(req.SecurityChargeID, ChargeSecurityDeposit, req.Security); err != nil {
		return nil, err
	}
	if err := add(req.ReservationChargeID, ChargeReservationDeposit, req.Reservation); err != nil {
		return nil, err
	}
	for _, leg := range []struct {
		id  ChargeID
		fee money.Money
		leg DeliveryLeg
	}{{req.OutboundDeliveryChargeID, req.OutboundDeliveryFee, LegOutbound}, {req.ReturnDeliveryChargeID, req.ReturnDeliveryFee, LegReturn}} {
		if err := add(leg.id, ChargeDeliveryFee, leg.fee); err != nil {
			return nil, err
		}
		if leg.fee.IsPositive() {
			l.Charges[len(l.Charges)-1].DeliveryLeg = leg.leg
		}
	}
	return l, nil
}

// AddFee records a delivery fee or an assessed late/damage/loss/other fee.
// Amounts are entered by staff: no automatic fee formula is approved yet.
func (l *Ledger) AddFee(id ChargeID, kind ChargeKind, amount money.Money, reason string, evidence []string, actor identity.Actor, now time.Time) (Charge, error) {
	if !actor.IsUser() {
		return Charge{}, ErrActorRequired
	}
	if id == "" || slices.ContainsFunc(l.Charges, func(c Charge) bool { return c.ID == id }) {
		return Charge{}, domainerr.Invalid("charge_id", "required and unique")
	}
	if !kind.assessed() && kind != ChargeDeliveryFee && kind != ChargeCancellationFee {
		return Charge{}, domainerr.Invalid("kind", "only fees can be added after booking")
	}
	if err := l.checkAmount(amount); err != nil {
		return Charge{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return Charge{}, ErrReasonRequired
	}
	c := Charge{ID: id, Kind: kind, Amount: amount, Reason: reason, Evidence: slices.Clone(evidence), CreatedBy: actor, CreatedAt: now.UTC()}
	l.Charges = append(l.Charges, c)
	return c, nil
}

// ReverseCharge waives a charge, e.g. rent after a cancellation approved for
// a refund. The original record stays.
func (l *Ledger) ReverseCharge(id ChargeID, reason string, actor identity.Actor, now time.Time) error {
	if !actor.IsUser() {
		return ErrActorRequired
	}
	if strings.TrimSpace(reason) == "" {
		return ErrReasonRequired
	}
	i := slices.IndexFunc(l.Charges, func(c Charge) bool { return c.ID == id })
	if i < 0 {
		return fmt.Errorf("charge %s: %w", id, ErrNotFound)
	}
	if l.Charges[i].Reversal != nil {
		return fmt.Errorf("charge %s already reversed: %w", id, ErrInvalidTransition)
	}
	if slices.ContainsFunc(l.Deductions, func(d Deduction) bool { return d.ChargeID == id && d.Status != DeductionRejected }) {
		return fmt.Errorf("charge %s: %w", id, ErrChargeHasDeductions)
	}
	l.Charges[i].Reversal = &Reversal{Reason: reason, By: actor, At: now.UTC()}
	return nil
}

// checkAmount validates a new entry and keeps the ledger's total volume (the
// sum of every recorded amount) within money.MaxAmount. Every derived balance
// is bounded by that volume, which is what makes l.money infallible.
func (l *Ledger) checkAmount(m money.Money) error {
	if m.Currency() != l.Currency || !m.IsPositive() {
		return ErrInvalidAmount
	}
	if m.Amount() > money.MaxAmount-l.volume() {
		return fmt.Errorf("ledger %s volume: %w", l.BookingRef, money.ErrOutOfRange)
	}
	return nil
}

func (l *Ledger) volume() int64 {
	var v int64
	for _, c := range l.Charges {
		v += c.Amount.Amount()
	}
	for _, p := range l.Payments {
		v += p.Amount.Amount()
	}
	for _, r := range l.Refunds {
		v += r.Amount.Amount()
	}
	for _, d := range l.Deductions {
		v += d.Amount.Amount()
	}
	for _, r := range l.Resolutions {
		v += r.Amount.Amount()
	}
	return v
}

// money wraps a derived balance. |v| never exceeds volume() <= MaxAmount and
// the currency was validated in OpenLedger, so New cannot fail here.
func (l *Ledger) money(v int64) money.Money {
	m, _ := money.New(v, l.Currency)
	return m
}
