package payments_test

import (
	"errors"
	"testing"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/payments"
)

// reservationLedger: 200k rent, 550k security, 60k reservation deposit.
func reservationLedger(t *testing.T, credit bool) *payments.Ledger {
	t.Helper()
	l, err := payments.OpenLedger(payments.OpenRequest{
		BookingRef: "bk-1", Rent: vnd(200_000), Security: vnd(550_000), Reservation: vnd(60_000),
		RentChargeID: "ch-rent", SecurityChargeID: "ch-dep", ReservationChargeID: "ch-res",
		CreditReservationToRent: credit, Actor: system, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func pay(t *testing.T, l *payments.Ledger, id string, ps ...payments.Portion) error {
	t.Helper()
	_, _, err := l.RecordCounterPayment(payments.NewPaymentRequest{
		ID: payments.PaymentID(id), IdempotencyKey: "key-" + id, Method: payments.MethodCash,
		Portions: ps, Actor: clerk, Now: now,
	})
	return err
}

func reservation(v int64) payments.Portion {
	return payments.Portion{Bucket: payments.BucketReservation, Amount: vnd(v)}
}

// A credited reservation is part of the rent: however it is split across
// portions or checkouts, the total never exceeds the rent.
func TestCreditedReservationCannotOvercollectRent(t *testing.T) {
	start := func(l *payments.Ledger, id string, ps ...payments.Portion) error {
		_, _, err := l.StartGatewayPayment(payments.NewPaymentRequest{
			ID: payments.PaymentID(id), IdempotencyKey: "key-" + id, Method: payments.MethodGateway, Provider: "psp",
			Portions: ps, Actor: identity.UserActor("cust-a"), Now: now,
		})
		return err
	}
	rental := func(v int64) payments.Portion { return payments.Portion{Bucket: payments.BucketRental, Amount: vnd(v)} }

	if err := start(reservationLedger(t, true), "p1", rental(200_000), reservation(60_000)); !errors.Is(err, payments.ErrExceedsOutstanding) {
		t.Fatalf("one payment for rent + reservation err = %v", err)
	}
	if err := start(reservationLedger(t, true), "p1", reservation(60_000), rental(200_000)); !errors.Is(err, payments.ErrExceedsOutstanding) {
		t.Fatalf("portion order must not matter, err = %v", err)
	}
	if err := start(reservationLedger(t, true), "p1", reservation(60_000), rental(140_000)); err != nil {
		t.Fatalf("exact split of the rent: %v", err)
	}
	if err := start(reservationLedger(t, true), "p1", rental(140_000), reservation(60_000)); err != nil {
		t.Fatalf("exact split with the rent listed first: %v", err)
	}

	l := reservationLedger(t, true)
	if err := start(l, "p1", reservation(60_000)); err != nil {
		t.Fatal(err)
	}
	if err := start(l, "p2", rental(200_000)); !errors.Is(err, payments.ErrExceedsOutstanding) {
		t.Fatalf("rent while the reservation is in checkout err = %v", err)
	}
	if err := start(l, "p3", rental(140_000)); err != nil {
		t.Fatalf("rest of the rent: %v", err)
	}

	// A reservation kept as a separate guarantee is paid on top of the rent.
	if err := start(reservationLedger(t, false), "p1", rental(200_000), reservation(60_000)); err != nil {
		t.Fatalf("separate guarantee: %v", err)
	}
}

// A credited reservation still held will pay rent, so rent prepaid on top of
// it is free to refund now rather than after the return.
func TestRentPaidOnTopOfACreditedReservationIsRefundable(t *testing.T) {
	l := reservationLedger(t, true) // 200k rent, 60k reservation
	if err := pay(t, l, "p1", reservation(60_000), payments.Portion{Bucket: payments.BucketRental, Amount: vnd(140_000)}); err != nil {
		t.Fatal(err)
	}
	if got := l.Refundable(payments.BucketRental); !got.IsZero() {
		t.Fatalf("refundable when paid exactly = %s", got)
	}
	if _, err := l.ReduceRent("complaint-1", "ch-rent-2", vnd(150_000), "giao thiếu", clerk, now); err != nil {
		t.Fatal(err)
	}
	if got := l.Refundable(payments.BucketRental); got.Amount() != 50_000 {
		t.Fatalf("refundable = %s, want the 50.000 the rent went down", got)
	}
}

func refundNow(t *testing.T, l *payments.Ledger, id string, bucket payments.Bucket, amount int64) {
	t.Helper()
	r, _, err := l.RequestRefund(payments.RefundID(id), id, bucket, vnd(amount), "hoàn", clerk, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.ApproveRefund(r.ID, finance, approval, now); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordCounterRefund(r.ID, cashNow, clerk, now); err != nil {
		t.Fatal(err)
	}
}

func TestReservationAppliedToRent(t *testing.T) {
	l := reservationLedger(t, true)
	if err := pay(t, l, "p1", reservation(60_000)); err != nil {
		t.Fatal(err)
	}
	s := l.Summary()
	if s.ReservationHeld.Amount() != 60_000 || s.ReservationCredit.Amount() != 60_000 || s.RentalCoveredWithCredit.Amount() != 60_000 {
		t.Fatalf("after reservation: %+v", s)
	}
	// Only the rent remaining after the reservation may be collected.
	if err := pay(t, l, "p2", portions(200_000, 0)...); !errors.Is(err, payments.ErrExceedsOutstanding) {
		t.Fatalf("collecting full rent on top of the reservation err = %v", err)
	}
	if err := pay(t, l, "p2", portions(140_000, 550_000)...); err != nil {
		t.Fatal(err)
	}
	if s := l.Summary(); s.State != payments.StatePaid || s.RentalCoveredWithCredit.Amount() != 200_000 {
		t.Fatalf("before handover: state=%s covered=%d", s.State, s.RentalCoveredWithCredit.Amount())
	}

	r, applied, err := l.ApplyReservationToRent("res-apply", clerk, now)
	if err != nil || !applied || r.Amount.Amount() != 60_000 {
		t.Fatalf("apply = %+v %v %v", r, applied, err)
	}
	if _, applied, err := l.ApplyReservationToRent("res-apply-2", clerk, now); err != nil || applied {
		t.Fatalf("second apply = %v, %v; want no-op", applied, err)
	}
	s = l.Summary()
	if !s.ReservationHeld.IsZero() || !s.RentalOutstanding.IsZero() || s.ReservationApplied.Amount() != 60_000 {
		t.Fatalf("after apply: %+v", s)
	}
	refundNow(t, l, "r1", payments.BucketSecurity, 550_000)
	if !l.Summary().IsSettled() {
		t.Fatalf("should be settled: %+v", l.Summary())
	}
}

func TestReservationAsSeparateGuarantee(t *testing.T) {
	l := reservationLedger(t, false)
	if err := pay(t, l, "p1", reservation(60_000)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.ApplyReservationToRent("res-apply", clerk, now); !errors.Is(err, payments.ErrNotCredited) {
		t.Fatalf("apply on a separate guarantee err = %v", err)
	}
	if err := pay(t, l, "p2", portions(200_000, 550_000)...); err != nil {
		t.Fatalf("full rent is still owed: %v", err)
	}
	if s := l.Summary(); s.State != payments.StatePaid || s.RentalCoveredWithCredit.Amount() != 200_000 || s.ReservationHeld.Amount() != 60_000 {
		t.Fatalf("summary = %+v", s)
	}
	refundNow(t, l, "r1", payments.BucketSecurity, 550_000)
	if l.Summary().IsSettled() {
		t.Fatal("the reservation must be refunded before the booking is settled")
	}
	refundNow(t, l, "r2", payments.BucketReservation, 60_000)
	if !l.Summary().IsSettled() {
		t.Fatalf("should be settled: %+v", l.Summary())
	}
}

func TestOpenLedgerRejectsReservationAboveRentWhenCredited(t *testing.T) {
	_, err := payments.OpenLedger(payments.OpenRequest{
		BookingRef: "bk-1", Rent: vnd(50_000), Security: vnd(0), Reservation: vnd(60_000),
		RentChargeID: "ch-rent", ReservationChargeID: "ch-res", CreditReservationToRent: true, Actor: system, Now: now,
	})
	if !errors.Is(err, payments.ErrInvalidAmount) {
		t.Fatalf("err = %v", err)
	}
}
