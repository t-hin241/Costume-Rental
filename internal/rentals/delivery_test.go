package rentals_test

import (
	"errors"
	"testing"
	"time"

	"webee_rentor/internal/delivery"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/rentals"
)

var zone = delivery.Zone{ID: "inner", ProvinceCode: "P1", DistrictCodes: []string{"D3"},
	OutboundFee: vnd(30_000), ReturnFee: vnd(30_000), RedeliveryFee: vnd(20_000), Active: true}

func courierSchedule(t *testing.T, out rentals.LegMode, slot string, ret rentals.LegMode) rentals.Schedule {
	t.Helper()
	s, err := calendar(t).ResolveFulfillment(date(t, "2026-11-10"), out, slot, 3*day, ret)
	if err != nil {
		t.Fatalf("resolve %s/%s/%s: %v", out, slot, ret, err)
	}
	return s
}

func placeCourier(t *testing.T, out rentals.LegMode, slot string, ret rentals.LegMode) rentals.Placement {
	t.Helper()
	z := zone
	return mustPlace(t, func(r *rentals.PlaceRequest) {
		r.Schedule = courierSchedule(t, out, slot, ret)
		r.Fulfillment = rentals.FulfillmentRequest{Outbound: out, Return: ret, Zone: &z, AreaVersion: "area-v1"}
	})
}

