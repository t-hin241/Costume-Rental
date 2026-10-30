package rentals_test

import (
	"errors"
	"testing"
	"time"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/rentals"
)

// The approved table (D8) on a 200,000đ rent.
func TestCancellationFeeTable(t *testing.T) {
	own := identity.UserActor(customer)
	byCustomer := func(before time.Duration) func(*rentals.Booking) error {
		return func(b *rentals.Booking) error {
			_, err := b.Cancel(rentals.CancelRequest{Actor: own, Scope: identity.ScopeOwn, Reason: rentals.CancelCustomerRequest, Now: b.Schedule.PickupAt.Add(-before)})
			return err
		}
	}
	byStaff := func(reason rentals.CancelReason, before time.Duration) func(*rentals.Booking) error {
		return func(b *rentals.Booking) error {
			_, err := b.Cancel(rentals.CancelRequest{Actor: staff, Scope: identity.ScopeAny, Reason: reason, Note: "ghi chú", Now: b.Schedule.PickupAt.Add(-before)})
			return err
		}
	}
	cases := []struct {
		name    string
		cancel  func(*rentals.Booking) error
		percent int
		fee     int64
	}{
		{"exactly 7 days ahead", byCustomer(7 * day), 0, 0},
		{"7 days minus a minute", byCustomer(7*day - time.Minute), 10, 20_000},
		{"exactly 72 hours ahead", byCustomer(72 * time.Hour), 10, 20_000},
		{"72 hours minus a minute", byCustomer(72*time.Hour - time.Minute), 30, 60_000},
		{"exactly 24 hours ahead", byCustomer(24 * time.Hour), 30, 60_000},
		{"24 hours minus a minute", byCustomer(24*time.Hour - time.Minute), 50, 100_000},
		{"1 hour after pickup time, before the no-show deadline", byCustomer(-time.Hour), 50, 100_000},
		{"customer calls the store 1 hour ahead", byStaff(rentals.CancelCustomerRequest, time.Hour), 50, 100_000},
		{"no-show 2 hours after pickup", byStaff(rentals.CancelNoShow, -2*time.Hour), 70, 140_000},
		{"store cancels", byStaff(rentals.CancelStaffDecision, 2*day), 0, 0},
		{"payment failed", func(b *rentals.Booking) error {
			_, err := b.Cancel(rentals.CancelRequest{Actor: system, Reason: rentals.CancelPaymentFailed, Now: now})
			return err
		}, 0, 0},
		{"hold expired", func(b *rentals.Booking) error {
			_, _, err := b.Expire(system, b.HoldExpiresAt)
			return err
		}, 0, 0},
	}
	for _, c := range cases {
		b := mustPlace(t).Booking
		if err := c.cancel(b); err != nil {
			t.Fatalf("%s: cancel: %v", c.name, err)
		}
		fee, err := b.CancellationFee()
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if fee.FeePercent != c.percent || fee.Fee.Amount() != c.fee || fee.Basis == "" {
			t.Errorf("%s: fee = %d%% %d (%s), want %d%% %d", c.name, fee.FeePercent, fee.Fee.Amount(), fee.Basis, c.percent, c.fee)
		}
	}

	active := mustPlace(t).Booking
	if _, err := active.CancellationFee(); !errors.Is(err, rentals.ErrInvalidTransition) {
		t.Fatalf("fee for an active booking err = %v", err)
	}
}

func TestCancellationFeeIsOnRentOnlyAndRoundsDown(t *testing.T) {
	q := bundleQuote(t, pkg3)
	q.RentAmount = vnd(200_005)
	b := mustPlace(t, func(r *rentals.PlaceRequest) { r.Lines = []rentals.LineRequest{{ID: "l1", Quote: q}} }).Booking
	if _, err := b.Cancel(rentals.CancelRequest{Actor: identity.UserActor(customer), Scope: identity.ScopeOwn,
		Reason: rentals.CancelCustomerRequest, Now: b.Schedule.PickupAt.Add(-5 * day)}); err != nil {
		t.Fatal(err)
	}
	fee, err := b.CancellationFee()
	if err != nil {
		t.Fatal(err)
	}
	// 10% of 200,005 = 20,000.5 → 20,000; the 550,000 security deposit is not part of the base.
	if fee.Fee.Amount() != 20_000 {
		t.Fatalf("fee = %d, want 20000", fee.Fee.Amount())
	}
}

func TestCancellationPolicyIsSnapshotted(t *testing.T) {
	p := testPolicy()
	b := mustPlace(t, func(r *rentals.PlaceRequest) { r.Policy = p }).Booking
	// The store later publishes a stricter table; this booking keeps the one agreed.
	p.Cancellation.Tiers[0].FeePercent = 50
	if _, err := b.Cancel(rentals.CancelRequest{Actor: identity.UserActor(customer), Scope: identity.ScopeOwn,
		Reason: rentals.CancelCustomerRequest, Now: b.Schedule.PickupAt.Add(-8 * day)}); err != nil {
		t.Fatal(err)
	}
	fee, err := b.CancellationFee()
	if err != nil || !fee.Fee.IsZero() {
		t.Fatalf("fee = %+v, %v; want the agreed 0%%", fee, err)
	}
}
