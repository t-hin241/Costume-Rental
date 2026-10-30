package inventory

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/period"
)

type AllocationID string

// AllocationStatus is the scheduling state of an allocation.
type AllocationStatus string

const (
	// AllocationActive blocks the asset for Block (or longer when overdue).
	AllocationActive AllocationStatus = "ACTIVE"
	// AllocationReleased was cancelled before handover and blocks nothing.
	AllocationReleased AllocationStatus = "RELEASED"
	// AllocationClosed finished after the asset came back and was resolved.
	AllocationClosed AllocationStatus = "CLOSED"
)

var (
	ErrAllocationInUse      = domainerr.New(domainerr.KindInvalidState, "inventory.allocation_in_use", "allocation has been handed over and cannot be released")
	ErrAllocationNotActive  = domainerr.New(domainerr.KindInvalidState, "inventory.allocation_not_active", "allocation is not active")
	ErrHandoverOutsideBlock = domainerr.New(domainerr.KindInvalidState, "inventory.handover_outside_block", "handover time is outside the allocation block")
	ErrAlreadyHandedOver    = domainerr.New(domainerr.KindInvalidState, "inventory.already_handed_over", "allocation was already handed over")
	ErrNotHandedOver        = domainerr.New(domainerr.KindInvalidState, "inventory.not_handed_over", "allocation was not handed over")
	ErrAssetNotResolved     = domainerr.New(domainerr.KindInvalidState, "inventory.asset_not_resolved", "asset must be returned and READY, under repair, missing or retired before closing")
)

// Allocation reserves one asset for one booking line component.
//
// Persistence must enforce at most one ACTIVE allocation per asset for any
// overlapping block (PostgreSQL EXCLUDE USING gist on asset_id and a
// tstzrange '[)' block), so two transactions competing for the last asset
// cannot both commit.
type Allocation struct {
	ID           AllocationID
	BookingRef   string
	LineRef      string
	ComponentKey string
	AssetID      AssetID
	VariantID    catalog.VariantID
	// Block is [blocked_from, blocked_until): preparation buffer before
	// pickup through turnaround buffer after due.
	Block         period.Range
	Status        AllocationStatus
	CreatedAt     time.Time
	HandedOverAt  *time.Time
	ReturnedAt    *time.Time
	ReleasedAt    *time.Time
	ReleaseReason string
	ClosedAt      *time.Time
	// ModificationRef marks a tentative hold for a pending booking
	// modification. It blocks like any active allocation, is not used for
	// handover, and is adopted or released when the modification ends.
	ModificationRef string
	// ExtendsAllocation marks a tentative hold that only covers the extra
	// time an existing allocation of the same asset will be reblocked to.
	ExtendsAllocation AllocationID
}

var ErrInvalidReblock = domainerr.New(domainerr.KindInvalid, "inventory.invalid_reblock", "the new block must keep the handover time inside it")

// NewAllocation materialises a planned allocation with a generated ID.
func NewAllocation(id AllocationID, p PlannedAllocation, now time.Time) (Allocation, error) {
	if id == "" {
		return Allocation{}, domainerr.Invalid("id", "required")
	}
	if p.BookingRef == "" || p.AssetID == "" || p.VariantID == "" || p.Block.IsZero() {
		return Allocation{}, domainerr.Invalid("allocation", "booking, asset, variant and block are required")
	}
	return Allocation{
		ID: id, BookingRef: p.BookingRef, LineRef: p.LineRef, ComponentKey: p.ComponentKey,
		AssetID: p.AssetID, VariantID: p.VariantID, Block: p.Block,
		Status: AllocationActive, CreatedAt: now.UTC(),
		ModificationRef: p.ModificationRef, ExtendsAllocation: p.ExtendsAllocation,
	}, nil
}

// Tentative reports whether the allocation is a hold for a pending change.
func (a Allocation) Tentative() bool { return a.ModificationRef != "" }

// Adopt turns a tentative hold into a regular allocation when its
// modification is confirmed.
func (a *Allocation) Adopt() error {
	if a.Status != AllocationActive || !a.Tentative() || a.ExtendsAllocation != "" {
		return fmt.Errorf("allocation %s: %w", a.ID, ErrAllocationNotActive)
	}
	a.ModificationRef = ""
	return nil
}

// Reblock changes the block of an active allocation when a confirmed
// modification keeps the same asset. Release its extension holds first in
// the same transaction so the exclusion constraint is not violated.
func (a *Allocation) Reblock(block period.Range) error {
	if a.Status != AllocationActive || a.Tentative() {
		return fmt.Errorf("allocation %s: %w", a.ID, ErrAllocationNotActive)
	}
	if a.HandedOverAt != nil && !block.Contains(*a.HandedOverAt) {
		return fmt.Errorf("allocation %s: %w", a.ID, ErrInvalidReblock)
	}
	a.Block = block
	return nil
}

