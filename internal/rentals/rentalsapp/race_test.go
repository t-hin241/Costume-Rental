package rentalsapp_test

import (
	"context"
	"errors"
	"fmt"
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

// collect turns steps into ones that store their errors.
func collect(errs []error, steps []func() error) []func() {
	out := make([]func(), len(steps))
	for i, step := range steps {
		out[i] = func() { errs[i] = step() }
	}
	return out
}

// all runs the steps at the same moment and returns their errors in order.
func (e *env) all(steps ...func() error) []error {
	errs := make([]error, len(steps))
	e.f.Race(collect(errs, steps)...)
	return errs
}

// behind is all with the rows of lockQuery held until every step waits.
func (e *env) behind(lockQuery string, id any, steps ...func() error) []error {
	errs := make([]error, len(steps))
	e.f.RaceBehind(lockQuery, []any{id}, collect(errs, steps)...)
	return errs
}

const (
	shelf  = `SELECT 1 FROM assets WHERE variant_id = $1 FOR UPDATE`
	ledger = `SELECT 1 FROM payment_ledgers WHERE booking_id = $1 FOR UPDATE`
)

func succeeded(errs []error) int {
	n := 0
	for _, err := range errs {
		if err == nil {
			n++
		}
	}
	return n
}

func (e *env) customer(name string) identity.Principal {
	e.t.Helper()
	return e.f.User(name, identity.RoleCustomer)
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.f.Pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) noAssetHeldTwice() {
	e.t.Helper()
	if n := e.count(`SELECT count(*) FROM allocations x JOIN allocations y
		ON x.asset_id = y.asset_id AND x.id < y.id AND x.block && y.block
		WHERE x.status = 'ACTIVE' AND y.status = 'ACTIVE'`); n != 0 {
		e.t.Fatalf("%d pairs of allocations hold one asset at the same time", n)
	}
}

// sizeL asks to move a booking's only line to the jacket in size L.
func (e *env) sizeL(b *rentals.Booking) rentalsapp.ModificationInput {
	return rentalsapp.ModificationInput{Reason: "đổi size", Changes: []rentalsapp.LineChangeInput{{LineID: b.Lines[0].ID, Line: e.item(e.c.JacketL)}}}
}

func TestTwoSizeChangesRaceForTheLastSize(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 2)
	e.f.Assets(e.c.JacketL, 1)
	a, b := e.customer("a"), e.customer("b")
	first := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	second := e.place(b, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	ma, err := e.svc.RequestModification(ctx, a, first.ID, e.sizeL(first))
	if err != nil {
		t.Fatal(err)
	}
	mb, err := e.svc.RequestModification(ctx, b, second.ID, e.sizeL(second))
	if err != nil {
		t.Fatal(err)
	}
	errs := e.behind(shelf, e.c.JacketL,
		func() error { _, err := e.svc.QuoteModification(ctx, a, ma.ID); return err },
		func() error { _, err := e.svc.QuoteModification(ctx, b, mb.ID); return err },
	)
	if succeeded(errs) != 1 {
		t.Fatalf("errs = %v, want one winner", errs)
	}
	for _, err := range errs {
		if err != nil {
			wantShort(t, err)
		}
	}
	if n := e.count(`SELECT count(*) FROM allocations WHERE variant_id = $1 AND status = 'ACTIVE'`, e.c.JacketL); n != 1 {
		t.Fatalf("L holds = %d", n)
	}
	e.noAssetHeldTwice()
}

func TestASizeChangeAndANewBookingRaceForTheLastSize(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	e.f.Assets(e.c.JacketL, 1)
	a, b := e.customer("a"), e.customer("b")
	booked := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	m, err := e.svc.RequestModification(ctx, a, booked.ID, e.sizeL(booked))
	if err != nil {
		t.Fatal(err)
	}
	errs := e.behind(shelf, e.c.JacketL,
		func() error { _, err := e.svc.QuoteModification(ctx, a, m.ID); return err },
		func() error {
			_, err := e.svc.PlaceBooking(ctx, b, e.at("2026-11-10", "morning", e.item(e.c.JacketL)))
			return err
		},
	)
	if succeeded(errs) != 1 {
		t.Fatalf("errs = %v, want one winner", errs)
	}
	e.noAssetHeldTwice()
}

func TestConfirmingAChangeTwiceAtOnceAppliesItOnce(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	e.f.Assets(e.c.JacketL, 1)
	a := e.customer("a")
	booked := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	m, err := e.svc.RequestModification(ctx, a, booked.ID, e.sizeL(booked))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.QuoteModification(ctx, a, m.ID); err != nil {
		t.Fatal(err)
	}
	confirm := func() error { _, err := e.svc.ConfirmModification(ctx, e.staff, m.ID); return err }
	errs := e.behind(ledger, booked.ID, confirm, confirm, confirm, confirm)
	if succeeded(errs) != 1 {
		t.Fatalf("errs = %v, want one confirmation", errs)
	}
	got := e.active(booked.ID)
	if len(got) != 1 || got[0].VariantID != e.c.JacketL {
		t.Fatalf("allocations = %+v", got)
	}
	if n := e.count(`SELECT cardinality(committed_repricings) FROM payment_ledgers WHERE booking_id = $1`, booked.ID); n != 1 {
		t.Fatalf("repriced %d times", n)
	}
	e.noAssetHeldTwice()
}

func TestConfirmingAndRejectingAChangeCannotBothHappen(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	e.f.Assets(e.c.JacketL, 1)
	a := e.customer("a")
	booked := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	m, err := e.svc.RequestModification(ctx, a, booked.ID, e.sizeL(booked))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.QuoteModification(ctx, a, m.ID); err != nil {
		t.Fatal(err)
	}
	errs := e.behind(ledger, booked.ID,
		func() error { _, err := e.svc.ConfirmModification(ctx, e.staff, m.ID); return err },
		func() error {
			_, err := e.svc.EndModification(ctx, e.staff, m.ID, rentalsapp.ModificationRejected, "không được")
			return err
		},
	)
	if succeeded(errs) != 1 {
		t.Fatalf("errs = %v, want one outcome", errs)
	}
	if got := e.active(booked.ID); len(got) != 1 {
		t.Fatalf("allocations = %+v, want one", got)
	}
}

// A customer cannot call a change the store's fault to keep the old price, and
// nobody else's booking can be changed.
func TestChangesStayWithTheirOwnerAndTheirPrice(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	e.f.Assets(e.c.JacketL, 1)
	a, b := e.customer("a"), e.customer("b")
	booked := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))

	in := e.sizeL(booked)
	if _, err := e.svc.RequestModification(ctx, b, booked.ID, in); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("a stranger changing a booking: err = %v", err)
	}
	in.StoreFault = true
	if _, err := e.svc.RequestModification(ctx, a, booked.ID, in); err == nil {
		t.Fatal("a customer declared the store at fault")
	}
	in.StoreFault = false
	m, err := e.svc.RequestModification(ctx, a, booked.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.QuoteModification(ctx, b, m.ID); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("a stranger quoting a change: err = %v", err)
	}
	if _, err := e.svc.ConfirmModification(ctx, b, m.ID); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("a stranger confirming a change: err = %v", err)
	}
	if _, err := e.svc.EndModification(ctx, a, m.ID, rentalsapp.ModificationRejected, "x"); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("a customer rejecting as the store: err = %v", err)
	}
}

