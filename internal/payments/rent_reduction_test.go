package payments_test

import (
	"errors"
	"testing"

	"webee_rentor/internal/payments"
)

func TestReduceRentForAcceptedComplaint(t *testing.T) {
	l, err := payments.OpenLedger(payments.OpenRequest{
		BookingRef: "bk-1", Rent: vnd(330_000), Security: vnd(0), Reservation: vnd(0),
		RentChargeID: "ch-rent", Actor: system, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := pay(t, l, "p1", portions(330_000, 0)...); err != nil {
		t.Fatal(err)
	}
	if _, err := l.ReduceRent("cp-1", "ch-rent-2", vnd(400_000), "x", clerk, now); !errors.Is(err, payments.ErrInvalidAmount) {
		t.Fatalf("raising rent through a complaint err = %v", err)
	}
	changed, err := l.ReduceRent("cp-1", "ch-rent-2", vnd(200_000), "khiếu nại cp-1: thiếu áo L", clerk, now)
	if err != nil || !changed {
		t.Fatal(err)
	}
	if changed, err := l.ReduceRent("cp-1", "ch-rent-3", vnd(100_000), "retry", clerk, now); err != nil || changed {
		t.Fatalf("retry = %v, %v; want no-op", changed, err)
	}
	if s := l.Summary(); s.RentalDue.Amount() != 200_000 || s.RentalOutstanding.Amount() != -130_000 {
		t.Fatalf("summary = %+v; 130,000 must be refundable", s)
	}
	refundNow(t, l, "r1", payments.BucketRental, 130_000)
	if !l.Summary().IsSettled() {
		t.Fatalf("should be settled: %+v", l.Summary())
	}
}
