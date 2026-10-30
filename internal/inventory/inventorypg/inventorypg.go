// Package inventorypg stores assets, their movement log and allocations in
// PostgreSQL.
//
// Locking contract, shared by every use case that adds or extends blocks:
//
//  1. Lock the candidate assets with LockAssetsOfVariants or LockAssets.
//     Locks are always taken in asset id order, so concurrent writers queue
//     instead of deadlocking.
//  2. Read their allocations with ActiveAllocationsOn after the locks are
//     held; under READ COMMITTED each statement sees the latest commits, so
//     the plan cannot be based on stale state.
//  3. Plan with the inventory domain and write in the same transaction.
//
// The allocations_no_overlap exclusion constraint is the backstop: if a
// writer ever skips the locks, the database still refuses an overlapping
// ACTIVE allocation and the error is reported as ErrInsufficientInventory.
package inventorypg

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/period"
	"webee_rentor/internal/platform/postgres"
)

const overlapConstraint = "allocations_no_overlap"

const assetColumns = `id, code, variant_id, status, status_changed_at`

// InsertAsset registers a new asset with its intake movement.
func InsertAsset(ctx context.Context, q postgres.Querier, a inventory.Asset, intake inventory.StatusChange) error {
	_, err := q.Exec(ctx, `
		INSERT INTO assets (id, code, variant_id, status, status_changed_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $5)`, a.ID, a.Code, a.VariantID, a.Status, a.StatusChangedAt.UTC())
	if postgres.IsUniqueViolation(err, "assets_code_key") {
		return fmt.Errorf("asset code %s: %w", a.Code, postgres.ErrDuplicate)
	}
	if err != nil {
		return fmt.Errorf("insert asset %s: %w", a.ID, err)
	}
	return appendChange(ctx, q, intake)
}

