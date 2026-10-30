package inventorypg_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/inventory/inventorypg"
	"webee_rentor/internal/period"
	"webee_rentor/internal/platform/postgres/pgtest"
)

func TestMain(m *testing.M) { pgtest.Main(m, nil) }

// insertBooking writes the minimal booking row allocations reference.
func insertBooking(t *testing.T, f *pgtest.Fixture, id string, customer identity.Principal) {
	t.Helper()
	_, err := f.Pool.Exec(context.Background(), `
		INSERT INTO bookings (id, code, customer_id, status, currency, pickup_at, due_at, package_id, package_code,
			package_label, package_duration_hours, gross_rent_total, discount_total, rent_total,
			security_deposit_total, reservation_deposit, policy_version, policy, fulfillment, created_at,
			hold_expires_at, updated_at)
		VALUES ($1, $1, $2, 'PENDING_CONFIRMATION', 'VND', $3, $3::timestamptz + interval '1 day', 'p', 'P', 'P', 24,
			0, 0, 0, 0, 0, 'v', '{}', '{}', $3, $3, $3)`, id, customer.UserID, pgtest.Now)
	if err != nil {
		t.Fatal(err)
	}
}

func block(t *testing.T, fromHours, toHours int) period.Range {
	t.Helper()
	r, err := period.New(pgtest.Now.Add(time.Duration(fromHours)*time.Hour), pgtest.Now.Add(time.Duration(toHours)*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// The exclusion constraint is the last line of defence: even a writer that
// skipped the asset locks cannot store two overlapping ACTIVE allocations.
func TestDatabaseRejectsOverlappingActiveAllocations(t *testing.T) {
	ctx := context.Background()
	f := pgtest.New(t)
	c := f.Catalog()
	asset := f.Assets(c.JacketM, 1)[0]
	cust := f.User("cust", identity.RoleCustomer)
	for _, b := range []string{f.Prefix + "-b1", f.Prefix + "-b2", f.Prefix + "-b3"} {
		insertBooking(t, f, b, cust)
	}
	alloc := func(id, booking string, r period.Range) inventory.Allocation {
		a, err := inventory.NewAllocation(inventory.AllocationID(f.Prefix+id), inventory.PlannedAllocation{
			BookingRef: booking, LineRef: "L1", ComponentKey: "item", AssetID: asset, VariantID: c.JacketM, Block: r,
		}, pgtest.Now)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	first := alloc("-a1", f.Prefix+"-b1", block(t, 10, 20))
	if err := inventorypg.InsertAllocations(ctx, f.Pool, []inventory.Allocation{first}); err != nil {
		t.Fatal(err)
	}
	overlap := alloc("-a2", f.Prefix+"-b2", block(t, 19, 30))
	if err := inventorypg.InsertAllocations(ctx, f.Pool, []inventory.Allocation{overlap}); !errors.Is(err, inventory.ErrInsufficientInventory) {
		t.Fatalf("overlapping insert err = %v, want ErrInsufficientInventory", err)
	}
	// Half-open blocks: ending exactly when the next starts is not an overlap.
	adjacent := alloc("-a3", f.Prefix+"-b2", block(t, 20, 30))
	if err := inventorypg.InsertAllocations(ctx, f.Pool, []inventory.Allocation{adjacent}); err != nil {
		t.Fatalf("adjacent insert: %v", err)
	}
	// A released allocation blocks nothing.
	if _, err := first.Release(pgtest.Now, "test"); err != nil {
		t.Fatal(err)
	}
	if err := inventorypg.UpdateAllocation(ctx, f.Pool, first); err != nil {
		t.Fatal(err)
	}
	reuse := alloc("-a4", f.Prefix+"-b3", block(t, 10, 20))
	if err := inventorypg.InsertAllocations(ctx, f.Pool, []inventory.Allocation{reuse}); err != nil {
		t.Fatalf("insert over a released block: %v", err)
	}
	// Re-blocking into a neighbour is rejected too.
	reuse.Block = block(t, 10, 25)
	if err := inventorypg.UpdateAllocation(ctx, f.Pool, reuse); !errors.Is(err, inventory.ErrInsufficientInventory) {
		t.Fatalf("overlapping reblock err = %v", err)
	}

	got, err := inventorypg.ActiveAllocationsOn(ctx, f.Pool, []inventory.AssetID{asset})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("active allocations = %d, want 2", len(got))
	}
}

func TestAllocationRowRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := pgtest.New(t)
	c := f.Catalog()
	asset := f.Assets(c.JacketM, 1)[0]
	cust := f.User("cust", identity.RoleCustomer)
	insertBooking(t, f, f.Prefix+"-b1", cust)
	a, err := inventory.NewAllocation(inventory.AllocationID(f.Prefix+"-a1"), inventory.PlannedAllocation{
		BookingRef: f.Prefix + "-b1", LineRef: "L1", ComponentKey: "item", AssetID: asset, VariantID: c.JacketM, Block: block(t, 1, 30),
	}, pgtest.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := inventorypg.InsertAllocations(ctx, f.Pool, []inventory.Allocation{a}); err != nil {
		t.Fatal(err)
	}
	if err := a.MarkHandedOver(pgtest.Now.Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := inventorypg.UpdateAllocation(ctx, f.Pool, a); err != nil {
		t.Fatal(err)
	}
	got, err := inventorypg.AllocationsOfBooking(ctx, f.Pool, f.Prefix+"-b1", false)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v, %v", got, err)
	}
	if !reflect.DeepEqual(got[0], a) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got[0], a)
	}
}

func TestAssetVariantMustMatchAllocation(t *testing.T) {
	ctx := context.Background()
	f := pgtest.New(t)
	c := f.Catalog()
	asset := f.Assets(c.JacketM, 1)[0]
	cust := f.User("cust", identity.RoleCustomer)
	insertBooking(t, f, f.Prefix+"-b1", cust)
	a, err := inventory.NewAllocation(inventory.AllocationID(f.Prefix+"-a1"), inventory.PlannedAllocation{
		BookingRef: f.Prefix + "-b1", LineRef: "L1", ComponentKey: "item", AssetID: asset, VariantID: c.JacketL, Block: block(t, 1, 30),
	}, pgtest.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := inventorypg.InsertAllocations(ctx, f.Pool, []inventory.Allocation{a}); err == nil {
		t.Fatal("an allocation claiming another variant than its asset must be rejected")
	}
}
