package inventory_test

import (
	"errors"
	"testing"
	"time"

	"webee_rentor/internal/inventory"
)

func TestTwoChangesCannotHoldTheSameReplacement(t *testing.T) {
	assets := []inventory.Asset{
		asset("JK-M-1", "jacket-m", inventory.AssetReady),
		asset("JK-M-2", "jacket-m", inventory.AssetReady),
		asset("JK-L-1", "jacket-l", inventory.AssetReady),
	}
	b := block(t, 0, 50*time.Hour)
	a1 := allocate(t, "a1", "bk-1", inventory.PlannedAllocation{LineRef: "l1", ComponentKey: "item", AssetID: "JK-M-1", VariantID: "jacket-m", Block: b})
	a2 := allocate(t, "a2", "bk-2", inventory.PlannedAllocation{LineRef: "l1", ComponentKey: "item", AssetID: "JK-M-2", VariantID: "jacket-m", Block: b})
	want := []inventory.Demand{{LineRef: "l1", ComponentKey: "item", VariantID: "jacket-l", Quantity: 1, Block: b}}

	first, err := inventory.PlanChange(inventory.ChangeRequest{
		BookingRef: "bk-1", ModificationRef: "m1", Demands: want, Own: []inventory.Allocation{a1},
		Assets: assets, Allocations: []inventory.Allocation{a1, a2}, Now: t0.Add(-72 * time.Hour),
	})
	if err != nil || len(first.Hold) != 1 || first.Hold[0].AssetID != "JK-L-1" || len(first.Release) != 1 {
		t.Fatalf("first plan = %+v, %v", first, err)
	}
	held := allocate(t, "h1", "bk-1", first.Hold[0])
	if !held.Tentative() {
		t.Fatal("hold must be tentative")
	}
	_, err = inventory.PlanChange(inventory.ChangeRequest{
		BookingRef: "bk-2", ModificationRef: "m2", Demands: want, Own: []inventory.Allocation{a2},
		Assets: assets, Allocations: []inventory.Allocation{a1, a2, held}, Now: t0.Add(-72 * time.Hour),
	})
	if !errors.Is(err, inventory.ErrInsufficientInventory) {
		t.Fatalf("second change for the held jacket L err = %v", err)
	}
}

func TestChangeKeepsOldAssetUntilConfirmed(t *testing.T) {
	// Only one jacket M exists and it is ours: shrinking or extending reuses it.
	assets := []inventory.Asset{asset("JK-M-1", "jacket-m", inventory.AssetReady)}
	own := allocate(t, "a1", "bk-1", inventory.PlannedAllocation{LineRef: "l1", ComponentKey: "item", AssetID: "JK-M-1", VariantID: "jacket-m", Block: block(t, 0, 50*time.Hour)})
	longer := block(t, 0, 98*time.Hour)
	plan, err := inventory.PlanChange(inventory.ChangeRequest{
		BookingRef: "bk-1", ModificationRef: "m1", Own: []inventory.Allocation{own},
		Demands: []inventory.Demand{{LineRef: "l1", ComponentKey: "item", VariantID: "jacket-m", Quantity: 1, Block: longer}},
		Assets:  assets, Allocations: []inventory.Allocation{own}, Now: t0.Add(-72 * time.Hour),
	})
	if err != nil || len(plan.Reblock) != 1 || len(plan.Hold) != 1 || plan.Hold[0].ExtendsAllocation != "a1" ||
		plan.Hold[0].Block != block(t, 50*time.Hour, 98*time.Hour) || len(plan.Release) != 0 {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	ext := allocate(t, "h1", "bk-1", plan.Hold[0])
	if err := ext.Adopt(); err == nil {
		t.Fatal("an extension hold is released, not adopted")
	}
	// Confirm: release the extension hold, then reblock the original.
	if _, err := ext.Release(t0, "modification confirmed"); err != nil {
		t.Fatal(err)
	}
	if err := own.Reblock(plan.Reblock[0].Block); err != nil || own.Block != longer {
		t.Fatalf("reblock = %v", err)
	}
}

func TestReblockKeepsHandoverInsideBlock(t *testing.T) {
	a := allocate(t, "a1", "bk-1", inventory.PlannedAllocation{AssetID: "JK-M-1", VariantID: "jacket-m", Block: block(t, 0, 50*time.Hour)})
	if err := a.MarkHandedOver(t0.Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := a.Reblock(block(t, 3*time.Hour, 60*time.Hour)); !errors.Is(err, inventory.ErrInvalidReblock) {
		t.Fatalf("moving the block past the handover err = %v", err)
	}
	if err := a.Reblock(block(t, 0, 98*time.Hour)); err != nil {
		t.Fatalf("extending an in-use allocation: %v", err)
	}
}
