package rentalsapp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/money"
	"webee_rentor/internal/payments"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalspg"
)

// chargeKinds records each approved fee in the ledger (fee policy §5).
var chargeKinds = map[rentals.FeeKind]payments.ChargeKind{
	rentals.FeeLateReturn:        payments.ChargeLateFee,
	rentals.FeeCleaning:          payments.ChargeCleaningFee,
	rentals.FeeDamageRepair:      payments.ChargeDamageFee,
	rentals.FeeDamageReplacement: payments.ChargeLossFee,
	rentals.FeeLostItem:          payments.ChargeLossFee,
	rentals.FeeRedelivery:        payments.ChargeRedeliveryFee,
}

// assessLateFee records the policy-computed late fee of a resolved item, which
// needs no approval; on-time items record nothing.
func (s *Service) assessLateFee(ctx context.Context, q postgres.Querier, b *rentals.Booking, l *payments.Ledger, existing []rentals.FeeAssessment, asset inventory.AssetID, actor identity.Actor, now time.Time) error {
	it, err := b.Item(asset)
	if err != nil {
		return err
	}
	a, ok, err := b.AssessLateFee(existing, rentals.AssessmentID(s.newID()), asset, it.AffectedNext, actor, now)
	if err != nil || !ok {
		return err
	}
	if err := rentalspg.InsertAssessment(ctx, q, a); err != nil {
		return err
	}
	return s.chargeFee(ctx, q, l, a, now)
}

// chargeFee records an approved assessment in the ledger.
func (s *Service) chargeFee(ctx context.Context, q postgres.Querier, l *payments.Ledger, a rentals.FeeAssessment, now time.Time) error {
	c, created, err := l.AddApprovedFee(payments.ChargeID(s.newID()), chargeKinds[a.Kind], a.Amount, string(a.ID), a.Basis, a.Evidence, *a.DecidedBy, now)
	if err != nil || !created {
		return err
	}
	return s.auditMoney(ctx, q, "fee.charged", "fee_assessment", string(a.ID), l.BookingRef, c.Reason, c.Amount, *a.DecidedBy, now)
}

// reverseFee takes an assessment's charge off the ledger and gives back what
// the deposit covered of it. It reports whether any deduction was reversed,
// which means the money of this booking had already been closed.
func (s *Service) reverseFee(ctx context.Context, q postgres.Querier, l *payments.Ledger, id rentals.AssessmentID, note string, actor identity.Actor, now time.Time) (bool, error) {
	c, ok := l.ChargeOfAssessment(string(id))
	if !ok {
		return false, nil
	}
	reversed, err := l.ReverseDeductions(c.ID, note, actor, now)
	if err != nil {
		return false, err
	}
	for _, d := range reversed {
		if err := s.auditMoney(ctx, q, "deduction.reversed", "deduction", string(d.ID), l.BookingRef, note, d.Amount, actor, now); err != nil {
			return false, err
		}
	}
	return len(reversed) > 0, l.ReverseCharge(c.ID, note, actor, now)
}

// FeeInput proposes a cleaning, repair, replacement or loss fee. The amount
// comes from staff with its basis.
type FeeInput struct {
	Asset              inventory.AssetID
	Kind               rentals.FeeKind
	Amount             money.Money
	Basis              string
	Evidence           []string
	ConfirmedNewDamage bool
}

// ProposeFee records a fee awaiting a decision. Nothing is charged until it
// is approved.
func (s *Service) ProposeFee(ctx context.Context, p identity.Principal, id rentals.BookingID, in FeeInput) (rentals.FeeAssessment, error) {
	var out rentals.FeeAssessment
	_, err := s.withLedger(ctx, id, func(tx pgx.Tx, b *rentals.Booking, l *payments.Ledger, now time.Time) ([]rentals.Transition, error) {
		existing, err := rentalspg.AssessmentsOfBooking(ctx, tx, b.ID)
		if err != nil {
			return nil, err
		}
		a, err := b.ProposeFee(existing, rentals.ProposeFeeRequest{
			ID: rentals.AssessmentID(s.newID()), Asset: in.Asset, Kind: in.Kind, Amount: in.Amount, Basis: in.Basis,
			Evidence: in.Evidence, ConfirmedNewDamage: in.ConfirmedNewDamage,
		}, p, now)
		if err != nil {
			return nil, err
		}
		if err := rentalspg.InsertAssessment(ctx, tx, a); err != nil {
			return nil, err
		}
		out = a
		return nil, s.auditMoney(ctx, tx, "fee.proposed", "fee_assessment", string(a.ID), l.BookingRef, a.Basis, a.Amount, p.Actor(), now)
	})
	if err != nil {
		return rentals.FeeAssessment{}, err
	}
	return out, nil
}

// FeeDecision is what is decided about an assessment.
type FeeDecision string

const (
	FeeApprove FeeDecision = "APPROVE"
	FeeReject  FeeDecision = "REJECT"
	// FeeDispute records the customer contesting an approved fee; the charge
	// stays until the dispute is resolved.
	FeeDispute FeeDecision = "DISPUTE"
	FeeUphold  FeeDecision = "UPHOLD"
	FeeWaive   FeeDecision = "WAIVE"
)

