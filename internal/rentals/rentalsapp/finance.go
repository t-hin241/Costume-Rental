package rentalsapp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/audit"
	"webee_rentor/internal/audit/auditpg"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
	"webee_rentor/internal/payments"
	"webee_rentor/internal/payments/paymentspg"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalspg"
)

// fundsOf is what the ledger reports to the booking's funds requirements.
func fundsOf(l *payments.Ledger) rentals.Funds {
	s := l.Summary()
	return rentals.Funds{RentalCovered: s.RentalCoveredWithCredit, SecurityHeld: s.SecurityHeld, ReservationHeld: s.ReservationHeld}
}

// openLedger creates the booking's ledger from its snapshot.
func (s *Service) openLedger(ctx context.Context, q postgres.Querier, b *rentals.Booking, actor identity.Actor, now time.Time) error {
	id := func() payments.ChargeID { return payments.ChargeID(s.newID()) }
	l, err := payments.OpenLedger(payments.OpenRequest{
		BookingRef: string(b.ID), Rent: b.RentTotal, Security: b.SecurityDepositTotal, Reservation: b.ReservationDeposit,
		OutboundDeliveryFee: b.Fulfillment.OutboundFee, ReturnDeliveryFee: b.Fulfillment.ReturnFee,
		RentChargeID: id(), SecurityChargeID: id(), ReservationChargeID: id(),
		OutboundDeliveryChargeID: id(), ReturnDeliveryChargeID: id(),
		CreditReservationToRent: b.ReservationCreditsRent(), Actor: actor, Now: now,
	})
	if err != nil {
		return fmt.Errorf("open ledger of booking %s: %w", b.ID, err)
	}
	_, err = paymentspg.SaveLedger(ctx, q, l, 0, now)
	return err
}

// settleCancellation closes the ledger of a cancelled or expired booking in
// the transaction of its transition: pending payments stop being waited for,
// the fee is charged and everything refundable goes to a refund awaiting
// approval. A fee still owed stays on the ledger; it is requested from the
// customer, never charged automatically.
func (s *Service) settleCancellation(ctx context.Context, q postgres.Querier, b *rentals.Booking, actor identity.Actor, now time.Time) error {
	l, lv, err := paymentspg.LoadLedger(ctx, q, string(b.ID), true)
	if err != nil {
		return err
	}
	fee, err := b.CancellationFee(l.Summary().TotalReceived.IsPositive())
	if err != nil {
		return err
	}
	if l.Staged != nil {
		l.DiscardRepricing(l.Staged.Ref)
	}
	l.ExpirePendingPayments(now)
	res, _, err := l.SettleCancellation(payments.CancellationRequest{
		Fee: fee.Fee, Basis: fee.Basis, FeeChargeID: payments.ChargeID(s.newID()),
		ResolutionID: payments.ResolutionID(s.newID()), Actor: actor, Now: now,
	})
	if err != nil {
		return err
	}
	reason := "booking cancelled: " + fee.Basis
	for _, r := range []struct {
		bucket payments.Bucket
		amount money.Money
	}{
		{payments.BucketRental, res.RefundableRental},
		{payments.BucketReservation, res.RefundableReservation},
		{payments.BucketSecurity, res.RefundableSecurity},
	} {
		if err := s.requestRefund(ctx, q, l, r.bucket, r.amount, reason, actor, now); err != nil {
			return err
		}
	}
	if _, err := paymentspg.SaveLedger(ctx, q, l, lv, now); err != nil {
		return err
	}
	return s.auditMoney(ctx, q, "ledger.cancellation_settled", "booking", string(b.ID), l.BookingRef, fee.Basis, res.Owed, actor, now)
}

// requestRefund starts a refund awaiting approval; a zero amount does nothing.
func (s *Service) requestRefund(ctx context.Context, q postgres.Querier, l *payments.Ledger, bucket payments.Bucket, amount money.Money, reason string, actor identity.Actor, now time.Time) error {
	if !amount.IsPositive() {
		return nil
	}
	id := payments.RefundID(s.newID())
	if _, _, err := l.RequestRefund(id, string(id), bucket, amount, reason, actor, now); err != nil {
		return err
	}
	return s.auditMoney(ctx, q, "refund.requested", "refund", string(id), l.BookingRef, reason, amount, actor, now)
}

