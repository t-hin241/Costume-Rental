// Package rentals owns bookings: schedule, line snapshots, lifecycle and the
// rules that coordinate catalog quotes with inventory allocation.
package rentals

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/money"
)

// FundsRequirementKind is how much must be paid before a step may happen.
type FundsRequirementKind string

const (
	FundsNone FundsRequirementKind = "NONE"
	// FundsReservationDeposit requires the booking's reservation deposit
	// (tiền giữ lịch) to be held.
	FundsReservationDeposit  FundsRequirementKind = "RESERVATION_DEPOSIT"
	FundsFullRent            FundsRequirementKind = "FULL_RENT"
	FundsFullRentAndSecurity FundsRequirementKind = "FULL_RENT_AND_SECURITY"
)

// FundsRequirement is a configurable payment precondition.
type FundsRequirement struct {
	Kind FundsRequirementKind
}

func (r FundsRequirement) validate(field string) error {
	switch r.Kind {
	case FundsNone, FundsReservationDeposit, FundsFullRent, FundsFullRentAndSecurity:
		return nil
	}
	return domainerr.Invalid(field+".kind", "unknown requirement")
}

// Amounts are the booking's snapshot totals a requirement is measured against.
type Amounts struct {
	Rent        money.Money
	Security    money.Money
	Reservation money.Money
	// Delivery is the courier fees of both legs, collected with the rent.
	Delivery money.Money
}

// Funds is what the payment ledger reports for a booking.
type Funds struct {
	// RentalCovered is rent and fees paid, net of rental refunds, including
	// approved security deductions, reservation deposit already applied to
	// rent, and — when the booking credits the reservation to rent — the
	// reservation deposit still held (it will be applied at handover).
	RentalCovered money.Money
	// SecurityHeld is refundable security deposit currently held.
	SecurityHeld money.Money
	// ReservationHeld is reservation deposit held and not yet applied,
	// refunded or retained.
	ReservationHeld money.Money
}

// SatisfiedBy reports whether funds meet the requirement.
func (r FundsRequirement) SatisfiedBy(a Amounts, f Funds) (bool, error) {
	c := a.Rent.Currency()
	if f.RentalCovered.Currency() != c || f.SecurityHeld.Currency() != c || f.ReservationHeld.Currency() != c {
		return false, fmt.Errorf("funds: %w", money.ErrCurrencyMismatch)
	}
	switch r.Kind {
	case FundsReservationDeposit:
		return f.ReservationHeld.Amount() >= a.Reservation.Amount(), nil
	case FundsFullRent:
		return f.RentalCovered.Amount() >= a.Rent.Amount()+a.Delivery.Amount(), nil
	case FundsFullRentAndSecurity:
		return f.RentalCovered.Amount() >= a.Rent.Amount()+a.Delivery.Amount() && f.SecurityHeld.Amount() >= a.Security.Amount(), nil
	}
	return true, nil
}

// ReservationDepositKind is how the reservation deposit amount is set.
type ReservationDepositKind string

const (
	ReservationNone ReservationDepositKind = "NONE"
	// ReservationFixed is a fixed amount per booking.
	ReservationFixed ReservationDepositKind = "FIXED"
	// ReservationRentPercent is a share of the booking's rent, rounded up to
	// the nearest đồng (owner decision D6).
	ReservationRentPercent ReservationDepositKind = "RENT_PERCENT"
)

// ReservationTreatment is what happens to the reservation deposit when the
// rental goes ahead, as agreed at booking time.
type ReservationTreatment string

const (
	// ReservationAppliedToRent deducts it from the rent still owed; the
	// ledger applies it at handover.
	ReservationAppliedToRent ReservationTreatment = "APPLIED_TO_RENT"
	// ReservationRefundedAfterReturn treats it as a separate guarantee that
	// is refunded once the rental is over.
	ReservationRefundedAfterReturn ReservationTreatment = "REFUNDED_AFTER_RETURN"
)

// ReservationPolicy describes the reservation deposit (tiền giữ lịch): the
// amount a customer pays so the store holds the items for their date. On
// cancellation it is used, like any other money paid, to cover the
// cancellation fee (see CancellationPolicy).
type ReservationPolicy struct {
	Kind ReservationDepositKind
	// Amount applies to ReservationFixed.
	Amount money.Money
	// RentPercent applies to ReservationRentPercent, 1..100.
	RentPercent int
	Treatment   ReservationTreatment
}

