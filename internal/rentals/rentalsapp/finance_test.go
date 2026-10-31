package rentalsapp_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/inventory/inventoryapp"
	"webee_rentor/internal/payments"
	"webee_rentor/internal/platform/postgres/pgtest"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalsapp"
)

// payInFull takes the reservation deposit, the rest of the rent and the
// security deposit at the counter.
func (e *env) payInFull(b *rentals.Booking) {
	e.t.Helper()
	rest, err := b.RentTotal.Sub(b.ReservationDeposit)
	if err != nil {
		e.t.Fatal(err)
	}
	_, err = e.svc.RecordPayment(context.Background(), e.staff, b.ID, rentalsapp.PaymentInput{
		IdempotencyKey: "in-full", Method: payments.MethodCash,
		Portions: []payments.Portion{
			{Bucket: payments.BucketReservation, Amount: b.ReservationDeposit},
			{Bucket: payments.BucketRental, Amount: rest},
			{Bucket: payments.BucketSecurity, Amount: b.SecurityDepositTotal},
		},
	})
	if err != nil {
		e.t.Fatalf("pay in full: %v", err)
	}
}

func (e *env) ledger(id rentals.BookingID) *payments.Ledger {
	e.t.Helper()
	l, err := e.svc.Ledger(context.Background(), e.finance, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return l
}

func refundOf(t *testing.T, l *payments.Ledger, bucket payments.Bucket) payments.Refund {
	t.Helper()
	i := slices.IndexFunc(l.Refunds, func(r payments.Refund) bool { return r.Bucket == bucket })
	if i < 0 {
		t.Fatalf("no %s refund in %+v", bucket, l.Refunds)
	}
	return l.Refunds[i]
}

// cash is a refund handed over at the counter just now.
var cash = payments.RefundPayout{Method: payments.MethodCash}

func wantAmount(t *testing.T, what string, got interface{ Amount() int64 }, want int64) {
	t.Helper()
	if got.Amount() != want {
		t.Errorf("%s = %d, want %d", what, got.Amount(), want)
	}
}

func TestPlacingOpensTheLedgerFromTheSnapshot(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))

	s := e.ledger(b.ID).Summary()
	wantAmount(t, "rent due", s.RentalDue, 50_000)
	wantAmount(t, "security required", s.SecurityRequired, 300_000)
	wantAmount(t, "reservation required", s.ReservationRequired, 15_000)
	if s.State != payments.StateUnpaid {
		t.Errorf("state = %s", s.State)
	}
	if _, err := e.svc.Confirm(ctx, e.staff, b.ID); !errors.Is(err, rentals.ErrInsufficientFunds) {
		t.Fatalf("confirming with nothing paid: err = %v", err)
	}
}

func TestCustomerReadsOnlyTheirOwnLedger(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	a := e.f.User("a", identity.RoleCustomer)
	b := e.f.User("b", identity.RoleCustomer)
	booked := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	if _, err := e.svc.Ledger(ctx, a, booked.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Ledger(ctx, b, booked.ID); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("another customer reading the ledger: err = %v", err)
	}
}

func TestCounterPaymentIsIdempotentAndCappedAtWhatIsOwed(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	in := rentalsapp.PaymentInput{IdempotencyKey: "desk-1", Method: payments.MethodCash,
		Portions: []payments.Portion{{Bucket: payments.BucketSecurity, Amount: pgtest.VND(300_000)}, {Bucket: payments.BucketRental, Amount: pgtest.VND(35_000)}}}

	if _, err := e.svc.RecordPayment(ctx, a, b.ID, in); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("customer recording a payment: err = %v", err)
	}
	first, err := e.svc.RecordPayment(ctx, e.staff, b.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(in.Portions) // the same payment, submitted in another order
	again, err := e.svc.RecordPayment(ctx, e.staff, b.ID, in)
	if err != nil || again.ID != first.ID {
		t.Fatalf("retry = %v, %v; want the first payment back", again.ID, err)
	}
	wantAmount(t, "received", e.ledger(b.ID).Summary().TotalReceived, 335_000)

	in.IdempotencyKey = "desk-2"
	if _, err := e.svc.RecordPayment(ctx, e.staff, b.ID, in); !errors.Is(err, payments.ErrExceedsOutstanding) {
		t.Fatalf("paying twice under another key: err = %v", err)
	}
}

// Two desks collecting the last amount: the booking lock serialises them and
// the second finds nothing left to collect.
func TestConcurrentPaymentsCannotOvercollect(t *testing.T) {
	e := setup(t)
	e.f.Assets(e.c.JacketM, 1)
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = e.svc.RecordPayment(context.Background(), e.staff, b.ID, rentalsapp.PaymentInput{
				IdempotencyKey: string(rune('a' + i)), Method: payments.MethodCash,
				Portions: []payments.Portion{{Bucket: payments.BucketSecurity, Amount: pgtest.VND(300_000)}},
			})
		}(i)
	}
	close(start)
	wg.Wait()
	wins := 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, payments.ErrExceedsOutstanding):
			t.Errorf("err = %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d payments succeeded, want 1", wins)
	}
	wantAmount(t, "security received", e.ledger(b.ID).Summary().SecurityReceived, 300_000)
}

// The snapshot policy decides, so one booking is switched to automatic
// confirmation without touching the active fixture policy.
func (e *env) confirmAutomatically(id rentals.BookingID) {
	e.t.Helper()
	if _, err := e.f.Pool.Exec(context.Background(), `UPDATE bookings SET policy = jsonb_set(policy, '{Confirmation,Mode}', '"AUTO"') WHERE id = $1`, id); err != nil {
		e.t.Fatal(err)
	}
}

func TestAutomaticConfirmationFollowsTheCounterPayment(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.confirmAutomatically(b.ID)

	security := rentalsapp.PaymentInput{IdempotencyKey: "s", Method: payments.MethodCash,
		Portions: []payments.Portion{{Bucket: payments.BucketSecurity, Amount: pgtest.VND(300_000)}}}
	if _, err := e.svc.RecordPayment(ctx, e.staff, b.ID, security); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.Booking(ctx, a, b.ID); got.Status != rentals.StatusPendingConfirmation {
		t.Fatalf("status = %s; the required funds are not in yet", got.Status)
	}
	reservation := rentalsapp.PaymentInput{IdempotencyKey: "r", Method: payments.MethodCash,
		Portions: []payments.Portion{{Bucket: payments.BucketReservation, Amount: pgtest.VND(15_000)}}}
	if _, err := e.svc.RecordPayment(ctx, e.staff, b.ID, reservation); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.Booking(ctx, a, b.ID); got.Status != rentals.StatusConfirmed {
		t.Fatalf("status = %s, want it confirmed with the payment", got.Status)
	}
}

