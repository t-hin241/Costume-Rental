package rentalsapp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/audit"
	"webee_rentor/internal/audit/auditpg"
	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/delivery"
	"webee_rentor/internal/delivery/deliverypg"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/inventory/inventorypg"
	"webee_rentor/internal/money"
	"webee_rentor/internal/payments"
	"webee_rentor/internal/payments/paymentspg"
	"webee_rentor/internal/period"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalspg"
)

// Delivery runs in manual mode (decision D43): staff book each shipment in
// the carrier's app and record here what happens to it. Every use case below
// locks the booking first, then its delivery task, then assets.

// ErrShipmentRequired: a courier booking is handed over only once its
// outbound shipment is booked with the carrier.
var ErrShipmentRequired = domainerr.New(domainerr.KindInvalidState, "delivery.shipment_required", "book the outbound shipment with the carrier first")

// createDeliveryTasks creates one task per courier leg with the booking: the
// outbound one waits for staff to book the shipment, the return one is
// scheduled until its pickup is arranged.
func (s *Service) createDeliveryTasks(ctx context.Context, q postgres.Querier, checked placement, b *rentals.Booking, now time.Time) error {
	if checked.area == nil || checked.address == nil {
		return nil
	}
	f := b.Fulfillment
	for _, leg := range []struct {
		dir    delivery.Direction
		mode   rentals.LegMode
		window *period.Range
		fee    money.Money
	}{{delivery.Outbound, f.Outbound, f.OutboundWindow, f.OutboundFee}, {delivery.Return, f.Return, f.ReturnWindow, f.ReturnFee}} {
		if leg.mode != rentals.LegCourier || leg.window == nil {
			continue
		}
		t, err := delivery.NewTask(delivery.NewTaskRequest{
			ID: delivery.TaskID(s.newID()), BookingRef: string(b.ID), CustomerID: b.CustomerID, Direction: leg.dir,
			ProviderCode: checked.area.ProviderCode, Store: checked.area.Origin, Customer: *checked.address, Window: *leg.window,
			Parcel:      delivery.Parcel{WeightGram: checked.area.ParcelWeightGram, PackageCount: 1, DeclaredValue: b.SecurityDepositTotal},
			CustomerFee: leg.fee, Now: now,
		})
		if err != nil {
			return err
		}
		if _, err := deliverypg.SaveTask(ctx, q, t, 0, now); err != nil {
			return err
		}
	}
	return nil
}

// cancelDeliveryTasks asks to cancel the booking's shipments still in
// progress. One not booked with the carrier yet is cancelled at once; a
// booked one waits until staff confirm the carrier cancelled it.
func (s *Service) cancelDeliveryTasks(ctx context.Context, q postgres.Querier, b *rentals.Booking, dir delivery.Direction, actor identity.Actor, reason string, now time.Time) error {
	tasks, err := deliverypg.TasksOfBooking(ctx, q, string(b.ID), true)
	if err != nil {
		return err
	}
	for _, st := range tasks {
		t := st.Task
		if dir != "" && t.Direction != dir {
			continue
		}
		from := t.Status
		if err := t.RequestCancel(actor, reason); errors.Is(err, delivery.ErrInvalidTransition) {
			continue // already over, cancelling, or with the carrier
		} else if err != nil {
			return err
		}
		if t.ProviderShipmentCode == "" {
			if err := t.ConfirmCancelled(); err != nil {
				return err
			}
		}
		if err := s.saveTask(ctx, q, t, st.Version, "delivery.cancel_requested", from, actor, reason, now); err != nil {
			return err
		}
	}
	return nil
}

// saveTask stores a changed task with its audit event. Addresses never go to
// the audit log.
func (s *Service) saveTask(ctx context.Context, q postgres.Querier, t *delivery.Task, v deliverypg.Version, action string, from delivery.Status, actor identity.Actor, reason string, now time.Time) error {
	if _, err := deliverypg.SaveTask(ctx, q, t, v, now); err != nil {
		return err
	}
	e := audit.Event{
		ID: s.newID(), OccurredAt: now, Actor: actor, Action: action, ResourceType: "delivery_task", ResourceID: string(t.ID),
		Result: audit.ResultSuccess, Reason: reason, References: map[string]string{"booking_id": t.BookingRef, "direction": string(t.Direction)},
	}
	if from != t.Status {
		e.Changes = map[string]audit.Change{"status": {From: string(from), To: string(t.Status)}}
	}
	return auditpg.Append(ctx, q, e)
}

