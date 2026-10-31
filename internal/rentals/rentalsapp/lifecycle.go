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
	"webee_rentor/internal/payments"
	"webee_rentor/internal/payments/paymentspg"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalspg"
)

// Confirm confirms a pending booking once the snapshot policy's funds
// requirement is met.
func (s *Service) Confirm(ctx context.Context, p identity.Principal, id rentals.BookingID) (*rentals.Booking, error) {
	return s.mutate(ctx, p, id, rentals.ActionConfirm, func(tx pgx.Tx, b *rentals.Booking, _ identity.Scope, now time.Time) ([]rentals.Transition, error) {
		l, _, err := paymentspg.LoadLedger(ctx, tx, string(b.ID), false)
		if err != nil {
			return nil, err
		}
		t, err := b.Confirm(p.Actor(), fundsOf(l), now)
		return []rentals.Transition{t}, err
	})
}

// CancelInput is a cancellation request; the scope (customer or staff
// rules) comes from authorization.
type CancelInput struct {
	Reason rentals.CancelReason
	Note   string
}

// Cancel cancels before handover, releases every allocation of the booking,
// including holds of an open change request, and settles its ledger.
func (s *Service) Cancel(ctx context.Context, p identity.Principal, id rentals.BookingID, in CancelInput) (*rentals.Booking, error) {
	return s.mutate(ctx, p, id, rentals.ActionCancel, func(tx pgx.Tx, b *rentals.Booking, scope identity.Scope, now time.Time) ([]rentals.Transition, error) {
		ts, err := endOpenModification(ctx, tx, b, p.Actor(), "booking cancelled", now)
		if err != nil {
			return nil, err
		}
		t, err := b.Cancel(rentals.CancelRequest{Actor: p.Actor(), Scope: scope, Reason: in.Reason, Note: in.Note, Now: now})
		if err != nil {
			return nil, err
		}
		return append(ts, t), s.settleCancellation(ctx, tx, b, p.Actor(), now)
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
	return expireEach(ctx, due, func(id rentals.BookingID) (bool, error) {
		changed := false
		var expired *rentals.Booking
		var done []rentals.Transition
		err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			changed = false
			b, v, err := rentalspg.LoadBooking(ctx, tx, id, true)
			if err != nil {
				return err
			}
			now := s.now().UTC()
			// Expire decides first: a booking confirmed since the list was read
			// is left alone, change request included.
			t, ok, err := b.Expire(jobActor, now)
			if err != nil || !ok {
				return err
			}
			ts, err := endOpenModification(ctx, tx, b, jobActor, "booking hold expired", now)
			if err != nil {
				return err
			}
			if err := s.settleCancellation(ctx, tx, b, jobActor, now); err != nil {
				return err
			}
			if _, err := rentalspg.SaveBooking(ctx, tx, b, v, "", now); err != nil {
				return err
			}
			changed, expired, done = true, b, append([]rentals.Transition{t}, ts...)
			return s.record(ctx, tx, done...)
		})
		if err == nil && changed {
			s.tell(ctx, expired, done)
		}
		return changed, err
	})
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
		view, err := lockBookingInventory(ctx, tx, b.ID, lineVariants(b.Lines), cfg.allocation)
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
		l, lv, err := paymentspg.LoadLedger(ctx, tx, string(b.ID), true)
		if err != nil {
			return nil, err
		}
		// Funds are read first: they already count a credited reservation as
		// rent, and applying it empties the held balance a RESERVATION_DEPOSIT
		// requirement looks at.
		t, err := b.RecordHandover(rentals.HandoverRequest{
			Actor: p.Actor(), Allocated: allocated, Delivered: in.Delivered,
			ChecklistCompleted: in.ChecklistCompleted, Funds: fundsOf(l), Now: now,
		})
		if err != nil {
			return nil, err
		}
		if l.CreditReservationToRent {
			if _, _, err := l.ApplyReservationToRent(payments.ResolutionID(s.newID()), p.Actor(), now); err != nil {
				return nil, err
			}
			if _, err := paymentspg.SaveLedger(ctx, tx, l, lv, now); err != nil {
				return nil, err
			}
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
// inspected. A late item gets its policy-computed fee in the ledger.
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
		l, lv, err := paymentspg.LoadLedger(ctx, tx, string(b.ID), true)
		if err != nil {
			return nil, err
		}
		fees, err := rentalspg.AssessmentsOfBooking(ctx, tx, b.ID)
		if err != nil {
			return nil, err
		}
		for i := range assets {
			j := slices.IndexFunc(allocs, func(a inventory.Allocation) bool { return a.AssetID == assets[i].ID && a.InUse() })
			if j < 0 {
				return nil, fmt.Errorf("asset %s has no allocation in use: %w", assets[i].ID, rentals.ErrAssetMismatch)
			}
			if err := noteLateImpact(ctx, tx, b, allocs[j], now); err != nil {
				return nil, err
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
			if err := s.assessLateFee(ctx, tx, b, l, fees, assets[i].ID, p.Actor(), now); err != nil {
				return nil, err
			}
		}
		_, err = paymentspg.SaveLedger(ctx, tx, l, lv, now)
		return []rentals.Transition{t}, err
	})
}

