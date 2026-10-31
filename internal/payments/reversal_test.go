package payments_test

import (
	"errors"
	"testing"
	"time"

	"webee_rentor/internal/payments"
)

var (
	cashNow     = payments.RefundPayout{Method: payments.MethodCash, PaidAt: now}
	transferNow = payments.RefundPayout{Method: payments.MethodBankTransfer, Reference: "FT1", PaidAt: now}
)

// settledFee is a 120.000đ late fee the deposit covered.
func settledFee(t *testing.T) *payments.Ledger {
	t.Helper()
	l := feeLedger(t, 500_000)
	if _, _, err := l.AddApprovedFee("ch-late", payments.ChargeLateFee, vnd(120_000), "a1", "trễ", nil, finance, now); err != nil {
		t.Fatal(err)
	}
	if _, err := l.ApplySecurityToApprovedFees("settle", clerk, now); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestReversingADeductionReturnsTheMoneyToTheDeposit(t *testing.T) {
	l := settledFee(t)
	if got := l.Summary().SecurityHeld; got.Amount() != 380_000 {
		t.Fatalf("held = %s", got)
	}
	if _, err := l.ReverseDeductions("ch-late", "", finance, now); !errors.Is(err, payments.ErrReasonRequired) {
		t.Fatalf("no reason: err = %v", err)
	}
	if _, err := l.ReverseDeductions("ch-late", "thu sai", system, now); !errors.Is(err, payments.ErrActorRequired) {
		t.Fatalf("a job reversing: err = %v", err)
	}
	reversed, err := l.ReverseDeductions("ch-late", "thu sai", finance, now)
	if err != nil || len(reversed) != 1 || reversed[0].Status != payments.DeductionReversed || reversed[0].ReversedBy == nil {
		t.Fatalf("reversed = %+v, %v", reversed, err)
	}
	if err := l.ReverseCharge("ch-late", "thu sai", finance, now); err != nil {
		t.Fatal(err)
	}
	s := l.Summary()
	if s.SecurityHeld.Amount() != 500_000 || !s.SecurityApplied.IsZero() || !s.RentalOutstanding.IsZero() {
		t.Fatalf("after reversal: %+v", s)
	}
	if len(l.Deductions) != 1 {
		t.Fatal("a reversed deduction must stay on record")
	}
	if again, err := l.ReverseDeductions("ch-late", "thu sai", finance, now); err != nil || len(again) != 0 {
		t.Fatalf("second reversal = %+v, %v", again, err)
	}
}

// requestedRefund leaves 120.000đ of the deposit awaiting refund.
func requestedRefund(t *testing.T) (*payments.Ledger, payments.RefundID) {
	t.Helper()
	l := feeLedger(t, 500_000)
	if _, _, err := l.RequestRefund("r1", "rk1", payments.BucketSecurity, vnd(120_000), "hoàn cọc", clerk, now); err != nil {
		t.Fatal(err)
	}
	return l, "r1"
}

// D30: cash at the counter only takes the confirmation, even entered later.
func TestCashRefundNeedsOnlyTheConfirmation(t *testing.T) {
	l, id := requestedRefund(t)
	paid := now.Add(-time.Hour) // handed over before anyone entered it
	in := payments.RefundPayout{Method: payments.MethodCash, Reference: "PC-001", PaidAt: paid}
	if err := l.RecordCounterRefund(id, in, clerk, now); err != nil {
		t.Fatal(err)
	}
	r := l.Refunds[0]
	if r.Status != payments.RefundSucceeded || r.DecidedBy != nil || !r.CompletedAt.Equal(paid) ||
		r.Reference != "PC-001" || *r.RecordedBy != clerk || !r.RecordedAt.Equal(now) {
		t.Fatalf("refund = %+v", r)
	}
	if err := l.RecordCounterRefund(id, in, clerk, now); !errors.Is(err, payments.ErrInvalidTransition) {
		t.Fatalf("recording it twice: err = %v", err)
	}
}

// D32: a transfer takes the same confirmation, with the code that ties it to
// the bank statement.
func TestTransferRefundNeedsItsBankCode(t *testing.T) {
	l, id := requestedRefund(t)
	blank := payments.RefundPayout{Method: payments.MethodBankTransfer, Reference: "  ", PaidAt: now}
	if err := l.RecordCounterRefund(id, blank, clerk, now); err == nil {
		t.Fatal("a transfer without its bank code was accepted")
	}
	if err := l.RecordCounterRefund(id, transferNow, clerk, now); err != nil {
		t.Fatal(err)
	}
	if r := l.Refunds[0]; r.Status != payments.RefundSucceeded || r.DecidedBy != nil || r.Method != payments.MethodBankTransfer || r.Reference != "FT1" {
		t.Fatalf("refund = %+v", r)
	}
}

func TestRejectedRefundCannotBePaidInCash(t *testing.T) {
	l, id := requestedRefund(t)
	if err := l.RejectRefund(id, finance, "sai số tiền", now); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordCounterRefund(id, cashNow, clerk, now); !errors.Is(err, payments.ErrInvalidTransition) {
		t.Fatalf("err = %v", err)
	}
}

func TestCounterRefundChecksThePayout(t *testing.T) {
	l, id := requestedRefund(t)
	cases := map[string]payments.RefundPayout{
		"gateway":    {Method: payments.MethodGateway, PaidAt: now},
		"no time":    {Method: payments.MethodBankTransfer, Reference: "FT1"},
		"the future": {Method: payments.MethodCash, PaidAt: now.Add(time.Minute)},
	}
	for name, in := range cases {
		if err := l.RecordCounterRefund(id, in, clerk, now); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