// refundExcess refunds what was paid above the booking's current terms, the
// security deposit it still needs excepted.
func (s *Service) refundExcess(ctx context.Context, q postgres.Querier, l *payments.Ledger, reason string, actor identity.Actor, now time.Time) error {
	if err := s.requestRefund(ctx, q, l, payments.BucketRental, l.Refundable(payments.BucketRental), reason, actor, now); err != nil {
		return err
	}
	return s.requestRefund(ctx, q, l, payments.BucketSecurity, l.ExcessSecurity(), reason, actor, now)
}

// auditMoney records a financial action with the booking it belongs to.
func (s *Service) auditMoney(ctx context.Context, q postgres.Querier, action, resourceType, resourceID, bookingRef, reason string, amount money.Money, actor identity.Actor, at time.Time) error {
	return auditpg.Append(ctx, q, audit.Event{
		ID: s.newID(), OccurredAt: at, Actor: actor, Action: action,
		ResourceType: resourceType, ResourceID: resourceID, Result: audit.ResultSuccess, Reason: reason,
		References: map[string]string{"booking_id": bookingRef, "amount": amount.String()},
	})
}

// ledgerChange is the body of a money use case, run with the booking and its
// ledger locked. Transitions it returns are recorded and the booking saved.
type ledgerChange func(tx pgx.Tx, b *rentals.Booking, l *payments.Ledger, now time.Time) ([]rentals.Transition, error)

func (s *Service) withLedger(ctx context.Context, id rentals.BookingID, fn ledgerChange) (*payments.Ledger, error) {
	var out *payments.Ledger
	var changed *rentals.Booking
	var done []rentals.Transition
	err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		b, bv, err := rentalspg.LoadBooking(ctx, tx, id, true)
		if err != nil {
			return err
		}
		l, lv, err := paymentspg.LoadLedger(ctx, tx, string(id), true)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		ts, err := fn(tx, b, l, now)
		if err != nil {
			return err
		}
		if _, err := paymentspg.SaveLedger(ctx, tx, l, lv, now); err != nil {
			return err
		}
		if len(ts) > 0 {
			if _, err := rentalspg.SaveBooking(ctx, tx, b, bv, "", now); err != nil {
				return err
			}
			if err := s.record(ctx, tx, ts...); err != nil {
				return err
			}
		}
		out, changed, done = l, b, ts
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("ledger of booking %s: %w", id, err)
	}
	s.tell(ctx, changed, done)
	return out, nil
}

// PaymentInput is money taken at the counter. Staff say what it pays for; the
// ledger refuses anything above what is owed.
type PaymentInput struct {
	// IdempotencyKey makes a retried submission record the payment only once.
	IdempotencyKey string
	Method         payments.Method
	Portions       []payments.Portion
	// ModificationRef collects the increase of a quoted booking change.
	ModificationRef rentals.ModificationID
}

// RecordPayment records cash or a verified bank transfer. Under automatic
// confirmation a pending booking that now has its funds is confirmed in the
// same transaction.
func (s *Service) RecordPayment(ctx context.Context, p identity.Principal, id rentals.BookingID, in PaymentInput) (payments.Payment, error) {
	if err := p.Require(identity.PermPaymentRecord); err != nil {
		return payments.Payment{}, err
	}
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		return payments.Payment{}, domainerr.Invalid("idempotency_key", "required")
	}
	// Stored portions come back ordered by bucket, so a retry must compare equal.
	portions := slices.Clone(in.Portions)
	slices.SortFunc(portions, func(a, b payments.Portion) int { return strings.Compare(string(a.Bucket), string(b.Bucket)) })
	var out payments.Payment
	_, err := s.withLedger(ctx, id, func(tx pgx.Tx, b *rentals.Booking, l *payments.Ledger, now time.Time) ([]rentals.Transition, error) {
		pay, created, err := l.RecordCounterPayment(payments.NewPaymentRequest{
			ID: payments.PaymentID(s.newID()), IdempotencyKey: string(id) + ":" + in.IdempotencyKey,
			Method: in.Method, Portions: portions, ModificationRef: string(in.ModificationRef), Actor: p.Actor(), Now: now,
		})
		if err != nil {
			return nil, err
		}
		out = pay
		if !created {
			return nil, nil
		}
		if err := s.auditMoney(ctx, tx, "payment.recorded", "payment", string(pay.ID), l.BookingRef, string(pay.Method), pay.Amount, p.Actor(), now); err != nil {
			return nil, err
		}
		return autoConfirm(b, l, now)
	})
	if err != nil {
		return payments.Payment{}, err
	}
	return out, nil
}

