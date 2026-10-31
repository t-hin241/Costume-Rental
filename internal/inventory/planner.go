package inventory

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/period"
)

var ErrInsufficientInventory = domainerr.New(domainerr.KindConflict, "inventory.insufficient", "not enough assets are available for the requested time")

// AllocationPolicy decides which physical statuses may receive new
// allocations. REPAIR, MISSING and RETIRED assets are never allocatable;
// READY assets always are (subject to the calendar). The values are store
// policy pending owner confirmation.
type AllocationPolicy struct {
	// AllowNotReadyForFuture lets RENTED, NEEDS_INSPECTION and CLEANING
	// assets be reserved for blocks that start at least NotReadyMinLead after
	// now. Handover still requires the asset to be READY.
	AllowNotReadyForFuture bool
	NotReadyMinLead        time.Duration
}

// Validate checks the policy.
func (p AllocationPolicy) Validate() error {
	if p.NotReadyMinLead < 0 {
		return domainerr.Invalid("allocation_policy.not_ready_min_lead", "must not be negative")
	}
	return nil
}

func (p AllocationPolicy) eligible(a Asset, block period.Range, now time.Time) bool {
	switch a.Status {
	case AssetReady:
		return true
	case AssetRented, AssetNeedsInspection, AssetCleaning:
		return p.AllowNotReadyForFuture && !block.Start().Before(now.Add(p.NotReadyMinLead))
	}
	return false
}

// Demand is the quantity of one variant a booking line component needs for
// a block.
type Demand struct {
	LineRef      string
	ComponentKey string
	VariantID    catalog.VariantID
	Quantity     int
	Block        period.Range
}

// PlanRequest is everything needed to decide allocations. Assets and
// Allocations must be read from transactional state (never a cache), and the
// caller locks the candidate assets so the decision cannot go stale before
// commit.
type PlanRequest struct {
	BookingRef string
	Demands    []Demand
	// Assets are candidates for the demanded variants.
	Assets []Asset
	// Allocations are existing allocations on those assets. For reschedules,
	// exclude the booking's own allocations (see ExcludeBooking).
	Allocations []Allocation
	Now         time.Time
	Policy      AllocationPolicy
}

// PlannedAllocation is an allocation decided but not yet persisted.
type PlannedAllocation struct {
	BookingRef   string
	LineRef      string
	ComponentKey string
	AssetID      AssetID
	VariantID    catalog.VariantID
	Block        period.Range
	// ModificationRef and ExtendsAllocation mark tentative holds made by
	// PlanChange.
	ModificationRef   string
	ExtendsAllocation AllocationID
}

// Shortage describes one unmet demand.
type Shortage struct {
	LineRef      string
	ComponentKey string
	VariantID    catalog.VariantID
	Requested    int
	Available    int
}

// ShortageError lists every unmet demand; it matches ErrInsufficientInventory.
type ShortageError struct {
	Shortages []Shortage
}

func (e *ShortageError) Error() string {
	parts := make([]string, 0, len(e.Shortages))
	for _, s := range e.Shortages {
		parts = append(parts, fmt.Sprintf("%s/%s variant %s: requested %d, available %d", s.LineRef, s.ComponentKey, s.VariantID, s.Requested, s.Available))
	}
	return ErrInsufficientInventory.Error() + ": " + strings.Join(parts, "; ")
}

func (e *ShortageError) Unwrap() error { return ErrInsufficientInventory }

// Plan assigns distinct assets to every demand, all or nothing. A bundle and
// an individual item share the same assets, and an asset chosen for one
// demand is never counted again for another overlapping demand.
func Plan(req PlanRequest) ([]PlannedAllocation, error) {
	if err := validatePlanInput(req.BookingRef, req.Demands, req.Policy); err != nil {
		return nil, err
	}
	pl := newPool(req.Assets, req.Allocations, req.Now, req.Policy)
	var plan []PlannedAllocation
	var shortages []Shortage
	for _, d := range req.Demands {
		got := 0
		for got < d.Quantity {
			a, ok := pl.take(d.VariantID, d.Block)
			if !ok {
				break
			}
			plan = append(plan, PlannedAllocation{
				BookingRef: req.BookingRef, LineRef: d.LineRef, ComponentKey: d.ComponentKey,
				AssetID: a, VariantID: d.VariantID, Block: d.Block,
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
		return nil, &ShortageError{Shortages: shortages}
	}
	return plan, nil
}

func validatePlanInput(bookingRef string, demands []Demand, policy AllocationPolicy) error {
	if bookingRef == "" {
		return domainerr.Invalid("booking_ref", "required")
	}
	if len(demands) == 0 {
		return domainerr.Invalid("demands", "at least one demand is required")
	}
	if err := policy.Validate(); err != nil {
		return err
	}
	for _, d := range demands {
		if d.Quantity < 1 || d.VariantID == "" || d.Block.IsZero() {
			return domainerr.Invalid("demands", "each demand needs a variant, a block and quantity >= 1")
		}
	}
	return nil
}

// pool picks free assets and remembers what this plan already took so no
// asset is counted twice.
type pool struct {
	byVariant map[catalog.VariantID][]Asset
	existing  map[AssetID][]Allocation
	picked    map[AssetID][]period.Range
	now       time.Time
	policy    AllocationPolicy
}

func newPool(assets []Asset, allocs []Allocation, now time.Time, policy AllocationPolicy) *pool {
	p := &pool{byVariant: map[catalog.VariantID][]Asset{}, existing: map[AssetID][]Allocation{}, picked: map[AssetID][]period.Range{}, now: now, policy: policy}
	for _, a := range assets {
		p.byVariant[a.VariantID] = append(p.byVariant[a.VariantID], a)
	}
	for v := range p.byVariant {
		// Prefer READY assets, then a stable order so plans are reproducible.
		slices.SortFunc(p.byVariant[v], func(x, y Asset) int {
			if (x.Status == AssetReady) != (y.Status == AssetReady) {
				if x.Status == AssetReady {
					return -1
				}
				return 1
			}
			return cmp.Or(cmp.Compare(x.Code, y.Code), cmp.Compare(x.ID, y.ID))
		})
	}
	for _, al := range allocs {
		p.existing[al.AssetID] = append(p.existing[al.AssetID], al)
	}
	return p
}

// candidate reports whether the asset is still among the plan's candidates.
func (p *pool) candidate(variant catalog.VariantID, asset AssetID) bool {
	return slices.ContainsFunc(p.byVariant[variant], func(a Asset) bool { return a.ID == asset })
}

// free reports whether block on asset is clear of other allocations
// (ignoring skip) and of this plan's own picks.
func (p *pool) free(asset AssetID, block period.Range, skip AllocationID) bool {
	if slices.ContainsFunc(p.picked[asset], block.Overlaps) {
		return false
	}
	return !slices.ContainsFunc(p.existing[asset], func(al Allocation) bool { return al.ID != skip && al.Blocks(block, p.now) })
}

func (p *pool) take(variant catalog.VariantID, block period.Range) (AssetID, bool) {
	for _, a := range p.byVariant[variant] {
		if p.policy.eligible(a, block, p.now) && p.free(a.ID, block, "") {
			p.picked[a.ID] = append(p.picked[a.ID], block)
			return a.ID, true
		}
	}
	return "", false
}

// ExcludeBooking drops a booking's own allocations, used when re-planning a
// reschedule whose old allocations will be released in the same transaction.
func ExcludeBooking(allocs []Allocation, bookingRef string) []Allocation {
	return slices.DeleteFunc(slices.Clone(allocs), func(a Allocation) bool { return a.BookingRef == bookingRef })
}
