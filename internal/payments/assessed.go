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

// AddApprovedFee records a fee whose amount and approval were decided by a
// rentals.FeeAssessment. Recording the same assessment twice returns the
// existing charge (created=false).
func (l *Ledger) AddApprovedFee(id ChargeID, kind ChargeKind, amount money.Money, assessmentRef, basis string, evidence []string, approvedBy identity.Actor, now time.Time) (Charge, bool, error) {
	if strings.TrimSpace(assessmentRef) == "" {
		return Charge{}, false, domainerr.Invalid("assessment_ref", "required")
	}
	if i := slices.IndexFunc(l.Charges, func(c Charge) bool { return c.AssessmentRef == assessmentRef }); i >= 0 {
		c := l.Charges[i]
		if c.Kind != kind || !c.Amount.Equal(amount) {
			return Charge{}, false, ErrIdempotencyConflict
		}
		return c, false, nil
	}
	if !kind.assessed() {
		return Charge{}, false, domainerr.Invalid("kind", "only late, cleaning, damage, loss or other fees come from assessments")
	}
	if !approvedBy.Valid() {
		return Charge{}, false, ErrActorRequired
	}
	if strings.TrimSpace(basis) == "" {
		return Charge{}, false, ErrReasonRequired
	}
	if id == "" || slices.ContainsFunc(l.Charges, func(c Charge) bool { return c.ID == id }) {
		return Charge{}, false, domainerr.Invalid("charge_id", "required and unique")
	}
	if err := l.checkAmount(amount); err != nil {
		return Charge{}, false, err
	}
	c := Charge{
		ID: id, Kind: kind, Amount: amount, Reason: basis, Evidence: slices.Clone(evidence),
		CreatedBy: approvedBy, CreatedAt: now.UTC(), AssessmentRef: assessmentRef, ApprovedBy: &approvedBy,
	}
	l.Charges = append(l.Charges, c)
	return c, true, nil
}

// ApplySecurityToApprovedFees settles the security deposit against fees that
// were approved through assessments (fee policy §5):
// refund_due = max(0, security collected - approved deductions). Each
// uncovered fee gets an APPROVED deduction (id prefix + charge id) up to the
// free deposit and the rent-and-fees still owed; the fee's own approval is
// the deduction's approval. Running it again only covers what is new.
func (l *Ledger) ApplySecurityToApprovedFees(prefix string, actor identity.Actor, now time.Time) ([]Deduction, error) {
	if !actor.Valid() {
		return nil, ErrActorRequired
	}
	if strings.TrimSpace(prefix) == "" {
		return nil, domainerr.Invalid("prefix", "required")
	}
	var out []Deduction
	for _, c := range l.Charges {
		if c.ApprovedBy == nil || c.Reversal != nil || !c.Kind.assessed() {
			continue
		}
		id := DeductionID(prefix + ":" + string(c.ID))
		if slices.ContainsFunc(l.Deductions, func(d Deduction) bool { return d.ID == id }) {
			continue
		}
		var covered int64
		for _, d := range l.Deductions {
			if d.ChargeID == c.ID && d.Status != DeductionRejected {
				covered += d.Amount.Amount()
			}
		}
		s := l.Summary()
		amount := min(c.Amount.Amount()-covered, s.securityFree(), s.RentalOutstanding.Amount())
		if amount <= 0 {
			continue
		}
		d := Deduction{
			ID: id, ChargeID: c.ID, Amount: l.money(amount), Reason: c.Reason, Evidence: slices.Clone(c.Evidence),
			Status: DeductionApproved, ProposedBy: actor, ProposedAt: now.UTC(),
			DecidedBy: c.ApprovedBy, DecidedAt: ptr(now), DecisionNote: fmt.Sprintf("approved via assessment %s", c.AssessmentRef),
		}
		l.Deductions = append(l.Deductions, d)
		out = append(out, d)
	}
	return out, nil
}
