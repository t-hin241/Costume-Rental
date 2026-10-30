package payments_test

import (
	"errors"
	"testing"

	"webee_rentor/internal/payments"
)

func cancel(t *testing.T, l *payments.Ledger, fee int64) payments.CancellationResult {
	t.Helper()
	res, created, err := l.SettleCancellation(payments.CancellationRequest{
		Fee: vnd(fee), Basis: "test tier", FeeChargeID: "ch-cancel", ResolutionID: "res-fee", Actor: clerk, Now: now,
	})
	if err != nil || !created {
		t.Fatalf("settle cancellation: %v %v", created, err)
	}
	return res
}

func TestCancellationFeeCoveredByReservation(t *testing.T) {
	// 200k rent, 60k reservation paid; cancelled 5 days ahead → 10% = 20k.
	l := reservationLedger(t, true)
	if err := pay(t, l, "p1", reservation(60_000)); err != nil {
		t.Fatal(err)
	}
	res := cancel(t, l, 20_000)
	if res.ReservationApplied.Amount() != 20_000 || res.RefundableReservation.Amount() != 40_000 ||
		!res.Owed.IsZero() || !res.RefundableRental.IsZero() || !res.RefundableSecurity.IsZero() {
		t.Fatalf("result = %+v", res)
	}
	// Nothing else may be collected for a cancelled booking.
	if err := pay(t, l, "p2", portions(1, 0)...); !errors.Is(err, payments.ErrExceedsOutstanding) {
		t.Fatalf("collecting rent after cancellation err = %v", err)
	}
	refundNow(t, l, "r1", payments.BucketReservation, 40_000)
	if !l.Summary().IsSettled() {
		t.Fatalf("should be settled: %+v", l.Summary())
	}

	again, created, err := l.SettleCancellation(payments.CancellationRequest{Fee: vnd(20_000), Basis: "retry", FeeChargeID: "x", ResolutionID: "y", Actor: clerk, Now: now})
	if err != nil || created || again.Fee.Amount() != 20_000 {
		t.Fatalf("retry = %+v %v %v; want no-op", again, created, err)
	}
	if _, _, err := l.SettleCancellation(payments.CancellationRequest{Fee: vnd(30_000), Basis: "retry", FeeChargeID: "x", ResolutionID: "y", Actor: clerk, Now: now}); !errors.Is(err, payments.ErrAlreadyCancelled) {
		t.Fatalf("different fee on retry err = %v", err)
	}
}

func TestCancellationAfterFullPaymentRefundsTheRestAndAllSecurity(t *testing.T) {
	// Everything paid (60k reservation + 140k rent + 550k security);
	// cancelled under 24h → 50% = 100k.
	l := reservationLedger(t, true)
	if err := pay(t, l, "p1", reservation(60_000)); err != nil {
		t.Fatal(err)
	}
	if err := pay(t, l, "p2", portions(140_000, 550_000)...); err != nil {
		t.Fatal(err)
	}
	res := cancel(t, l, 100_000)
	if res.ReservationApplied.Amount() != 60_000 || res.RefundableRental.Amount() != 100_000 ||
		res.RefundableSecurity.Amount() != 550_000 || !res.RefundableReservation.IsZero() || !res.Owed.IsZero() {
		t.Fatalf("result = %+v", res)
	}
	if _, err := l.ProposeDeduction("d1", "ch-cancel", vnd(1), "x", nil, clerk, now); !errors.Is(err, payments.ErrNotDeductible) {
		t.Fatalf("security must never cover the cancellation fee, err = %v", err)
	}
	refundNow(t, l, "r1", payments.BucketRental, 100_000)
	refundNow(t, l, "r2", payments.BucketSecurity, 550_000)
	if !l.Summary().IsSettled() {
		t.Fatalf("should be settled: %+v", l.Summary())
	}
}

func TestCancellationFeeAbovePaidIsOwedNotCharged(t *testing.T) {
	// No-show (70% = 140k) with only the 60k reservation paid.
	l := reservationLedger(t, true)
	if err := pay(t, l, "p1", reservation(60_000)); err != nil {
		t.Fatal(err)
	}
	res := cancel(t, l, 140_000)
	if res.ReservationApplied.Amount() != 60_000 || res.Owed.Amount() != 80_000 {
		t.Fatalf("result = %+v", res)
	}
	s := l.Summary()
	if s.IsSettled() || s.RentalOutstanding.Amount() != 80_000 {
		t.Fatalf("summary = %+v", s)
	}
	// Staff may collect exactly what is owed, no more.
	if err := pay(t, l, "p2", portions(80_001, 0)...); !errors.Is(err, payments.ErrExceedsOutstanding) {
		t.Fatalf("overcollect err = %v", err)
	}
	if err := pay(t, l, "p2", portions(80_000, 0)...); err != nil {
		t.Fatal(err)
	}
	if !l.Summary().IsSettled() {
		t.Fatalf("should be settled: %+v", l.Summary())
	}
}

func TestCancellationFeeCanBeWaived(t *testing.T) {
	l := reservationLedger(t, true)
	res := cancel(t, l, 100_000)
	if res.Owed.Amount() != 100_000 {
		t.Fatalf("owed = %d", res.Owed.Amount())
	}
	if err := l.ReverseCharge("ch-cancel", "khách quen, miễn phí hủy", finance, now); err != nil {
		t.Fatal(err)
	}
	if !l.Summary().IsSettled() {
		t.Fatalf("waived fee should leave nothing owed: %+v", l.Summary())
	}
}

func TestZeroFeeCancellationRefundsEverything(t *testing.T) {
	l := reservationLedger(t, false)
	if err := pay(t, l, "p1", reservation(60_000)); err != nil {
		t.Fatal(err)
	}
	res := cancel(t, l, 0)
	if res.RefundableReservation.Amount() != 60_000 || !res.ReservationApplied.IsZero() || !res.Owed.IsZero() {
		t.Fatalf("result = %+v", res)
	}
	for _, c := range l.Charges {
		if c.Kind == payments.ChargeCancellationFee {
			t.Fatal("a zero fee must not create a charge")
		}
	}
}