// openTask finds the booking's shipment in progress for a direction.
func openTask(tasks []deliverypg.Stored, dir delivery.Direction) (deliverypg.Stored, bool) {
	for _, st := range tasks {
		if st.Task.Direction == dir && st.Task.Status != delivery.StatusCancelled && st.Task.Status != delivery.StatusClosedUnfulfilled {
			return st, true
		}
	}
	return deliverypg.Stored{}, false
}

// dispatchOutbound records the parcel leaving with the courier at handover.
func (s *Service) dispatchOutbound(ctx context.Context, q postgres.Querier, p identity.Principal, b *rentals.Booking, now time.Time) error {
	if b.Fulfillment.Outbound != rentals.LegCourier {
		return nil
	}
	tasks, err := deliverypg.TasksOfBooking(ctx, q, string(b.ID), true)
	if err != nil {
		return err
	}
	st, ok := openTask(tasks, delivery.Outbound)
	if !ok || st.Task.ProviderShipmentCode == "" || (st.Task.Status != delivery.StatusCreated && st.Task.Status != delivery.StatusReadyForPickup) {
		return fmt.Errorf("booking %s: %w", b.ID, ErrShipmentRequired)
	}
	from := st.Task.Status
	if _, err := st.Task.ManualEvent(p, delivery.StatusPickedUp, "handed to the courier at the store", "", now); err != nil {
		return err
	}
	return s.saveTask(ctx, q, st.Task, st.Version, "delivery.picked_up", from, p.Actor(), "handed to the courier", now)
}

// taskChange is the body of a delivery use case, run with the booking and the
// task locked; the task is saved and audited afterwards. It may return a new
// task to insert once the changed one is saved (a follow-up shipment).
type taskChange func(tx pgx.Tx, b *rentals.Booking, t *delivery.Task, now time.Time) ([]rentals.Transition, *delivery.Task, error)

