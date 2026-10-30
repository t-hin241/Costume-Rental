package rentalspg_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/catalog"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/platform/postgres/pgtest"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalspg"
)

func TestMain(m *testing.M) { pgtest.Main(m, pgtest.SeedConfig) }

func placed(t *testing.T, f *pgtest.Fixture, c pgtest.Catalog, customer identity.Principal) *rentals.Booking {
	t.Helper()
	ctx := context.Background()
	loc, _ := businesstime.LoadLocation()
	cal, err := rentalspg.LoadCalendar(ctx, f.Pool, loc)
	if err != nil {
		t.Fatal(err)
	}
	s, err := cal.ResolveSchedule(pgtest.Date(t, "2026-11-10"), "morning", c.OneDay.Duration)
	if err != nil {
		t.Fatal(err)
	}
	items := map[catalog.ItemID]catalog.Item{c.Jacket.ID: c.Jacket, c.Pants.ID: c.Pants, c.Belt.ID: c.Belt}
	q, err := catalog.QuoteBundle(c.Set, items, map[string]catalog.VariantID{"jacket": c.JacketM}, c.OneDay, 1)
	if err != nil {
		t.Fatal(err)
	}
	assets := append(append(f.Assets(c.JacketM, 1), f.Assets(c.PantsM, 1)...), f.Assets(c.BeltStd, 1)...)
	view := rentals.InventoryView{}
	for _, id := range assets {
		view.Assets = append(view.Assets, inventory.Asset{ID: id, Status: inventory.AssetReady})
	}
	view.Assets[0].VariantID, view.Assets[1].VariantID, view.Assets[2].VariantID = c.JacketM, c.PantsM, c.BeltStd
	pl, err := rentals.PlaceBooking(rentals.PlaceRequest{
		BookingID: rentals.BookingID(f.Prefix + "-bk"), Code: f.Prefix + "-code", CustomerID: customer.UserID,
		Actor: customer.Actor(), Schedule: s, Calendar: cal, Lines: []rentals.LineRequest{{ID: "L1", Quote: q}},
		Policy: pgtest.Policy(), Inventory: view, Now: pgtest.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return pl.Booking
}

// The stored booking must read back exactly as the domain wrote it: lines,
// components, snapshots, money and timestamps.
func TestBookingRoundTripAndOptimisticVersion(t *testing.T) {
	ctx := context.Background()
	f := pgtest.New(t)
	c := f.Catalog()
	b := placed(t, f, c, f.User("cust", identity.RoleCustomer))

	v, err := rentalspg.SaveBooking(ctx, f.Pool, b, 0, "key-1", pgtest.Now)
	if err != nil || v != 1 {
		t.Fatalf("insert = %d, %v", v, err)
	}
	got, gv, err := rentalspg.LoadBooking(ctx, f.Pool, b.ID, false)
	if err != nil || gv != 1 {
		t.Fatalf("load = %d, %v", gv, err)
	}
	if !reflect.DeepEqual(got, b) {
		t.Fatalf("round trip differs:\n got %+v\nwant %+v", got, b)
	}

	if _, err := got.Confirm(identity.UserActor("staff"), rentals.Funds{
		RentalCovered: b.RentTotal, SecurityHeld: b.SecurityDepositTotal, ReservationHeld: b.ReservationDeposit,
	}, pgtest.Now); err != nil {
		t.Fatal(err)
	}
	if v, err = rentalspg.SaveBooking(ctx, f.Pool, got, gv, "", pgtest.Now); err != nil || v != 2 {
		t.Fatalf("update = %d, %v", v, err)
	}
	if _, err := rentalspg.SaveBooking(ctx, f.Pool, got, gv, "", pgtest.Now); !errors.Is(err, postgres.ErrStale) {
		t.Fatalf("writing with an old version: err = %v, want ErrStale", err)
	}
	again, _, err := rentalspg.LoadBooking(ctx, f.Pool, b.ID, false)
	if err != nil || again.Status != rentals.StatusConfirmed || !reflect.DeepEqual(again, got) {
		t.Fatalf("after confirm: %v %v", again.Status, err)
	}
}

func TestModificationDocumentRoundTripAndOneOpenPerBooking(t *testing.T) {
	ctx := context.Background()
	f := pgtest.New(t)
	c := f.Catalog()
	b := placed(t, f, c, f.User("cust", identity.RoleCustomer))
	if _, err := rentalspg.SaveBooking(ctx, f.Pool, b, 0, "", pgtest.Now); err != nil {
		t.Fatal(err)
	}
	items := map[catalog.ItemID]catalog.Item{c.Jacket.ID: c.Jacket, c.Pants.ID: c.Pants, c.Belt.ID: c.Belt}
	q, err := catalog.QuoteBundle(c.Set, items, map[string]catalog.VariantID{"jacket": c.JacketL}, c.OneDay, 1)
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := b.RequestModification(rentals.ModificationRequest{
		ID: rentals.ModificationID(f.Prefix + "-m1"), Actor: identity.UserActor("staff"), Scope: identity.ScopeAny,
		Reason: "size", Changes: []rentals.LineChange{{LineID: "L1", Quote: q}}, Now: pgtest.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rentalspg.SaveModification(ctx, f.Pool, m, 0, pgtest.Now); err != nil {
		t.Fatal(err)
	}
	got, _, err := rentalspg.LoadModification(ctx, f.Pool, m.ID, false)
	if err != nil || !reflect.DeepEqual(got, m) {
		t.Fatalf("round trip differs (%v):\n got %+v\nwant %+v", err, got, m)
	}
	second := *m
	second.ID = rentals.ModificationID(f.Prefix + "-m2")
	if _, err := rentalspg.SaveModification(ctx, f.Pool, &second, 0, pgtest.Now); !errors.Is(err, rentals.ErrModificationOpen) {
		t.Fatalf("second open modification: err = %v", err)
	}
}

func TestCalendarLoadsSlotsAndClosures(t *testing.T) {
	ctx := context.Background()
	p := pgtest.Pool(t)
	loc, _ := businesstime.LoadLocation()
	var cal *rentals.StoreCalendar
	err := postgres.InTx(ctx, p, func(tx pgx.Tx) error {
		var err error
		cal, err = rentalspg.LoadCalendar(ctx, tx, loc)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cal.ResolveSchedule(pgtest.Date(t, "2026-11-20"), "morning", 24*time.Hour); !errors.Is(err, rentals.ErrStoreClosed) {
		t.Fatalf("closure not applied: %v", err)
	}
	s, err := cal.ResolveSchedule(pgtest.Date(t, "2026-11-10"), "afternoon", 24*time.Hour)
	if err != nil || !s.PickupAt.Equal(pgtest.Local(t, "2026-11-10 15:00")) {
		t.Fatalf("afternoon slot = %v, %v", s.PickupAt, err)
	}
}
