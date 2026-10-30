package payments_test

import (
	"errors"
	"testing"

	"webee_rentor/internal/payments"
)

func payForModification(l *payments.Ledger, id, ref string, rental int64) error {
	_, _, err := l.RecordCounterPayment(payments.NewPaymentRequest{
		ID: payments.PaymentID(id), IdempotencyKey: "key-" + id, Method: payments.MethodCash,
		Portions: portions(rental, 0), ModificationRef: ref, Actor: clerk, Now: now,
	})
	return err
}

// Modification policy test 4: the increase is collected before confirming.
func TestRepricingIncreaseIsCollectedFirst(t *testing.T) {
	l := feeLedger(t, 550_000) // 200k rent and 550k security paid
	if err := payForModification(l, "p2", "mod-1", 60_000); !errors.Is(err, payments.ErrInvalidTransition) {
		t.Fatalf("payment for an unstaged change err = %v", err)
	}
	r, err := l.StageRepricing("mod-1", vnd(260_000), vnd(500_000), clerk, now)
	if err != nil || r.RentDelta.Amount() != 60_000 || r.SecurityDelta.Amount() != -50_000 {
		t.Fatalf("stage = %+v, %v", r, err)
	}
	if _, err := l.StageRepricing("mod-2", vnd(1), vnd(0), clerk, now); !errors.Is(err, payments.ErrRepricingConflict) {
		t.Fatalf("second staged change err = %v", err)
	}
	if err := payForModification(l, "p2", "mod-1", 60_001); !errors.Is(err, payments.ErrExceedsOutstanding) {
		t.Fatalf("collecting more than the increase err = %v", err)
	}
	if err := payForModification(l, "p2", "mod-1", 60_000); err != nil {
		t.Fatal(err)
	}
	if got := l.PaidForModification("mod-1"); got.Amount() != 60_000 {
		t.Fatalf("paid = %s", got)
	}
	if err := l.CommitRepricing("mod-1", "ch-rent-2", "ch-dep-2", clerk, now); err != nil {
		t.Fatal(err)
	}
	if err := l.CommitRepricing("mod-1", "x", "y", clerk, now); err != nil {
		t.Fatalf("idempotent commit: %v", err)
	}
	s := l.Summary()
	if s.RentalDue.Amount() != 260_000 || !s.RentalOutstanding.IsZero() || s.SecurityRequired.Amount() != 500_000 {
		t.Fatalf("summary = %+v", s)
	}
	// The lower security deposit is refundable later with the rest of it.
	if s.SecurityHeld.Amount() != 550_000 {
		t.Fatalf("held = %s", s.SecurityHeld)
	}
}

// Modification policy test 5: a cheaper package refunds the difference once.
func TestRepricingDecreaseRefundsOnce(t *testing.T) {
	l := feeLedger(t, 550_000)
	if _, err := l.StageRepricing("mod-1", vnd(80_000), vnd(550_000), clerk, now); err != nil {
		t.Fatal(err)
	}
	if err := l.CommitRepricing("mod-1", "ch-rent-2", "ch-dep-2", clerk, now); err != nil {
		t.Fatal(err)
	}
	if got := l.Summary().RentalOutstanding.Amount(); got != -120_000 {
		t.Fatalf("outstanding = %d, want -120000 refundable", got)
	}
	_, created, err := l.RequestRefund("r1", "mod-1-refund", payments.BucketRental, vnd(120_000), "đổi gói 1 ngày", clerk, now)
	if err != nil || !created {
		t.Fatal(err)
	}
	if _, created, err := l.RequestRefund("r1-retry", "mod-1-refund", payments.BucketRental, vnd(120_000), "đổi gói 1 ngày", clerk, now); err != nil || created {
		t.Fatalf("retried refund = %v, %v; want the existing one", created, err)
	}
	if _, _, err := l.RequestRefund("r2", "other", payments.BucketRental, vnd(1), "x", clerk, now); !errors.Is(err, payments.ErrExceedsRefundable) {
		t.Fatalf("refunding twice err = %v", err)
	}
}

// Modification policy test 6: the change is abandoned after payment.
func TestDiscardedRepricingLeavesPaymentRefundable(t *testing.T) {
	l := feeLedger(t, 550_000)
	if _, err := l.StageRepricing("mod-1", vnd(260_000), vnd(550_000), clerk, now); err != nil {
		t.Fatal(err)
	}
	if err := payForModification(l, "p2", "mod-1", 60_000); err != nil {
		t.Fatal(err)
	}
	l.DiscardRepricing("mod-1")
	l.DiscardRepricing("mod-1")
	s := l.Summary()
	if s.RentalDue.Amount() != 200_000 || s.RentalOutstanding.Amount() != -60_000 {
		t.Fatalf("summary = %+v; the original charges stay and the payment is refundable", s)
	}
	if err := l.CommitRepricing("mod-1", "a", "b", clerk, now); !errors.Is(err, payments.ErrInvalidTransition) {
		t.Fatalf("committing a discarded change err = %v", err)
	}
}