func TestExpiryJobsRacingEachOtherRefundOnce(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	a := e.customer("a")
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	if _, err := e.svc.RecordPayment(ctx, e.staff, b.ID, rentalsapp.PaymentInput{IdempotencyKey: "p", Method: payments.MethodCash,
		Portions: []payments.Portion{{Bucket: payments.BucketReservation, Amount: pgtest.VND(15_000)}}}); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(pgtest.Now.Add(time.Hour))
	steps := make([]func() error, 5)
	for i := range steps {
		steps[i] = func() error { _, err := e.svc.ExpireHolds(ctx, 1000); return err }
	}
	if errs := e.behind(ledger, b.ID, steps...); succeeded(errs) != len(steps) {
		t.Fatalf("errs = %v", errs)
	}
	if n := e.count(`SELECT count(*) FROM booking_transitions WHERE booking_id = $1 AND action = $2`, b.ID, rentals.ActionExpire); n != 1 {
		t.Fatalf("expired %d times", n)
	}
	l := e.ledger(b.ID)
	if len(l.Refunds) != 1 {
		t.Fatalf("refunds = %+v", l.Refunds)
	}
	wantAmount(t, "refund", l.Refunds[0].Amount, 15_000)
	if got := len(e.active(b.ID)); got != 0 {
		t.Fatalf("active allocations = %d", got)
	}
}