// autoConfirm confirms a pending booking the policy lets the system confirm.
// Funds still short, or a hold that ran out, leave the payment recorded and
// the booking pending; the expiry job then refunds what was paid.
func autoConfirm(b *rentals.Booking, l *payments.Ledger, now time.Time) ([]rentals.Transition, error) {
	if b.Status != rentals.StatusPendingConfirmation || b.Policy.Confirmation.Mode != rentals.ConfirmAuto {
		return nil, nil
	}
	t, err := b.Confirm(jobActor, fundsOf(l), now)
	if errors.Is(err, rentals.ErrInsufficientFunds) || errors.Is(err, rentals.ErrHoldExpired) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []rentals.Transition{t}, nil
}

// RefundInput asks for a refund outside the automatic ones.
type RefundInput struct {
	IdempotencyKey string
	Bucket         payments.Bucket
	Amount         money.Money
	Reason         string
}

// RequestRefund creates a refund awaiting approval. While the booking is live
// only what it no longer needs can go: the deposit and the reservation still
// back fees not assessed yet, so closing them is SettleRental's job.
func (s *Service) RequestRefund(ctx context.Context, p identity.Principal, id rentals.BookingID, in RefundInput) (payments.Refund, error) {
	if err := p.Require(identity.PermRefundCreate); err != nil {
		return payments.Refund{}, err
	}
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		return payments.Refund{}, domainerr.Invalid("idempotency_key", "required")
	}
	var out payments.Refund
	_, err := s.withLedger(ctx, id, func(tx pgx.Tx, b *rentals.Booking, l *payments.Ledger, now time.Time) ([]rentals.Transition, error) {
		if !b.Status.IsTerminal() && in.Bucket != payments.BucketRental && in.Amount.Amount() > l.ExcessSecurity().Amount() {
			return nil, fmt.Errorf("refund %s on a live booking: %w", in.Bucket, payments.ErrExceedsRefundable)
		}
		r, created, err := l.RequestRefund(payments.RefundID(s.newID()), string(id)+":"+in.IdempotencyKey, in.Bucket, in.Amount, in.Reason, p.Actor(), now)
		if err != nil {
			return nil, err
		}
		out = r
		if !created {
			return nil, nil
		}
		return nil, s.auditMoney(ctx, tx, "refund.requested", "refund", string(r.ID), l.BookingRef, r.Reason, r.Amount, p.Actor(), now)
	})
	if err != nil {
		return payments.Refund{}, err
	}
	return out, nil
}

// ApproveRefund approves a requested refund under the active approval policy:
// above its limit the approver must not be the requester.
func (s *Service) ApproveRefund(ctx context.Context, p identity.Principal, id rentals.BookingID, refund payments.RefundID) (*payments.Ledger, error) {
	if err := p.Require(identity.PermRefundApprove); err != nil {
		return nil, err
	}
	return s.withLedger(ctx, id, func(tx pgx.Tx, _ *rentals.Booking, l *payments.Ledger, now time.Time) ([]rentals.Transition, error) {
		var policy payments.ApprovalPolicy
		if _, err := postgres.ActiveDocument(ctx, tx, postgres.DocPaymentApproval, &policy); err != nil {
			return nil, err
		}
		if err := l.ApproveRefund(refund, p.Actor(), policy, now); err != nil {
			return nil, err
		}
		return nil, s.auditRefund(ctx, tx, l, refund, "refund.approved", "", p.Actor(), now)
	})
}

// RejectRefund declines a refund and frees its amount.
func (s *Service) RejectRefund(ctx context.Context, p identity.Principal, id rentals.BookingID, refund payments.RefundID, note string) (*payments.Ledger, error) {
	if err := p.Require(identity.PermRefundApprove); err != nil {
		return nil, err
	}
	return s.withLedger(ctx, id, func(tx pgx.Tx, _ *rentals.Booking, l *payments.Ledger, now time.Time) ([]rentals.Transition, error) {
		if err := l.RejectRefund(refund, p.Actor(), note, now); err != nil {
			return nil, err
		}
		return nil, s.auditRefund(ctx, tx, l, refund, "refund.rejected", note, p.Actor(), now)
	})
}