func (p ReservationPolicy) validate() error {
	switch p.Kind {
	case ReservationNone:
		return nil
	case ReservationFixed:
		if !p.Amount.Currency().Valid() || !p.Amount.IsPositive() {
			return domainerr.Invalid("policy.reservation.amount", "must be positive in a supported currency")
		}
	case ReservationRentPercent:
		if p.RentPercent < 1 || p.RentPercent > 100 {
			return domainerr.Invalid("policy.reservation.rent_percent", "must be between 1 and 100")
		}
	default:
		return domainerr.Invalid("policy.reservation.kind", "unknown kind")
	}
	if p.Treatment != ReservationAppliedToRent && p.Treatment != ReservationRefundedAfterReturn {
		return domainerr.Invalid("policy.reservation.treatment", "unknown treatment")
	}
	return nil
}

// FeeTier charges FeePercent of the rent when a customer-requested
// cancellation happens at least MinNotice before pickup_at.
type FeeTier struct {
	MinNotice  time.Duration
	FeePercent int
}

// CancellationPolicy is the published cancellation fee, charged on the rent
// only — never on the security deposit (owner decision D8). It is
// snapshotted into each booking.
type CancellationPolicy struct {
	// Tiers apply to CUSTOMER_REQUEST cancellations. The tier with the
	// largest MinNotice not exceeding the notice applies; a zero-notice tier
	// is required and also covers cancellations made after pickup_at but
	// before the no-show deadline.
	Tiers []FeeTier
	// NoShowAfter is how long after pickup_at a customer who has neither
	// come nor cancelled is a no-show. Customers may cancel until then.
	NoShowAfter      time.Duration
	NoShowFeePercent int
	// StoreCancelFeePercent applies when the store cancels (STAFF_DECISION);
	// the owner set it to 0% (decision D17).
	StoreCancelFeePercent int
}

func (c CancellationPolicy) validate() error {
	if c.NoShowAfter < 0 {
		return domainerr.Invalid("policy.cancellation.no_show_after", "must not be negative")
	}
	if !validPercent(c.NoShowFeePercent) || !validPercent(c.StoreCancelFeePercent) {
		return domainerr.Invalid("policy.cancellation", "fee percents must be between 0 and 100")
	}
	seen := map[time.Duration]bool{}
	for _, t := range c.Tiers {
		if t.MinNotice < 0 || seen[t.MinNotice] || !validPercent(t.FeePercent) {
			return domainerr.Invalid("policy.cancellation.tiers", "notices must be unique and non-negative, percents 0..100")
		}
		seen[t.MinNotice] = true
	}
	if !seen[0] {
		return domainerr.Invalid("policy.cancellation.tiers", "a tier with zero notice is required")
	}
	return nil
}

// tierFor returns the tier for a notice; negative notice uses the zero tier.
func (c CancellationPolicy) tierFor(notice time.Duration) FeeTier {
	notice = max(notice, 0)
	best := FeeTier{MinNotice: -1}
	for _, t := range c.Tiers {
		if notice >= t.MinNotice && t.MinNotice > best.MinNotice {
			best = t
		}
	}
	return best
}

// amountFor computes the reservation deposit for a rent total. When it is
// applied to rent it can never exceed the rent itself (owner decision D16).
func (p ReservationPolicy) amountFor(rent money.Money) (money.Money, error) {
	var v int64
	switch p.Kind {
	case ReservationFixed:
		if p.Amount.Currency() != rent.Currency() {
			return money.Money{}, fmt.Errorf("reservation deposit: %w", money.ErrCurrencyMismatch)
		}
		v = p.Amount.Amount()
	case ReservationRentPercent:
		v = (rent.Amount()*int64(p.RentPercent) + 99) / 100
	}
	if p.Treatment == ReservationAppliedToRent {
		v = min(v, rent.Amount())
	}
	return money.New(v, rent.Currency())
}

func validPercent(p int) bool { return p >= 0 && p <= 100 }

// ConfirmMode decides who may confirm a pending booking.
type ConfirmMode string

const (
	// ConfirmByStaff requires a user with booking.confirm.
	ConfirmByStaff ConfirmMode = "STAFF"
	// ConfirmAuto lets the system confirm as soon as funds are met; staff can
	// still confirm.
	ConfirmAuto ConfirmMode = "AUTO"
)

// ConfirmationRule is the owner-selected PENDING_CONFIRMATION → CONFIRMED
// precondition.
type ConfirmationRule struct {
	Mode  ConfirmMode
	Funds FundsRequirement
}

// Buffer is the inventory block around a rental: preparation before pickup
// and turnaround (return, inspection, cleaning) after due.
type Buffer struct {
	Preparation time.Duration
	Turnaround  time.Duration
}

// BufferPolicy selects buffers by catalog buffer class.
type BufferPolicy struct {
	Default Buffer
	ByClass map[string]Buffer
}

