package rentalsapp

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/inventory/inventorypg"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalspg"
)

// Confirm confirms a pending booking once the snapshot policy's funds
// requirement is met.
func (s *Service) Confirm(ctx context.Context, p identity.Principal, id rentals.BookingID) (*rentals.Booking, error) {
	return s.mutate(ctx, p, id, rentals.ActionConfirm, func(tx pgx.Tx, b *rentals.Booking, _ identity.Scope, now time.Time) ([]rentals.Transition, error) {
		funds, err := s.funds.Funds(ctx, tx, b)
		if err != nil {
			return nil, err
		}
		t, err := b.Confirm(p.Actor(), funds, now)
		return []rentals.Transition{t}, err
	})
}

// CancelInput is a cancellation request; the scope (customer or staff
// rules) comes from authorization.
type CancelInput struct {
	Reason rentals.CancelReason
	Note   string
}

// Cancel cancels before handover and releases every allocation of the
// booking, including holds of an open change request. Settling the
// cancellation fee belongs to the payments use case.
func (s *Service) Cancel(ctx context.Context, p identity.Principal, id rentals.BookingID, in CancelInput) (*rentals.Booking, error) {
	return s.mutate(ctx, p, id, rentals.ActionCancel, func(tx pgx.Tx, b *rentals.Booking, scope identity.Scope, now time.Time) ([]rentals.Transition, error) {
		ts, err := endOpenModification(ctx, tx, b, p.Actor(), "booking cancelled", now)
		if err != nil {
			return nil, err
		}
		t, err := b.Cancel(rentals.CancelRequest{Actor: p.Actor(), Scope: scope, Reason: in.Reason, Note: in.Note, Now: now})
		return append(ts, t), err
	})
}

// ExpireHolds expires pending bookings whose hold has run out and releases
// their allocations. It is safe to run concurrently and repeatedly: each
// booking is expired in its own transaction, and one that was confirmed or
// already expired meanwhile is left alone. It returns how many expired.
func (s *Service) ExpireHolds(ctx context.Context, limit int) (int, error) {
	due, err := rentalspg.ExpiredHolds(ctx, s.pool, s.now(), limit)
	if err != nil {
		return 0, err
	}
	expired := 0
	for _, id := range due {
		changed := false
		err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			changed = false
			b, v, err := rentalspg.LoadBooking(ctx, tx, id, true)
			if err != nil {
				return err
			}
			now := s.now().UTC()
			ts, err := endOpenModification(ctx, tx, b, jobActor, "booking hold expired", now)
			if err != nil {
				return err
			}
			t, ok, err := b.Expire(jobActor, now)
			if err != nil || !ok {
				return err
			}
			if _, err := rentalspg.SaveBooking(ctx, tx, b, v, "", now); err != nil {
				return err
			}
			changed = true
			return s.record(ctx, tx, append(ts, t)...)
		})
		if err != nil {
			return expired, fmt.Errorf("expire booking %s: %w", id, err)
		}
		if changed {
			expired++
		}
	}
	return expired, nil
}

// RescheduleInput is the new pickup date and slot; the package and the
// fulfillment modes stay the same.
type RescheduleInput struct {
	PickupDate businesstime.Date
	SlotID     string
}

// Reschedule moves the booking and re-allocates in one transaction: the new
// allocations are planned while the old ones are still held, and the old
// ones are only released once the new plan succeeded. If the new time is
// unavailable the booking keeps its current schedule and allocations.
func (s *Service) Reschedule(ctx context.Context, p identity.Principal, id rentals.BookingID, in RescheduleInput) (*rentals.Booking, error) {
	return s.mutate(ctx, p, id, rentals.ActionReschedule, func(tx pgx.Tx, b *rentals.Booking, scope identity.Scope, now time.Time) ([]rentals.Transition, error) {
		cfg, err := s.config(ctx, tx)
		if err != nil {
			return nil, err
		}
		schedule, err := cfg.calendar.ResolveFulfillment(in.PickupDate, b.Fulfillment.Outbound, in.SlotID, b.Package.Duration, b.Fulfillment.Return)
		if err != nil {
			return nil, err
		}
		view, err := lockInventory(ctx, tx, lineVariants(b.Lines), cfg.allocation)
		if err != nil {
			return nil, err
		}
		r, err := b.Reschedule(rentals.RescheduleRequest{
			Actor: p.Actor(), Scope: scope, Schedule: schedule, Calendar: cfg.calendar, Inventory: view, Now: now,
		})
		if err != nil {
			return nil, err
		}
		if err := releaseByID(ctx, tx, view.Allocations, r.Release, "rescheduled", now); err != nil {
			return nil, err
		}
		allocs, err := s.materialise(r.Allocations, now)
		if err != nil {
			return nil, err
		}
		if err := inventorypg.InsertAllocations(ctx, tx, allocs); err != nil {
			return nil, err
		}
		return []rentals.Transition{r.Transition}, nil
	})
}

// StartPreparation marks staff picking and checking the items.
func (s *Service) StartPreparation(ctx context.Context, p identity.Principal, id rentals.BookingID) (*rentals.Booking, error) {
	return s.mutate(ctx, p, id, rentals.ActionStartPreparation, func(_ pgx.Tx, b *rentals.Booking, _ identity.Scope, now time.Time) ([]rentals.Transition, error) {
		t, err := b.StartPreparation(p.Actor(), now)
		return []rentals.Transition{t}, err
	})
}