// noteLateImpact records whether the item, back only now, no longer fits its
// turnaround before another booking's block starts. It must run while the
// allocation is still in use.
func noteLateImpact(ctx context.Context, q postgres.Querier, b *rentals.Booking, a inventory.Allocation, now time.Time) error {
	others, err := inventorypg.ActiveAllocationsOn(ctx, q, []inventory.AssetID{a.AssetID})
	if err != nil {
		return err
	}
	others = slices.DeleteFunc(others, func(o inventory.Allocation) bool { return o.BookingRef == string(b.ID) })
	if len(inventory.OverdueConflicts(a, others, now, a.Block.End().Sub(b.Schedule.DueAt))) == 0 {
		return nil
	}
	return b.NoteAffectedNext(a.AssetID)
}

// InspectionInput is the finding for one returned item.
type InspectionInput struct {
	Asset      inventory.AssetID
	Conditions []rentals.Condition
	Notes      string
	Evidence   []string
}

// RecordInspection records the finding and moves the asset accordingly: a
// clean item becomes READY and its allocation closes, a soiled one goes to
// cleaning. Items needing verification, repair or retirement stay in
// inspection and keep blocking until inventory management decides. Changing
// a finding later never moves an asset that already left inspection.
func (s *Service) RecordInspection(ctx context.Context, p identity.Principal, id rentals.BookingID, in InspectionInput) (*rentals.Booking, error) {
	return s.mutate(ctx, p, id, rentals.ActionInspectAsset, func(tx pgx.Tx, b *rentals.Booking, _ identity.Scope, now time.Time) ([]rentals.Transition, error) {
		f := rentals.InspectionFinding{Conditions: in.Conditions, Notes: in.Notes, Evidence: in.Evidence}
		t, err := b.RecordInspection(p.Actor(), in.Asset, f, now)
		if err != nil {
			return nil, err
		}
		assets, err := inventorypg.LockAssets(ctx, tx, []inventory.AssetID{in.Asset})
		if err != nil {
			return nil, err
		}
		if assets[0].Status == inventory.AssetNeedsInspection {
			if err := s.leaveInspection(ctx, tx, b, &assets[0], f, p.Actor(), now); err != nil {
				return nil, err
			}
		}
		return []rentals.Transition{t}, nil
	})
}