// A hold past its time still blocks the asset until the job ends it, and the
// new customer racing the job either gets the item or a clean refusal.
func TestAnExpiredHoldNeverSharesItsAsset(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	a, b := e.customer("a"), e.customer("b")
	first := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.clock.Set(pgtest.Now.Add(time.Hour))

	_, err := e.svc.PlaceBooking(ctx, b, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	wantShort(t, err)

	errs := e.all(
		func() error { _, err := e.svc.ExpireHolds(ctx, 1000); return err },
		func() error {
			_, err := e.svc.PlaceBooking(ctx, b, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
			if errors.Is(err, inventory.ErrInsufficientInventory) {
				return nil
			}
			return err
		},
	)
	if succeeded(errs) != 2 {
		t.Fatalf("errs = %v", errs)
	}
	if got := len(e.active(first.ID)); got != 0 {
		t.Fatalf("the expired hold keeps %d allocations", got)
	}
	e.noAssetHeldTwice()
	if _, err := e.svc.PlaceBooking(ctx, e.customer("c"), e.at("2026-11-24", "morning", e.item(e.c.JacketM))); err != nil {
		t.Fatalf("the freed asset is not bookable: %v", err)
	}
}

// A payment racing the expiry job: the money is never lost and never counted
// as still held by an ended booking.
func TestAPaymentRacingTheExpiryJobIsRefunded(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	a := e.customer("a")
	b := e.place(a, e.at("2026-11-10", "morning", e.item(e.c.JacketM)))
	e.clock.Set(pgtest.Now.Add(time.Hour))
	errs := e.behind(ledger, b.ID,
		func() error { _, err := e.svc.ExpireHolds(ctx, 1000); return err },
		func() error {
			_, err := e.svc.RecordPayment(ctx, e.staff, b.ID, rentalsapp.PaymentInput{IdempotencyKey: "p", Method: payments.MethodCash,
				Portions: []payments.Portion{{Bucket: payments.BucketReservation, Amount: pgtest.VND(15_000)}}})
			if errors.Is(err, payments.ErrExceedsOutstanding) {
				return nil // the job went first and nothing is owed any more
			}
			return err
		},
	)
	if succeeded(errs) != 2 {
		t.Fatalf("errs = %v", errs)
	}
	if _, err := e.svc.ExpireHolds(ctx, 1000); err != nil {
		t.Fatal(err)
	}
	s := e.ledger(b.ID).Summary()
	wantAmount(t, "owed to the customer", s.PendingRefunds, s.TotalReceived.Amount())
}

func TestABundleChangeNeverLeavesPartOfItAllocated(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	e.f.Assets(e.c.JacketL, 1)
	e.f.Assets(e.c.PantsM, 1)
	e.f.Assets(e.c.BeltStd, 1)
	a, b := e.customer("a"), e.customer("b")
	booked := e.place(a, e.at("2026-11-10", "morning", e.set(e.c.JacketM)))
	m, err := e.svc.RequestModification(ctx, a, booked.ID, rentalsapp.ModificationInput{Reason: "đổi size",
		Changes: []rentalsapp.LineChangeInput{{LineID: booked.Lines[0].ID, Line: e.set(e.c.JacketL)}}})
	if err != nil {
		t.Fatal(err)
	}
	errs := e.behind(shelf, e.c.JacketL,
		func() error { _, err := e.svc.QuoteModification(ctx, a, m.ID); return err },
		func() error {
			_, err := e.svc.PlaceBooking(ctx, b, e.at("2026-11-10", "morning", e.item(e.c.JacketL)))
			return err
		},
	)
	if succeeded(errs) != 1 {
		t.Fatalf("errs = %v, want one winner", errs)
	}
	e.noAssetHeldTwice()
	for _, v := range []catalog.VariantID{e.c.PantsM, e.c.BeltStd} {
		if n := e.count(`SELECT count(*) FROM allocations WHERE variant_id = $1 AND status = 'ACTIVE'`, v); n != 1 {
			t.Fatalf("%s holds = %d, want the booking's own only", v, n)
		}
	}
}

// Many bookings, cancellations and reschedules at once on two shelves: the
// shop may refuse anything, but the books and the shelves must stay true.
func TestAStormOfBookingsLeavesTheShelvesConsistent(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 2)
	e.f.Assets(e.c.PantsM, 1)
	e.f.Assets(e.c.BeltStd, 2)
	dates := []string{"2026-11-10", "2026-11-11", "2026-11-12", "2026-11-13", "2026-11-16"}
	var steps []func() error
	for i := range 24 {
		who := e.customer(fmt.Sprint("c", i))
		date := dates[i%len(dates)]
		in := e.at(date, "morning", e.item(e.c.JacketM))
		if i%3 == 0 {
			in = e.at(date, "morning", e.set(e.c.JacketM))
		}
		in.RequestKey = fmt.Sprint("k", i)
		steps = append(steps, func() error {
			b, err := e.svc.PlaceBooking(ctx, who, in)
			if errors.Is(err, inventory.ErrInsufficientInventory) {
				return nil
			}
			if err != nil {
				return err
			}
			switch i % 4 {
			case 1:
				_, err = e.svc.Cancel(ctx, who, b.ID, rentalsapp.CancelInput{Reason: rentals.CancelCustomerRequest})
			case 2:
				_, err = e.svc.Reschedule(ctx, who, b.ID, rentalsapp.RescheduleInput{PickupDate: pgtest.Date(t, "2026-11-24"), SlotID: "morning"})
				if errors.Is(err, inventory.ErrInsufficientInventory) {
					return nil
				}
			}
			return err
		})
	}
	if errs := e.all(steps...); succeeded(errs) != len(steps) {
		t.Fatalf("errs = %v", errs)
	}
	e.noAssetHeldTwice()
	// Every live booking holds what it needs; every ended one holds nothing.
	mine := e.f.Prefix + "-%"
	if n := e.count(`SELECT count(*) FROM bookings b WHERE b.customer_id LIKE $1 AND b.status IN ('CANCELLED', 'EXPIRED')
		AND EXISTS (SELECT 1 FROM allocations a WHERE a.booking_id = b.id AND a.status = 'ACTIVE')`, mine); n != 0 {
		t.Fatalf("%d ended bookings still hold assets", n)
	}
	if n := e.count(`SELECT count(*) FROM bookings b JOIN booking_lines l ON l.booking_id = b.id
		WHERE b.customer_id LIKE $1 AND b.status = 'PENDING_CONFIRMATION' AND l.kind = 'BUNDLE'
		AND (SELECT count(*) FROM allocations a WHERE a.booking_id = b.id AND a.status = 'ACTIVE') <> 3`, mine); n != 0 {
		t.Fatalf("%d bundle bookings are partly allocated", n)
	}
}