// A hold that ran out before the money came keeps the payment on record and
// the booking unconfirmed; the expiry job then refunds it.
func TestPaymentAfterTheHoldEndedIsRefundedByTheExpiryJob(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.confirmAutomatically(b.ID)
	e.clock.Set(pgtest.Now.Add(31 * time.Minute))
	e.payInFull(b)
	if got, _ := e.svc.Booking(ctx, a, b.ID); got.Status != rentals.StatusPendingConfirmation {
		t.Fatalf("status = %s", got.Status)
	}
	if _, err := e.svc.ExpireHolds(ctx, 1000); err != nil {
		t.Fatal(err)
	}
	l := e.ledger(b.ID)
	wantAmount(t, "cancellation fee", l.Cancellation.Fee, 0)
	wantAmount(t, "rent refund", refundOf(t, l, payments.BucketRental).Amount, 35_000)
	wantAmount(t, "reservation refund", refundOf(t, l, payments.BucketReservation).Amount, 15_000)
	wantAmount(t, "security refund", refundOf(t, l, payments.BucketSecurity).Amount, 300_000)
	if r := refundOf(t, l, payments.BucketRental); r.RequestedBy.Kind != identity.ActorSystem || r.Status != payments.RefundRequested {
		t.Fatalf("refund = %+v, want it requested by the job and awaiting approval", r)
	}
	if n, err := e.svc.ExpireHolds(ctx, 1000); err != nil || n != 0 {
		t.Fatalf("second run = %d, %v", n, err)
	}
	if got := e.ledger(b.ID); len(got.Refunds) != 3 {
		t.Fatalf("a repeated run must not refund again: %d refunds", len(got.Refunds))
	}
}

func TestHandoverAppliesTheReservationToRent(t *testing.T) {
	e := setup(t)
	e.f.Assets(e.c.JacketM, 1)
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.handOver(b, pgtest.Local(t, "2026-11-10 08:30"))
	s := e.ledger(b.ID).Summary()
	wantAmount(t, "reservation applied", s.ReservationApplied, 15_000)
	wantAmount(t, "reservation held", s.ReservationHeld, 0)
	wantAmount(t, "rent outstanding", s.RentalOutstanding, 0)
}

