package payments_test

import (
	"errors"
	"testing"

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

func refundNow(t *testing.T, l *payments.Ledger, id string, bucket payments.Bucket, amount int64) {
	t.Helper()
	r, _, err := l.RequestRefund(payments.RefundID(id), id, bucket, vnd(amount), "hoàn", clerk, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.ApproveRefund(r.ID, finance, approval, now); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordCounterRefund(r.ID, clerk, now); err != nil {
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