// For returns the buffer for a class, falling back to Default.
func (b BufferPolicy) For(class string) Buffer {
	if v, ok := b.ByClass[class]; ok {
		return v
	}
	return b.Default
}

// Policy holds every commercial/operational rule a booking depends on. There
// are deliberately no built-in defaults: values come from owner-approved
// configuration, and each booking stores a snapshot with Version.
type Policy struct {
	Version string
	// HoldTTL is how long a PENDING_CONFIRMATION booking keeps its
	// allocations before it expires.
	HoldTTL time.Duration
	// MinLeadTime is the minimum notice between booking and pickup.
	MinLeadTime time.Duration
	// MaxAdvance is how far ahead pickup may be booked.
	MaxAdvance         time.Duration
	MaxLinesPerBooking int
	MaxQuantityPerLine int
	Confirmation       ConfirmationRule
	// Handover is what must be paid before items leave the store.
	Handover FundsRequirement
	// CustomerRescheduleCutoff: customers may self-reschedule only until
	// pickup minus this duration.
	CustomerRescheduleCutoff time.Duration
	Buffers                  BufferPolicy
	// Reservation is the reservation deposit, as agreed when booking.
	Reservation ReservationPolicy
	// Cancellation is the cancellation fee policy published to customers.
	// Customers may self-cancel until pickup_at + Cancellation.NoShowAfter.
	Cancellation CancellationPolicy
	// LateFee, Loss and FeeApproval implement the fee policy (decision D9).
	LateFee     LateFeePolicy
	Loss        LossPolicy
	FeeApproval FeeApprovalPolicy
	// Modification governs size, bundle and package changes (decision D10).
	Modification ModificationPolicy
	// Delivery adds courier transit to inventory blocks (decision D19).
	Delivery DeliveryPolicy
}

// DeliveryPolicy holds courier timing buffers; values come from the real
// route and carrier SLA, never from code.
type DeliveryPolicy struct {
	// OutboundLead: the parcel leaves the store this long before the
	// delivery window starts.
	OutboundLead time.Duration
	// ReturnTransit: time from the end of the pickup window until the parcel
	// is back at the store.
	ReturnTransit time.Duration
	// ComplaintWindow: how long after delivery the customer may report a
	// missing, wrong or damaged item (decision D20).
	ComplaintWindow time.Duration
}

// ModificationPolicy is the booking change policy
// (docs/note/modification-repricing-policy.md).
type ModificationPolicy struct {
	// SelfServiceCutoff: customers may change a booking themselves only
	// while pickup is at least this far away; later requests go to staff.
	SelfServiceCutoff time.Duration
	// SelfServiceSizeChanges is how many size exchanges a customer may make
	// online before handover; further ones need staff. No fee is charged.
	SelfServiceSizeChanges int
	// HoldTTL is how long a quoted change keeps its tentative allocations
	// while waiting for acceptance or payment.
	HoldTTL time.Duration
}

// Validate checks that every field is set to a usable value.
func (p Policy) Validate() error {
	switch {
	case strings.TrimSpace(p.Version) == "":
		return domainerr.Invalid("policy.version", "required")
	case p.HoldTTL <= 0:
		return domainerr.Invalid("policy.hold_ttl", "must be positive")
	case p.MinLeadTime < 0:
		return domainerr.Invalid("policy.min_lead_time", "must not be negative")
	case p.MaxAdvance <= p.MinLeadTime:
		return domainerr.Invalid("policy.max_advance", "must be greater than min lead time")
	case p.MaxLinesPerBooking < 1:
		return domainerr.Invalid("policy.max_lines_per_booking", "must be at least 1")
	case p.MaxQuantityPerLine < 1:
		return domainerr.Invalid("policy.max_quantity_per_line", "must be at least 1")
	case p.Confirmation.Mode != ConfirmByStaff && p.Confirmation.Mode != ConfirmAuto:
		return domainerr.Invalid("policy.confirmation.mode", "unknown mode")
	case p.Confirmation.Mode == ConfirmAuto && p.Confirmation.Funds.Kind == FundsNone:
		return domainerr.Invalid("policy.confirmation", "auto confirmation needs a funds requirement")
	case p.CustomerRescheduleCutoff < 0:
		return domainerr.Invalid("policy.customer_reschedule_cutoff", "must not be negative")
	case p.Loss.VerificationWindow <= 0:
		return domainerr.Invalid("policy.loss.verification_window", "must be positive")
	case !p.FeeApproval.ManagerApprovalAbove.Currency().Valid() || p.FeeApproval.ManagerApprovalAbove.IsNegative():
		return domainerr.Invalid("policy.fee_approval.manager_approval_above", "must be zero or positive in a supported currency")
	}
	if err := p.LateFee.validate(); err != nil {
		return err
	}
	if p.Delivery.OutboundLead < 0 || p.Delivery.ReturnTransit < 0 || p.Delivery.ComplaintWindow < 0 {
		return domainerr.Invalid("policy.delivery", "buffers must not be negative")
	}
	if p.Modification.SelfServiceCutoff < 0 || p.Modification.SelfServiceSizeChanges < 0 || p.Modification.HoldTTL <= 0 {
		return domainerr.Invalid("policy.modification", "cutoff and size-change allowance must not be negative; hold TTL must be positive")
	}
	if err := p.Cancellation.validate(); err != nil {
		return err
	}
	if err := p.Confirmation.Funds.validate("policy.confirmation.funds"); err != nil {
		return err
	}
	if err := p.Handover.validate("policy.handover"); err != nil {
		return err
	}
	if err := p.Reservation.validate(); err != nil {
		return err
	}
	if p.Reservation.Kind == ReservationNone &&
		(p.Confirmation.Funds.Kind == FundsReservationDeposit || p.Handover.Kind == FundsReservationDeposit) {
		return domainerr.Invalid("policy.reservation", "a reservation deposit requirement needs a reservation deposit")
	}
	for class, b := range p.Buffers.ByClass {
		if b.Preparation < 0 || b.Turnaround < 0 {
			return domainerr.Invalid("policy.buffers."+class, "must not be negative")
		}
	}
	if p.Buffers.Default.Preparation < 0 || p.Buffers.Default.Turnaround < 0 {
		return domainerr.Invalid("policy.buffers.default", "must not be negative")
	}
	return nil
}