// DecideFee applies a decision and updates the ledger with it: approving
// charges the fee and waiving reverses the charge, with what the deposit
// covered of it. A waiver also works on a completed booking (decision D29).
// The domain checks who may decide what, and the approval limit.
func (s *Service) DecideFee(ctx context.Context, p identity.Principal, id rentals.AssessmentID, decision FeeDecision, note string) (rentals.FeeAssessment, error) {
	return s.onFee(ctx, p, id, decision == FeeWaive, func(tx pgx.Tx, b *rentals.Booking, l *payments.Ledger, a *rentals.FeeAssessment, now time.Time) (bool, error) {
		switch decision {
		case FeeApprove:
			if err := a.Approve(b, p, now); err != nil {
				return false, err
			}
			return false, s.chargeFee(ctx, tx, l, *a, now)
		case FeeReject:
			return false, a.Reject(p, note, now)
		case FeeDispute:
			return false, a.Dispute(p, note, now)
		case FeeUphold:
			return false, a.UpholdDispute(p, note, now)
		case FeeWaive:
			if err := a.Waive(p, note, now); err != nil {
				return false, err
			}
			return s.reverseFee(ctx, tx, l, a.ID, note, p.Actor(), now)
		}
		return false, domainerr.Invalid("decision", "unknown fee decision")
	})
}

// AdjustFee lowers an approved or disputed fee: the old charge is reversed
// and the new, lower amount recorded under its own assessment. Like a waiver
// it also works on a completed booking.
func (s *Service) AdjustFee(ctx context.Context, p identity.Principal, id rentals.AssessmentID, amount money.Money, note string) (rentals.FeeAssessment, error) {
	var out rentals.FeeAssessment
	_, err := s.onFee(ctx, p, id, true, func(tx pgx.Tx, _ *rentals.Booking, l *payments.Ledger, a *rentals.FeeAssessment, now time.Time) (bool, error) {
		n, err := a.Adjust(rentals.AssessmentID(s.newID()), amount, p, note, now)
		if err != nil {
			return false, err
		}
		reopened, err := s.reverseFee(ctx, tx, l, a.ID, note, p.Actor(), now)
		if err != nil {
			return false, err
		}
		if err := rentalspg.InsertAssessment(ctx, tx, n); err != nil {
			return false, err
		}
		out = n
		return reopened, s.chargeFee(ctx, tx, l, n, now)
	})
	if err != nil {
		return rentals.FeeAssessment{}, err
	}
	return out, nil
}

// feeChange applies a decision and reports whether it reopened money that was
// already closed.
type feeChange func(tx pgx.Tx, b *rentals.Booking, l *payments.Ledger, a *rentals.FeeAssessment, now time.Time) (bool, error)

// onFee runs a decision with the assessment's booking and ledger locked. A
// finished booking takes no more decisions, except corrections on a completed
// one, which stay on the ledger as new records (decisions D13, D29). A decision
// that reopened closed money closes it again at once, unless another fee is
// still open.
func (s *Service) onFee(ctx context.Context, p identity.Principal, id rentals.AssessmentID, correction bool, fn feeChange) (rentals.FeeAssessment, error) {
	peek, err := rentalspg.LoadAssessment(ctx, s.pool, id)
	if err != nil {
		return rentals.FeeAssessment{}, err
	}
	var out rentals.FeeAssessment
	_, err = s.withLedger(ctx, peek.BookingID, func(tx pgx.Tx, b *rentals.Booking, l *payments.Ledger, now time.Time) ([]rentals.Transition, error) {
		if b.Status.IsTerminal() && !(correction && b.Status == rentals.StatusCompleted) {
			return nil, fmt.Errorf("fee %s on booking %s: %w", id, b.Status, rentals.ErrInvalidTransition)
		}
		a, err := rentalspg.LoadAssessment(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		reopened, err := fn(tx, b, l, &a, now)
		if err != nil {
			return nil, err
		}
		if err := rentalspg.UpdateAssessment(ctx, tx, a); err != nil {
			return nil, err
		}
		out = a
		reason := a.DecisionNote + a.DisputeNote
		if reopened || b.Status == rentals.StatusCompleted {
			if err := s.reclose(ctx, tx, b, l, reason, p.Actor(), now); err != nil {
				return nil, err
			}
		}
		return nil, s.auditMoney(ctx, tx, "fee."+strings.ToLower(string(a.Status)), "fee_assessment", string(a.ID), l.BookingRef, reason, a.Amount, p.Actor(), now)
	})
	if err != nil {
		return rentals.FeeAssessment{}, err
	}
	return out, nil
}

// reclose closes money a fee reversal reopened.
func (s *Service) reclose(ctx context.Context, q postgres.Querier, b *rentals.Booking, l *payments.Ledger, reason string, actor identity.Actor, now time.Time) error {
	fees, err := rentalspg.AssessmentsOfBooking(ctx, q, b.ID)
	if err != nil {
		return err
	}
	if rentals.CountOpenAssessments(fees) > 0 {
		return nil
	}
	return s.closeMoney(ctx, q, l, reason, actor, now)
}

// Fees lists a booking's assessments for whoever may read the booking.
func (s *Service) Fees(ctx context.Context, p identity.Principal, id rentals.BookingID) ([]rentals.FeeAssessment, error) {
	b, _, err := rentalspg.LoadBooking(ctx, s.pool, id, false)
	if err != nil {
		return nil, err
	}
	if _, err := rentals.Authorize(p, rentals.ActionRead, b); err != nil {
		return nil, err
	}
	return rentalspg.AssessmentsOfBooking(ctx, s.pool, id)
}
