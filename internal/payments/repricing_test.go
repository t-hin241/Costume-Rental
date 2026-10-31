package payments_test

import (
	"errors"
	"testing"

	"webee_rentor/internal/payments"
)

// A lower deposit frees only the difference; the rest still backs the rental.
func TestExcessSecurityKeepsWhatTheBookingStillNeeds(t *testing.T) {
	l := feeLedger(t, 550_000)
	if got := l.ExcessSecurity(); !got.IsZero() {
		t.Fatalf("excess before any change = %s", got)
	}
	if _, err := l.StageRepricing("mod-1", vnd(200_000), vnd(500_000), clerk, now); err != nil {
		t.Fatal(err)
	}
	if err := l.CommitRepricing("mod-1", "ch-rent-2", "ch-dep-2", clerk, now); err != nil {
		t.Fatal(err)
	}
	if got := l.ExcessSecurity(); got.Amount() != 50_000 {
		t.Fatalf("excess = %s, want the 50.000 the deposit went down", got)
	}
	if got := l.Refundable(payments.BucketSecurity); got.Amount() != 550_000 {
		t.Fatalf("refundable = %s, the whole held deposit when closing", got)
	}
}

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
// Money collected for a staged change stays put until the change ends:
// PaidForModification must not count money that already went back.
func TestNoRefundWhileARepricingIsStaged(t *testing.T) {
	l := ledger(t)
	payAtCounter(t, l, "p1", 200_000, 550_000)
	if _, err := l.StageRepricing("mod-1", vnd(260_000), vnd(550_000), clerk, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.RecordCounterPayment(payments.NewPaymentRequest{
		ID: "p2", IdempotencyKey: "k2", Method: payments.MethodCash, ModificationRef: "mod-1",
		Portions: portions(60_000, 0), Actor: clerk, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.RequestRefund("r1", "rk1", payments.BucketRental, vnd(60_000), "đổi ý", clerk, now); !errors.Is(err, payments.ErrRepricingConflict) {
		t.Fatalf("refund during repricing err = %v", err)
	}
	l.DiscardRepricing("mod-1")
	if _, _, err := l.RequestRefund("r1", "rk1", payments.BucketRental, vnd(60_000), "đổi ý", clerk, now); err != nil {
		t.Fatalf("refund after discard: %v", err)
	}
}

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
