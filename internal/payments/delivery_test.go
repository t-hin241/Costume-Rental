package payments_test

import (
	"testing"

	"webee_rentor/internal/payments"
)

func deliveryLedger(t *testing.T) *payments.Ledger {
	t.Helper()
	l, err := payments.OpenLedger(payments.OpenRequest{
		BookingRef: "bk-1", Rent: vnd(200_000), Security: vnd(550_000), Reservation: vnd(0),
		OutboundDeliveryFee: vnd(30_000), ReturnDeliveryFee: vnd(30_000),
		RentChargeID: "ch-rent", SecurityChargeID: "ch-dep", OutboundDeliveryChargeID: "ch-out", ReturnDeliveryChargeID: "ch-ret",
		Actor: system, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestDeliveryFeesAreSeparateCharges(t *testing.T) {
	l := deliveryLedger(t)
	var legs []payments.DeliveryLeg
	for _, c := range l.Charges {
		if c.Kind == payments.ChargeDeliveryFee {
			legs = append(legs, c.DeliveryLeg)
		}
	}
	if len(legs) != 2 || legs[0] != payments.LegOutbound || legs[1] != payments.LegReturn {
		t.Fatalf("legs = %v", legs)
	}
	if s := l.Summary(); s.RentalDue.Amount() != 260_000 {
		t.Fatalf("rental due = %s, want rent + both legs", s.RentalDue)
	}
}

func TestCancellationWaivesUndispatchedLegs(t *testing.T) {
	l := deliveryLedger(t)
	if err := pay(t, l, "p1", portions(260_000, 0)...); err != nil {
		t.Fatal(err)
	}
	res := cancel(t, l, 20_000) // 10% of rent
	// Both legs never left: 260,000 paid - 20,000 fee = 240,000 refundable.
	if res.RefundableRental.Amount() != 240_000 || !res.Owed.IsZero() {
		t.Fatalf("result = %+v", res)
	}

	l = deliveryLedger(t)
	if err := pay(t, l, "p1", portions(260_000, 0)...); err != nil {
		t.Fatal(err)
	}
	res, _, err := l.SettleCancellation(payments.CancellationRequest{Fee: vnd(20_000), Basis: "x", FeeChargeID: "ch-cancel",
		ResolutionID: "res", DispatchedLegs: []payments.DeliveryLeg{payments.LegOutbound}, Actor: clerk, Now: now})
	if err != nil || res.RefundableRental.Amount() != 210_000 {
		t.Fatalf("dispatched outbound kept: %+v, %v", res, err)
	}
}

func TestRedeliveryFeeIsDeductible(t *testing.T) {
	l := deliveryLedger(t)
	if err := pay(t, l, "p1", portions(260_000, 550_000)...); err != nil {
		t.Fatal(err)
	}
	approvedFee(t, l, "redeliver", payments.ChargeRedeliveryFee, 20_000)
	ds, err := l.ApplySecurityToApprovedFees("settle", clerk, now)
	if err != nil || len(ds) != 1 || l.Summary().SecurityHeld.Amount() != 530_000 {
		t.Fatalf("deductions = %+v, %v", ds, err)
	}
}
