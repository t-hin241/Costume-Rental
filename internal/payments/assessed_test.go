package payments_test

import (
	"errors"
	"testing"

	"webee_rentor/internal/payments"
)

func feeLedger(t *testing.T, security int64) *payments.Ledger {
	t.Helper()
	l, err := payments.OpenLedger(payments.OpenRequest{
		BookingRef: "bk-1", Rent: vnd(200_000), Security: vnd(security), Reservation: vnd(0),
		RentChargeID: "ch-rent", SecurityChargeID: "ch-dep", Actor: system, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := pay(t, l, "p1", portions(200_000, security)...); err != nil {
		t.Fatal(err)
	}
	return l
}

func approvedFee(t *testing.T, l *payments.Ledger, id string, kind payments.ChargeKind, amount int64) {
	t.Helper()
	if _, created, err := l.AddApprovedFee(payments.ChargeID("ch-"+id), kind, vnd(amount), "as-"+id, "đã xác minh", nil, finance, now); err != nil || !created {
		t.Fatalf("add approved fee %s: %v %v", id, created, err)
	}
}

// Fee policy §5 example.
func TestSecuritySettlementAgainstApprovedFees(t *testing.T) {
	l := feeLedger(t, 500_000)
	approvedFee(t, l, "late", payments.ChargeLateFee, 60_000)
	approvedFee(t, l, "zip", payments.ChargeDamageFee, 80_000)

	ds, err := l.ApplySecurityToApprovedFees("settle", clerk, now)
	if err != nil || len(ds) != 2 {
		t.Fatalf("deductions = %+v, %v", ds, err)
	}
	s := l.Summary()
	if s.SecurityApplied.Amount() != 140_000 || s.SecurityHeld.Amount() != 360_000 || !s.RentalOutstanding.IsZero() {
		t.Fatalf("summary = %+v", s)
	}
	if again, err := l.ApplySecurityToApprovedFees("settle", clerk, now); err != nil || len(again) != 0 {
		t.Fatalf("rerun = %+v, %v; want nothing new", again, err)
	}
	refundNow(t, l, "r1", payments.BucketSecurity, 360_000)
	if !l.Summary().IsSettled() {
		t.Fatalf("should be settled: %+v", l.Summary())
	}
}

func TestApprovedFeeIsRecordedOnce(t *testing.T) {
	l := feeLedger(t, 500_000)
	approvedFee(t, l, "late", payments.ChargeLateFee, 60_000)
	if _, created, err := l.AddApprovedFee("ch-other", payments.ChargeLateFee, vnd(60_000), "as-late", "x", nil, finance, now); err != nil || created {
		t.Fatalf("replay = %v, %v; want the existing charge", created, err)
	}
	if _, _, err := l.AddApprovedFee("ch-other", payments.ChargeLateFee, vnd(70_000), "as-late", "x", nil, finance, now); !errors.Is(err, payments.ErrIdempotencyConflict) {
		t.Fatalf("same assessment, different amount err = %v", err)
	}
	if _, _, err := l.AddApprovedFee("ch-x", payments.ChargeCancellationFee, vnd(1), "as-x", "x", nil, finance, now); err == nil {
		t.Fatal("cancellation fees do not come from return assessments")
	}
}

func TestFeesAboveSecurityLeaveBalanceDue(t *testing.T) {
	l := feeLedger(t, 100_000)
	approvedFee(t, l, "loss", payments.ChargeLossFee, 250_000)
	if _, err := l.ApplySecurityToApprovedFees("settle", clerk, now); err != nil {
		t.Fatal(err)
	}
	s := l.Summary()
	if !s.SecurityHeld.IsZero() || s.RentalOutstanding.Amount() != 150_000 || s.IsSettled() {
		t.Fatalf("summary = %+v", s)
	}
}

func TestWaivedFeeIsNotDeducted(t *testing.T) {
	l := feeLedger(t, 500_000)
	approvedFee(t, l, "late", payments.ChargeLateFee, 60_000)
	if err := l.ReverseCharge("ch-late", "miễn: lỗi cửa hàng", finance, now); err != nil {
		t.Fatal(err)
	}
	ds, err := l.ApplySecurityToApprovedFees("settle", clerk, now)
	if err != nil || len(ds) != 0 || l.Summary().SecurityHeld.Amount() != 500_000 {
		t.Fatalf("deductions = %+v, %v", ds, err)
	}
}

func TestFeePaidInCashIsNotAlsoDeducted(t *testing.T) {
	l := feeLedger(t, 500_000)
	approvedFee(t, l, "clean", payments.ChargeCleaningFee, 40_000)
	if err := pay(t, l, "p2", portions(40_000, 0)...); err != nil {
		t.Fatal(err)
	}
	ds, err := l.ApplySecurityToApprovedFees("settle", clerk, now)
	if err != nil || len(ds) != 0 {
		t.Fatalf("deductions = %+v, %v; the fee is already paid", ds, err)
	}
}