// HandoverInput lists the assets scanned at the counter.
type HandoverInput struct {
	Delivered          []rentals.DeliveredItem
	ChecklistCompleted bool
}

// RecordHandover hands the allocated assets to the customer: the booking
// becomes RENTED, each asset READY → RENTED and each allocation is marked
// handed over, all or nothing. An asset that is not READY (still cleaning,
// say) stops the handover.
func (s *Service) RecordHandover(ctx context.Context, p identity.Principal, id rentals.BookingID, in HandoverInput) (*rentals.Booking, error) {
	return s.mutate(ctx, p, id, rentals.ActionHandover, func(tx pgx.Tx, b *rentals.Booking, _ identity.Scope, now time.Time) ([]rentals.Transition, error) {
		allocs, err := inventorypg.AllocationsOfBooking(ctx, tx, string(b.ID), true)
		if err != nil {
			return nil, err
		}
		allocs = slices.DeleteFunc(allocs, func(a inventory.Allocation) bool { return a.Tentative() || a.HandedOverAt != nil })
		assets, err := inventorypg.LockAssets(ctx, tx, allocatedAssets(allocs))
		if err != nil {
			return nil, err
		}
		allocated := make([]rentals.AllocatedAsset, len(allocs))
		for i, a := range allocs {
			allocated[i] = rentals.AllocatedAsset{AssetID: a.AssetID, LineID: rentals.LineID(a.LineRef), ComponentKey: a.ComponentKey}
		}
		funds, err := s.funds.Funds(ctx, tx, b)
		if err != nil {
			return nil, err
		}
		t, err := b.RecordHandover(rentals.HandoverRequest{
			Actor: p.Actor(), Allocated: allocated, Delivered: in.Delivered,
			ChecklistCompleted: in.ChecklistCompleted, Funds: funds, Now: now,
		})
		if err != nil {
			return nil, err
		}
		for i := range allocs {
			if err := allocs[i].MarkHandedOver(now); err != nil {
				return nil, err
			}
			if err := inventorypg.UpdateAllocation(ctx, tx, allocs[i]); err != nil {
				return nil, err
			}
		}
		for i := range assets {
			c, err := assets[i].HandOver(p.Actor(), now)
			if err != nil {
				return nil, err
			}
			if err := s.recordAsset(ctx, tx, assets[i], c, b.ID); err != nil {
				return nil, err
			}
		}
		return []rentals.Transition{t}, nil
	})
}

func allocatedAssets(allocs []inventory.Allocation) []inventory.AssetID {
	out := make([]inventory.AssetID, len(allocs))
	for i, a := range allocs {
		out[i] = a.AssetID
	}
	return out
}

// RecordReturn records assets brought back to the counter. Each asset goes
// RENTED → NEEDS_INSPECTION and its allocation is marked returned; the
// allocation keeps blocking its turnaround buffer until the asset is
// inspected (inventoryapp closes it then).
func (s *Service) RecordReturn(ctx context.Context, p identity.Principal, id rentals.BookingID, returned []inventory.AssetID) (*rentals.Booking, error) {
	return s.mutate(ctx, p, id, rentals.ActionReturn, func(tx pgx.Tx, b *rentals.Booking, _ identity.Scope, now time.Time) ([]rentals.Transition, error) {
		t, err := b.RecordReturn(rentals.ReturnRequest{Actor: p.Actor(), Assets: returned, Now: now})
		if err != nil {
			return nil, err
		}
		assets, err := inventorypg.LockAssets(ctx, tx, returned)
		if err != nil {
			return nil, err
		}
		allocs, err := inventorypg.AllocationsOfBooking(ctx, tx, string(b.ID), true)
		if err != nil {
			return nil, err
		}
		for i := range assets {
			j := slices.IndexFunc(allocs, func(a inventory.Allocation) bool { return a.AssetID == assets[i].ID && a.InUse() })
			if j < 0 {
				return nil, fmt.Errorf("asset %s has no allocation in use: %w", assets[i].ID, rentals.ErrAssetMismatch)
			}
			if err := allocs[j].MarkReturned(now); err != nil {
				return nil, err
			}
			if err := inventorypg.UpdateAllocation(ctx, tx, allocs[j]); err != nil {
				return nil, err
			}
			c, err := assets[i].ReceiveReturn(p.Actor(), now)
			if err != nil {
				return nil, err
			}
			if err := s.recordAsset(ctx, tx, assets[i], c, b.ID); err != nil {
				return nil, err
			}
		}
		return []rentals.Transition{t}, nil
	})
}

// Booking reads a booking the principal may see.
func (s *Service) Booking(ctx context.Context, p identity.Principal, id rentals.BookingID) (*rentals.Booking, error) {
	b, _, err := rentalspg.LoadBooking(ctx, s.pool, id, false)
	if err != nil {
		return nil, err
	}
	if _, err := rentals.Authorize(p, rentals.ActionRead, b); err != nil {
		return nil, err
	}
	return b, nil
}
