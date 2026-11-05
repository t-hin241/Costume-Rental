package deliverypg_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"webee_rentor/internal/delivery"
	"webee_rentor/internal/delivery/deliverypg"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/period"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/platform/postgres/pgtest"
	"webee_rentor/internal/rentals/rentalsapp"
)

func TestMain(m *testing.M) { pgtest.Main(m, pgtest.SeedConfig) }

// booking places a store booking to hang tasks on; the courier legs do not
// matter to storage.
func booking(t *testing.T, f *pgtest.Fixture) (string, identity.UserID) {
	t.Helper()
	c := f.Catalog()
	f.Assets(c.JacketM, 1)
	loc, _ := time.LoadLocation("Asia/Ho_Chi_Minh")
	svc := rentalsapp.New(f.Pool, loc, rentalsapp.WithClock(func() time.Time { return pgtest.Now }))
	buyer := f.User("buyer", identity.RoleCustomer)
	b, err := svc.PlaceBooking(context.Background(), buyer, rentalsapp.PlaceInput{PackageID: c.OneDay.ID, PickupDate: pgtest.Date(t, "2026-11-10"),
		SlotID: "morning", Lines: []rentalsapp.LineInput{{Kind: "ITEM", OfferingID: string(c.Jacket.ID), VariantID: c.JacketM, Quantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b.ID), buyer.UserID
}

func TestTasksReadBackAsWritten(t *testing.T) {
	f := pgtest.New(t)
	ctx := context.Background()
	id, customer := booking(t, f)
	area := pgtest.DeliveryArea()
	home := delivery.Address{RecipientName: "Khách Test", Phone: "0911111111", Line: "2 Đường B", WardCode: "W1", ProvinceCode: "P1"}
	w, _ := period.New(pgtest.Now.Add(time.Hour), pgtest.Now.Add(3*time.Hour))
	tk, err := delivery.NewTask(delivery.NewTaskRequest{ID: delivery.TaskID(f.Prefix + "-t1"), BookingRef: id, CustomerID: customer, Direction: delivery.Outbound,
		ProviderCode: "ghn", Store: area.Origin, Customer: home, Window: w, Parcel: delivery.Parcel{WeightGram: 1000, PackageCount: 1, DeclaredValue: pgtest.VND(300_000)},
		CustomerFee: pgtest.VND(30_000), Now: pgtest.Now})
	if err != nil {
		t.Fatal(err)
	}
	v, err := deliverypg.SaveTask(ctx, f.Pool, tk, 0, pgtest.Now)
	if err != nil {
		t.Fatal(err)
	}
	est := pgtest.VND(25_000)
	if _, err := tk.RecordCreated(delivery.ShipmentResult{ProviderShipmentCode: f.Prefix + "-GHN", EstimatedFee: &est}); err != nil {
		t.Fatal(err)
	}
	staff := f.User("staff", identity.RoleRentalStaff)
	_, _ = tk.ApplyCarrierEvent(delivery.CarrierEvent{Provider: "ghn", ShipmentCode: tk.ProviderShipmentCode, Key: "p1", Status: delivery.StatusPickedUp, OccurredAt: pgtest.Now, ReceivedAt: pgtest.Now})
	if _, err := tk.ManualAttempt(staff, delivery.StatusDeliveryFailed, delivery.FaultCustomer, true, "khách vắng", "", pgtest.Now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := tk.CloseUnfulfilled(staff, delivery.FollowUpAtStore, "khách đến lấy", pgtest.Now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if v, err = deliverypg.SaveTask(ctx, f.Pool, tk, v, pgtest.Now); err != nil {
		t.Fatal(err)
	}
	got, gv, err := deliverypg.LoadTask(ctx, f.Pool, tk.ID, false)
	if err != nil || gv != v {
		t.Fatalf("load = %v, version %d want %d", err, gv, v)
	}
	if !reflect.DeepEqual(got, tk) {
		t.Fatalf("read back\n%+v\nwant\n%+v", got, tk)
	}
	if _, err := deliverypg.SaveTask(ctx, f.Pool, tk, v-1, pgtest.Now); !errors.Is(err, postgres.ErrStale) {
		t.Fatalf("stale write err = %v", err)
	}

	// The closed shipment is history: a new one for the leg may start, but
	// only one at a time.
	for i, want := range []error{nil, deliverypg.ErrLegInProgress} {
		n, _ := delivery.NewTask(delivery.NewTaskRequest{ID: delivery.TaskID(f.Prefix + "-n" + string(rune('a'+i))), BookingRef: id, CustomerID: customer,
			Direction: delivery.Outbound, ProviderCode: "ghn", Store: area.Origin, Customer: home, Window: w,
			Parcel: delivery.Parcel{WeightGram: 1000, PackageCount: 1}, CustomerFee: pgtest.VND(0), Now: pgtest.Now})
		if _, err := deliverypg.SaveTask(ctx, f.Pool, n, 0, pgtest.Now); !errors.Is(err, want) {
			t.Fatalf("new shipment %d err = %v, want %v", i, err, want)
		}
	}
}
