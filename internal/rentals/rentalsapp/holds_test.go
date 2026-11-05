package rentalsapp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/payments"
	"webee_rentor/internal/platform/postgres/pgtest"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalsapp"
)

// limitHolds publishes the fixture policy with a lower limit on unpaid holds.
func (e *env) limitHolds(n int) {
	e.t.Helper()
	p := pgtest.Policy()
	p.Version, p.MaxUnpaidHolds = e.f.Prefix+"-holds", n
	if err := e.svc.PublishPolicy(context.Background(), e.f.User("owner", identity.RoleAdmin), p, "hạn mức giữ hàng"); err != nil {
		e.t.Fatal(err)
	}
	// The next test must find the fixture policy again.
	e.t.Cleanup(func() {
		if err := pgtest.SeedConfig(context.Background(), e.f.Pool); err != nil {
			e.t.Fatal(err)
		}
	})
}

func wantTooManyHolds(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, rentals.ErrTooManyUnpaidHolds) {
		t.Fatalf("err = %v, want ErrTooManyUnpaidHolds", err)
	}
}

func TestACustomerStopsAtTheLimitOfUnpaidHolds(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.limitHolds(2)
	e.f.Assets(e.c.JacketM, 5)
	a, b := e.customer("a"), e.customer("b")
	in := e.at("2026-11-10", "morning", e.item(e.c.JacketM))
	e.place(a, in)
	e.place(a, in)

	_, err := e.svc.PlaceBooking(ctx, a, in)
	wantTooManyHolds(t, err)
	if n := e.bookingsOf(a); n != 2 {
		t.Fatalf("customer has %d bookings, want 2", n)
	}
	if n := e.count(`SELECT count(*) FROM allocations x JOIN bookings y ON y.id = x.booking_id
		WHERE y.customer_id = $1 AND x.status = 'ACTIVE'`, a.UserID); n != 2 {
		t.Fatalf("a refused booking left allocations: %d held", n)
	}
	e.place(b, in) // the limit is per customer
}

func TestAHoldStopsCountingWhenItEnds(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		end  func(*env, identity.Principal, *rentals.Booking)
	}{
		{"money received", func(e *env, _ identity.Principal, b *rentals.Booking) {
			if _, err := e.svc.RecordPayment(ctx, e.staff, b.ID, rentalsapp.PaymentInput{IdempotencyKey: "r", Method: payments.MethodCash,
				Portions: []payments.Portion{{Bucket: payments.BucketReservation, Amount: b.ReservationDeposit}}}); err != nil {
				e.t.Fatal(err)
			}
		}},
		{"cancelled", func(e *env, a identity.Principal, b *rentals.Booking) {
			if _, err := e.svc.Cancel(ctx, a, b.ID, rentalsapp.CancelInput{Reason: rentals.CancelCustomerRequest}); err != nil {
				e.t.Fatal(err)
			}
		}},
		{"expired, before the job ran", func(e *env, _ identity.Principal, b *rentals.Booking) {
			e.clock.Set(b.HoldExpiresAt)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t)
			e.limitHolds(1)
			e.f.Assets(e.c.JacketM, 3)
			a := e.customer("a")
			in := e.at("2026-11-10", "morning", e.item(e.c.JacketM))
			b := e.place(a, in)

			e.clock.Set(b.HoldExpiresAt.Add(-time.Second))
			_, err := e.svc.PlaceBooking(ctx, a, in)
			wantTooManyHolds(t, err)

			c.end(e, a, b)
			e.place(a, in)
		})
	}
}

func TestARetryAtTheLimitReturnsTheBookingItMade(t *testing.T) {
	e := setup(t)
	e.limitHolds(1)
	e.f.Assets(e.c.JacketM, 3)
	a := e.customer("a")
	in := e.at("2026-11-10", "morning", e.item(e.c.JacketM))
	in.RequestKey = "k"
	first := e.place(a, in)

	if again := e.place(a, in); again.ID != first.ID {
		t.Fatalf("retry made booking %s, want %s", again.ID, first.ID)
	}
	if n := e.bookingsOf(a); n != 1 {
		t.Fatalf("customer has %d bookings, want 1", n)
	}
}

func TestADoubleClickAtTheLimitIsOneBooking(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.limitHolds(1)
	e.f.Assets(e.c.JacketM, 3)
	a := e.customer("a")
	in := e.at("2026-11-10", "morning", e.item(e.c.JacketM))
	in.RequestKey = "k"

	steps := make([]func() error, 6)
	for i := range steps {
		steps[i] = func() error {
			_, err := e.svc.PlaceBooking(ctx, a, in)
			return err
		}
	}
	if got := succeeded(e.all(steps...)); got != len(steps) {
		t.Fatalf("%d of %d clicks got the booking", got, len(steps))
	}
	if n := e.bookingsOf(a); n != 1 {
		t.Fatalf("customer has %d bookings, want 1", n)
	}
}

// Each request wants a different shelf, so asset locks do not keep them apart;
// only the customer's lock does. The gate holds the customer row, which every
// insert must wait for, so without that lock all of them count before any of
// them commits.
func TestConcurrentBookingsCannotPassTheLimit(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.limitHolds(1)
	e.f.Assets(e.c.JacketM, 1)
	e.f.Assets(e.c.JacketL, 1)
	e.f.Assets(e.c.PantsM, 1)
	a := e.customer("a")
	pants := rentalsapp.LineInput{Kind: catalog.KindItem, OfferingID: string(e.c.Pants.ID), VariantID: e.c.PantsM, Quantity: 1}

	var steps []func() error
	for _, line := range []rentalsapp.LineInput{e.item(e.c.JacketM), e.item(e.c.JacketL), pants} {
		steps = append(steps, func() error {
			_, err := e.svc.PlaceBooking(ctx, a, e.at("2026-11-10", "morning", line))
			return err
		})
	}
	errs := e.behind(`SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, a.UserID, steps...)

	if got := succeeded(errs); got != 1 {
		t.Fatalf("%d of %d bookings got through: %v", got, len(errs), errs)
	}
	for _, err := range errs {
		if err != nil {
			wantTooManyHolds(t, err)
		}
	}
	if n := e.bookingsOf(a); n != 1 {
		t.Fatalf("customer has %d bookings, want 1", n)
	}
}

// The limit counts bookings, not items (D35): one booking may still take a whole
// shelf, up to MaxQuantityPerLine on each line.
func TestTheLimitCountsBookingsNotItems(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.limitHolds(1)
	e.f.Assets(e.c.JacketM, 3)
	a, b := e.customer("a"), e.customer("b")
	shelf := e.item(e.c.JacketM)
	shelf.Quantity = 3
	e.place(a, e.at("2026-11-10", "morning", shelf))

	_, err := e.svc.PlaceBooking(ctx, b, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	if !errors.Is(err, inventory.ErrInsufficientInventory) {
		t.Fatalf("err = %v, want ErrInsufficientInventory", err)
	}
}