// InUse reports whether the asset is with the customer under this allocation.
func (a Allocation) InUse() bool {
	return a.HandedOverAt != nil && a.ReturnedAt == nil
}

// Blocks reports whether this allocation prevents using its asset during r.
// An asset still with the customer after its block ended (late return) blocks
// every later range until it is returned, so it can never be double-allocated.
func (a Allocation) Blocks(r period.Range, now time.Time) bool {
	if a.Status != AllocationActive {
		return false
	}
	if a.InUse() && !now.Before(a.Block.End()) {
		return r.End().After(a.Block.Start())
	}
	return a.Block.Overlaps(r)
}

// Release frees the allocation before handover (cancel, expiry, payment
// failure, reschedule). Releasing twice is a no-op so retries are safe.
func (a *Allocation) Release(at time.Time, reason string) (bool, error) {
	switch a.Status {
	case AllocationReleased:
		return false, nil
	case AllocationClosed:
		return false, fmt.Errorf("allocation %s: %w", a.ID, ErrAllocationNotActive)
	}
	if a.HandedOverAt != nil {
		return false, fmt.Errorf("allocation %s: %w", a.ID, ErrAllocationInUse)
	}
	if strings.TrimSpace(reason) == "" {
		return false, ErrReasonRequired
	}
	t := at.UTC()
	a.Status, a.ReleasedAt, a.ReleaseReason = AllocationReleased, &t, reason
	return true, nil
}

// MarkHandedOver records that the allocated asset left the store. Handover
// before the block starts would collide with the previous booking's buffer.
func (a *Allocation) MarkHandedOver(at time.Time) error {
	if a.Status != AllocationActive {
		return fmt.Errorf("allocation %s: %w", a.ID, ErrAllocationNotActive)
	}
	if a.HandedOverAt != nil {
		return fmt.Errorf("allocation %s: %w", a.ID, ErrAlreadyHandedOver)
	}
	if !a.Block.Contains(at) {
		return fmt.Errorf("allocation %s at %s: %w", a.ID, at.UTC(), ErrHandoverOutsideBlock)
	}
	t := at.UTC()
	a.HandedOverAt = &t
	return nil
}

// MarkReturned records that the asset is physically back.
func (a *Allocation) MarkReturned(at time.Time) error {
	if a.Status != AllocationActive {
		return fmt.Errorf("allocation %s: %w", a.ID, ErrAllocationNotActive)
	}
	if !a.InUse() {
		return fmt.Errorf("allocation %s: %w", a.ID, ErrNotHandedOver)
	}
	if at.Before(*a.HandedOverAt) {
		return domainerr.Invalid("returned_at", "cannot be before handover")
	}
	t := at.UTC()
	a.ReturnedAt = &t
	return nil
}

// Close ends the allocation once the asset is resolved, freeing any remaining
// turnaround buffer early. A returned asset must be READY (or REPAIR, which is
// itself never allocatable); a missing or retired asset may close without a
// return.
func (a *Allocation) Close(at time.Time, asset Asset) error {
	if a.Status != AllocationActive {
		return fmt.Errorf("allocation %s: %w", a.ID, ErrAllocationNotActive)
	}
	if asset.ID != a.AssetID {
		return domainerr.Invalid("asset", "does not belong to this allocation")
	}
	resolved := asset.Status == AssetMissing || asset.Status == AssetRetired ||
		(a.ReturnedAt != nil && (asset.Status == AssetReady || asset.Status == AssetRepair))
	if !resolved {
		return fmt.Errorf("allocation %s asset %s %s: %w", a.ID, asset.ID, asset.Status, ErrAssetNotResolved)
	}
	t := at.UTC()
	a.Status, a.ClosedAt = AllocationClosed, &t
	return nil
}

// OverdueConflicts lists the upcoming allocations of the same asset that a
// late return endangers: active, not handed over, and starting before
// now+horizon. Staff must resolve them (equivalent asset, reschedule,
// contact, cancel per policy); the system never double-allocates.
func OverdueConflicts(overdue Allocation, others []Allocation, now time.Time, horizon time.Duration) []Allocation {
	if !overdue.InUse() || overdue.Status != AllocationActive {
		return nil
	}
	limit := now.Add(horizon)
	var out []Allocation
	for _, o := range others {
		if o.ID == overdue.ID || o.AssetID != overdue.AssetID || o.Status != AllocationActive || o.HandedOverAt != nil {
			continue
		}
		if o.Block.Start().Before(overdue.Block.Start()) {
			continue
		}
		if o.Block.Start().Before(limit) {
			out = append(out, o)
		}
	}
	slices.SortFunc(out, func(x, y Allocation) int { return x.Block.Start().Compare(y.Block.Start()) })
	return out
}
