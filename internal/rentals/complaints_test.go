package rentals_test

import (
	"errors"
	"testing"
	"time"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/delivery"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/rentals"
)

// deliveredBooking places a courier booking with the bundle (l1) and a jacket L
// (l2), dispatches it and records delivery at the rental start.
func deliveredBooking(t *testing.T) (*rentals.Booking, []inventory.AssetID) {
	t.Helper()
	items, _ := catalogFixture()
	jacketL, err := catalog.QuoteItem(items["jacket"], "jacket-l", pkg3, 1)
	if err != nil {
		t.Fatal(err)
	}
	z := zone
	p := mustPlace(t, func(r *rentals.PlaceRequest) {
		r.Schedule = courierSchedule(t, rentals.LegCourier, "mid", rentals.LegCourier)
		r.Fulfillment = rentals.FulfillmentRequest{Outbound: rentals.LegCourier, Return: rentals.LegCourier, Zone: &z, AreaVersion: "area-v1"}
		r.Lines = append(r.Lines, rentals.LineRequest{ID: "l2", Quote: jacketL})
	})
	b := p.Booking
	_, _ = b.Confirm(system, funds(99_000, 0, 99_000), now)
	_, _ = b.StartPreparation(staff, now)
	assets := allocatedAssets(p)
	if _, err := b.RecordHandover(rentals.HandoverRequest{Actor: staff, Allocated: allocated(p), Delivered: delivered(assets...),
		ChecklistCompleted: true, Funds: funds(390_000, 850_000, 99_000), Now: b.Schedule.PickupAt.Add(-3 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.RecordDelivered(system, b.Schedule.PickupAt); err != nil {
		t.Fatal(err)
	}
	return b, assets
}

func complaint(t *testing.T, b *rentals.Booking, existing []rentals.DeliveryComplaint, kind rentals.ComplaintKind, at time.Time, assets ...inventory.AssetID) (*rentals.DeliveryComplaint, error) {
	t.Helper()
	c, _, err := b.RaiseDeliveryComplaint(existing, rentals.ComplaintRequest{
		ID: rentals.ComplaintID("cp-" + string(kind)), Actor: identity.UserActor(customer), Kind: kind,
		Assets: assets, Description: "không đúng đơn", Evidence: photo, Now: at,
	})
	return c, err
}

func TestComplaintWindowAndScope(t *testing.T) {
	b, assets := deliveredBooking(t)
	start := b.Schedule.PickupAt
	if _, err := complaint(t, b, nil, rentals.ComplaintWrongItem, start.Add(2*time.Hour), assets[0]); !errors.Is(err, rentals.ErrComplaintWindowClosed) {
		t.Fatalf("after the 2h window err = %v", err)
	}
	if _, _, err := b.RaiseDeliveryComplaint(nil, rentals.ComplaintRequest{ID: "c", Actor: identity.UserActor(customer), Kind: rentals.ComplaintDamagedOnArrival,
		Assets: assets[:1], Description: "rách", Now: start}); !errors.Is(err, rentals.ErrEvidenceRequired) {
		t.Fatalf("damage without photo err = %v", err)
	}
	c, err := complaint(t, b, nil, rentals.ComplaintWrongItem, start.Add(time.Hour), assets[0])
	if err != nil || c.Status != rentals.ComplaintOpen {
		t.Fatalf("complaint = %+v, %v", c, err)
	}
	if _, err := complaint(t, b, []rentals.DeliveryComplaint{*c}, rentals.ComplaintDamagedOnArrival, start.Add(time.Hour), assets[0]); !errors.Is(err, rentals.ErrComplaintDuplicate) {
		t.Fatalf("second complaint on the same item err = %v", err)
	}

	// Counter pickups are checked in front of the customer: no window.
	counter, counterAssets := handedOver(t)
	if _, err := complaint(t, counter, nil, rentals.ComplaintMissingItem, counter.Schedule.PickupAt, counterAssets[0]); !errors.Is(err, rentals.ErrComplaintNotAllowed) {
		t.Fatalf("store pickup complaint err = %v", err)
	}

	owner := principal(t, customer, identity.RoleCustomer)
	other := principal(t, "cust-b", identity.RoleCustomer)
	if s, err := rentals.Authorize(owner, rentals.ActionComplain, b); err != nil || s != identity.ScopeOwn {
		t.Fatalf("owner = %v, %v", s, err)
	}
	if _, err := rentals.Authorize(other, rentals.ActionComplain, b); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("other customer err = %v", err)
	}
}

func TestComplaintDecisionNeedsStaff(t *testing.T) {
	b, assets := deliveredBooking(t)
	c, _ := complaint(t, b, nil, rentals.ComplaintWrongItem, b.Schedule.PickupAt, assets[0])
	owner := principal(t, customer, identity.RoleCustomer)
	clerk := principal(t, "staff1", identity.RoleRentalStaff)
	if _, err := c.Accept(b, owner, delivery.FaultStore, "tự duyệt", now); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("customer accepting err = %v", err)
	}
	if _, err := c.Accept(b, clerk, delivery.FaultCustomer, "x", now); err == nil {
		t.Fatal("customer-caused complaints are rejected, not accepted")
	}
	if _, err := c.Reject(b, clerk, "", now); !errors.Is(err, rentals.ErrReasonRequired) {
		t.Fatalf("reject without note err = %v", err)
	}
	if _, err := c.Reject(b, clerk, "ảnh cho thấy đúng size đã đặt", now); err != nil || c.Status != rentals.ComplaintRejected {
		t.Fatalf("reject = %v %s", err, c.Status)
	}
}

func TestWrongItemReplacedFreeOfCharge(t *testing.T) {
	b, assets := deliveredBooking(t)
	clerk := principal(t, "staff1", identity.RoleRentalStaff)
	at := b.Schedule.PickupAt.Add(time.Hour)
	c, _ := complaint(t, b, nil, rentals.ComplaintWrongItem, at, assets[0]) // jacket M of the bundle
	if _, err := c.Accept(b, clerk, delivery.FaultStore, "đóng gói nhầm", at); err != nil {
		t.Fatal(err)
	}
	ds, err := c.ReplacementDemands(b, at)
	if err != nil || len(ds) != 1 || ds[0].VariantID != "jacket-m" || !ds[0].Block.Start().Equal(at) {
		t.Fatalf("demands = %+v, %v", ds, err)
	}
	if _, err := c.ResolveWithReplacement(b, clerk, []rentals.Replacement{{Old: assets[0], New: "JK-M-2"}}, "giao bù", at); err != nil {
		t.Fatal(err)
	}
	old, _ := b.Item(assets[0])
	repl, err := b.Item("JK-M-2")
	if err != nil || repl.ReplacementFor != assets[0] || old.ReplacedBy != "JK-M-2" || repl.DailyRate.Amount() != 50_000 || c.Status != rentals.ComplaintReplaced {
		t.Fatalf("old=%+v new=%+v", old, repl)
	}
	// The store collects the wrong item: no late fee even when it comes back late.
	_, _ = b.RecordCourierPickup(system, []inventory.AssetID{assets[0]}, b.Schedule.DueAt.Add(30*time.Hour))
	if q, _ := b.QuoteLateFee(assets[0], false); !q.Amount.IsZero() {
		t.Fatalf("late fee on a store-collected item = %s", q.Amount)
	}
	// Damage on it needs evidence of new damage.
	_, _ = b.RecordReceivedAtStore(staff, []inventory.AssetID{assets[0]}, b.Schedule.DueAt.Add(31*time.Hour))
	_, _ = b.RecordInspection(staff, assets[0], rentals.InspectionFinding{Conditions: []rentals.Condition{rentals.CondMinorRepairable}, Evidence: photo}, b.Schedule.DueAt.Add(31*time.Hour))
	if _, err := b.ProposeFee(nil, rentals.ProposeFeeRequest{ID: "f1", Asset: assets[0], Kind: rentals.FeeDamageRepair, Amount: vnd(10_000), Basis: "x"}, clerk, now); !errors.Is(err, rentals.ErrNewDamageUnproven) {
		t.Fatalf("damage fee on an arrival-issue item err = %v", err)
	}
}

func TestMissingItemRefundsTheWholeLine(t *testing.T) {
	b, assets := deliveredBooking(t)
	clerk := principal(t, "staff1", identity.RoleRentalStaff)
	at := b.Schedule.PickupAt.Add(30 * time.Minute)
	jacketL := assets[len(assets)-1] // line l2
	c, _ := complaint(t, b, nil, rentals.ComplaintMissingItem, at, jacketL)
	if _, _, err := c.ResolveWithRefund(b, clerk, "x", at); !errors.Is(err, rentals.ErrInvalidTransition) {
		t.Fatalf("refund before acceptance err = %v", err)
	}
	if _, err := c.Accept(b, clerk, delivery.FaultCarrier, "shipper làm rơi", at); err != nil {
		t.Fatal(err)
	}
	credit, _, err := c.ResolveWithRefund(b, clerk, "không còn áo L để giao bù", at)
	if err != nil || credit.Amount.Amount() != 130_000 || b.RentTotal.Amount() != 200_000 || len(b.ServiceCredits) != 1 {
		t.Fatalf("credit = %+v rent = %s, %v", credit, b.RentTotal, err)
	}
	_, _ = b.RecordCourierPickup(system, assets[:len(assets)-1], b.Schedule.DueAt)
	if b.IsOverdue(b.Schedule.DueAt.Add(time.Hour)) {
		t.Fatal("an item the customer never received does not make the booking overdue")
	}
	if q, _ := b.QuoteLateFee(jacketL, false); !q.Amount.IsZero() {
		t.Fatalf("late fee on an item never received = %s", q.Amount)
	}
	if _, err := b.ProposeFee(nil, rentals.ProposeFeeRequest{ID: "f1", Asset: jacketL, Kind: rentals.FeeLostItem, Amount: vnd(1), Basis: "x"}, clerk, now); !errors.Is(err, rentals.ErrFeeNotAllowed) {
		t.Fatalf("loss fee on an item never received err = %v", err)
	}
	if _, err := b.RecordUndeliveredItemBack(staff, jacketL, at.Add(5*time.Hour)); err != nil {
		t.Fatalf("undelivered item back: %v", err)
	}
}

func TestBundleRefundIsProRataByDailyRate(t *testing.T) {
	b, assets := deliveredBooking(t) // l1 bundle 200,000: jacket 50k, pants 30k, belt 10k a day
	clerk := principal(t, "staff1", identity.RoleRentalStaff)
	at := b.Schedule.PickupAt.Add(30 * time.Minute)
	refund := func(id string, asset inventory.AssetID) (rentals.ServiceCredit, error) {
		t.Helper()
		c, _, err := b.RaiseDeliveryComplaint(nil, rentals.ComplaintRequest{ID: rentals.ComplaintID(id), Actor: identity.UserActor(customer),
			Kind: rentals.ComplaintMissingItem, Assets: []inventory.AssetID{asset}, Description: "thiếu", Now: at})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Accept(b, clerk, delivery.FaultStore, "đóng gói thiếu", at); err != nil {
			t.Fatal(err)
		}
		credit, _, err := c.ResolveWithRefund(b, clerk, "không giao bù kịp", at)
		return credit, err
	}
	jacket, pants, belt := assets[0], assets[1], assets[2]
	// 200,000 × 30/90 = 66,666.67 → 66,667 (rounded up for the customer).
	if c, err := refund("cp-pants", pants); err != nil || c.Amount.Amount() != 66_667 {
		t.Fatalf("pants refund = %+v, %v", c, err)
	}
	// 200,000 × 10/90 = 22,222.2 → 22,223.
	if c, err := refund("cp-belt", belt); err != nil || c.Amount.Amount() != 22_223 {
		t.Fatalf("belt refund = %+v, %v", c, err)
	}
	// 200,000 × 50/90 = 111,111.1 → capped so the line total stays 200,000.
	if c, err := refund("cp-jacket", jacket); err != nil || c.Amount.Amount() != 111_110 {
		t.Fatalf("jacket refund = %+v, %v", c, err)
	}
	var lineTotal int64
	for _, sc := range b.ServiceCredits {
		for _, lc := range sc.Lines {
			if lc.LineID == "l1" {
				lineTotal += lc.Amount.Amount()
			}
		}
	}
	if lineTotal != 200_000 || b.RentTotal.Amount() != 130_000 {
		t.Fatalf("line refunded %d, rent now %s", lineTotal, b.RentTotal)
	}
}