// dispatched places a courier/courier booking and hands it to the courier.
func dispatched(t *testing.T) (*rentals.Booking, []inventory.AssetID) {
	t.Helper()
	p := placeCourier(t, rentals.LegCourier, "mid", rentals.LegCourier)
	b := p.Booking
	_, _ = b.Confirm(system, funds(60_000, 0, 60_000), now)
	_, _ = b.StartPreparation(staff, now)
	assets := allocatedAssets(p)
	if _, err := b.RecordHandover(rentals.HandoverRequest{Actor: staff, Allocated: allocated(p), Delivered: delivered(assets...),
		ChecklistCompleted: true, Funds: funds(260_000, 550_000, 60_000), Now: b.Schedule.PickupAt.Add(-3 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return b, assets
}

func TestResolveFulfillment(t *testing.T) {
	// Courier both ways: delivery 09-11 → rental from 11:00 (local) → due 11:00,
	// collected in the 09-11 window that ends at due.
	s := courierSchedule(t, rentals.LegCourier, "mid", rentals.LegCourier)
	if want := time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC); !s.PickupAt.Equal(want) {
		t.Fatalf("rental start = %s, want end of delivery window", s.PickupAt)
	}
	// Courier out, store return: delivered 07-09 → start 09:00 → due on a store slot.
	_ = courierSchedule(t, rentals.LegCourier, "early", rentals.LegStore)
	// Store pickup, courier return: due 09:00 is the end of the 07-09 window.
	_ = courierSchedule(t, rentals.LegStore, "morning", rentals.LegCourier)

	cal := calendar(t)
	if _, err := cal.ResolveFulfillment(date(t, "2026-11-10"), rentals.LegCourier, "mid", 3*day, rentals.LegStore); !errors.Is(err, rentals.ErrReturnOutsideSlots) {
		t.Fatalf("due 11:00 is not a store slot start, err = %v", err)
	}
	if _, err := cal.ResolveFulfillment(date(t, "2026-11-10"), rentals.LegCourier, "night", 3*day, rentals.LegCourier); !errors.Is(err, rentals.ErrNoDeliveryWindow) {
		t.Fatalf("unknown window err = %v", err)
	}
}

func TestCourierBookingSnapshotsFeesAndTransitBlocks(t *testing.T) {
	p := placeCourier(t, rentals.LegCourier, "mid", rentals.LegCourier)
	b := p.Booking
	f := b.Fulfillment
	if f.OutboundFee.Amount() != 30_000 || f.ReturnFee.Amount() != 30_000 || f.RedeliveryFee.Amount() != 20_000 || f.ZoneID != "inner" {
		t.Fatalf("fees = %+v", f)
	}
	if f.OutboundWindow == nil || f.ReturnWindow == nil || !f.ReturnWindow.End().Equal(b.Schedule.DueAt) {
		t.Fatalf("windows = %+v", f)
	}
	var jacket inventory.PlannedAllocation
	for _, a := range p.Allocations {
		if a.ComponentKey == "jacket" {
			jacket = a
		}
	}
	// Leaves the store 1h before the 09:00 window, minus 2h preparation;
	// back 3h after due, plus 24h turnaround.
	if !jacket.Block.Start().Equal(f.OutboundWindow.Start().Add(-3*time.Hour)) || !jacket.Block.End().Equal(b.Schedule.DueAt.Add(27*time.Hour)) {
		t.Fatalf("block = %s", jacket.Block)
	}

	_, _ = b.Confirm(system, funds(60_000, 0, 60_000), now)
	_, _ = b.StartPreparation(staff, now)
	req := rentals.HandoverRequest{Actor: staff, Allocated: allocated(p), Delivered: delivered(allocatedAssets(p)...),
		ChecklistCompleted: true, Funds: funds(200_000, 550_000, 60_000), Now: b.Schedule.PickupAt.Add(-3 * time.Hour)}
	if _, err := b.RecordHandover(req); !errors.Is(err, rentals.ErrInsufficientFunds) {
		t.Fatalf("delivery fees must be paid with the rent, err = %v", err)
	}

	if _, err := place(t, func(r *rentals.PlaceRequest) {
		r.Schedule = courierSchedule(t, rentals.LegCourier, "mid", rentals.LegCourier)
		r.Fulfillment = rentals.FulfillmentRequest{Outbound: rentals.LegCourier, Return: rentals.LegCourier}
	}); !errors.Is(err, rentals.ErrDeliveryNotOffered) {
		t.Fatalf("courier without a zone err = %v", err)
	}
}

func TestCourierReturnFlow(t *testing.T) {
	b, assets := dispatched(t)
	due := b.Schedule.DueAt
	if _, err := b.RecordDelivered(system, b.Schedule.PickupAt.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.RecordCourierPickup(system, assets, due.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if b.Status != rentals.StatusRented || b.IsOverdue(due.Add(time.Hour)) {
		t.Fatalf("items with the courier: status %s, overdue %v", b.Status, b.IsOverdue(due.Add(time.Hour)))
	}
	if _, err := b.RecordInspection(staff, assets[0], rentals.InspectionFinding{}, due); !errors.Is(err, rentals.ErrAssetMismatch) {
		t.Fatalf("inspection before the parcel is back err = %v", err)
	}
	if _, err := b.RecordReceivedAtStore(staff, assets, due.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if b.Status != rentals.StatusReturnInspection || b.InspectionCompletedAt != nil {
		t.Fatalf("received at store: status %s; assets are not ready until inspected", b.Status)
	}
	q, _ := b.QuoteLateFee(assets[0], false)
	if !q.Amount.IsZero() {
		t.Fatalf("handed to the courier before due, late fee = %s", q.Amount)
	}
}

func TestServiceDelaysAreNotCustomerLateness(t *testing.T) {
	// Outbound delivered 3h after the rental start; the customer hands the
	// parcel over 3h30 after due → only 30 minutes count, inside the grace.
	b, assets := dispatched(t)
	due := b.Schedule.DueAt
	_, _ = b.RecordDelivered(system, b.Schedule.PickupAt.Add(3*time.Hour))
	_, _ = b.RecordCourierPickup(system, assets[:1], due.Add(210*time.Minute))
	if q, _ := b.QuoteLateFee(assets[0], false); !q.Amount.IsZero() || q.Lateness != 30*time.Minute {
		t.Fatalf("quote = %+v", q)
	}

	// The courier missed the pickup at due (carrier fault): lateness stops there.
	b, assets = dispatched(t)
	due = b.Schedule.DueAt
	_, _ = b.RecordDelivered(system, b.Schedule.PickupAt)
	if _, err := b.RecordReturnPickupFailure(system, delivery.Attempt{ID: "a1", At: due.Add(-30 * time.Minute), Fault: delivery.FaultCarrier, Reason: "shipper không đến"}); err != nil {
		t.Fatal(err)
	}
	_, _ = b.RecordCourierPickup(system, assets[:1], due.Add(26*time.Hour))
	if q, _ := b.QuoteLateFee(assets[0], false); !q.Amount.IsZero() {
		t.Fatalf("carrier-caused delay charged: %+v", q)
	}

	// The customer was absent: lateness keeps running.
	b, assets = dispatched(t)
	due = b.Schedule.DueAt
	_, _ = b.RecordDelivered(system, b.Schedule.PickupAt)
	_, _ = b.RecordReturnPickupFailure(system, delivery.Attempt{ID: "a1", At: due.Add(-30 * time.Minute), Fault: delivery.FaultCustomer, Reason: "khách vắng"})
	_, _ = b.RecordCourierPickup(system, assets[:1], due.Add(5*time.Hour))
	if q, _ := b.QuoteLateFee(assets[0], false); q.Percent != 50 {
		t.Fatalf("customer-caused 5h late = %d%%, want 50%%", q.Percent)
	}
	if _, err := b.RecordReturnPickupFailure(system, delivery.Attempt{ID: "a2", At: due, Fault: delivery.FaultUnknown}); err == nil {
		t.Fatal("unclassified attempts must be classified first")
	}
}

func TestCarrierLossIsNotCharged(t *testing.T) {
	clerk := principal(t, "staff1", identity.RoleRentalStaff)
	b, assets := dispatched(t)
	// Lost by the carrier before reaching the customer.
	if _, err := b.RecordLostInTransit(staff, assets[0], "GHN báo thất lạc", b.Schedule.PickupAt); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ProposeFee(nil, rentals.ProposeFeeRequest{ID: "f1", Asset: assets[0], Kind: rentals.FeeLostItem, Amount: vnd(300_000), Basis: "x"}, clerk, now); !errors.Is(err, rentals.ErrFeeNotAllowed) {
		t.Fatalf("customer charged for a carrier loss, err = %v", err)
	}
	if q, _ := b.QuoteLateFee(assets[0], false); !q.Amount.IsZero() {
		t.Fatalf("late fee for an item never delivered = %s", q.Amount)
	}
	_, _ = b.RecordDelivered(system, b.Schedule.PickupAt)
	_, _ = b.RecordCourierPickup(system, assets[1:], b.Schedule.DueAt)
	if _, err := b.RecordLostInTransit(staff, assets[1], "thất lạc chiều về", b.Schedule.DueAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.RecordCarrierDamage(system, assets[2], b.Schedule.DueAt); err != nil {
		t.Fatal(err)
	}
	if _, err := b.RecordReceivedAtStore(staff, assets[2:], b.Schedule.DueAt.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.RecordInspection(staff, assets[2], rentals.InspectionFinding{Conditions: []rentals.Condition{rentals.CondMinorRepairable}, Evidence: photo}, b.Schedule.DueAt); err != nil {
		t.Fatal(err)
	}
	req := rentals.ProposeFeeRequest{ID: "f2", Asset: assets[2], Kind: rentals.FeeDamageRepair, Amount: vnd(30_000), Basis: "sửa"}
	if _, err := b.ProposeFee(nil, req, clerk, now); !errors.Is(err, rentals.ErrNewDamageUnproven) {
		t.Fatalf("damage after a carrier damage report needs evidence, err = %v", err)
	}
}

func TestRedeliveryFee(t *testing.T) {
	clerk := principal(t, "staff1", identity.RoleRentalStaff)
	b, _ := dispatched(t)
	customerFault := delivery.Attempt{ID: "att-1", Fault: delivery.FaultCustomer, Reason: "sai địa chỉ"}
	fee, err := b.ProposeFee(nil, rentals.ProposeFeeRequest{ID: "f1", Kind: rentals.FeeRedelivery, Attempt: &customerFault}, clerk, now)
	if err != nil || fee.Amount.Amount() != 20_000 || fee.Ref != "att-1" || fee.Status != rentals.AssessmentProposed {
		t.Fatalf("redelivery fee = %+v, %v", fee, err)
	}
	if _, err := b.ProposeFee([]rentals.FeeAssessment{fee}, rentals.ProposeFeeRequest{ID: "f2", Kind: rentals.FeeRedelivery, Attempt: &customerFault}, clerk, now); !errors.Is(err, rentals.ErrDuplicateFee) {
		t.Fatalf("second fee for one attempt err = %v", err)
	}
	carrierFault := delivery.Attempt{ID: "att-2", Fault: delivery.FaultCarrier}
	if _, err := b.ProposeFee(nil, rentals.ProposeFeeRequest{ID: "f3", Kind: rentals.FeeRedelivery, Attempt: &carrierFault}, clerk, now); !errors.Is(err, rentals.ErrFeeNotAllowed) {
		t.Fatalf("redelivery fee for a carrier failure err = %v", err)
	}
}