func (p Policy) clone() Policy {
	p.Buffers.ByClass = maps.Clone(p.Buffers.ByClass)
	p.Cancellation.Tiers = slices.Clone(p.Cancellation.Tiers)
	p.LateFee.Tiers = slices.Clone(p.LateFee.Tiers)
	return p
}

// LateFeeTier charges Percent of the item's daily rate when the lateness is
// at most UpTo (and above the previous tier).
type LateFeeTier struct {
	UpTo    time.Duration
	Percent int
}

// LateFeePolicy is the late-return fee (fee policy §2). Lateness is measured
// per item from due_at to its actual return (or loss confirmation).
type LateFeePolicy struct {
	// Grace is free lateness. With GraceRequiresNoConflict it is free only if
	// the late return did not affect the next booking; otherwise the first
	// tier applies.
	Grace                   time.Duration
	GraceRequiresNoConflict bool
	// Tiers ascend by UpTo; the first starts right after Grace.
	Tiers []LateFeeTier
	// Beyond the last tier, ExtraPercent is added for every ExtraPeriod or
	// part of it.
	ExtraPeriod  time.Duration
	ExtraPercent int
}

func (p LateFeePolicy) validate() error {
	if p.Grace < 0 || p.ExtraPeriod <= 0 || p.ExtraPercent < 0 || len(p.Tiers) == 0 {
		return domainerr.Invalid("policy.late_fee", "grace >= 0, extra period > 0, extra percent >= 0 and at least one tier are required")
	}
	prev := p.Grace
	for _, t := range p.Tiers {
		if t.UpTo <= prev || t.Percent < 0 {
			return domainerr.Invalid("policy.late_fee.tiers", "tiers must ascend above the grace period with non-negative percents")
		}
		prev = t.UpTo
	}
	return nil
}

// percentFor returns the share of the daily rate owed for a lateness.
func (p LateFeePolicy) percentFor(late time.Duration, affectedNext bool) int {
	switch {
	case late <= 0:
		return 0
	case late <= p.Grace:
		if p.GraceRequiresNoConflict && affectedNext {
			return p.Tiers[0].Percent
		}
		return 0
	}
	for _, t := range p.Tiers {
		if late <= t.UpTo {
			return t.Percent
		}
	}
	last := p.Tiers[len(p.Tiers)-1]
	periods := (late - last.UpTo + p.ExtraPeriod - 1) / p.ExtraPeriod
	return last.Percent + int(periods)*p.ExtraPercent
}

// LossPolicy governs missing items (fee policy §4).
type LossPolicy struct {
	// VerificationWindow is how long a missing item stays MISSING_PENDING
	// for the customer to find or return it before a loss can be confirmed.
	VerificationWindow time.Duration
}

// FeeApprovalPolicy sets who may approve discretionary fees.
type FeeApprovalPolicy struct {
	// ManagerApprovalAbove: fees strictly above this need fee.approve_high
	// and an approver other than the proposer.
	ManagerApprovalAbove money.Money
}
