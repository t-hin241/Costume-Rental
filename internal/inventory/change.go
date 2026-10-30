package inventory

import (
	"slices"
	"time"

	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/period"
)

// ChangeRequest re-plans the allocations of the booking lines a modification
// touches (size, bundle or package change, or an extension).
type ChangeRequest struct {
	BookingRef      string
	ModificationRef string
	// Demands are the full target demands of the changed lines.
	Demands []Demand
	// Own are the booking's current active, non-tentative allocations of the
	// changed lines; they may be kept, reblocked or released.
	Own []Allocation
	// Assets are candidates for the demanded variants; Allocations are all
	// allocations on those assets (own ones included).
	Assets      []Asset
	Allocations []Allocation
	Now         time.Time
	Policy      AllocationPolicy
}

// Reblock moves an own allocation to a new block on the same asset.
type Reblock struct {
	AllocationID AllocationID
	Block        period.Range
}

// ChangePlan is applied in two steps. Now: insert Hold (tentative, tagged
// with the modification). On confirmation, in one transaction: release the
// extension holds, apply Reblock, adopt the remaining holds, release Release.
// On rejection or expiry: release every hold. Old allocations are never
// released before their replacements are held.
type ChangePlan struct {
	Keep    []AllocationID
	Reblock []Reblock
	Hold    []PlannedAllocation
	Release []AllocationID
}

// PlanChange reuses an own allocation of the same line, component and
// variant when possible (unchanged, or reblocked with its extra time held
// separately), and otherwise holds a different free asset. All or nothing.
func PlanChange(req ChangeRequest) (ChangePlan, error) {
	if err := validatePlanInput(req.BookingRef, req.Demands, req.Policy); err != nil {
		return ChangePlan{}, err
	}
	if req.ModificationRef == "" {
		return ChangePlan{}, domainerr.Invalid("modification_ref", "required")
	}
	pl := newPool(req.Assets, req.Allocations, req.Now, req.Policy)
	used := map[AllocationID]bool{}
	var plan ChangePlan
	var shortages []Shortage
	for _, d := range req.Demands {
		got := 0
		for got < d.Quantity {
			if own, ok := reusable(req.Own, used, d); ok {
				extra := d.Block.Minus(own.Block)
				if slices.ContainsFunc(extra, func(r period.Range) bool { return !pl.free(own.AssetID, r, own.ID) }) {
					used[own.ID] = true // cannot stretch it; try another asset
					continue
				}
				used[own.ID] = true
				pl.picked[own.AssetID] = append(pl.picked[own.AssetID], d.Block)
				if own.Block == d.Block {
					plan.Keep = append(plan.Keep, own.ID)
				} else {
					plan.Reblock = append(plan.Reblock, Reblock{AllocationID: own.ID, Block: d.Block})
					for _, r := range extra {
						plan.Hold = append(plan.Hold, PlannedAllocation{
							BookingRef: req.BookingRef, LineRef: d.LineRef, ComponentKey: d.ComponentKey,
							AssetID: own.AssetID, VariantID: d.VariantID, Block: r,
							ModificationRef: req.ModificationRef, ExtendsAllocation: own.ID,
						})
					}
				}
				got++
				continue
			}
			asset, ok := pl.take(d.VariantID, d.Block)
			if !ok {
				break
			}
			plan.Hold = append(plan.Hold, PlannedAllocation{
				BookingRef: req.BookingRef, LineRef: d.LineRef, ComponentKey: d.ComponentKey,
				AssetID: asset, VariantID: d.VariantID, Block: d.Block, ModificationRef: req.ModificationRef,
			})
			got++
		}
		if got < d.Quantity {
			shortages = append(shortages, Shortage{
				LineRef: d.LineRef, ComponentKey: d.ComponentKey, VariantID: d.VariantID,
				Requested: d.Quantity, Available: got,
			})
		}
	}
	if len(shortages) > 0 {
		return ChangePlan{}, &ShortageError{Shortages: shortages}
	}
	for _, o := range req.Own {
		if !slices.Contains(plan.Keep, o.ID) && !slices.ContainsFunc(plan.Reblock, func(r Reblock) bool { return r.AllocationID == o.ID }) {
			plan.Release = append(plan.Release, o.ID)
		}
	}
	return plan, nil
}

func reusable(own []Allocation, used map[AllocationID]bool, d Demand) (Allocation, bool) {
	for _, o := range own {
		if !used[o.ID] && o.Status == AllocationActive && !o.Tentative() &&
			o.LineRef == d.LineRef && o.ComponentKey == d.ComponentKey && o.VariantID == d.VariantID {
			return o, true
		}
	}
	return Allocation{}, false
}
