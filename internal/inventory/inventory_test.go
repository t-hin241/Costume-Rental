package inventory_test

import (
	"errors"
	"testing"
	"time"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/period"
)

var (
	staff = identity.UserActor("staff1")
	t0    = time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC) // 08:00 Asia/Ho_Chi_Minh
	ready = inventory.AllocationPolicy{}
)

func block(t *testing.T, from, to time.Duration) period.Range {
	t.Helper()
	r, err := period.New(t0.Add(from), t0.Add(to))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func asset(id inventory.AssetID, v catalog.VariantID, s inventory.AssetStatus) inventory.Asset {
	return inventory.Asset{ID: id, Code: string(id), VariantID: v, Status: s}
}

func allocate(t *testing.T, id inventory.AllocationID, booking string, p inventory.PlannedAllocation) inventory.Allocation {
	t.Helper()
	p.BookingRef = booking
	a, err := inventory.NewAllocation(id, p, t0.Add(-48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestReturnDoesNotMakeAssetReady(t *testing.T) {
	a, intake, err := inventory.NewAsset("CST-1", "CST-00042", "jacket-m", staff, t0)
	if err != nil || a.Status != inventory.AssetNeedsInspection || intake.To != inventory.AssetNeedsInspection {
		t.Fatalf("new asset = %+v, %v", a, err)
	}
	if _, err := a.PassInspection(staff, t0, inventory.InspectionChecklist{Completed: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.HandOver(staff, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ReceiveReturn(staff, t0.Add(day)); err != nil {
		t.Fatal(err)
	}
	if a.Status == inventory.AssetReady {
		t.Fatal("a returned asset must not be READY before inspection")
	}
	if _, err := a.HandOver(staff, t0.Add(day)); !errors.Is(err, inventory.ErrInvalidAssetTransition) {
		t.Fatalf("handover of uninspected asset err = %v", err)
	}
	if _, err := a.PassInspection(staff, t0, inventory.InspectionChecklist{}); !errors.Is(err, inventory.ErrChecklistIncomplete) {
		t.Fatalf("incomplete checklist err = %v", err)
	}
	if _, err := a.RequireCleaning(staff, t0, inventory.InspectionChecklist{Completed: true}); err != nil {
		t.Fatal(err)
	}
	if a.Status != inventory.AssetCleaning {
		t.Fatalf("status = %s", a.Status)
	}
	change, err := a.CompleteCleaning(staff, t0.Add(30*time.Hour))
	if err != nil || a.Status != inventory.AssetReady || change.From != inventory.AssetCleaning {
		t.Fatalf("complete cleaning = %+v, %v", change, err)
	}
}

func TestRepairGoesBackThroughInspection(t *testing.T) {
	a := asset("CST-1", "jacket-m", inventory.AssetNeedsInspection)
	if _, err := a.SendToRepair(staff, t0, ""); !errors.Is(err, inventory.ErrReasonRequired) {
		t.Fatalf("repair without reason err = %v", err)
	}
	if _, err := a.SendToRepair(staff, t0, "rách tay áo"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CompleteRepair(staff, t0); err != nil || a.Status != inventory.AssetNeedsInspection {
		t.Fatalf("after repair status = %s, %v", a.Status, err)
	}
	if _, err := a.Retire(staff, t0, "hỏng không sửa được"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RecordFound(staff, t0, "x"); !errors.Is(err, inventory.ErrInvalidAssetTransition) {
		t.Fatalf("retired is terminal, err = %v", err)
	}
}

func TestBundleAndSingleItemShareStock(t *testing.T) {
	// One jacket M in stock. A bundle booking takes it, so a single-jacket
	// booking for the same time cannot get it.
	assets := []inventory.Asset{
		asset("JK-M-1", "jacket-m", inventory.AssetReady),
		asset("PT-L-1", "pants-l", inventory.AssetReady),
		asset("BL-1", "belt-std", inventory.AssetReady),
	}
	b := block(t, 0, 50*time.Hour)
	bundlePlan, err := inventory.Plan(inventory.PlanRequest{
		BookingRef: "bundle-booking", Now: t0.Add(-72 * time.Hour), Assets: assets, Policy: ready,
		Demands: []inventory.Demand{
			{LineRef: "l1", ComponentKey: "jacket", VariantID: "jacket-m", Quantity: 1, Block: b},
			{LineRef: "l1", ComponentKey: "pants", VariantID: "pants-l", Quantity: 1, Block: b},
			{LineRef: "l1", ComponentKey: "belt", VariantID: "belt-std", Quantity: 1, Block: b},
		},
	})
	if err != nil || len(bundlePlan) != 3 {
		t.Fatalf("bundle plan = %+v, %v", bundlePlan, err)
	}
	var existing []inventory.Allocation
	for i, p := range bundlePlan {
		existing = append(existing, allocate(t, inventory.AllocationID(rune('a'+i)), "bundle-booking", p))
	}

	_, err = inventory.Plan(inventory.PlanRequest{
		BookingRef: "single-booking", Now: t0.Add(-72 * time.Hour), Assets: assets, Allocations: existing, Policy: ready,
		Demands: []inventory.Demand{{LineRef: "l1", ComponentKey: "item", VariantID: "jacket-m", Quantity: 1, Block: block(t, 10*time.Hour, 20*time.Hour)}},
	})
	var shortage *inventory.ShortageError
	if !errors.As(err, &shortage) || !errors.Is(err, inventory.ErrInsufficientInventory) {
		t.Fatalf("single jacket err = %v", err)
	}
	if s := shortage.Shortages[0]; s.VariantID != "jacket-m" || s.Requested != 1 || s.Available != 0 {
		t.Fatalf("shortage = %+v", s)
	}

	// Pants M was never part of the bundle allocation, so it is unaffected.
	assets = append(assets, asset("PT-M-1", "pants-m", inventory.AssetReady))
	if _, err := inventory.Plan(inventory.PlanRequest{
		BookingRef: "single-pants", Now: t0.Add(-72 * time.Hour), Assets: assets, Allocations: existing, Policy: ready,
		Demands: []inventory.Demand{{LineRef: "l1", ComponentKey: "item", VariantID: "pants-m", Quantity: 1, Block: b}},
	}); err != nil {
		t.Fatalf("pants M should be free: %v", err)
	}
}

func TestSameVariantNeverCountedTwiceInOneBooking(t *testing.T) {
	// A bundle and a separate jacket line in one booking both need jacket M;
	// only one unit exists.
	assets := []inventory.Asset{asset("JK-M-1", "jacket-m", inventory.AssetReady)}
	b := block(t, 0, 24*time.Hour)
	_, err := inventory.Plan(inventory.PlanRequest{
		BookingRef: "b1", Now: t0.Add(-72 * time.Hour), Assets: assets, Policy: ready,
		Demands: []inventory.Demand{
			{LineRef: "bundle", ComponentKey: "jacket", VariantID: "jacket-m", Quantity: 1, Block: b},
			{LineRef: "extra", ComponentKey: "item", VariantID: "jacket-m", Quantity: 1, Block: b},
		},
	})
	if !errors.Is(err, inventory.ErrInsufficientInventory) {
		t.Fatalf("err = %v, want shortage", err)
	}
}

func TestAdjacentAndOverlappingBlocks(t *testing.T) {
	assets := []inventory.Asset{asset("JK-M-1", "jacket-m", inventory.AssetReady)}
	first := allocate(t, "a1", "b1", inventory.PlannedAllocation{
		AssetID: "JK-M-1", VariantID: "jacket-m", Block: block(t, 0, 50*time.Hour),
	})
	plan := func(from, to time.Duration) error {
		_, err := inventory.Plan(inventory.PlanRequest{
			BookingRef: "b2", Now: t0.Add(-72 * time.Hour), Assets: assets,
			Allocations: []inventory.Allocation{first}, Policy: ready,
			Demands: []inventory.Demand{{LineRef: "l", ComponentKey: "item", VariantID: "jacket-m", Quantity: 1, Block: block(t, from, to)}},
		})
		return err
	}
	if err := plan(50*time.Hour, 80*time.Hour); err != nil {
		t.Fatalf("block starting exactly at previous block end must fit: %v", err)
	}
	if err := plan(49*time.Hour, 80*time.Hour); !errors.Is(err, inventory.ErrInsufficientInventory) {
		t.Fatalf("one hour into previous turnaround buffer err = %v", err)
	}
	if err := plan(-20*time.Hour, time.Minute); !errors.Is(err, inventory.ErrInsufficientInventory) {
		t.Fatalf("overlap at start err = %v", err)
	}
}

func TestStatusEligibility(t *testing.T) {
	now := t0
	soon := block(t, 2*time.Hour, 26*time.Hour)
	later := block(t, 7*day, 8*day)
	demand := func(b period.Range) []inventory.Demand {
		return []inventory.Demand{{LineRef: "l", ComponentKey: "item", VariantID: "jacket-m", Quantity: 1, Block: b}}
	}
	for _, s := range []inventory.AssetStatus{inventory.AssetRepair, inventory.AssetMissing, inventory.AssetRetired} {
		policy := inventory.AllocationPolicy{AllowNotReadyForFuture: true}
		_, err := inventory.Plan(inventory.PlanRequest{BookingRef: "b", Now: now, Policy: policy,
			Assets: []inventory.Asset{asset("x", "jacket-m", s)}, Demands: demand(later)})
		if !errors.Is(err, inventory.ErrInsufficientInventory) {
			t.Errorf("%s must never be allocatable, err = %v", s, err)
		}
	}
	cleaning := []inventory.Asset{asset("x", "jacket-m", inventory.AssetCleaning)}
	if _, err := inventory.Plan(inventory.PlanRequest{BookingRef: "b", Now: now, Policy: ready, Assets: cleaning, Demands: demand(later)}); !errors.Is(err, inventory.ErrInsufficientInventory) {
		t.Errorf("cleaning asset must not be allocatable when policy disallows it, err = %v", err)
	}
	future := inventory.AllocationPolicy{AllowNotReadyForFuture: true, NotReadyMinLead: 3 * day}
	if _, err := inventory.Plan(inventory.PlanRequest{BookingRef: "b", Now: now, Policy: future, Assets: cleaning, Demands: demand(soon)}); !errors.Is(err, inventory.ErrInsufficientInventory) {
		t.Errorf("cleaning asset must not be allocated inside the lead time, err = %v", err)
	}
	if _, err := inventory.Plan(inventory.PlanRequest{BookingRef: "b", Now: now, Policy: future, Assets: cleaning, Demands: demand(later)}); err != nil {
		t.Errorf("cleaning asset may be reserved beyond the lead time when allowed: %v", err)
	}
}

func TestNotReadyAssetsAreOutOfStockByDefault(t *testing.T) {
	// Decision D33: what is available for rent never exceeds what is ready.
	far := block(t, 30*day, 31*day)
	demand := []inventory.Demand{{LineRef: "l", ComponentKey: "item", VariantID: "jacket-m", Quantity: 1, Block: far}}
	for _, s := range []inventory.AssetStatus{inventory.AssetRented, inventory.AssetNeedsInspection, inventory.AssetCleaning} {
		_, err := inventory.Plan(inventory.PlanRequest{BookingRef: "b", Now: t0, Policy: inventory.AllocationPolicy{},
			Assets: []inventory.Asset{asset("x", "jacket-m", s)}, Demands: demand})
		if !errors.Is(err, inventory.ErrInsufficientInventory) {
			t.Errorf("%s must not count as available even a month ahead, err = %v", s, err)
		}
	}
}

func TestLateReturnBlocksUntilReturned(t *testing.T) {
	assets := []inventory.Asset{asset("JK-M-1", "jacket-m", inventory.AssetRented)}
	out := allocate(t, "a1", "b1", inventory.PlannedAllocation{AssetID: "JK-M-1", VariantID: "jacket-m", Block: block(t, 0, 50*time.Hour)})
	if err := out.MarkHandedOver(t0.Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	policy := inventory.AllocationPolicy{AllowNotReadyForFuture: true}
	request := block(t, 10*day, 11*day)
	plan := func(now time.Time, al inventory.Allocation) error {
		_, err := inventory.Plan(inventory.PlanRequest{
			BookingRef: "b2", Now: now, Assets: assets, Allocations: []inventory.Allocation{al}, Policy: policy,
			Demands: []inventory.Demand{{LineRef: "l", ComponentKey: "item", VariantID: "jacket-m", Quantity: 1, Block: request}},
		})
		return err
	}
	if err := plan(t0.Add(3*time.Hour), out); err != nil {
		t.Fatalf("before the block ends a future booking may be taken: %v", err)
	}
	if err := plan(t0.Add(51*time.Hour), out); !errors.Is(err, inventory.ErrInsufficientInventory) {
		t.Fatalf("an overdue asset must block every later range, err = %v", err)
	}

	next := allocate(t, "a2", "b2", inventory.PlannedAllocation{AssetID: "JK-M-1", VariantID: "jacket-m", Block: block(t, 52*time.Hour, 80*time.Hour)})
	far := allocate(t, "a3", "b3", inventory.PlannedAllocation{AssetID: "JK-M-1", VariantID: "jacket-m", Block: block(t, 30*day, 31*day)})
	conflicts := inventory.OverdueConflicts(out, []inventory.Allocation{far, next, out}, t0.Add(51*time.Hour), 3*day)
	if len(conflicts) != 1 || conflicts[0].ID != "a2" {
		t.Fatalf("conflicts = %+v, want only a2", conflicts)
	}
}

func TestAllocationLifecycle(t *testing.T) {
	al := allocate(t, "a1", "b1", inventory.PlannedAllocation{AssetID: "JK-M-1", VariantID: "jacket-m", Block: block(t, 0, 50*time.Hour)})
	if err := al.MarkHandedOver(t0.Add(-time.Minute)); !errors.Is(err, inventory.ErrHandoverOutsideBlock) {
		t.Fatalf("handover before block err = %v", err)
	}
	if err := al.MarkHandedOver(t0.Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := al.Release(t0, "cancel"); !errors.Is(err, inventory.ErrAllocationInUse) {
		t.Fatalf("release in use err = %v", err)
	}
	if err := al.MarkReturned(t0.Add(26 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	cleaning := asset("JK-M-1", "jacket-m", inventory.AssetCleaning)
	if err := al.Close(t0.Add(27*time.Hour), cleaning); !errors.Is(err, inventory.ErrAssetNotResolved) {
		t.Fatalf("close while cleaning err = %v", err)
	}
	readyAsset := asset("JK-M-1", "jacket-m", inventory.AssetReady)
	if err := al.Close(t0.Add(30*time.Hour), readyAsset); err != nil {
		t.Fatal(err)
	}
	// Closed early: the remaining turnaround buffer no longer blocks.
	if al.Blocks(block(t, 31*time.Hour, 40*time.Hour), t0.Add(30*time.Hour)) {
		t.Fatal("closed allocation must not block")
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	al := allocate(t, "a1", "b1", inventory.PlannedAllocation{AssetID: "JK-M-1", VariantID: "jacket-m", Block: block(t, 0, time.Hour)})
	changed, err := al.Release(t0, "hold expired")
	if err != nil || !changed {
		t.Fatalf("first release = %v, %v", changed, err)
	}
	changed, err = al.Release(t0.Add(time.Minute), "hold expired")
	if err != nil || changed {
		t.Fatalf("second release = %v, %v; want no-op", changed, err)
	}
	if al.Blocks(block(t, 0, time.Hour), t0) {
		t.Fatal("released allocation must not block")
	}
}

const day = 24 * time.Hour
