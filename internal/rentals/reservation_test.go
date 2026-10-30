package rentals_test

import (
	"errors"
	"testing"

	"webee_rentor/internal/rentals"
)

func TestReservationDepositAmount(t *testing.T) {
	b := mustPlace(t).Booking
	if b.ReservationDeposit.Amount() != 60_000 || !b.ReservationCreditsRent() {
		t.Fatalf("reservation = %s credits rent = %v; want 30%% of 200000 credited", b.ReservationDeposit, b.ReservationCreditsRent())
	}

	q := bundleQuote(t, pkg3)
	q.RentAmount = vnd(200_001)
	b = mustPlace(t, func(r *rentals.PlaceRequest) { r.Lines = []rentals.LineRequest{{ID: "l1", Quote: q}} }).Booking
	if b.ReservationDeposit.Amount() != 60_001 {
		t.Fatalf("30%% of 200001 = %d, want 60001 (rounded up)", b.ReservationDeposit.Amount())
	}

	fixed := testPolicy()
	fixed.Reservation.Kind, fixed.Reservation.Amount = rentals.ReservationFixed, vnd(300_000)
	b = mustPlace(t, func(r *rentals.PlaceRequest) { r.Policy = fixed }).Booking
	if b.ReservationDeposit.Amount() != 200_000 {
		t.Fatalf("fixed reservation credited to rent = %d, want capped at rent 200000", b.ReservationDeposit.Amount())
	}
	fixed.Reservation.Treatment = rentals.ReservationRefundedAfterReturn
	b = mustPlace(t, func(r *rentals.PlaceRequest) { r.Policy = fixed }).Booking
	if b.ReservationDeposit.Amount() != 300_000 || b.ReservationCreditsRent() {
		t.Fatalf("separate guarantee = %d credits=%v, want 300000 not credited", b.ReservationDeposit.Amount(), b.ReservationCreditsRent())
	}
}

func TestConfirmRequiresReservationDeposit(t *testing.T) {
	b := mustPlace(t).Booking
	if _, err := b.Confirm(system, funds(0, 0, 59_999), now); !errors.Is(err, rentals.ErrInsufficientFunds) {
		t.Fatalf("short reservation err = %v", err)
	}
	// Rent paid without the reservation does not hold the date.
	if _, err := b.Confirm(system, funds(200_000, 550_000, 0), now); !errors.Is(err, rentals.ErrInsufficientFunds) {
		t.Fatalf("no reservation err = %v", err)
	}
	if _, err := b.Confirm(system, funds(60_000, 0, 60_000), now); err != nil {
		t.Fatalf("confirm with reservation: %v", err)
	}
}