// RecordCounterRefund records money handed back in cash or by manual transfer.
// It may be entered after the fact: PaidAt is when the money moved, and leaving
// it empty means now. Neither cash nor a transfer needs more than this
// confirmation; a transfer must name its bank transaction.
func (s *Service) RecordCounterRefund(ctx context.Context, p identity.Principal, id rentals.BookingID, refund payments.RefundID, in payments.RefundPayout) (*payments.Ledger, error) {
	if err := p.Require(identity.PermPaymentRecord); err != nil {
		return nil, err
	}
	return s.withLedger(ctx, id, func(tx pgx.Tx, _ *rentals.Booking, l *payments.Ledger, now time.Time) ([]rentals.Transition, error) {
		if in.PaidAt.IsZero() {
			in.PaidAt = now
		}
		if err := l.RecordCounterRefund(refund, in, p.Actor(), now); err != nil {
			return nil, err
		}
		return nil, s.auditRefund(ctx, tx, l, refund, "refund.completed", string(in.Method)+" "+in.Reference, p.Actor(), now)
	})
}

func (s *Service) auditRefund(ctx context.Context, q postgres.Querier, l *payments.Ledger, id payments.RefundID, action, reason string, actor identity.Actor, now time.Time) error {
	i := slices.IndexFunc(l.Refunds, func(r payments.Refund) bool { return r.ID == id })
	return s.auditMoney(ctx, q, action, "refund", string(id), l.BookingRef, reason, l.Refunds[i].Amount, actor, now)
}

// SettleRental closes the money of a returned rental once inspection is done
// and no fee is open: the deposit covers approved fees and what is left of
// each bucket goes to a refund awaiting approval. Running it again only adds
// what became refundable since. Any amount still owed stays on the ledger for
// the customer to pay; completion waits for it.
func (s *Service) SettleRental(ctx context.Context, p identity.Principal, id rentals.BookingID) (*payments.Ledger, error) {
	if err := p.Require(identity.PermRefundCreate); err != nil {
		return nil, err
	}
	return s.withLedger(ctx, id, func(tx pgx.Tx, b *rentals.Booking, l *payments.Ledger, now time.Time) ([]rentals.Transition, error) {
		fees, err := rentalspg.AssessmentsOfBooking(ctx, tx, b.ID)
		if err != nil {
			return nil, err
		}
		if err := b.ReadyToSettle(fees); err != nil {
			return nil, err
		}
		return nil, s.closeMoney(ctx, tx, l, "rental settled", p.Actor(), now)
	})
}

// closeMoney applies the deposit to approved fees and starts a refund for what
// is left of each bucket.
func (s *Service) closeMoney(ctx context.Context, q postgres.Querier, l *payments.Ledger, reason string, actor identity.Actor, now time.Time) error {
	deductions, err := l.ApplySecurityToApprovedFees("settle", actor, now)
	if err != nil {
		return err
	}
	for _, d := range deductions {
		if err := s.auditMoney(ctx, q, "deduction.applied", "deduction", string(d.ID), l.BookingRef, d.Reason, d.Amount, actor, now); err != nil {
			return err
		}
	}
	for _, bucket := range []payments.Bucket{payments.BucketRental, payments.BucketReservation, payments.BucketSecurity} {
		if err := s.requestRefund(ctx, q, l, bucket, l.Refundable(bucket), reason, actor, now); err != nil {
			return err
		}
	}
	return nil
}

// Complete closes a booking whose items are inspected, whose fees are decided
// and whose ledger is settled.
func (s *Service) Complete(ctx context.Context, p identity.Principal, id rentals.BookingID) (*rentals.Booking, error) {
	return s.mutate(ctx, p, id, rentals.ActionComplete, func(tx pgx.Tx, b *rentals.Booking, _ identity.Scope, now time.Time) ([]rentals.Transition, error) {
		l, _, err := paymentspg.LoadLedger(ctx, tx, string(b.ID), false)
		if err != nil {
			return nil, err
		}
		fees, err := rentalspg.AssessmentsOfBooking(ctx, tx, b.ID)
		if err != nil {
			return nil, err
		}
		t, err := b.Complete(p.Actor(), rentals.CompletionCheck{
			LedgerSettled: l.Summary().IsSettled(), OpenFeeAssessments: rentals.CountOpenAssessments(fees),
		}, now)
		return []rentals.Transition{t}, err
	})
}

// Ledger reads a booking's money for its customer or for finance.
func (s *Service) Ledger(ctx context.Context, p identity.Principal, id rentals.BookingID) (*payments.Ledger, error) {
	b, _, err := rentalspg.LoadBooking(ctx, s.pool, id, false)
	if err != nil {
		return nil, err
	}
	if _, err := p.RequireOwnOrAny(identity.PermPaymentReadOwn, identity.PermPaymentRead, b.CustomerID); err != nil {
		return nil, err
	}
	l, _, err := paymentspg.LoadLedger(ctx, s.pool, string(id), false)
	return l, err
}