func (s *Service) leaveInspection(ctx context.Context, q postgres.Querier, b *rentals.Booking, a *inventory.Asset, f rentals.InspectionFinding, actor identity.Actor, now time.Time) error {
	checklist := inventory.InspectionChecklist{Completed: true, Notes: f.Notes}
	var c inventory.StatusChange
	var err error
	switch f.AssetAction() {
	case inventory.ActionPassInspection:
		c, err = a.PassInspection(actor, now, checklist)
	case inventory.ActionRequireCleaning:
		c, err = a.RequireCleaning(actor, now, checklist)
	default:
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.recordAsset(ctx, q, *a, c, b.ID); err != nil {
		return err
	}
	if a.Status != inventory.AssetReady {
		return nil
	}
	allocs, err := inventorypg.AllocationsOfBooking(ctx, q, string(b.ID), true)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(allocs, func(al inventory.Allocation) bool { return al.AssetID == a.ID && al.ReturnedAt != nil })
	if i < 0 {
		return nil
	}
	if err := allocs[i].Close(now, *a); err != nil {
		return err
	}
	return inventorypg.UpdateAllocation(ctx, q, allocs[i])
}

// ReplaceAsset swaps an asset allocated to the booking for another of the
// same variant before handover, e.g. it was damaged in the meantime. If no
// other asset is free the booking keeps its allocation.
func (s *Service) ReplaceAsset(ctx context.Context, p identity.Principal, id rentals.BookingID, allocation inventory.AllocationID, reason string) (*rentals.Booking, error) {
	return s.mutate(ctx, p, id, rentals.ActionReplaceAsset, func(tx pgx.Tx, b *rentals.Booking, _ identity.Scope, now time.Time) ([]rentals.Transition, error) {
		cfg, err := s.config(ctx, tx)
		if err != nil {
			return nil, err
		}
		view, err := lockBookingInventory(ctx, tx, b.ID, lineVariants(b.Lines), cfg.allocation)
		if err != nil {
			return nil, err
		}
		r, err := b.ReplaceAsset(rentals.ReplaceAssetRequest{Actor: p.Actor(), Allocation: allocation, Reason: reason, Inventory: view, Now: now})
		if err != nil {
			return nil, err
		}
		if err := releaseByID(ctx, tx, view.Allocations, []inventory.AllocationID{r.Release}, "asset replaced", now); err != nil {
			return nil, err
		}
		allocs, err := s.materialise([]inventory.PlannedAllocation{r.Allocation}, now)
		if err != nil {
			return nil, err
		}
		return []rentals.Transition{r.Transition}, inventorypg.InsertAllocations(ctx, tx, allocs)
	})
}

// ReportMissing marks an item that did not come back as missing-pending; the
// customer has the policy's verification window to find it. The asset stays
// RENTED and keeps blocking its calendar meanwhile.
func (s *Service) ReportMissing(ctx context.Context, p identity.Principal, id rentals.BookingID, asset inventory.AssetID) (*rentals.Booking, error) {
	return s.mutate(ctx, p, id, rentals.ActionReportMissing, func(_ pgx.Tx, b *rentals.Booking, _ identity.Scope, now time.Time) ([]rentals.Transition, error) {
		t, err := b.ReportMissing(p.Actor(), asset, now)
		return []rentals.Transition{t}, err
	})
}

// ConfirmLost turns a missing-pending item into a confirmed loss after the
// verification window: the item is resolved on the booking, the asset becomes
// MISSING and its allocation closes, all or nothing. Lateness stops here, so
// the item's late fee is assessed now.
func (s *Service) ConfirmLost(ctx context.Context, p identity.Principal, id rentals.BookingID, asset inventory.AssetID, reason string) (*rentals.Booking, error) {
	return s.mutate(ctx, p, id, rentals.ActionConfirmLost, func(tx pgx.Tx, b *rentals.Booking, _ identity.Scope, now time.Time) ([]rentals.Transition, error) {
		t, err := b.ConfirmLost(p.Actor(), asset, reason, now)
		if err != nil {
			return nil, err
		}
		assets, err := inventorypg.LockAssets(ctx, tx, []inventory.AssetID{asset})
		if err != nil {
			return nil, err
		}
		allocs, err := inventorypg.AllocationsOfBooking(ctx, tx, string(b.ID), true)
		if err != nil {
			return nil, err
		}
		i := slices.IndexFunc(allocs, func(a inventory.Allocation) bool { return a.AssetID == asset && a.InUse() })
		if i < 0 {
			return nil, fmt.Errorf("asset %s has no allocation in use: %w", asset, rentals.ErrAssetMismatch)
		}
		if err := noteLateImpact(ctx, tx, b, allocs[i], now); err != nil {
			return nil, err
		}
		c, err := assets[0].ReportMissing(p.Actor(), now, reason)
		if err != nil {
			return nil, err
		}
		if err := s.recordAsset(ctx, tx, assets[0], c, b.ID); err != nil {
			return nil, err
		}
		if err := allocs[i].Close(now, assets[0]); err != nil {
			return nil, err
		}
		if err := inventorypg.UpdateAllocation(ctx, tx, allocs[i]); err != nil {
			return nil, err
		}
		l, lv, err := paymentspg.LoadLedger(ctx, tx, string(b.ID), true)
		if err != nil {
			return nil, err
		}
		fees, err := rentalspg.AssessmentsOfBooking(ctx, tx, b.ID)
		if err != nil {
			return nil, err
		}
		if err := s.assessLateFee(ctx, tx, b, l, fees, asset, p.Actor(), now); err != nil {
			return nil, err
		}
		_, err = paymentspg.SaveLedger(ctx, tx, l, lv, now)
		return []rentals.Transition{t}, err
	})
}

// maxOverdueList bounds one overdue listing.
const maxOverdueList = 200

// OverdueBookings lists rented bookings with an item still out past due_at
// plus the grace period, oldest first.
func (s *Service) OverdueBookings(ctx context.Context, p identity.Principal) ([]*rentals.Booking, error) {
	if err := p.Require(identity.PermBookingReadAll); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	due, err := rentalspg.RentedDueBy(ctx, s.pool, now, maxOverdueList)
	if err != nil {
		return nil, err
	}
	var out []*rentals.Booking
	for _, id := range due {
		b, _, err := rentalspg.LoadBooking(ctx, s.pool, id, false)
		if err != nil {
			return nil, err
		}
		if b.IsOverdue(now) {
			out = append(out, b)
		}
	}
	return out, nil
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
