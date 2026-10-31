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

// DeductionStatus is the approval state of covering a fee from the deposit.
type DeductionStatus string

const (
	DeductionProposed DeductionStatus = "PROPOSED"
	DeductionApproved DeductionStatus = "APPROVED"
	DeductionRejected DeductionStatus = "REJECTED"
	// DeductionReversed gave an applied deduction back to the deposit because
	// its fee was waived or lowered. The record stays.
	DeductionReversed DeductionStatus = "REVERSED"
)

// counts reports whether the deduction covers its fee.
func (s DeductionStatus) counts() bool {
	return s == DeductionProposed || s == DeductionApproved
}

// Deduction applies part of the held security deposit to an assessed fee.
type Deduction struct {
	ID           DeductionID
	ChargeID     ChargeID
	Amount       money.Money
	Reason       string
	Evidence     []string
	Status       DeductionStatus
	ProposedBy   identity.Actor
	ProposedAt   time.Time
	DecidedBy    *identity.Actor
	DecidedAt    *time.Time
	DecisionNote string
	// Set when the deduction is reversed.
	ReversedBy     *identity.Actor
	ReversedAt     *time.Time
	ReversalReason string
}

// ProposeDeduction reserves deposit to cover a fee, pending approval. The
// amount cannot exceed the fee still uncovered or the deposit still free.
func (l *Ledger) ProposeDeduction(id DeductionID, chargeID ChargeID, amount money.Money, reason string, evidence []string, actor identity.Actor, now time.Time) (Deduction, error) {
	if !actor.IsUser() {
		return Deduction{}, ErrActorRequired
	}
	if id == "" || slices.ContainsFunc(l.Deductions, func(d Deduction) bool { return d.ID == id }) {
		return Deduction{}, domainerr.Invalid("deduction_id", "required and unique")
	}
	if err := l.checkAmount(amount); err != nil {
		return Deduction{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return Deduction{}, ErrReasonRequired
	}
	i := slices.IndexFunc(l.Charges, func(c Charge) bool { return c.ID == chargeID })
	if i < 0 {
		return Deduction{}, fmt.Errorf("charge %s: %w", chargeID, ErrNotFound)
	}
	c := l.Charges[i]
	if !c.Kind.assessed() || c.Reversal != nil {
		return Deduction{}, fmt.Errorf("charge %s: %w", chargeID, ErrNotDeductible)
	}
	var covered int64
	for _, d := range l.Deductions {
		if d.ChargeID == chargeID && d.Status.counts() {
			covered += d.Amount.Amount()
		}
	}
	if amount.Amount() > c.Amount.Amount()-covered {
		return Deduction{}, fmt.Errorf("charge %s remaining: %w", chargeID, ErrExceedsRefundable)
	}
	if amount.Amount() > l.Summary().securityFree() {
		return Deduction{}, fmt.Errorf("deposit available: %w", ErrExceedsRefundable)
	}
	d := Deduction{
		ID: id, ChargeID: chargeID, Amount: amount, Reason: reason, Evidence: slices.Clone(evidence),
		Status: DeductionProposed, ProposedBy: actor, ProposedAt: now.UTC(),
	}
	l.Deductions = append(l.Deductions, d)
	return d, nil
}

// ApproveDeduction applies the deduction, enforcing separation of duties.
func (l *Ledger) ApproveDeduction(id DeductionID, approver identity.Actor, policy ApprovalPolicy, now time.Time) error {
	d, err := l.deduction(id)
	if err != nil {
		return err
	}
	if d.Status != DeductionProposed {
		return fmt.Errorf("deduction %s %s: %w", id, d.Status, ErrInvalidTransition)
	}
	if err := policy.check(d.ProposedBy, approver, d.Amount); err != nil {
		return err
	}
	d.Status, d.DecidedBy, d.DecidedAt = DeductionApproved, &approver, ptr(now)
	return nil
}

// RejectDeduction frees the reserved deposit.
func (l *Ledger) RejectDeduction(id DeductionID, actor identity.Actor, note string, now time.Time) error {
	if !actor.IsUser() {
		return ErrActorRequired
	}
	if strings.TrimSpace(note) == "" {
		return ErrReasonRequired
	}
	d, err := l.deduction(id)
	if err != nil {
		return err
	}
	if d.Status != DeductionProposed {
		return fmt.Errorf("deduction %s %s: %w", id, d.Status, ErrInvalidTransition)
	}
	d.Status, d.DecidedBy, d.DecidedAt, d.DecisionNote = DeductionRejected, &actor, ptr(now), note
	return nil
}

// ReverseDeductions gives back to the deposit what it covered of a charge,
// for a fee being waived or lowered (owner decision D29). The records stay;
// the amount becomes free deposit again. It returns what was reversed.
func (l *Ledger) ReverseDeductions(chargeID ChargeID, reason string, actor identity.Actor, now time.Time) ([]Deduction, error) {
	if !actor.IsUser() {
		return nil, ErrActorRequired
	}
	if strings.TrimSpace(reason) == "" {
		return nil, ErrReasonRequired
	}
	var out []Deduction
	for i := range l.Deductions {
		d := &l.Deductions[i]
		if d.ChargeID == chargeID && d.Status == DeductionApproved {
			d.Status, d.ReversedBy, d.ReversedAt, d.ReversalReason = DeductionReversed, &actor, ptr(now), reason
			out = append(out, *d)
		}
	}
	return out, nil
}

func (l *Ledger) deduction(id DeductionID) (*Deduction, error) {
	i := slices.IndexFunc(l.Deductions, func(d Deduction) bool { return d.ID == id })
	if i < 0 {
		return nil, fmt.Errorf("deduction %s: %w", id, ErrNotFound)
	}
	return &l.Deductions[i], nil
}