// UpdateAsset persists a physical transition and its movement row. The asset
// row must have been locked in this transaction.
func UpdateAsset(ctx context.Context, q postgres.Querier, a inventory.Asset, change inventory.StatusChange) error {
	tag, err := q.Exec(ctx, `UPDATE assets SET status = $2, status_changed_at = $3 WHERE id = $1 AND status = $4`,
		a.ID, a.Status, a.StatusChangedAt.UTC(), change.From)
	if err != nil {
		return fmt.Errorf("update asset %s: %w", a.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("update asset %s from %s: %w", a.ID, change.From, postgres.ErrStale)
	}
	return appendChange(ctx, q, change)
}

func appendChange(ctx context.Context, q postgres.Querier, c inventory.StatusChange) error {
	var from *inventory.AssetStatus
	if c.From != "" {
		from = &c.From
	}
	_, err := q.Exec(ctx, `
		INSERT INTO asset_status_changes (asset_id, action, from_status, to_status, actor_kind, actor_id, at, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		c.AssetID, c.Action, from, c.To, c.Actor.Kind, c.Actor.ID, c.At.UTC(), c.Reason)
	if err != nil {
		return fmt.Errorf("asset %s movement %s: %w", c.AssetID, c.Action, err)
	}
	return nil
}

// LockAssetsOfVariants locks every asset of the variants, in id order, and
// returns them. Retired assets are skipped; they can never be allocated.
func LockAssetsOfVariants(ctx context.Context, q postgres.Querier, variants []catalog.VariantID) ([]inventory.Asset, error) {
	rows, err := q.Query(ctx, `SELECT `+assetColumns+` FROM assets
		WHERE variant_id = ANY($1) AND status <> 'RETIRED' ORDER BY id FOR UPDATE`, unique(variants))
	if err != nil {
		return nil, fmt.Errorf("lock assets of variants: %w", err)
	}
	return collectAssets(rows)
}

// LockAssets locks the given assets in id order. Every ID must exist.
func LockAssets(ctx context.Context, q postgres.Querier, ids []inventory.AssetID) ([]inventory.Asset, error) {
	ids = unique(ids)
	rows, err := q.Query(ctx, `SELECT `+assetColumns+` FROM assets WHERE id = ANY($1) ORDER BY id FOR UPDATE`, ids)
	if err != nil {
		return nil, fmt.Errorf("lock assets: %w", err)
	}
	assets, err := collectAssets(rows)
	if err != nil {
		return nil, err
	}
	if len(assets) != len(ids) {
		return nil, fmt.Errorf("lock assets: %d of %d found: %w", len(assets), len(ids), postgres.ErrNotFound)
	}
	return assets, nil
}

// Assets reads assets without locking, for reference views only.
func Assets(ctx context.Context, q postgres.Querier, variants []catalog.VariantID) ([]inventory.Asset, error) {
	rows, err := q.Query(ctx, `SELECT `+assetColumns+` FROM assets
		WHERE variant_id = ANY($1) AND status <> 'RETIRED' ORDER BY id`, unique(variants))
	if err != nil {
		return nil, fmt.Errorf("read assets: %w", err)
	}
	return collectAssets(rows)
}

func collectAssets(rows pgx.Rows) ([]inventory.Asset, error) {
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (inventory.Asset, error) {
		var a inventory.Asset
		err := r.Scan(&a.ID, &a.Code, &a.VariantID, &a.Status, &a.StatusChangedAt)
		a.StatusChangedAt = a.StatusChangedAt.UTC()
		return a, err
	})
	if err != nil {
		return nil, fmt.Errorf("scan assets: %w", err)
	}
	return out, nil
}

const allocationColumns = `id, booking_id, line_id, component_key, asset_id, variant_id, blocked_from, blocked_until,
	status, created_at, handed_over_at, returned_at, released_at, release_reason, closed_at,
	coalesce(modification_id, ''), coalesce(extends_allocation_id, '')`

// ActiveAllocationsOn returns the ACTIVE allocations of the assets, which
// is everything that can block them (released and closed ones never do).
func ActiveAllocationsOn(ctx context.Context, q postgres.Querier, assets []inventory.AssetID) ([]inventory.Allocation, error) {
	rows, err := q.Query(ctx, `SELECT `+allocationColumns+` FROM allocations
		WHERE asset_id = ANY($1) AND status = 'ACTIVE' ORDER BY blocked_from, id`, unique(assets))
	if err != nil {
		return nil, fmt.Errorf("read allocations: %w", err)
	}
	return collectAllocations(rows)
}

// AllocationsOfBooking returns a booking's allocations, optionally only the
// ACTIVE ones.
func AllocationsOfBooking(ctx context.Context, q postgres.Querier, bookingID string, activeOnly bool) ([]inventory.Allocation, error) {
	rows, err := q.Query(ctx, `SELECT `+allocationColumns+` FROM allocations
		WHERE booking_id = $1 AND (NOT $2 OR status = 'ACTIVE') ORDER BY created_at, id`, bookingID, activeOnly)
	if err != nil {
		return nil, fmt.Errorf("read allocations of booking %s: %w", bookingID, err)
	}
	return collectAllocations(rows)
}

// InUseEndedBefore returns handed-over, not yet returned allocations whose
// block ended before now: late returns that keep blocking their asset.
func InUseEndedBefore(ctx context.Context, q postgres.Querier, now time.Time, limit int) ([]inventory.Allocation, error) {
	rows, err := q.Query(ctx, `SELECT `+allocationColumns+` FROM allocations
		WHERE status = 'ACTIVE' AND handed_over_at IS NOT NULL AND returned_at IS NULL AND blocked_until <= $1
		ORDER BY blocked_until, id LIMIT $2`, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("read overdue allocations: %w", err)
	}
	return collectAllocations(rows)
}

func collectAllocations(rows pgx.Rows) ([]inventory.Allocation, error) {
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (inventory.Allocation, error) {
		var a inventory.Allocation
		var from, until time.Time
		if err := r.Scan(&a.ID, &a.BookingRef, &a.LineRef, &a.ComponentKey, &a.AssetID, &a.VariantID, &from, &until,
			&a.Status, &a.CreatedAt, &a.HandedOverAt, &a.ReturnedAt, &a.ReleasedAt, &a.ReleaseReason, &a.ClosedAt,
			&a.ModificationRef, &a.ExtendsAllocation); err != nil {
			return a, err
		}
		block, err := period.New(from, until)
		if err != nil {
			return a, fmt.Errorf("allocation %s block: %w", a.ID, err)
		}
		a.Block, a.CreatedAt = block, a.CreatedAt.UTC()
		a.HandedOverAt, a.ReturnedAt = postgres.UTCPtr(a.HandedOverAt), postgres.UTCPtr(a.ReturnedAt)
		a.ReleasedAt, a.ClosedAt = postgres.UTCPtr(a.ReleasedAt), postgres.UTCPtr(a.ClosedAt)
		return a, nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan allocations: %w", err)
	}
	return out, nil
}

// InsertAllocations writes new allocations. An overlap with another ACTIVE
// allocation is reported as inventory.ErrInsufficientInventory.
func InsertAllocations(ctx context.Context, q postgres.Querier, allocs []inventory.Allocation) error {
	for _, a := range allocs {
		_, err := q.Exec(ctx, `
			INSERT INTO allocations (id, booking_id, line_id, component_key, asset_id, variant_id,
				blocked_from, blocked_until, status, created_at, modification_id, extends_allocation_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, nullif($11, ''), nullif($12, ''))`,
			a.ID, a.BookingRef, a.LineRef, a.ComponentKey, a.AssetID, a.VariantID,
			a.Block.Start(), a.Block.End(), a.Status, a.CreatedAt.UTC(), a.ModificationRef, a.ExtendsAllocation)
		if err := allocationWriteError(err, a); err != nil {
			return err
		}
	}
	return nil
}

// UpdateAllocation persists the mutable fields of an allocation: block,
// status, handover/return/release/close stamps and the tentative marker.
func UpdateAllocation(ctx context.Context, q postgres.Querier, a inventory.Allocation) error {
	tag, err := q.Exec(ctx, `
		UPDATE allocations SET blocked_from = $2, blocked_until = $3, status = $4, handed_over_at = $5,
			returned_at = $6, released_at = $7, release_reason = $8, closed_at = $9, modification_id = nullif($10, '')
		WHERE id = $1`,
		a.ID, a.Block.Start(), a.Block.End(), a.Status, a.HandedOverAt, a.ReturnedAt, a.ReleasedAt,
		a.ReleaseReason, a.ClosedAt, a.ModificationRef)
	if err := allocationWriteError(err, a); err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("allocation %s: %w", a.ID, postgres.ErrNotFound)
	}
	return nil
}

func allocationWriteError(err error, a inventory.Allocation) error {
	switch {
	case err == nil:
		return nil
	case postgres.IsExclusionViolation(err, overlapConstraint):
		return fmt.Errorf("asset %s %s is already allocated: %w", a.AssetID, a.Block, inventory.ErrInsufficientInventory)
	}
	return fmt.Errorf("write allocation %s: %w", a.ID, err)
}

// ReleaseActive releases every ACTIVE, not handed-over allocation matching
// the filter and returns them. It is idempotent: released rows stay
// released. Exactly one of bookingID or modificationID is used.
func ReleaseActive(ctx context.Context, q postgres.Querier, bookingID, modificationID, reason string, at time.Time) ([]inventory.Allocation, error) {
	if (bookingID == "") == (modificationID == "") {
		return nil, fmt.Errorf("release allocations: exactly one of booking or modification is required")
	}
	var allocs []inventory.Allocation
	var err error
	if bookingID != "" {
		allocs, err = AllocationsOfBooking(ctx, q, bookingID, true)
	} else {
		allocs, err = holdsOf(ctx, q, modificationID)
	}
	if err != nil {
		return nil, err
	}
	var out []inventory.Allocation
	for _, a := range allocs {
		changed, err := a.Release(at, reason)
		if err != nil {
			return nil, err
		}
		if !changed {
			continue
		}
		if err := UpdateAllocation(ctx, q, a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func holdsOf(ctx context.Context, q postgres.Querier, modificationID string) ([]inventory.Allocation, error) {
	rows, err := q.Query(ctx, `SELECT `+allocationColumns+` FROM allocations
		WHERE modification_id = $1 AND status = 'ACTIVE' ORDER BY id`, modificationID)
	if err != nil {
		return nil, fmt.Errorf("read holds of modification %s: %w", modificationID, err)
	}
	return collectAllocations(rows)
}

func unique[T ~string](v []T) []T {
	return slices.Compact(slices.Sorted(slices.Values(v)))
}