// Funds short at handover: nothing moves, the reservation included.
func TestHandoverWithoutTheDepositChangesNothing(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	asset := e.f.Assets(e.c.JacketM, 1)[0]
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	if _, err := e.svc.RecordPayment(ctx, e.staff, b.ID, rentalsapp.PaymentInput{IdempotencyKey: "r", Method: payments.MethodCash,
		Portions: []payments.Portion{{Bucket: payments.BucketReservation, Amount: pgtest.VND(15_000)}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Confirm(ctx, e.staff, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StartPreparation(ctx, e.staff, b.ID); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(pgtest.Local(t, "2026-11-10 08:30"))
	_, err := e.svc.RecordHandover(ctx, e.staff, b.ID, rentalsapp.HandoverInput{
		Delivered: []rentals.DeliveredItem{{AssetID: asset}}, ChecklistCompleted: true})
	if !errors.Is(err, rentals.ErrInsufficientFunds) {
		t.Fatalf("err = %v", err)
	}
	if s := e.ledger(b.ID).Summary(); s.ReservationApplied.Amount() != 0 || s.ReservationHeld.Amount() != 15_000 {
		t.Fatalf("reservation moved by a failed handover: %+v", s)
	}
	if e.assetStatus(asset) != inventory.AssetReady {
		t.Fatal("asset moved by a failed handover")
	}
}

// Fee from the snapshot tier: the reservation covers it first, the prepaid
// rent and the deposit go to refunds awaiting finance.
func TestCancelChargesTheFeeAndRequestsRefunds(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.payInFull(b)
	if _, err := e.svc.Confirm(ctx, e.staff, b.ID); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(pgtest.Local(t, "2026-11-08 09:00")) // 48 hours before pickup: the 30% tier
	if _, err := e.svc.Cancel(ctx, a, b.ID, rentalsapp.CancelInput{Reason: rentals.CancelCustomerRequest}); err != nil {
		t.Fatal(err)
	}
	l := e.ledger(b.ID)
	wantAmount(t, "fee", l.Cancellation.Fee, 15_000)
	wantAmount(t, "reservation applied", l.Cancellation.ReservationApplied, 15_000)
	rent, security := refundOf(t, l, payments.BucketRental), refundOf(t, l, payments.BucketSecurity)
	wantAmount(t, "rent refund", rent.Amount, 35_000)
	wantAmount(t, "security refund", security.Amount, 300_000)
	if slices.ContainsFunc(l.Refunds, func(r payments.Refund) bool { return r.Bucket == payments.BucketReservation }) {
		t.Fatal("the reservation paid the fee; nothing of it is refundable")
	}

	if _, err := e.svc.ApproveRefund(ctx, e.staff, b.ID, rent.ID); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("staff approving a refund: err = %v", err)
	}
	for _, r := range []payments.Refund{rent, security} {
		if _, err := e.svc.ApproveRefund(ctx, e.finance, b.ID, r.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.RecordCounterRefund(ctx, e.staff, b.ID, r.ID, cash); err != nil {
			t.Fatal(err)
		}
	}
	s := e.ledger(b.ID).Summary()
	if !s.IsSettled() {
		t.Fatalf("ledger not settled after refunds: %+v", s)
	}
	wantAmount(t, "kept", s.RentalDue, 15_000)
}

// D26: inside a paid tier, what decides is whether money came in.
func TestCancellingAnUnpaidHoldOwesNothing(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 2)
	a := e.f.User("a", identity.RoleCustomer)
	unpaid := e.place(a, e.at("2026-11-03", "morning", e.item(e.c.JacketM)))
	paid := e.place(a, e.at("2026-11-03", "morning", e.item(e.c.JacketM)))
	if _, err := e.svc.RecordPayment(ctx, e.staff, paid.ID, rentalsapp.PaymentInput{IdempotencyKey: "r", Method: payments.MethodCash,
		Portions: []payments.Portion{{Bucket: payments.BucketReservation, Amount: pgtest.VND(15_000)}}}); err != nil {
		t.Fatal(err)
	}
	for _, b := range []*rentals.Booking{unpaid, paid} {
		if _, err := e.svc.Cancel(ctx, a, b.ID, rentalsapp.CancelInput{Reason: rentals.CancelCustomerRequest}); err != nil {
			t.Fatal(err)
		}
	}
	l := e.ledger(unpaid.ID)
	wantAmount(t, "unpaid fee", l.Cancellation.Fee, 0)
	wantAmount(t, "unpaid owed", l.Summary().RentalOutstanding, 0)
	if len(l.Refunds) != 0 {
		t.Fatalf("nothing was paid, nothing to refund: %+v", l.Refunds)
	}
	wantAmount(t, "paid fee", e.ledger(paid.ID).Cancellation.Fee, 15_000)
}

func TestLargeRefundNeedsADifferentApprover(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	e.f.Assets(e.c.PantsM, 1)
	e.f.Assets(e.c.BeltStd, 1)
	a := e.f.User("a", identity.RoleCustomer)
	owner := e.f.User("owner", identity.RoleAdmin)
	b := e.place(a, e.at("2026-11-10", "morning", e.set(e.c.JacketM)))
	e.payInFull(b)
	if _, err := e.svc.Cancel(ctx, owner, b.ID, rentalsapp.CancelInput{Reason: rentals.CancelStaffDecision, Note: "áo hỏng"}); err != nil {
		t.Fatal(err)
	}
	security := refundOf(t, e.ledger(b.ID), payments.BucketSecurity)
	wantAmount(t, "security refund", security.Amount, 550_000)
	if _, err := e.svc.ApproveRefund(ctx, owner, b.ID, security.ID); !errors.Is(err, payments.ErrApproverMustDiffer) {
		t.Fatalf("approving your own large refund: err = %v", err)
	}
	if _, err := e.svc.ApproveRefund(ctx, e.finance, b.ID, security.ID); err != nil {
		t.Fatal(err)
	}
}

// The deposit backs fees nobody has assessed yet, so it only leaves through
// settlement while the booking is live.
func TestDepositCannotBeRefundedByHandBeforeSettlement(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	asset := e.f.Assets(e.c.JacketM, 1)[0]
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.inspectedDirty(b, asset)
	for _, bucket := range []payments.Bucket{payments.BucketSecurity, payments.BucketReservation} {
		if _, err := e.svc.RequestRefund(ctx, e.staff, b.ID, rentalsapp.RefundInput{
			IdempotencyKey: string(bucket), Bucket: bucket, Amount: pgtest.VND(1), Reason: "hoàn sớm",
		}); !errors.Is(err, payments.ErrExceedsRefundable) {
			t.Fatalf("%s: err = %v", bucket, err)
		}
	}
	fee := e.proposeCleaning(b, asset, 100_000)
	if _, err := e.svc.DecideFee(ctx, e.finance, fee.ID, rentalsapp.FeeApprove, ""); err != nil {
		t.Fatal(err)
	}
	l, err := e.svc.SettleRental(ctx, e.finance, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantAmount(t, "deposit applied", l.Summary().SecurityApplied, 100_000)
}

func TestNoShowKeepsOnlyTheFee(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.payInFull(b)
	if _, err := e.svc.Confirm(ctx, e.staff, b.ID); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(pgtest.Local(t, "2026-11-10 11:30")) // past pickup + 2 hours
	if _, err := e.svc.Cancel(ctx, e.staff, b.ID, rentalsapp.CancelInput{Reason: rentals.CancelNoShow, Note: "không đến"}); err != nil {
		t.Fatal(err)
	}
	l := e.ledger(b.ID)
	wantAmount(t, "fee", l.Cancellation.Fee, 35_000)
	var refunded int64
	for _, r := range l.Refunds {
		refunded += r.Amount.Amount()
	}
	if kept := l.Summary().TotalReceived.Amount() - refunded; kept != 35_000 {
		t.Fatalf("kept %d, want exactly the fee", kept)
	}
}

func TestRejectedRefundFreesItsAmountForANewRequest(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.payInFull(b)
	if _, err := e.svc.Cancel(ctx, a, b.ID, rentalsapp.CancelInput{Reason: rentals.CancelCustomerRequest}); err != nil {
		t.Fatal(err)
	}
	rent := refundOf(t, e.ledger(b.ID), payments.BucketRental)
	if _, err := e.svc.RejectRefund(ctx, e.finance, b.ID, rent.ID, "sai tài khoản"); err != nil {
		t.Fatal(err)
	}
	in := rentalsapp.RefundInput{IdempotencyKey: "retry", Bucket: payments.BucketRental, Amount: rent.Amount, Reason: "hoàn lại đúng tài khoản"}
	r, err := e.svc.RequestRefund(ctx, e.staff, b.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := e.svc.RequestRefund(ctx, e.staff, b.ID, in); err != nil || again.ID != r.ID {
		t.Fatalf("retry = %v, %v", again.ID, err)
	}
	in.IdempotencyKey = "too-much"
	if _, err := e.svc.RequestRefund(ctx, e.staff, b.ID, in); !errors.Is(err, payments.ErrExceedsRefundable) {
		t.Fatalf("second refund of the same money: err = %v", err)
	}
}

// returned hands the jacket back at when.
func (e *env) returned(b *rentals.Booking, asset inventory.AssetID, when time.Time) {
	e.t.Helper()
	e.clock.Set(when)
	if _, err := e.svc.RecordReturn(context.Background(), e.staff, b.ID, []inventory.AssetID{asset}); err != nil {
		e.t.Fatalf("return: %v", err)
	}
}

func (e *env) fees(id rentals.BookingID) []rentals.FeeAssessment {
	e.t.Helper()
	got, err := e.svc.Fees(context.Background(), e.staff, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return got
}

func TestInspectionMovesTheAsset(t *testing.T) {
	cases := []struct {
		name       string
		conditions []rentals.Condition
		want       inventory.AssetStatus
		closed     bool
	}{
		{name: "clean", want: inventory.AssetReady, closed: true},
		{name: "normal wear", conditions: []rentals.Condition{rentals.CondNormalWear}, want: inventory.AssetReady, closed: true},
		{name: "routine soiling", conditions: []rentals.Condition{rentals.CondRoutineSoiling}, want: inventory.AssetCleaning},
		{name: "abnormal soiling", conditions: []rentals.Condition{rentals.CondAbnormalSoiling}, want: inventory.AssetCleaning},
		{name: "repairable waits for inventory", conditions: []rentals.Condition{rentals.CondMinorRepairable}, want: inventory.AssetNeedsInspection},
		{name: "irreparable waits for inventory", conditions: []rentals.Condition{rentals.CondIrreparable}, want: inventory.AssetNeedsInspection},
		{name: "needs verification stays open", conditions: []rentals.Condition{rentals.CondNeedsVerification}, want: inventory.AssetNeedsInspection},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t)
			ctx := context.Background()
			asset := e.f.Assets(e.c.JacketM, 1)[0]
			a := e.f.User("a", identity.RoleCustomer)
			b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
			e.handOver(b, pgtest.Local(t, "2026-11-10 08:30"))
			e.returned(b, asset, pgtest.Local(t, "2026-11-11 09:00"))
			in := rentalsapp.InspectionInput{Asset: asset, Conditions: c.conditions, Evidence: []string{"photos/1"}}
			if _, err := e.svc.RecordInspection(ctx, a, b.ID, in); !errors.Is(err, identity.ErrForbidden) {
				t.Fatalf("customer inspecting: err = %v", err)
			}
			if _, err := e.svc.RecordInspection(ctx, e.staff, b.ID, in); err != nil {
				t.Fatal(err)
			}
			if got := e.assetStatus(asset); got != c.want {
				t.Fatalf("asset = %s, want %s", got, c.want)
			}
			if closed := e.allocations(b.ID)[0].Status == inventory.AllocationClosed; closed != c.closed {
				t.Fatalf("allocation closed = %v, want %v", closed, c.closed)
			}
		})
	}
}

func TestReclassifyingLaterDoesNotMoveTheAssetAgain(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	asset := e.f.Assets(e.c.JacketM, 1)[0]
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.handOver(b, pgtest.Local(t, "2026-11-10 08:30"))
	e.returned(b, asset, pgtest.Local(t, "2026-11-11 09:00"))
	for _, c := range [][]rentals.Condition{{rentals.CondRoutineSoiling}, {rentals.CondNormalWear}} {
		if _, err := e.svc.RecordInspection(ctx, e.staff, b.ID, rentalsapp.InspectionInput{Asset: asset, Conditions: c}); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.assetStatus(asset); got != inventory.AssetCleaning {
		t.Fatalf("asset = %s; it stays in cleaning until cleaning is confirmed", got)
	}
}

func TestLateFeeIsChargedAtReturnAndKeptToOnePerItem(t *testing.T) {
	e := setup(t)
	asset := e.f.Assets(e.c.JacketM, 1)[0]
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.handOver(b, pgtest.Local(t, "2026-11-10 08:30"))
	e.returned(b, asset, pgtest.Local(t, "2026-11-11 14:00")) // 5 hours late: 50% of the daily rate
	fees := e.fees(b.ID)
	if len(fees) != 1 || fees[0].Kind != rentals.FeeLateReturn || fees[0].Status != rentals.AssessmentApproved || fees[0].Amount.Amount() != 25_000 {
		t.Fatalf("fees = %+v", fees)
	}
	l := e.ledger(b.ID)
	if c, ok := l.ChargeOfAssessment(string(fees[0].ID)); !ok || c.Kind != payments.ChargeLateFee {
		t.Fatalf("charge = %+v, %v", c, ok)
	}
	wantAmount(t, "owed", l.Summary().RentalOutstanding, 25_000)
}

func TestReturnInsideGraceIsFreeUnlessItHitsTheNextBooking(t *testing.T) {
	cases := []struct {
		name string
		next bool
		want int64
	}{
		{name: "no next booking", want: 0},
		{name: "next booking affected", next: true, want: 12_500},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t)
			ctx := context.Background()
			asset := e.f.Assets(e.c.JacketM, 1)[0]
			a := e.f.User("a", identity.RoleCustomer)
			n := e.f.User("n", identity.RoleCustomer)
			b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
			if c.next {
				next := e.place(n, e.at("2026-11-12", "afternoon", e.item(e.c.JacketM)))
				// The turnaround ends at 09:00; this block starts right after it.
				if _, err := e.f.Pool.Exec(ctx, `UPDATE allocations SET blocked_from = $2 WHERE booking_id = $1`,
					next.ID, pgtest.Local(t, "2026-11-12 09:10")); err != nil {
					t.Fatal(err)
				}
			}
			e.handOver(b, pgtest.Local(t, "2026-11-10 08:30"))
			e.returned(b, asset, pgtest.Local(t, "2026-11-11 09:20")) // inside the 30-minute grace
			var total int64
			for _, f := range e.fees(b.ID) {
				total += f.Amount.Amount()
			}
			if total != c.want {
				t.Fatalf("late fees = %d, want %d", total, c.want)
			}
			got, err := e.svc.Booking(ctx, a, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Items[0].AffectedNext != c.next {
				t.Fatalf("affected next = %v", got.Items[0].AffectedNext)
			}
		})
	}
}

func TestLateFeeOfALostItemStopsAtConfirmation(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	asset := e.f.Assets(e.c.JacketM, 1)[0]
	a := e.f.User("a", identity.RoleCustomer)
	owner := e.f.User("owner", identity.RoleAdmin)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.handOver(b, pgtest.Local(t, "2026-11-10 08:30"))
	e.clock.Set(pgtest.Local(t, "2026-11-11 09:00"))
	if _, err := e.svc.ReportMissing(ctx, e.staff, b.ID, asset); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(pgtest.Local(t, "2026-11-13 09:00")) // 48 hours late: two periods
	if _, err := e.svc.ConfirmLost(ctx, owner, b.ID, asset, "khách không tìm thấy"); err != nil {
		t.Fatal(err)
	}
	fees := e.fees(b.ID)
	if len(fees) != 1 || fees[0].Amount.Amount() != 100_000 {
		t.Fatalf("fees = %+v, want 200%% of the 50.000 daily rate", fees)
	}
}

// inspectedDirty returns the jacket on time, found abnormally soiled.
func (e *env) inspectedDirty(b *rentals.Booking, asset inventory.AssetID) {
	e.t.Helper()
	e.handOver(b, pgtest.Local(e.t, "2026-11-10 08:30"))
	e.returned(b, asset, pgtest.Local(e.t, "2026-11-11 09:00"))
	if _, err := e.svc.RecordInspection(context.Background(), e.staff, b.ID, rentalsapp.InspectionInput{
		Asset: asset, Conditions: []rentals.Condition{rentals.CondAbnormalSoiling}, Evidence: []string{"photos/1"},
	}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) proposeCleaning(b *rentals.Booking, asset inventory.AssetID, amount int64) rentals.FeeAssessment {
	e.t.Helper()
	f, err := e.svc.ProposeFee(context.Background(), e.staff, b.ID, rentalsapp.FeeInput{
		Asset: asset, Kind: rentals.FeeCleaning, Amount: pgtest.VND(amount), Basis: "bảng giá vệ sinh"})
	if err != nil {
		e.t.Fatal(err)
	}
	return f
}

// The whole return: fee proposed and approved, the deposit covers it, the rest
// is refunded, and only then can the booking complete.
func TestReturnLoopFromInspectionToCompletion(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	asset := e.f.Assets(e.c.JacketM, 1)[0]
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.inspectedDirty(b, asset)

	fee := e.proposeCleaning(b, asset, 100_000)
	if _, ok := e.ledger(b.ID).ChargeOfAssessment(string(fee.ID)); ok {
		t.Fatal("a proposed fee must not be charged")
	}
	if _, err := e.svc.SettleRental(ctx, e.finance, b.ID); !errors.Is(err, rentals.ErrOpenFeeAssessments) {
		t.Fatalf("settling with a fee open: err = %v", err)
	}
	if _, err := e.svc.DecideFee(ctx, e.staff, fee.ID, rentalsapp.FeeApprove, ""); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("staff approving a fee: err = %v", err)
	}
	if _, err := e.svc.DecideFee(ctx, e.finance, fee.ID, rentalsapp.FeeApprove, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Complete(ctx, e.staff, b.ID); !errors.Is(err, rentals.ErrNotSettled) {
		t.Fatalf("completing with money open: err = %v", err)
	}

	l, err := e.svc.SettleRental(ctx, e.finance, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantAmount(t, "deposit applied", l.Summary().SecurityApplied, 100_000)
	refund := refundOf(t, l, payments.BucketSecurity)
	wantAmount(t, "deposit refund", refund.Amount, 200_000)
	if again, err := e.svc.SettleRental(ctx, e.finance, b.ID); err != nil || len(again.Refunds) != 1 || len(again.Deductions) != 1 {
		t.Fatalf("settling twice: %v, %d refunds, %d deductions", err, len(again.Refunds), len(again.Deductions))
	}
	if _, err := e.svc.Complete(ctx, e.staff, b.ID); !errors.Is(err, rentals.ErrNotSettled) {
		t.Fatalf("completing before the refund is paid: err = %v", err)
	}
	if _, err := e.svc.ApproveRefund(ctx, e.finance, b.ID, refund.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RecordCounterRefund(ctx, e.staff, b.ID, refund.ID, cash); err != nil {
		t.Fatal(err)
	}
	done, err := e.svc.Complete(ctx, e.staff, b.ID)
	if err != nil || done.Status != rentals.StatusCompleted {
		t.Fatalf("complete = %v, %v", done, err)
	}
	if e.assetStatus(asset) != inventory.AssetCleaning {
		t.Fatal("completing a booking must not make its asset READY")
	}
	if _, err := e.svc.DecideFee(ctx, e.staff, fee.ID, rentalsapp.FeeDispute, "muộn"); !errors.Is(err, rentals.ErrInvalidTransition) {
		t.Fatalf("disputing a fee after completion: err = %v", err)
	}
	if got := e.fees(b.ID); got[0].Status != rentals.AssessmentApproved {
		t.Fatalf("a refused dispute changed the fee to %s", got[0].Status)
	}
}

func TestDamageBeyondTheDepositStaysOwedUntilPaid(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	asset := e.f.Assets(e.c.JacketM, 1)[0]
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.inspectedDirty(b, asset)
	fee := e.proposeCleaning(b, asset, 400_000)
	if _, err := e.svc.DecideFee(ctx, e.finance, fee.ID, rentalsapp.FeeApprove, ""); err != nil {
		t.Fatal(err)
	}
	l, err := e.svc.SettleRental(ctx, e.finance, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	s := l.Summary()
	wantAmount(t, "deposit applied", s.SecurityApplied, 300_000)
	wantAmount(t, "owed", s.RentalOutstanding, 100_000)
	if len(l.Refunds) != 0 {
		t.Fatalf("nothing is left to refund: %+v", l.Refunds)
	}
	if _, err := e.svc.Complete(ctx, e.staff, b.ID); !errors.Is(err, rentals.ErrNotSettled) {
		t.Fatalf("err = %v", err)
	}
	if _, err := e.svc.RecordPayment(ctx, e.staff, b.ID, rentalsapp.PaymentInput{IdempotencyKey: "rest", Method: payments.MethodCash,
		Portions: []payments.Portion{{Bucket: payments.BucketRental, Amount: pgtest.VND(100_000)}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Complete(ctx, e.staff, b.ID); err != nil {
		t.Fatal(err)
	}
}

// A disputed fee holds the deposit until decided; waiving takes it off the
// ledger and the whole deposit goes back.
func TestDisputedFeeHoldsTheDepositUntilWaived(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	asset := e.f.Assets(e.c.JacketM, 1)[0]
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.inspectedDirty(b, asset)
	fee := e.proposeCleaning(b, asset, 100_000)
	if _, err := e.svc.DecideFee(ctx, e.finance, fee.ID, rentalsapp.FeeApprove, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DecideFee(ctx, e.staff, fee.ID, rentalsapp.FeeDispute, "khách nói vết bẩn có sẵn"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SettleRental(ctx, e.finance, b.ID); !errors.Is(err, rentals.ErrOpenFeeAssessments) {
		t.Fatalf("settling during a dispute: err = %v", err)
	}
	if _, err := e.svc.DecideFee(ctx, e.finance, fee.ID, rentalsapp.FeeWaive, "ảnh lúc giao đã có vết"); err != nil {
		t.Fatal(err)
	}
	l, err := e.svc.SettleRental(ctx, e.finance, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantAmount(t, "fees due", l.Summary().RentalDue, 50_000)
	wantAmount(t, "deposit refund", refundOf(t, l, payments.BucketSecurity).Amount, 300_000)
}

func TestAdjustedFeeReplacesTheCharge(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	asset := e.f.Assets(e.c.JacketM, 1)[0]
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.inspectedDirty(b, asset)
	fee := e.proposeCleaning(b, asset, 100_000)
	if _, err := e.svc.DecideFee(ctx, e.finance, fee.ID, rentalsapp.FeeApprove, ""); err != nil {
		t.Fatal(err)
	}
	lower, err := e.svc.AdjustFee(ctx, e.finance, fee.ID, pgtest.VND(60_000), "giảm theo hóa đơn giặt")
	if err != nil {
		t.Fatal(err)
	}
	if lower.Supersedes != fee.ID {
		t.Fatalf("adjustment = %+v", lower)
	}
	wantAmount(t, "owed", e.ledger(b.ID).Summary().RentalOutstanding, 60_000)
	fees := e.fees(b.ID)
	if i := slices.IndexFunc(fees, func(f rentals.FeeAssessment) bool { return f.ID == fee.ID }); fees[i].Status != rentals.AssessmentSuperseded || fees[i].SupersededBy != lower.ID {
		t.Fatalf("original = %+v", fees[i])
	}
}

// settledFee leaves a 100.000đ cleaning fee covered by the deposit and the
// other 200.000đ awaiting approval as a refund.
func (e *env) settledFee(b *rentals.Booking, asset inventory.AssetID) rentals.FeeAssessment {
	e.t.Helper()
	ctx := context.Background()
	e.inspectedDirty(b, asset)
	fee := e.proposeCleaning(b, asset, 100_000)
	if _, err := e.svc.DecideFee(ctx, e.finance, fee.ID, rentalsapp.FeeApprove, ""); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.svc.SettleRental(ctx, e.finance, b.ID); err != nil {
		e.t.Fatal(err)
	}
	return fee
}

func lastRefund(t *testing.T, l *payments.Ledger) payments.Refund {
	t.Helper()
	got := slices.MaxFunc(l.Refunds, func(a, b payments.Refund) int { return a.RequestedAt.Compare(b.RequestedAt) })
	return got
}

// D29: the fee comes off the ledger and the deposit gets the money back.
func TestWaivingAFeeTheDepositCoveredGivesTheMoneyBack(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	asset := e.f.Assets(e.c.JacketM, 1)[0]
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	fee := e.settledFee(b, asset)
	first := refundOf(t, e.ledger(b.ID), payments.BucketSecurity)

	e.clock.Set(e.clock.Now().Add(time.Minute))
	if _, err := e.svc.DecideFee(ctx, e.staff, fee.ID, rentalsapp.FeeWaive, "thu sai"); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("staff waiving: err = %v", err)
	}
	if _, err := e.svc.DecideFee(ctx, e.finance, fee.ID, rentalsapp.FeeWaive, "ảnh lúc giao đã có vết"); err != nil {
		t.Fatal(err)
	}
	l := e.ledger(b.ID)
	s := l.Summary()
	wantAmount(t, "deposit applied", s.SecurityApplied, 0)
	wantAmount(t, "rent and fees due", s.RentalDue, 50_000)
	if l.Deductions[0].Status != payments.DeductionReversed {
		t.Fatalf("deduction = %+v", l.Deductions[0])
	}
	back := lastRefund(t, l)
	if len(l.Refunds) != 2 || back.Amount.Amount() != 100_000 || back.Bucket != payments.BucketSecurity {
		t.Fatalf("refunds = %+v", l.Refunds)
	}
	for _, r := range []payments.Refund{first, back} {
		if _, err := e.svc.RecordCounterRefund(ctx, e.staff, b.ID, r.ID, cash); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.svc.Complete(ctx, e.staff, b.ID); err != nil {
		t.Fatal(err)
	}
}

// D30, D32: a shop run by one person. The same person cancels and hands the
// money back, above the two-person limit, and that confirmation is all it takes.
func TestCounterRefundsTakeOneConfirmation(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	e.f.Assets(e.c.PantsM, 1)
	e.f.Assets(e.c.BeltStd, 1)
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.set(e.c.JacketM)))
	e.payInFull(b)
	if _, err := e.svc.Cancel(ctx, e.staff, b.ID, rentalsapp.CancelInput{Reason: rentals.CancelStaffDecision, Note: "áo hỏng"}); err != nil {
		t.Fatal(err)
	}
	l := e.ledger(b.ID)
	security := refundOf(t, l, payments.BucketSecurity)
	wantAmount(t, "security refund", security.Amount, 550_000)
	if _, err := e.svc.RecordCounterRefund(ctx, a, b.ID, security.ID, cash); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("customer recording a refund: err = %v", err)
	}
	paidAt := e.clock.Now().Add(-time.Hour)
	if _, err := e.svc.RecordCounterRefund(ctx, e.staff, b.ID, security.ID, payments.RefundPayout{Method: payments.MethodCash, Reference: "PC-1", PaidAt: paidAt}); err != nil {
		t.Fatal(err)
	}
	rent := refundOf(t, l, payments.BucketRental)
	if _, err := e.svc.RecordCounterRefund(ctx, e.staff, b.ID, rent.ID, payments.RefundPayout{Method: payments.MethodBankTransfer}); err == nil {
		t.Fatal("a transfer without its bank code was accepted")
	}
	if _, err := e.svc.RecordCounterRefund(ctx, e.staff, b.ID, rent.ID, payments.RefundPayout{Method: payments.MethodBankTransfer, Reference: "FT123"}); err != nil {
		t.Fatal(err)
	}
	got := refundOf(t, e.ledger(b.ID), payments.BucketSecurity)
	if got.Status != payments.RefundSucceeded || got.DecidedBy != nil || !got.CompletedAt.Equal(paidAt) || got.Reference != "PC-1" {
		t.Fatalf("paid refund = %+v", got)
	}
}

func TestLoweringAFeeTheDepositCoveredReturnsTheDifference(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	asset := e.f.Assets(e.c.JacketM, 1)[0]
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	fee := e.settledFee(b, asset)
	e.clock.Set(e.clock.Now().Add(time.Minute))
	if _, err := e.svc.AdjustFee(ctx, e.finance, fee.ID, pgtest.VND(60_000), "theo hóa đơn giặt"); err != nil {
		t.Fatal(err)
	}
	l := e.ledger(b.ID)
	wantAmount(t, "deposit applied", l.Summary().SecurityApplied, 60_000)
	wantAmount(t, "owed", l.Summary().RentalOutstanding, 0)
	if back := lastRefund(t, l); len(l.Refunds) != 2 || back.Amount.Amount() != 40_000 {
		t.Fatalf("refunds = %+v", l.Refunds)
	}
}

// The booking stays closed; the correction is new records on its ledger.
func TestCorrectingAFeeAfterCompletion(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	asset := e.f.Assets(e.c.JacketM, 1)[0]
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	fee := e.settledFee(b, asset)
	first := refundOf(t, e.ledger(b.ID), payments.BucketSecurity)
	if _, err := e.svc.ApproveRefund(ctx, e.finance, b.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RecordCounterRefund(ctx, e.staff, b.ID, first.ID, cash); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Complete(ctx, e.staff, b.ID); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(pgtest.Local(t, "2026-11-20 10:00"))
	if _, err := e.svc.DecideFee(ctx, e.finance, fee.ID, rentalsapp.FeeWaive, "khách khiếu nại đúng"); err != nil {
		t.Fatal(err)
	}
	l := e.ledger(b.ID)
	back := lastRefund(t, l)
	if len(l.Refunds) != 2 || back.Amount.Amount() != 100_000 {
		t.Fatalf("refunds = %+v", l.Refunds)
	}
	if _, err := e.svc.RecordCounterRefund(ctx, e.staff, b.ID, back.ID, cash); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.Booking(ctx, a, b.ID); got.Status != rentals.StatusCompleted {
		t.Fatalf("status = %s", got.Status)
	}
	if s := e.ledger(b.ID).Summary(); !s.IsSettled() {
		t.Fatalf("ledger not settled: %+v", s)
	}
}

// A fee the customer paid in cash touched no deposit, so nothing was reversed;
// completion alone is what tells the money must go back.
func TestCorrectingACashPaidFeeAfterCompletion(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	asset := e.f.Assets(e.c.JacketM, 1)[0]
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.inspectedDirty(b, asset)
	fee := e.proposeCleaning(b, asset, 100_000)
	if _, err := e.svc.DecideFee(ctx, e.finance, fee.ID, rentalsapp.FeeApprove, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RecordPayment(ctx, e.staff, b.ID, rentalsapp.PaymentInput{IdempotencyKey: "fee", Method: payments.MethodCash,
		Portions: []payments.Portion{{Bucket: payments.BucketRental, Amount: pgtest.VND(100_000)}}}); err != nil {
		t.Fatal(err)
	}
	l, err := e.svc.SettleRental(ctx, e.finance, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	deposit := refundOf(t, l, payments.BucketSecurity)
	if len(l.Deductions) != 0 {
		t.Fatalf("the fee was paid, the deposit must not cover it: %+v", l.Deductions)
	}
	if _, err := e.svc.ApproveRefund(ctx, e.finance, b.ID, deposit.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RecordCounterRefund(ctx, e.staff, b.ID, deposit.ID, cash); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Complete(ctx, e.staff, b.ID); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(e.clock.Now().Add(time.Minute))
	if _, err := e.svc.DecideFee(ctx, e.finance, fee.ID, rentalsapp.FeeWaive, "khách khiếu nại đúng"); err != nil {
		t.Fatal(err)
	}
	l = e.ledger(b.ID)
	if back := lastRefund(t, l); len(l.Refunds) != 2 || back.Bucket != payments.BucketRental || back.Amount.Amount() != 100_000 {
		t.Fatalf("refunds = %+v", l.Refunds)
	}
}

// D30 + D31: one owner runs a whole rental with a loss above every limit,
// from the booking to completion, without anyone else.
func TestOnePersonShopRunsARentalAlone(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	owner := e.f.User("owner", identity.RoleAdmin)
	e.staff, e.finance = owner, owner
	asset := e.f.Assets(e.c.JacketM, 1)[0]
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.inspectedDirty(b, asset)
	fee := e.proposeCleaning(b, asset, 600_000)
	if _, err := e.svc.DecideFee(ctx, owner, fee.ID, rentalsapp.FeeApprove, ""); err != nil {
		t.Fatalf("approving their own large fee: %v", err)
	}
	l, err := e.svc.SettleRental(ctx, owner, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantAmount(t, "deposit applied", l.Summary().SecurityApplied, 300_000)
	wantAmount(t, "owed", l.Summary().RentalOutstanding, 300_000)
	if _, err := e.svc.RecordPayment(ctx, owner, b.ID, rentalsapp.PaymentInput{IdempotencyKey: "rest", Method: payments.MethodCash,
		Portions: []payments.Portion{{Bucket: payments.BucketRental, Amount: pgtest.VND(300_000)}}}); err != nil {
		t.Fatal(err)
	}
	if done, err := e.svc.Complete(ctx, owner, b.ID); err != nil || done.Status != rentals.StatusCompleted {
		t.Fatalf("complete = %v, %v", done, err)
	}
}

// Finance alone stays below the limit: fee.approve_high is still required.
func TestLargeFeeStillNeedsTheHighApprovalPermission(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	asset := e.f.Assets(e.c.JacketM, 1)[0]
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.inspectedDirty(b, asset)
	fee := e.proposeCleaning(b, asset, 600_000)
	if _, err := e.svc.DecideFee(ctx, e.finance, fee.ID, rentalsapp.FeeApprove, ""); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("err = %v", err)
	}
}

// Another fee still open keeps the deposit in place: settlement closes it once
// that fee is decided.
func TestReversalWaitsForAnotherOpenFee(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	asset := e.f.Assets(e.c.JacketM, 1)[0]
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	fee := e.settledFee(b, asset)
	if _, err := e.svc.RecordInspection(ctx, e.staff, b.ID, rentalsapp.InspectionInput{Asset: asset, Evidence: []string{"photos/2"},
		Conditions: []rentals.Condition{rentals.CondAbnormalSoiling, rentals.CondMinorRepairable}}); err != nil {
		t.Fatal(err)
	}
	repair, err := e.svc.ProposeFee(ctx, e.staff, b.ID, rentalsapp.FeeInput{Asset: asset, Kind: rentals.FeeDamageRepair, Amount: pgtest.VND(50_000), Basis: "báo giá"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DecideFee(ctx, e.finance, fee.ID, rentalsapp.FeeWaive, "thu sai"); err != nil {
		t.Fatal(err)
	}
	if got := e.ledger(b.ID).Refunds; len(got) != 1 {
		t.Fatalf("refunded with a fee still open: %+v", got)
	}
	if _, err := e.svc.DecideFee(ctx, e.finance, repair.ID, rentalsapp.FeeApprove, ""); err != nil {
		t.Fatal(err)
	}
	l, err := e.svc.SettleRental(ctx, e.finance, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantAmount(t, "deposit applied", l.Summary().SecurityApplied, 50_000)
}

func (e *env) requestChange(p identity.Principal, b *rentals.Booking, in rentalsapp.ModificationInput) *rentals.Modification {
	e.t.Helper()
	ctx := context.Background()
	m, err := e.svc.RequestModification(ctx, p, b.ID, in)
	if err != nil {
		e.t.Fatal(err)
	}
	if m, err = e.svc.QuoteModification(ctx, p, m.ID); err != nil {
		e.t.Fatal(err)
	}
	return m
}

func (e *env) threeDays(b *rentals.Booking) rentalsapp.ModificationInput {
	return rentalsapp.ModificationInput{NewPackageID: e.c.ThreeDays.ID, Reason: "thuê thêm",
		Changes: []rentalsapp.LineChangeInput{{LineID: "L1", Line: e.item(e.c.JacketM)}}}
}

func TestSizeChangeSwapsTheAllocationAtConfirmation(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	e.f.Assets(e.c.JacketL, 1)
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	old := e.active(b.ID)[0]
	m := e.requestChange(a, b, rentalsapp.ModificationInput{Reason: "size",
		Changes: []rentalsapp.LineChangeInput{{LineID: "L1", Line: e.item(e.c.JacketL)}}})
	if _, err := e.svc.ConfirmModification(ctx, a, m.ID); err != nil {
		t.Fatal(err)
	}
	active := e.active(b.ID)
	if len(active) != 1 || active[0].ID == old.ID || active[0].VariantID != e.c.JacketL || active[0].Tentative() {
		t.Fatalf("allocations = %+v, want one regular allocation of size L", active)
	}
	got, _ := e.svc.Booking(ctx, a, b.ID)
	if got.OpenModification != "" || got.Lines[0].Components[0].VariantID != e.c.JacketL || got.SelfServiceSizeChanges != 1 {
		t.Fatalf("booking = %+v", got)
	}
	if l := e.ledger(b.ID); l.Staged != nil || len(l.CommittedRepricings) != 1 {
		t.Fatalf("ledger staged %+v committed %v", l.Staged, l.CommittedRepricings)
	}
}

func TestPackageChangeNeedsTheIncreasePaidThenRepricesTheLedger(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	own := e.active(b.ID)[0]
	m := e.requestChange(a, b, e.threeDays(b))
	wantAmount(t, "required payment", m.RequiredPayment, 80_000)

	if _, err := e.svc.ConfirmModification(ctx, a, m.ID); !errors.Is(err, rentals.ErrInsufficientFunds) {
		t.Fatalf("confirming unpaid: err = %v", err)
	}
	if _, err := e.svc.RecordPayment(ctx, e.staff, b.ID, rentalsapp.PaymentInput{IdempotencyKey: "extra", Method: payments.MethodCash,
		ModificationRef: m.ID, Portions: []payments.Portion{{Bucket: payments.BucketRental, Amount: pgtest.VND(80_000)}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ConfirmModification(ctx, a, m.ID); err != nil {
		t.Fatal(err)
	}
	active := e.active(b.ID)
	if len(active) != 1 || active[0].ID != own.ID || !active[0].Block.End().After(own.Block.End()) {
		t.Fatalf("allocations = %+v; the same allocation must stretch", active)
	}
	got, _ := e.svc.Booking(ctx, a, b.ID)
	wantAmount(t, "rent", got.RentTotal, 130_000)
	wantAmount(t, "rent due", e.ledger(b.ID).Summary().RentalDue, 130_000)
}

func TestAbandonedChangeRefundsWhatWasPaidForIt(t *testing.T) {
	end := map[string]func(*env, *rentals.Modification){
		"rejected": func(e *env, m *rentals.Modification) {
			if _, err := e.svc.EndModification(context.Background(), e.staff, m.ID, rentalsapp.ModificationRejected, "hết hàng"); err != nil {
				e.t.Fatal(err)
			}
		},
		"expired": func(e *env, m *rentals.Modification) {
			e.clock.Set(pgtest.Now.Add(31 * time.Minute))
			if _, err := e.svc.ExpireModificationHolds(context.Background(), 1000); err != nil {
				e.t.Fatal(err)
			}
		},
	}
	for name, fn := range end {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			ctx := context.Background()
			e.f.Assets(e.c.JacketM, 1)
			a := e.f.User("a", identity.RoleCustomer)
			b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
			e.payInFull(b)
			m := e.requestChange(e.staff, b, e.threeDays(b))
			if _, err := e.svc.RecordPayment(ctx, e.staff, b.ID, rentalsapp.PaymentInput{IdempotencyKey: "extra", Method: payments.MethodCash,
				ModificationRef: m.ID, Portions: []payments.Portion{{Bucket: payments.BucketRental, Amount: pgtest.VND(80_000)}}}); err != nil {
				t.Fatal(err)
			}
			fn(e, m)
			l := e.ledger(b.ID)
			if l.Staged != nil {
				t.Fatal("the staged prices must be gone")
			}
			wantAmount(t, "refund", refundOf(t, l, payments.BucketRental).Amount, 80_000)
			if slices.ContainsFunc(l.Refunds, func(r payments.Refund) bool { return r.Bucket == payments.BucketSecurity }) {
				t.Fatal("the booking still needs its deposit")
			}
		})
	}
}

func TestCancellingWithAChangeOpenDropsItsPrices(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.payInFull(b)
	e.requestChange(a, b, e.threeDays(b))
	if _, err := e.svc.Cancel(ctx, a, b.ID, rentalsapp.CancelInput{Reason: rentals.CancelCustomerRequest}); err != nil {
		t.Fatal(err)
	}
	l := e.ledger(b.ID)
	if l.Staged != nil || len(l.Refunds) != 3 {
		t.Fatalf("staged %+v, refunds %d", l.Staged, len(l.Refunds))
	}
}

func TestReplaceAssetBeforeHandover(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 2)
	a := e.f.User("a", identity.RoleCustomer)
	mgr := e.f.User("mgr", identity.RoleInventoryManager)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	old := e.active(b.ID)[0]
	if _, err := e.inv.ChangeStatus(ctx, mgr, old.AssetID, inventoryapp.StatusInput{Action: inventory.ActionSendToRepair, Reason: "rách tay áo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ReplaceAsset(ctx, a, b.ID, old.ID, "x"); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("customer replacing: err = %v", err)
	}
	if _, err := e.svc.ReplaceAsset(ctx, e.staff, b.ID, old.ID, ""); !errors.Is(err, rentals.ErrReasonRequired) {
		t.Fatalf("without a reason: err = %v", err)
	}
	if _, err := e.svc.ReplaceAsset(ctx, e.staff, b.ID, old.ID, "áo hỏng"); err != nil {
		t.Fatal(err)
	}
	active := e.active(b.ID)
	if len(active) != 1 || active[0].AssetID == old.AssetID || active[0].Block != old.Block || active[0].LineRef != old.LineRef {
		t.Fatalf("allocations = %+v", active)
	}
}

func TestReplaceAssetWithNothingFreeKeepsTheAllocation(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	old := e.active(b.ID)[0]
	_, err := e.svc.ReplaceAsset(ctx, e.staff, b.ID, old.ID, "áo hỏng")
	wantShort(t, err)
	if active := e.active(b.ID); len(active) != 1 || active[0].ID != old.ID {
		t.Fatalf("allocations = %+v", active)
	}
}

func TestOverdueListShowsOnlyItemsStillOut(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 2)
	a := e.f.User("a", identity.RoleCustomer)
	out := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	back := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.handOver(out, pgtest.Local(t, "2026-11-10 08:30"))
	e.clock.Set(pgtest.Now) // the second hold is confirmed inside its own window
	e.handOver(back, pgtest.Local(t, "2026-11-10 08:40"))
	e.returned(back, e.active(back.ID)[0].AssetID, pgtest.Local(t, "2026-11-11 09:00"))

	if _, err := e.svc.OverdueBookings(ctx, a); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("customer listing overdue bookings: err = %v", err)
	}
	e.clock.Set(pgtest.Local(t, "2026-11-11 09:20")) // still inside the grace period
	if got, err := e.svc.OverdueBookings(ctx, e.staff); err != nil || slices.ContainsFunc(got, func(b *rentals.Booking) bool { return b.ID == out.ID }) {
		t.Fatalf("inside grace: %v, %v", got, err)
	}
	e.clock.Set(pgtest.Local(t, "2026-11-11 10:00"))
	got, err := e.svc.OverdueBookings(ctx, e.staff)
	if err != nil {
		t.Fatal(err)
	}
	has := func(id rentals.BookingID) bool {
		return slices.ContainsFunc(got, func(b *rentals.Booking) bool { return b.ID == id })
	}
	if !has(out.ID) || has(back.ID) {
		t.Fatalf("overdue = %+v", got)
	}
}

type recordedNotice struct {
	code   string
	action rentals.Action
}

// notices records what customers were told; fail makes every send fail.
type notices struct {
	mu   sync.Mutex
	got  []recordedNotice
	fail bool
}

func (n *notices) BookingChanged(_ context.Context, _ identity.UserID, code string, action rentals.Action, _ time.Time) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.got = append(n.got, recordedNotice{code, action})
	if n.fail {
		return errors.New("smtp down")
	}
	return nil
}

func (n *notices) of(code string) []rentals.Action {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []rentals.Action
	for _, r := range n.got {
		if r.code == code {
			out = append(out, r.action)
		}
	}
	return out
}

func (e *env) withNotices(n *notices) {
	loc, err := businesstime.LoadLocation()
	if err != nil {
		e.t.Fatal(err)
	}
	e.svc = rentalsapp.New(e.f.Pool, loc, rentalsapp.WithClock(e.clock.Now), rentalsapp.WithNotifier(n))
}

func TestCustomerIsToldAfterTheChangeCommits(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	n := &notices{}
	e.withNotices(n)
	e.f.Assets(e.c.JacketM, 2)
	a := e.f.User("a", identity.RoleCustomer)

	confirmed := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.confirmAutomatically(confirmed.ID)
	e.payInFull(confirmed) // confirmed by the payment, not by a person
	if _, err := e.svc.Cancel(ctx, a, confirmed.ID, rentalsapp.CancelInput{Reason: rentals.CancelCustomerRequest}); err != nil {
		t.Fatal(err)
	}
	if got := n.of(confirmed.Code); !slices.Equal(got, []rentals.Action{rentals.ActionConfirm, rentals.ActionCancel}) {
		t.Fatalf("notices = %v", got)
	}

	refused := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	if _, err := e.svc.Confirm(ctx, e.staff, refused.ID); !errors.Is(err, rentals.ErrInsufficientFunds) {
		t.Fatalf("err = %v", err)
	}
	if got := n.of(refused.Code); len(got) != 0 {
		t.Fatalf("a refused change told the customer %v", got)
	}

	e.clock.Set(pgtest.Now.Add(31 * time.Minute))
	if _, err := e.svc.ExpireHolds(ctx, 1000); err != nil {
		t.Fatal(err)
	}
	if got := n.of(refused.Code); !slices.Equal(got, []rentals.Action{rentals.ActionExpire}) {
		t.Fatalf("notices = %v", got)
	}
}

func TestFailedNotificationDoesNotUndoTheChange(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.withNotices(&notices{fail: true})
	e.f.Assets(e.c.JacketM, 1)
	a := e.f.User("a", identity.RoleCustomer)
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	got, err := e.svc.Cancel(ctx, a, b.ID, rentalsapp.CancelInput{Reason: rentals.CancelCustomerRequest})
	if err != nil || got.Status != rentals.StatusCancelled {
		t.Fatalf("cancel = %v, %v", got, err)
	}
}
