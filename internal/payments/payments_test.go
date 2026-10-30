package payments_test

import (
	"errors"
	"testing"
	"time"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
	"webee_rentor/internal/payments"
)

var (
	now      = time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
	clerk    = identity.UserActor("staff1")
	finance  = identity.UserActor("finance1")
	system   = identity.SystemActor("booking")
	approval = payments.ApprovalPolicy{DistinctApproverAbove: vnd(0)}
)

func vnd(v int64) money.Money {
	m, err := money.New(v, money.VND)
	if err != nil {
		panic(err)
	}
	return m
}

// ledger for a 200k rent + 550k deposit booking.
func ledger(t *testing.T) *payments.Ledger {
	t.Helper()
	l, err := payments.OpenLedger(payments.OpenRequest{
		BookingRef: "bk-1", Rent: vnd(200_000), Security: vnd(550_000), Reservation: vnd(0),
		RentChargeID: "ch-rent", SecurityChargeID: "ch-dep", Actor: system, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func portions(rental, security int64) []payments.Portion {
	var ps []payments.Portion
	if rental > 0 {
		ps = append(ps, payments.Portion{Bucket: payments.BucketRental, Amount: vnd(rental)})
	}
	if security > 0 {
		ps = append(ps, payments.Portion{Bucket: payments.BucketSecurity, Amount: vnd(security)})
	}
	return ps
}

func payAtCounter(t *testing.T, l *payments.Ledger, id string, rental, security int64) {
	t.Helper()
	if _, _, err := l.RecordCounterPayment(payments.NewPaymentRequest{
		ID: payments.PaymentID(id), IdempotencyKey: "key-" + id, Method: payments.MethodCash,
		Portions: portions(rental, security), Actor: clerk, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func gateway(t *testing.T, l *payments.Ledger, id string, rental, security int64) payments.Payment {
	t.Helper()
	p, created, err := l.StartGatewayPayment(payments.NewPaymentRequest{
		ID: payments.PaymentID(id), IdempotencyKey: "key-" + id, Method: payments.MethodGateway, Provider: "psp",
		Portions: portions(rental, security), Actor: identity.UserActor("cust-a"), Now: now,
	})
	if err != nil || !created {
		t.Fatalf("start gateway payment: %v %v", created, err)
	}
	return p
}

func success(id string, payment payments.PaymentID, amount int64) payments.PaymentEvent {
	return payments.PaymentEvent{Provider: "psp", EventID: id, PaymentID: payment, ProviderTxnID: "txn-" + string(payment),
		Amount: vnd(amount), Status: payments.EventSucceeded, OccurredAt: now}
}

func TestDepositDeductionAndRefundReconcile(t *testing.T) {
	// docs §6.2: 550k deposit, 120k verified deduction, 430k refunded.
	l := ledger(t)
	payAtCounter(t, l, "p1", 200_000, 550_000)
	s := l.Summary()
	if s.State != payments.StatePaid || s.SecurityHeld.Amount() != 550_000 || !s.RentalOutstanding.IsZero() {
		t.Fatalf("after payment: %+v", s)
	}

	if _, err := l.AddFee("ch-dmg", payments.ChargeDamageFee, vnd(120_000), "", nil, clerk, now); !errors.Is(err, payments.ErrReasonRequired) {
		t.Fatalf("fee without reason err = %v", err)
	}
	if _, err := l.AddFee("ch-dmg", payments.ChargeDamageFee, vnd(120_000), "rách vạt áo", []string{"evidence/abc.jpg"}, clerk, now); err != nil {
		t.Fatal(err)
	}
	if _, err := l.ProposeDeduction("d1", "ch-rent", vnd(1), "x", nil, clerk, now); !errors.Is(err, payments.ErrNotDeductible) {
		t.Fatalf("deducting rent err = %v", err)
	}
	if _, err := l.ProposeDeduction("d1", "ch-dmg", vnd(120_001), "rách vạt áo", nil, clerk, now); !errors.Is(err, payments.ErrExceedsRefundable) {
		t.Fatalf("deduction above fee err = %v", err)
	}
	if _, err := l.ProposeDeduction("d1", "ch-dmg", vnd(120_000), "rách vạt áo", []string{"evidence/abc.jpg"}, clerk, now); err != nil {
		t.Fatal(err)
	}
	// The proposed deduction reserves deposit: it cannot be refunded meanwhile.
	if _, _, err := l.RequestRefund("r0", "rk0", payments.BucketSecurity, vnd(550_000), "hoàn cọc", clerk, now); !errors.Is(err, payments.ErrExceedsRefundable) {
		t.Fatalf("refund over reserved deposit err = %v", err)
	}
	if err := l.ApproveDeduction("d1", clerk, approval, now); !errors.Is(err, payments.ErrApproverMustDiffer) {
		t.Fatalf("self-approval err = %v", err)
	}
	if err := l.ApproveDeduction("d1", finance, approval, now); err != nil {
		t.Fatal(err)
	}
	if err := l.ReverseCharge("ch-dmg", "nhầm", finance, now); !errors.Is(err, payments.ErrChargeHasDeductions) {
		t.Fatalf("reversing a deducted fee err = %v", err)
	}

	s = l.Summary()
	if s.SecurityHeld.Amount() != 430_000 || !s.RentalOutstanding.IsZero() || s.IsSettled() {
		t.Fatalf("after deduction: held=%d outstanding=%d", s.SecurityHeld.Amount(), s.RentalOutstanding.Amount())
	}
	r, _, err := l.RequestRefund("r1", "rk1", payments.BucketSecurity, vnd(430_000), "hoàn cọc", clerk, now)
	if err != nil {
		t.Fatal(err)
	}
	if l.Summary().State != payments.StateRefundPending {
		t.Fatalf("state = %s", l.Summary().State)
	}
	if err := l.RecordCounterRefund(r.ID, clerk, now); !errors.Is(err, payments.ErrInvalidTransition) {
		t.Fatalf("paying out an unapproved refund err = %v", err)
	}
	if err := l.ApproveRefund(r.ID, finance, approval, now); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordCounterRefund(r.ID, clerk, now); err != nil {
		t.Fatal(err)
	}
	s = l.Summary()
	if !s.IsSettled() || s.SecurityRefunded.Amount() != 430_000 || s.State != payments.StatePartiallyRefunded {
		t.Fatalf("final summary: %+v", s)
	}
}

func TestDamageAboveDepositLeavesBalanceDue(t *testing.T) {
	l := ledger(t)
	payAtCounter(t, l, "p1", 200_000, 550_000)
	if _, err := l.AddFee("ch-loss", payments.ChargeLossFee, vnd(700_000), "mất áo khoác", nil, clerk, now); err != nil {
		t.Fatal(err)
	}
	if _, err := l.ProposeDeduction("d1", "ch-loss", vnd(600_000), "mất áo", nil, clerk, now); !errors.Is(err, payments.ErrExceedsRefundable) {
		t.Fatalf("deducting more than held err = %v", err)
	}
	if _, err := l.ProposeDeduction("d1", "ch-loss", vnd(550_000), "mất áo", nil, clerk, now); err != nil {
		t.Fatal(err)
	}
	if err := l.ApproveDeduction("d1", finance, approval, now); err != nil {
		t.Fatal(err)
	}
	s := l.Summary()
	if s.RentalOutstanding.Amount() != 150_000 || !s.SecurityHeld.IsZero() || s.IsSettled() {
		t.Fatalf("outstanding=%d held=%d", s.RentalOutstanding.Amount(), s.SecurityHeld.Amount())
	}
	// The remainder is requested from the customer, never auto-charged.
	payAtCounter(t, l, "p2", 150_000, 0)
	if !l.Summary().IsSettled() {
		t.Fatal("ledger should be settled after the remainder is paid")
	}
}

func TestPaymentIdempotencyAndOverpayment(t *testing.T) {
	l := ledger(t)
	req := payments.NewPaymentRequest{ID: "p1", IdempotencyKey: "checkout-1", Method: payments.MethodGateway, Provider: "psp",
		Portions: portions(200_000, 550_000), Actor: identity.UserActor("cust-a"), Now: now}
	first, created, err := l.StartGatewayPayment(req)
	if err != nil || !created {
		t.Fatal(err)
	}
	retry := req
	retry.ID = "p1-retry"
	again, created, err := l.StartGatewayPayment(retry)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("retried checkout = %v %v %v; want the original payment", again.ID, created, err)
	}
	changed := retry
	changed.Portions = portions(100_000, 0)
	if _, _, err := l.StartGatewayPayment(changed); !errors.Is(err, payments.ErrIdempotencyConflict) {
		t.Fatalf("reused key with different amount err = %v", err)
	}
	// A second checkout while the first is pending would double charge.
	if _, _, err := l.StartGatewayPayment(payments.NewPaymentRequest{ID: "p2", IdempotencyKey: "checkout-2", Method: payments.MethodGateway,
		Provider: "psp", Portions: portions(1, 0), Actor: identity.UserActor("cust-a"), Now: now}); !errors.Is(err, payments.ErrExceedsOutstanding) {
		t.Fatalf("second pending checkout err = %v", err)
	}
	if _, _, err := l.RecordCounterPayment(payments.NewPaymentRequest{ID: "p3", IdempotencyKey: "k3", Method: payments.MethodCash,
		Portions: portions(1, 0), Actor: system, Now: now}); !errors.Is(err, payments.ErrActorRequired) {
		t.Fatalf("counter payment by system err = %v", err)
	}
}

func TestWebhookRules(t *testing.T) {
	l := ledger(t)
	p := gateway(t, l, "p1", 200_000, 0)

	wrongAmount := success("ev1", p.ID, 199_999)
	if _, err := l.ApplyPaymentEvent(wrongAmount); !errors.Is(err, payments.ErrProviderMismatch) {
		t.Fatalf("amount mismatch err = %v", err)
	}
	wrongCurrency := success("ev1", p.ID, 200_000)
	wrongCurrency.Amount = money.Money{}
	if _, err := l.ApplyPaymentEvent(wrongCurrency); !errors.Is(err, payments.ErrProviderMismatch) {
		t.Fatalf("currency mismatch err = %v", err)
	}
	otherProvider := success("ev1", p.ID, 200_000)
	otherProvider.Provider = "other"
	if _, err := l.ApplyPaymentEvent(otherProvider); !errors.Is(err, payments.ErrProviderMismatch) {
		t.Fatalf("provider mismatch err = %v", err)
	}
	if _, err := l.ApplyPaymentEvent(success("ev1", "unknown", 200_000)); !errors.Is(err, payments.ErrNotFound) {
		t.Fatalf("unknown reference err = %v", err)
	}
	if l.Summary().RentalPaid.Amount() != 0 {
		t.Fatal("rejected events must not change balances")
	}

	out, err := l.ApplyPaymentEvent(success("ev1", p.ID, 200_000))
	if err != nil || out != payments.OutcomeApplied {
		t.Fatalf("apply = %s, %v", out, err)
	}
	out, err = l.ApplyPaymentEvent(success("ev1", p.ID, 200_000))
	if err != nil || out != payments.OutcomeDuplicate {
		t.Fatalf("replayed event = %s, %v", out, err)
	}
	out, err = l.ApplyPaymentEvent(success("ev2", p.ID, 200_000))
	if err != nil || out != payments.OutcomeNoChange {
		t.Fatalf("second success notification = %s, %v", out, err)
	}
	if got := l.Summary().RentalPaid.Amount(); got != 200_000 {
		t.Fatalf("rental paid = %d; duplicates must not double count", got)
	}
	failed := success("ev3", p.ID, 200_000)
	failed.Status = payments.EventFailed
	if _, err := l.ApplyPaymentEvent(failed); !errors.Is(err, payments.ErrInvalidTransition) {
		t.Fatalf("failure after success err = %v", err)
	}
}

func TestLateSuccessAfterExpiryIsRecordedForReview(t *testing.T) {
	l := ledger(t)
	p := gateway(t, l, "p1", 200_000, 0)
	if changed, err := l.ExpirePayment(p.ID, now); err != nil || !changed {
		t.Fatalf("expire = %v, %v", changed, err)
	}
	if changed, err := l.ExpirePayment(p.ID, now); err != nil || changed {
		t.Fatalf("second expire = %v, %v", changed, err)
	}
	out, err := l.ApplyPaymentEvent(success("ev1", p.ID, 200_000))
	if err != nil || out != payments.OutcomeAppliedNeedsReview {
		t.Fatalf("late success = %s, %v", out, err)
	}
	if l.Payments[0].Status != payments.PaymentSucceeded || !l.Payments[0].NeedsReview {
		t.Fatalf("payment = %+v", l.Payments[0])
	}
}

func TestRefundProviderFailureAndRetry(t *testing.T) {
	l := ledger(t)
	payAtCounter(t, l, "p1", 200_000, 550_000)
	r, _, err := l.RequestRefund("r1", "rk1", payments.BucketSecurity, vnd(550_000), "hoàn cọc", clerk, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := l.RequestRefund("r1-dup", "rk1", payments.BucketSecurity, vnd(550_000), "hoàn cọc", clerk, now); err != nil || created {
		t.Fatalf("retried refund request = %v, %v; want existing", created, err)
	}
	if err := l.ApproveRefund(r.ID, finance, approval, now); err != nil {
		t.Fatal(err)
	}
	if err := l.MarkRefundSubmitted(r.ID, "psp", "rf-1"); err != nil {
		t.Fatal(err)
	}
	if err := l.MarkRefundSubmitted(r.ID, "psp", "rf-1"); err != nil {
		t.Fatalf("idempotent submit: %v", err)
	}
	bad := payments.RefundEvent{Provider: "psp", EventID: "rev1", RefundID: r.ID, ProviderRef: "rf-1", Amount: vnd(1), Status: payments.EventSucceeded, OccurredAt: now}
	if _, err := l.ApplyRefundEvent(bad); !errors.Is(err, payments.ErrProviderMismatch) {
		t.Fatalf("refund amount mismatch err = %v", err)
	}
	fail := payments.RefundEvent{Provider: "psp", EventID: "rev1", RefundID: r.ID, ProviderRef: "rf-1", Amount: vnd(550_000), Status: payments.EventFailed, OccurredAt: now}
	if out, err := l.ApplyRefundEvent(fail); err != nil || out != payments.OutcomeApplied {
		t.Fatalf("refund failure = %s, %v", out, err)
	}
	if out, _ := l.ApplyRefundEvent(fail); out != payments.OutcomeDuplicate {
		t.Fatalf("replayed refund event = %s", out)
	}
	s := l.Summary()
	if s.SecurityHeld.Amount() != 550_000 || !s.PendingRefunds.IsZero() {
		t.Fatalf("a failed refund must free its amount: %+v", s)
	}

	// Partial retry: two refunds.
	for i, amt := range []int64{300_000, 250_000} {
		id := payments.RefundID([]string{"r2", "r3"}[i])
		if _, _, err := l.RequestRefund(id, string(id), payments.BucketSecurity, vnd(amt), "hoàn cọc lần 2", clerk, now); err != nil {
			t.Fatal(err)
		}
		if err := l.ApproveRefund(id, finance, approval, now); err != nil {
			t.Fatal(err)
		}
		if err := l.RecordCounterRefund(id, clerk, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := l.RequestRefund("r4", "r4", payments.BucketSecurity, vnd(1), "x", clerk, now); !errors.Is(err, payments.ErrExceedsRefundable) {
		t.Fatalf("over-refund err = %v", err)
	}
	if s := l.Summary(); !s.SecurityHeld.IsZero() || s.SecurityRefunded.Amount() != 550_000 {
		t.Fatalf("after partial refunds: %+v", s)
	}
}

func TestCancellationRefundNeedsExplicitReversal(t *testing.T) {
	l := ledger(t)
	payAtCounter(t, l, "p1", 200_000, 0)
	if _, _, err := l.RequestRefund("r1", "rk1", payments.BucketRental, vnd(200_000), "hủy", clerk, now); !errors.Is(err, payments.ErrExceedsRefundable) {
		t.Fatalf("rent is not refundable until the charge is reversed, err = %v", err)
	}
	if err := l.ReverseCharge("ch-rent", "hủy trước hạn theo chính sách", finance, now); err != nil {
		t.Fatal(err)
	}
	if err := l.ReverseCharge("ch-rent", "again", finance, now); !errors.Is(err, payments.ErrInvalidTransition) {
		t.Fatalf("double reversal err = %v", err)
	}
	r, _, err := l.RequestRefund("r1", "rk1", payments.BucketRental, vnd(200_000), "hủy", clerk, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.ApproveRefund(r.ID, finance, approval, now); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordCounterRefund(r.ID, finance, now); err != nil {
		t.Fatal(err)
	}
	if s := l.Summary(); s.State != payments.StateRefunded || !s.IsSettled() {
		t.Fatalf("summary = %+v", s)
	}
}

func TestApprovalThresholdAllowsSmallSelfApproval(t *testing.T) {
	l := ledger(t)
	payAtCounter(t, l, "p1", 200_000, 550_000)
	policy := payments.ApprovalPolicy{DistinctApproverAbove: vnd(100_000)}
	small, _, _ := l.RequestRefund("r1", "rk1", payments.BucketSecurity, vnd(100_000), "x", finance, now)
	if err := l.ApproveRefund(small.ID, finance, policy, now); err != nil {
		t.Fatalf("self-approval at threshold: %v", err)
	}
	big, _, _ := l.RequestRefund("r2", "rk2", payments.BucketSecurity, vnd(100_001), "x", finance, now)
	if err := l.ApproveRefund(big.ID, finance, policy, now); !errors.Is(err, payments.ErrApproverMustDiffer) {
		t.Fatalf("self-approval above threshold err = %v", err)
	}
}