// withTask runs a staff delivery operation (delivery.manage) on one task.
func (s *Service) withTask(ctx context.Context, p identity.Principal, id delivery.TaskID, action, reason string, fn taskChange) (*delivery.Task, error) {
	if err := p.Require(identity.PermDeliveryManage); err != nil {
		return nil, err
	}
	peek, _, err := deliverypg.LoadTask(ctx, s.pool, id, false)
	if err != nil {
		return nil, err
	}
	var out *delivery.Task
	_, err = s.mutate(ctx, p, rentals.BookingID(peek.BookingRef), rentals.ActionDelivery, func(tx pgx.Tx, b *rentals.Booking, _ identity.Scope, now time.Time) ([]rentals.Transition, error) {
		t, v, err := deliverypg.LoadTask(ctx, tx, id, true)
		if err != nil {
			return nil, err
		}
		from := t.Status
		ts, next, err := fn(tx, b, t, now)
		if err != nil {
			return nil, err
		}
		if err := s.saveTask(ctx, tx, t, v, action, from, p.Actor(), reason, now); err != nil {
			return nil, err
		}
		if next != nil {
			if err := s.saveTask(ctx, tx, next, 0, "delivery.follow_up_created", "", p.Actor(), "replaces "+string(t.ID), now); err != nil {
				return nil, err
			}
		}
		out = t
		return ts, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ShipmentInput is the carrier shipment staff booked in the carrier's app.
type ShipmentInput struct {
	Code         string
	ExpectedAt   *time.Time
	EstimatedFee *money.Money
}

// RecordShipment stores the shipment code of a task. A return pickup is
// booked from its scheduled state; an outbound shipment only once the
// booking is confirmed.
func (s *Service) RecordShipment(ctx context.Context, p identity.Principal, id delivery.TaskID, in ShipmentInput) (*delivery.Task, error) {
	return s.withTask(ctx, p, id, "delivery.shipment_recorded", "shipment "+in.Code, func(_ pgx.Tx, b *rentals.Booking, t *delivery.Task, _ time.Time) ([]rentals.Transition, *delivery.Task, error) {
		if b.Status == rentals.StatusPendingConfirmation || b.Status.IsTerminal() {
			return nil, nil, fmt.Errorf("booking %s is %s: %w", b.ID, b.Status, rentals.ErrInvalidTransition)
		}
		if t.Direction == delivery.Return && t.Status == delivery.StatusScheduled {
			if err := t.RequestCreation(); err != nil {
				return nil, nil, err
			}
		}
		_, err := t.RecordCreated(delivery.ShipmentResult{ProviderShipmentCode: in.Code, ExpectedAt: in.ExpectedAt, EstimatedFee: in.EstimatedFee})
		return nil, nil, err
	})
}

// MarkReadyForPickup records the outbound parcel packed and checked.
func (s *Service) MarkReadyForPickup(ctx context.Context, p identity.Principal, id delivery.TaskID) (*delivery.Task, error) {
	return s.withTask(ctx, p, id, "delivery.ready_for_pickup", "", func(_ pgx.Tx, _ *rentals.Booking, t *delivery.Task, _ time.Time) ([]rentals.Transition, *delivery.Task, error) {
		return nil, nil, t.MarkReadyForPickup(p.Actor())
	})
}

// DeliveryEventInput is a status staff saw in the carrier's app. Fault and
// GaveUp only apply to a failed attempt; At defaults to now.
type DeliveryEventInput struct {
	Status   delivery.Status
	Fault    delivery.Fault
	GaveUp   bool
	Reason   string
	Evidence string
	At       *time.Time
}

// RecordDeliveryEvent records a carrier status and applies it to the booking:
// delivery starts the customer's use (and may move the due time), a courier
// pickup ends their return obligation, and a classified failed attempt
// follows decisions D19 and D42. Leaving the store is recorded by the
// handover, and the store receipt by ReceiveReturnParcel.
func (s *Service) RecordDeliveryEvent(ctx context.Context, p identity.Principal, id delivery.TaskID, in DeliveryEventInput) (*delivery.Task, error) {
	return s.withTask(ctx, p, id, "delivery.event_recorded", in.Reason, func(tx pgx.Tx, b *rentals.Booking, t *delivery.Task, now time.Time) ([]rentals.Transition, *delivery.Task, error) {
		at := now
		if in.At != nil {
			if in.At.After(now) {
				return nil, nil, domainerr.Invalid("at", "cannot be in the future")
			}
			at = in.At.UTC()
		}
		switch {
		case in.Status == delivery.StatusPickupFailed || in.Status == delivery.StatusDeliveryFailed || in.Status == delivery.StatusReturnFailed:
			out, err := t.ManualAttempt(p, in.Status, in.Fault, in.GaveUp, in.Reason, in.Evidence, at)
			if err != nil || out == delivery.OutcomeStale {
				return nil, nil, err
			}
			a := t.Attempts[len(t.Attempts)-1]
			if a.Fault == delivery.FaultUnknown {
				return nil, nil, nil
			}
			ts, err := attemptOnBooking(b, t, a, p.Actor())
			return ts, nil, err
		case in.Status == delivery.StatusInTransit:
			_, err := t.ManualEvent(p, in.Status, reasonOr(in.Reason, "in transit"), in.Evidence, at)
			return nil, nil, err
		case in.Status == delivery.StatusDelivered && t.Direction == delivery.Outbound:
			if _, err := t.ManualEvent(p, in.Status, reasonOr(in.Reason, "delivered"), in.Evidence, at); err != nil {
				return nil, nil, err
			}
			tr, err := b.RecordDelivered(p.Actor(), at)
			if err != nil {
				return nil, nil, err
			}
			return []rentals.Transition{tr}, nil, s.extendDelayed(ctx, tx, b, p.Actor(), now)
		case in.Status == delivery.StatusPickupConfirmed && t.Direction == delivery.Return:
			if _, err := t.ManualEvent(p, in.Status, reasonOr(in.Reason, "picked up from the customer"), in.Evidence, at); err != nil {
				return nil, nil, err
			}
			var out []inventory.AssetID
			for _, it := range b.Items {
				if it.ReturnedAt == nil && !it.Resolved() && !it.NotReceived {
					out = append(out, it.AssetID)
				}
			}
			tr, err := b.RecordCourierPickup(p.Actor(), out, at)
			return []rentals.Transition{tr}, nil, err
		}
		return nil, nil, domainerr.Invalid("status", fmt.Sprintf("%s cannot be recorded on a %s shipment here", in.Status, t.Direction))
	})
}

func reasonOr(r, fallback string) string {
	if r == "" {
		return fallback
	}
	return r
}

// attemptOnBooking applies a classified failed attempt to the booking: a
// failed delivery may stop crediting the delay (D42), a failed pickup from the
// customer may stop lateness (D19). Other failures leave the booking alone.
func attemptOnBooking(b *rentals.Booking, t *delivery.Task, a delivery.Attempt, actor identity.Actor) ([]rentals.Transition, error) {
	var tr rentals.Transition
	var err error
	switch {
	case t.Direction == delivery.Outbound && a.Status == delivery.StatusDeliveryFailed:
		tr, err = b.RecordDeliveryFailure(actor, a)
	case t.Direction == delivery.Return && a.Status == delivery.StatusPickupFailed:
		tr, err = b.RecordReturnPickupFailure(actor, a)
	default:
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []rentals.Transition{tr}, nil
}

// ClassifyDeliveryAttempt records who caused a failed attempt and applies it
// to the booking.
func (s *Service) ClassifyDeliveryAttempt(ctx context.Context, p identity.Principal, id delivery.TaskID, attempt string, fault delivery.Fault, reason string) (*delivery.Task, error) {
	return s.withTask(ctx, p, id, "delivery.attempt_classified", reason, func(_ pgx.Tx, b *rentals.Booking, t *delivery.Task, _ time.Time) ([]rentals.Transition, *delivery.Task, error) {
		if err := t.ClassifyAttempt(p.Actor(), attempt, fault, reason); err != nil {
			return nil, nil, err
		}
		a, _ := t.Attempt(attempt)
		ts, err := attemptOnBooking(b, t, a, p.Actor())
		return ts, nil, err
	})
}

// ConfirmShipmentCancelled records that the carrier cancelled a shipment the
// store asked to cancel.
func (s *Service) ConfirmShipmentCancelled(ctx context.Context, p identity.Principal, id delivery.TaskID) (*delivery.Task, error) {
	return s.withTask(ctx, p, id, "delivery.cancelled", "carrier confirmed", func(_ pgx.Tx, _ *rentals.Booking, t *delivery.Task, _ time.Time) ([]rentals.Transition, *delivery.Task, error) {
		return nil, nil, t.ConfirmCancelled()
	})
}

// ReceiveReturnParcel is staff confirming the return parcel arrived: every
// item handed to the courier is back, its asset goes to inspection (never
// straight to READY) and its late fee is assessed.
func (s *Service) ReceiveReturnParcel(ctx context.Context, p identity.Principal, id delivery.TaskID) (*delivery.Task, error) {
	return s.withTask(ctx, p, id, "delivery.received_at_store", "", func(tx pgx.Tx, b *rentals.Booking, t *delivery.Task, now time.Time) ([]rentals.Transition, *delivery.Task, error) {
		if err := t.ReceiveAtStore(p.Actor(), now); err != nil {
			return nil, nil, err
		}
		var back []inventory.AssetID
		for _, it := range b.Items {
			if it.ReturnedAt != nil && !it.Resolved() {
				back = append(back, it.AssetID)
			}
		}
		tr, err := b.RecordReceivedAtStore(p.Actor(), back, now)
		if err != nil {
			return nil, nil, err
		}
		return []rentals.Transition{tr}, nil, s.receiveAssets(ctx, tx, b, back, p.Actor(), true, now)
	})
}

// CloseInput is what staff decided once the carrier stopped attempting. A new
// shipment needs the date and courier window it goes out in.
type CloseInput struct {
	FollowUp delivery.FollowUp
	Reason   string
	Date     businesstime.Date
	WindowID string
}

// CloseShipment closes a shipment the carrier stopped attempting (decision
// D42) and starts the follow-up: a free new shipment for the same leg, the
// counter, or ending a rental the customer never received.
func (s *Service) CloseShipment(ctx context.Context, p identity.Principal, id delivery.TaskID, in CloseInput) (*delivery.Task, error) {
	return s.withTask(ctx, p, id, "delivery.closed", in.Reason, func(tx pgx.Tx, b *rentals.Booking, t *delivery.Task, now time.Time) ([]rentals.Transition, *delivery.Task, error) {
		var window period.Range
		if in.FollowUp == delivery.FollowUpNewShipment {
			cfg, err := s.config(ctx, tx)
			if err != nil {
				return nil, nil, err
			}
			if window, err = cfg.calendar.DeliveryWindow(in.Date, in.WindowID); err != nil {
				return nil, nil, err
			}
			if !window.Start().After(now) {
				return nil, nil, domainerr.Invalid("date", "the new window must be in the future")
			}
		}
		if err := t.CloseUnfulfilled(p, in.FollowUp, in.Reason, now); err != nil {
			return nil, nil, err
		}
		switch in.FollowUp {
		case delivery.FollowUpNewShipment:
			store, customer := t.From, t.To
			if t.Direction == delivery.Return {
				store, customer = t.To, t.From
			}
			// The closed task links to the new one; the link is checked at commit,
			// once both are saved.
			n, err := t.FollowUpTask(delivery.NewTaskRequest{
				ID: delivery.TaskID(s.newID()), BookingRef: t.BookingRef, CustomerID: t.CustomerID, Direction: t.Direction,
				ProviderCode: t.ProviderCode, Store: store, Customer: customer, Window: window, Parcel: t.Parcel,
				CustomerFee: money.Zero(t.CustomerFee.Currency()), Now: now,
			})
			return nil, n, err
		case delivery.FollowUpEndBooking:
			ts, err := s.endUndelivered(ctx, tx, b, t, p.Actor(), now)
			return ts, nil, err
		}
		return nil, nil, nil
	})
}

// endUndelivered ends a rental whose parcel never reached the customer: the
// items are back in the store and go to inspection, the rent kept follows
// D42, the waived delivery fees are reversed and the return pickup cancelled.
func (s *Service) endUndelivered(ctx context.Context, q postgres.Querier, b *rentals.Booking, t *delivery.Task, actor identity.Actor, now time.Time) ([]rentals.Transition, error) {
	end, tr, err := b.EndUndelivered(rentals.UndeliveredRequest{Actor: actor, Task: t, Now: now})
	if err != nil {
		return nil, err
	}
	l, lv, err := paymentspg.LoadLedger(ctx, q, string(b.ID), true)
	if err != nil {
		return nil, err
	}
	if end.Rent.Amount() < end.RentBefore.Amount() {
		if _, err := l.ReduceRent("undelivered:"+string(t.ID), payments.ChargeID(s.newID()), end.Rent, end.Basis, actor, now); err != nil {
			return nil, err
		}
	}
	for _, c := range l.Charges {
		waive := (c.DeliveryLeg == payments.LegOutbound && end.WaiveOutbound) || (c.DeliveryLeg == payments.LegReturn && end.WaiveReturn)
		if c.Kind == payments.ChargeDeliveryFee && waive && c.Reversal == nil {
			if err := l.ReverseCharge(c.ID, "undelivered: "+end.Basis, actor, now); err != nil {
				return nil, err
			}
		}
	}
	if _, err := paymentspg.SaveLedger(ctx, q, l, lv, now); err != nil {
		return nil, err
	}
	if err := s.auditMoney(ctx, q, "booking.ended_undelivered", "booking", string(b.ID), string(b.ID), end.Basis, end.Rent, actor, now); err != nil {
		return nil, err
	}
	var back []inventory.AssetID
	for _, it := range b.Items {
		if it.NotReceived && it.ReceivedAt != nil && it.LostConfirmedAt == nil {
			back = append(back, it.AssetID)
		}
	}
	if err := s.receiveAssets(ctx, q, b, back, actor, false, now); err != nil {
		return nil, err
	}
	return []rentals.Transition{tr}, s.cancelDeliveryTasks(ctx, q, b, delivery.Return, actor, "booking ended undelivered", now)
}

// CollectAtStore records the customer collecting at the counter a parcel the
// carrier gave up on (follow-up AT_STORE).
func (s *Service) CollectAtStore(ctx context.Context, p identity.Principal, id delivery.TaskID) (*delivery.Task, error) {
	return s.withTask(ctx, p, id, "delivery.collected_at_store", "", func(tx pgx.Tx, b *rentals.Booking, t *delivery.Task, now time.Time) ([]rentals.Transition, *delivery.Task, error) {
		tr, err := b.RecordCollectedAtStore(p.Actor(), t, now)
		if err != nil {
			return nil, nil, err
		}
		return []rentals.Transition{tr}, nil, s.extendDelayed(ctx, tx, b, p.Actor(), now)
	})
}

// extendDelayed lengthens the allocations of a booking whose due time a late
// delivery moved, when the extra time is free. Otherwise the allocation stays
// and the conflict is audited; the asset is still never double-allocated.
func (s *Service) extendDelayed(ctx context.Context, q postgres.Querier, b *rentals.Booking, actor identity.Actor, now time.Time) error {
	allocs, err := inventorypg.AllocationsOfBooking(ctx, q, string(b.ID), true)
	if err != nil {
		return err
	}
	allocs = slices.DeleteFunc(allocs, func(a inventory.Allocation) bool { return !a.InUse() })
	if _, err := inventorypg.LockAssets(ctx, q, allocatedAssets(allocs)); err != nil {
		return err
	}
	for i := range allocs {
		end, ok, err := b.DelayedBlockEnd(allocs[i].AssetID)
		if err != nil || !ok {
			return err
		}
		others, err := inventorypg.ActiveAllocationsOn(ctx, q, []inventory.AssetID{allocs[i].AssetID})
		if err != nil {
			return err
		}
		conflicts, err := allocs[i].Extend(end, others)
		if err != nil {
			return err
		}
		if len(conflicts) == 0 {
			if err := inventorypg.UpdateAllocation(ctx, q, allocs[i]); err != nil {
				return err
			}
			continue
		}
		refs := map[string]string{"booking_id": string(b.ID), "asset_id": string(allocs[i].AssetID)}
		for j, c := range conflicts {
			refs[fmt.Sprintf("conflicting_booking_%d", j+1)] = c.BookingRef
		}
		if err := auditpg.Append(ctx, q, audit.Event{
			ID: s.newID(), OccurredAt: now, Actor: actor, Action: "allocation.extend_conflict", ResourceType: "allocation",
			ResourceID: string(allocs[i].ID), Result: audit.ResultSuccess, References: refs,
			Reason: "late delivery moved the due time into another booking",
		}); err != nil {
			return err
		}
	}
	return nil
}

// receiveAssets marks items physically back in the store: the allocation is
// returned and the asset goes to inspection. lateFee assesses the late fee as
// for a counter return.
func (s *Service) receiveAssets(ctx context.Context, q postgres.Querier, b *rentals.Booking, back []inventory.AssetID, actor identity.Actor, lateFee bool, now time.Time) error {
	if len(back) == 0 {
		return nil
	}
	assets, err := inventorypg.LockAssets(ctx, q, back)
	if err != nil {
		return err
	}
	allocs, err := inventorypg.AllocationsOfBooking(ctx, q, string(b.ID), true)
	if err != nil {
		return err
	}
	var l *payments.Ledger
	var lv paymentspg.Version
	var fees []rentals.FeeAssessment
	if lateFee {
		if l, lv, err = paymentspg.LoadLedger(ctx, q, string(b.ID), true); err != nil {
			return err
		}
		if fees, err = rentalspg.AssessmentsOfBooking(ctx, q, b.ID); err != nil {
			return err
		}
	}
	for i := range assets {
		j := slices.IndexFunc(allocs, func(a inventory.Allocation) bool { return a.AssetID == assets[i].ID && a.InUse() })
		if j < 0 {
			return fmt.Errorf("asset %s has no allocation in use: %w", assets[i].ID, rentals.ErrAssetMismatch)
		}
		if lateFee {
			if err := noteLateImpact(ctx, q, b, allocs[j], now); err != nil {
				return err
			}
		}
		if err := allocs[j].MarkReturned(now); err != nil {
			return err
		}
		if err := inventorypg.UpdateAllocation(ctx, q, allocs[j]); err != nil {
			return err
		}
		c, err := assets[i].ReceiveReturn(actor, now)
		if err != nil {
			return err
		}
		if err := s.recordAsset(ctx, q, assets[i], c, b.ID); err != nil {
			return err
		}
		if lateFee {
			if err := s.assessLateFee(ctx, q, b, l, fees, assets[i].ID, actor, now); err != nil {
				return err
			}
		}
	}
	if lateFee {
		_, err = paymentspg.SaveLedger(ctx, q, l, lv, now)
	}
	return err
}
