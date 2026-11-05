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
	p := dispatchedPlacement(t)
	return p.Booking, allocatedAssets(p)
}

func dispatchedPlacement(t *testing.T) rentals.Placement {
	t.Helper()
	p := placeCourier(t, rentals.LegCourier, "mid", rentals.LegCourier)
	b := p.Booking
	_, _ = b.Confirm(system, funds(60_000, 0, 60_000), now)
	_, _ = b.StartPreparation(staff, now)
	if _, err := b.RecordHandover(rentals.HandoverRequest{Actor: staff, Allocated: allocated(p), Delivered: delivered(allocatedAssets(p)...),
		ChecklistCompleted: true, Funds: funds(260_000, 550_000, 60_000), Now: b.Schedule.PickupAt.Add(-3 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return p
}

// closedOutbound is the booking's outbound shipment that the carrier gave up
// on, classified and closed by staff with the parcel back in the store.
func closedOutbound(t *testing.T, b *rentals.Booking, fault delivery.Fault, follow delivery.FollowUp, back time.Time) *delivery.Task {
	t.Helper()
	// Fake test addresses only.
	store := delivery.Address{RecipientName: "Cửa hàng", Phone: "0900000000", Line: "1 Đường A", WardCode: "W1", DistrictCode: "D1", ProvinceCode: "P1"}
	home := delivery.Address{RecipientName: "Khách Test", Phone: "0911111111", Line: "2 Đường B", WardCode: "W2", DistrictCode: "D3", ProvinceCode: "P1"}
	tk, err := delivery.NewTask(delivery.NewTaskRequest{ID: delivery.TaskID("out-" + string(follow)), BookingRef: string(b.ID), CustomerID: b.CustomerID,
		Direction: delivery.Outbound, ProviderCode: "ghn", Store: store, Customer: home, Window: *b.Fulfillment.OutboundWindow,
		Parcel: delivery.Parcel{WeightGram: 800, PackageCount: 1}, CustomerFee: vnd(30_000), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tk.RecordCreated(delivery.ShipmentResult{ProviderShipmentCode: "GHN1"}); err != nil {
		t.Fatal(err)
	}
	start := b.Schedule.PickupAt
	ev := func(key string, s delivery.Status, at time.Time) delivery.CarrierEvent {
		return delivery.CarrierEvent{Provider: "ghn", ShipmentCode: "GHN1", Key: key, Status: s, OccurredAt: at, ReceivedAt: at}
	}
	_, _ = tk.ApplyCarrierEvent(ev("p1", delivery.StatusPickedUp, start.Add(-2*time.Hour)))
	last := ev("f1", delivery.StatusDeliveryFailed, start.Add(time.Hour))
	last.GaveUp, last.Reason = true, "không liên lạc được"
	if out, err := tk.ApplyCarrierEvent(last); err != nil || out != delivery.OutcomeReview {
		t.Fatalf("give up = %s, %v", out, err)
	}
	if err := tk.ClassifyAttempt(staff, "f1", fault, "đã gọi 3 lần"); err != nil {
		t.Fatal(err)
	}
	if err := tk.CloseUnfulfilled(principal(t, "staff1", identity.RoleRentalStaff), follow, "hàng đã về kho", back); err != nil {
		t.Fatal(err)
	}
	return tk
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
	customerFault := delivery.Attempt{ID: "att-1", TaskID: "task-1", Fault: delivery.FaultCustomer, Reason: "sai địa chỉ"}
	fee, err := b.ProposeFee(nil, rentals.ProposeFeeRequest{ID: "f1", Kind: rentals.FeeRedelivery, Attempt: &customerFault}, clerk, now)
	if err != nil || fee.Amount.Amount() != 20_000 || fee.Ref != "task-1" || fee.Status != rentals.AssessmentProposed {
		t.Fatalf("redelivery fee = %+v, %v", fee, err)
	}
	propose := func(id string, a delivery.Attempt, existing ...rentals.FeeAssessment) (rentals.FeeAssessment, error) {
		return b.ProposeFee(existing, rentals.ProposeFeeRequest{ID: rentals.AssessmentID(id), Kind: rentals.FeeRedelivery, Attempt: &a}, clerk, now)
	}
	if _, err := propose("f2", customerFault, fee); !errors.Is(err, rentals.ErrDuplicateFee) {
		t.Fatalf("second fee for one attempt err = %v", err)
	}
	// Decision D42: the carrier tried the same shipment again and the customer
	// was absent again; still one fee for the shipment.
	again := delivery.Attempt{ID: "att-2", TaskID: "task-1", Fault: delivery.FaultCustomer, Reason: "khách vắng"}
	if _, err := propose("f2", again, fee); !errors.Is(err, rentals.ErrDuplicateFee) {
		t.Fatalf("second fee for one shipment err = %v", err)
	}
	newShipment := delivery.Attempt{ID: "att-3", TaskID: "task-2", Fault: delivery.FaultCustomer, Reason: "khách vắng"}
	if f, err := propose("f3", newShipment, fee); err != nil || f.Ref != "task-2" {
		t.Fatalf("fee for a new shipment = %+v, %v", f, err)
	}
	if _, err := propose("f4", delivery.Attempt{ID: "att-4", Fault: delivery.FaultCustomer}); err == nil {
		t.Fatal("an attempt without its shipment cannot be charged")
	}
	carrierFault := delivery.Attempt{ID: "att-5", TaskID: "task-3", Fault: delivery.FaultCarrier}
	if _, err := propose("f5", carrierFault); !errors.Is(err, rentals.ErrFeeNotAllowed) {
		t.Fatalf("redelivery fee for a carrier failure err = %v", err)
	}
}

// Decision D42: delivery delay only counts for the customer until the first
// attempt the customer caused to fail.
func TestCustomerMissedDeliveryStopsTheDelayCredit(t *testing.T) {
	b, assets := dispatched(t)
	start, due := b.Schedule.PickupAt, b.Schedule.DueAt
	if _, err := b.RecordDeliveryFailure(system, delivery.Attempt{ID: "a1", At: start.Add(time.Hour), Fault: delivery.FaultUnknown}); err == nil {
		t.Fatal("unclassified attempts must be classified first")
	}
	if _, err := b.RecordDeliveryFailure(system, delivery.Attempt{ID: "a1", At: start.Add(time.Hour), Fault: delivery.FaultCustomer, Reason: "khách vắng"}); err != nil {
		t.Fatal(err)
	}
	_, _ = b.RecordDelivered(system, start.Add(25*time.Hour))
	if _, err := b.RecordDeliveryFailure(system, delivery.Attempt{ID: "a2", At: start, Fault: delivery.FaultCustomer}); !errors.Is(err, rentals.ErrInvalidTransition) {
		t.Fatalf("failure after delivery err = %v", err)
	}
	// Handed back 3h after due: 1h of delay is credited, 2h are late (25%).
	_, _ = b.RecordCourierPickup(system, assets[:1], due.Add(3*time.Hour))
	if q, _ := b.QuoteLateFee(assets[0], false); q.Lateness != 2*time.Hour || q.Percent != 25 {
		t.Fatalf("customer-caused delay quote = %+v", q)
	}

	// The carrier failed: the whole delay is credited.
	b, assets = dispatched(t)
	start, due = b.Schedule.PickupAt, b.Schedule.DueAt
	_, _ = b.RecordDeliveryFailure(system, delivery.Attempt{ID: "a1", At: start.Add(time.Hour), Fault: delivery.FaultCarrier, Reason: "shipper hỏng xe"})
	_, _ = b.RecordDelivered(system, start.Add(25*time.Hour))
	_, _ = b.RecordCourierPickup(system, assets[:1], due.Add(3*time.Hour))
	if q, _ := b.QuoteLateFee(assets[0], false); q.Lateness != 0 {
		t.Fatalf("carrier-caused delay quote = %+v", q)
	}
}

func TestDelayedBlockEnd(t *testing.T) {
	p := dispatchedPlacement(t)
	b := p.Booking
	if _, ok, err := b.DelayedBlockEnd(p.Allocations[0].AssetID); ok || err != nil {
		t.Fatalf("no delay yet: ok %v, err %v", ok, err)
	}
	_, _ = b.RecordDelivered(system, b.Schedule.PickupAt.Add(3*time.Hour))
	for _, a := range p.Allocations {
		end, ok, err := b.DelayedBlockEnd(a.AssetID)
		if err != nil || !ok || !end.Equal(a.Block.End().Add(3*time.Hour)) {
			t.Fatalf("%s: end %s ok %v err %v, want block end %s + 3h", a.AssetID, end, ok, err, a.Block.End())
		}
	}
}

func TestCollectedAtStoreAfterFailedDelivery(t *testing.T) {
	b, assets := dispatched(t)
	back := b.Schedule.PickupAt.Add(30 * time.Hour)
	ended := closedOutbound(t, b, delivery.FaultCarrier, delivery.FollowUpEndBooking, back)
	if _, err := b.RecordCollectedAtStore(staff, ended, back.Add(time.Hour)); !errors.Is(err, rentals.ErrInvalidTransition) {
		t.Fatalf("collecting a shipment closed to end the booking err = %v", err)
	}
	tk := closedOutbound(t, b, delivery.FaultCarrier, delivery.FollowUpAtStore, back)
	if _, err := b.RecordCollectedAtStore(staff, tk, back.Add(-time.Hour)); err == nil {
		t.Fatal("collected before the parcel was back")
	}
	if _, err := b.RecordCollectedAtStore(staff, tk, back.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !b.Fulfillment.CollectedAtStore || !b.Fulfillment.DeliveredAt.Equal(back.Add(2*time.Hour)) {
		t.Fatalf("fulfillment = %+v", b.Fulfillment)
	}
	if _, err := b.RecordCollectedAtStore(staff, tk, back.Add(3*time.Hour)); !errors.Is(err, rentals.ErrInvalidTransition) {
		t.Fatalf("collecting twice err = %v", err)
	}
	_, _, err := b.RaiseDeliveryComplaint(nil, rentals.ComplaintRequest{ID: "c1", Actor: identity.UserActor(customer), Kind: rentals.ComplaintMissingItem,
		Assets: assets[:1], Description: "thiếu", Now: back.Add(3 * time.Hour)})
	if !errors.Is(err, rentals.ErrComplaintNotAllowed) {
		t.Fatalf("complaint after a counter collection err = %v", err)
	}
	if _, ok, _ := b.DelayedBlockEnd(assets[0]); !ok {
		t.Fatal("the carrier caused the delay: the due time moves")
	}
}

func TestEndUndelivered(t *testing.T) {
	clerk := principal(t, "staff1", identity.RoleRentalStaff)
	b, assets := dispatched(t)
	rent := b.RentTotal
	back := b.Schedule.PickupAt.Add(30 * time.Hour)
	if _, err := b.RecordLostInTransit(staff, assets[0], "thất lạc", b.Schedule.PickupAt); err != nil {
		t.Fatal(err)
	}
	atStore := closedOutbound(t, b, delivery.FaultCustomer, delivery.FollowUpAtStore, back)
	if _, _, err := b.EndUndelivered(rentals.UndeliveredRequest{Actor: staff, Task: atStore, Now: back}); !errors.Is(err, rentals.ErrInvalidTransition) {
		t.Fatalf("ending with a shipment closed for counter pickup err = %v", err)
	}
	tk := closedOutbound(t, b, delivery.FaultCustomer, delivery.FollowUpEndBooking, back)
	end, tr, err := b.EndUndelivered(rentals.UndeliveredRequest{Actor: staff, Task: tk, Now: back.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	want := rent.Amount() * 70 / 100
	if end.Rent.Amount() != want || b.RentTotal.Amount() != want || end.RentBefore != rent || end.FeePercent != 70 ||
		!end.WaiveReturn || end.WaiveOutbound || end.Fault != delivery.FaultCustomer || b.Fulfillment.Undelivered == nil {
		t.Fatalf("customer-caused end = %+v, rent %s", end, b.RentTotal)
	}
	if tr.To != rentals.StatusReturnInspection || b.Status != rentals.StatusReturnInspection {
		t.Fatalf("status = %s", b.Status)
	}
	lost, _ := b.Item(assets[0])
	if lost.LostConfirmedAt == nil || lost.ReceivedAt != nil || lost.NotReceived {
		t.Fatalf("the item lost in transit stays lost: %+v", lost)
	}
	it, _ := b.Item(assets[1])
	if !it.NotReceived || it.ReceivedAt == nil || !it.ReceivedAt.Equal(back) {
		t.Fatalf("undelivered item = %+v", it)
	}
	if q, _ := b.QuoteLateFee(assets[1], false); !q.Amount.IsZero() {
		t.Fatalf("late fee for an item never delivered = %s", q.Amount)
	}
	if _, err := b.RecordInspection(staff, assets[1], rentals.InspectionFinding{Conditions: []rentals.Condition{rentals.CondMinorRepairable}, Evidence: photo}, back); err != nil {
		t.Fatal(err)
	}
	req := rentals.ProposeFeeRequest{ID: "f1", Asset: assets[1], Kind: rentals.FeeDamageRepair, Amount: vnd(30_000), Basis: "sửa", Evidence: photo, ConfirmedNewDamage: true}
	if _, err := b.ProposeFee(nil, req, clerk, now); !errors.Is(err, rentals.ErrFeeNotAllowed) {
		t.Fatalf("damage fee on an item the customer never had err = %v", err)
	}
	if _, _, err := b.EndUndelivered(rentals.UndeliveredRequest{Actor: staff, Task: tk, Now: back}); !errors.Is(err, rentals.ErrInvalidTransition) {
		t.Fatalf("ending twice err = %v", err)
	}

	// The store or the carrier caused it: nothing is kept.
	b, _ = dispatched(t)
	tk = closedOutbound(t, b, delivery.FaultCarrier, delivery.FollowUpEndBooking, back)
	end, _, err = b.EndUndelivered(rentals.UndeliveredRequest{Actor: staff, Task: tk, Now: back})
	if err != nil || !end.Rent.IsZero() || !b.RentTotal.IsZero() || end.FeePercent != 0 || !end.WaiveOutbound || !end.WaiveReturn {
		t.Fatalf("carrier-caused end = %+v, %v", end, err)
	}

	// Another booking's shipment, or a delivered booking, cannot end it.
	b, _ = dispatched(t)
	tk = closedOutbound(t, b, delivery.FaultCarrier, delivery.FollowUpEndBooking, back)
	tk.BookingRef = "someone-else"
	if _, _, err := b.EndUndelivered(rentals.UndeliveredRequest{Actor: staff, Task: tk, Now: back}); err == nil {
		t.Fatal("another booking's shipment ended this booking")
	}
	_, _ = b.RecordDelivered(system, b.Schedule.PickupAt)
	tk = closedOutbound(t, b, delivery.FaultCarrier, delivery.FollowUpEndBooking, back)
	if _, _, err := b.EndUndelivered(rentals.UndeliveredRequest{Actor: staff, Task: tk, Now: back}); !errors.Is(err, rentals.ErrInvalidTransition) {
		t.Fatalf("ending a delivered booking err = %v", err)
	}
}
