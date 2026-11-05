package delivery_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"webee_rentor/internal/delivery"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
	"webee_rentor/internal/period"
)

var (
	now   = time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC)
	staff = identity.UserActor("staff1")
)

func vnd(v int64) money.Money {
	m, err := money.New(v, money.VND)
	if err != nil {
		panic(err)
	}
	return m
}

// Fake test addresses only.
var (
	store    = delivery.Address{RecipientName: "Cửa hàng", Phone: "0900000000", Line: "1 Đường A", WardCode: "W1", DistrictCode: "D1", ProvinceCode: "P1"}
	customer = delivery.Address{RecipientName: "Khách Test", Phone: "0911111111", Line: "2 Đường B", WardCode: "W2", DistrictCode: "D3", ProvinceCode: "P1"}
)

func task(t *testing.T, dir delivery.Direction) *delivery.Task {
	t.Helper()
	w, _ := period.New(now, now.Add(2*time.Hour))
	tk, err := delivery.NewTask(delivery.NewTaskRequest{
		ID: delivery.TaskID("task-" + string(dir)), BookingRef: "bk-1", CustomerID: "cust-a", Direction: dir, ProviderCode: "ghn",
		Store: store, Customer: customer, Window: w, Parcel: delivery.Parcel{WeightGram: 800, PackageCount: 1, DeclaredValue: vnd(550_000)},
		CustomerFee: vnd(30_000), Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func created(t *testing.T, dir delivery.Direction) *delivery.Task {
	t.Helper()
	tk := task(t, dir)
	if dir == delivery.Return {
		if err := tk.RequestCreation(); err != nil {
			t.Fatal(err)
		}
	}
	est := vnd(25_000)
	if _, err := tk.RecordCreated(delivery.ShipmentResult{ProviderShipmentCode: "GHN123", EstimatedFee: &est}); err != nil {
		t.Fatal(err)
	}
	return tk
}

func event(key string, s delivery.Status, at time.Time) delivery.CarrierEvent {
	return delivery.CarrierEvent{Provider: "ghn", ShipmentCode: "GHN123", Key: key, Status: s, OccurredAt: at, ReceivedAt: at}
}

func TestServiceArea(t *testing.T) {
	area := delivery.ServiceArea{ProviderCode: "ghn", Version: "area-v1", Origin: store, ParcelWeightGram: 1000, Zones: []delivery.Zone{
		{ID: "inner", ProvinceCode: "P1", DistrictCodes: []string{"D1", "D3"}, OutboundFee: vnd(30_000), ReturnFee: vnd(30_000), RedeliveryFee: vnd(20_000), Active: true},
	}}
	if err := area.Validate(); err != nil {
		t.Fatal(err)
	}
	noOrigin := area
	noOrigin.Origin = delivery.Address{}
	if err := noOrigin.Validate(); err == nil {
		t.Fatal("an area without the store address was accepted")
	}
	z, err := area.Resolve(customer)
	if err != nil || z.ID != "inner" {
		t.Fatalf("zone = %+v, %v", z, err)
	}
	far := customer
	far.DistrictCode = "D99"
	if _, err := area.Resolve(far); !errors.Is(err, delivery.ErrOutOfServiceArea) {
		t.Fatalf("out of area err = %v", err)
	}
	// Ward-based zones (GHN since 2025-07): no district needed.
	wards := delivery.ServiceArea{ProviderCode: "ghn", Version: "area-v2", Origin: store, ParcelWeightGram: 1000, Zones: []delivery.Zone{
		{ID: "inner", ProvinceCode: "P1", Wards: []delivery.Ward{{Code: "W2", Name: "Phường 2"}}, OutboundFee: vnd(30_000), ReturnFee: vnd(30_000), RedeliveryFee: vnd(20_000), Active: true},
	}}
	noDistrict := customer
	noDistrict.DistrictCode = ""
	if z, err := wards.Resolve(noDistrict); err != nil || z.ID != "inner" {
		t.Fatalf("ward zone = %+v, %v", z, err)
	}
	if z, ok := wards.ZoneOfWard("W2"); !ok || z.ID != "inner" {
		t.Fatalf("zone of ward = %+v, %v", z, ok)
	}
	if _, ok := wards.ZoneOfWard("W9"); ok {
		t.Fatal("a ward outside the area has a zone")
	}
	otherWard := noDistrict
	otherWard.WardCode = "W9"
	if _, err := wards.Resolve(otherWard); !errors.Is(err, delivery.ErrOutOfServiceArea) {
		t.Fatalf("ward outside the area err = %v", err)
	}
	if m := customer.ForStaffAfterContact(); m.Line != "" || m.Phone != "***111" || m.RecipientName != customer.RecipientName || m.WardCode != customer.WardCode {
		t.Fatalf("address after the contact period = %+v", m)
	}
	bad := customer
	bad.Phone = "abc"
	if _, err := area.Resolve(bad); !errors.Is(err, delivery.ErrInvalidAddress) {
		t.Fatalf("invalid address err = %v", err)
	}
	if m := customer.Masked(); strings.Contains(m, "0911111111") || strings.Contains(m, "Đường B") {
		t.Fatalf("masked address leaks PII: %s", m)
	}
}

// Policy tests 1 and 2.
func TestCreateShipmentIsIdempotent(t *testing.T) {
	tk := task(t, delivery.Outbound)
	if tk.ClientReference != "task-OUTBOUND" {
		t.Fatalf("client reference = %s", tk.ClientReference)
	}
	if err := tk.RecordCreationFailed("TIMEOUT", ""); !errors.Is(err, delivery.ErrReasonRequired) {
		t.Fatalf("failure without reason err = %v", err)
	}
	if err := tk.RecordCreationFailed("VALIDATION", "invalid ward"); err != nil {
		t.Fatal(err)
	}
	if err := tk.RetryCreation(staff); err != nil || tk.ClientReference != "task-OUTBOUND" {
		t.Fatalf("retry = %v, ref %s", err, tk.ClientReference)
	}
	if created, err := tk.RecordCreated(delivery.ShipmentResult{ProviderShipmentCode: "GHN123"}); err != nil || !created || tk.Status != delivery.StatusCreated {
		t.Fatalf("created = %v %v %s", created, err, tk.Status)
	}
	if created, err := tk.RecordCreated(delivery.ShipmentResult{ProviderShipmentCode: "GHN123"}); err != nil || created {
		t.Fatalf("replayed result = %v %v", created, err)
	}
	if _, err := tk.RecordCreated(delivery.ShipmentResult{ProviderShipmentCode: "GHN999"}); !errors.Is(err, delivery.ErrShipmentMismatch) {
		t.Fatalf("second shipment err = %v", err)
	}
}

// Policy tests 5, 6 and 7.
func TestWebhookRules(t *testing.T) {
	tk := created(t, delivery.Outbound)
	if err := tk.MarkReadyForPickup(staff); err != nil {
		t.Fatal(err)
	}
	wrong := event("e0", delivery.StatusPickedUp, now)
	wrong.ShipmentCode = "OTHER"
	if _, err := tk.ApplyCarrierEvent(wrong); !errors.Is(err, delivery.ErrShipmentMismatch) {
		t.Fatalf("foreign shipment err = %v", err)
	}
	if out, err := tk.ApplyCarrierEvent(event("e1", delivery.StatusInTransit, now.Add(time.Hour))); err != nil || out != delivery.OutcomeApplied {
		t.Fatalf("in transit = %s %v", out, err)
	}
	if out, _ := tk.ApplyCarrierEvent(event("e1", delivery.StatusInTransit, now.Add(time.Hour))); out != delivery.OutcomeDuplicate {
		t.Fatalf("replay = %s", out)
	}
	// A late "picked up" arrives after "in transit": recorded, no regression.
	if out, _ := tk.ApplyCarrierEvent(event("e2", delivery.StatusPickedUp, now.Add(30*time.Minute))); out != delivery.OutcomeStale || tk.Status != delivery.StatusInTransit {
		t.Fatalf("stale = %s status %s", out, tk.Status)
	}
	if out, _ := tk.ApplyCarrierEvent(event("e3", delivery.StatusDelivered, now.Add(2*time.Hour))); out != delivery.OutcomeApplied || tk.DeliveredAt == nil {
		t.Fatalf("delivered = %s", out)
	}
	// Contradiction after a final state goes to manual review.
	if out, _ := tk.ApplyCarrierEvent(event("e4", delivery.StatusLost, now.Add(3*time.Hour))); out != delivery.OutcomeReview || tk.Status != delivery.StatusManualReview {
		t.Fatalf("contradiction = %s status %s", out, tk.Status)
	}
	if len(tk.Events) != 4 {
		t.Fatalf("history has %d events, want 4 (duplicates are not stored twice)", len(tk.Events))
	}
}

// Policy test 9.
func TestFailedDeliveryIsNotDelivered(t *testing.T) {
	tk := created(t, delivery.Outbound)
	e := event("e1", delivery.StatusDeliveryFailed, now.Add(2*time.Hour))
	e.Reason = "khách không nghe máy"
	if _, err := tk.ApplyCarrierEvent(e); err != nil {
		t.Fatal(err)
	}
	if tk.DeliveredAt != nil || tk.Status != delivery.StatusDeliveryFailed || len(tk.Attempts) != 1 || tk.Attempts[0].Fault != delivery.FaultUnknown {
		t.Fatalf("task = %+v", tk)
	}
	if err := tk.ClassifyAttempt(staff, "e1", delivery.FaultCustomer, "gọi 3 lần không nghe"); err != nil {
		t.Fatal(err)
	}
	// Re-attempt succeeds.
	if out, _ := tk.ApplyCarrierEvent(event("e2", delivery.StatusDelivered, now.Add(26*time.Hour))); out != delivery.OutcomeApplied {
		t.Fatalf("re-delivery = %s", out)
	}
}

// Policy tests 10, 11 and 14.
func TestReturnTask(t *testing.T) {
	tk := task(t, delivery.Return)
	if tk.Status != delivery.StatusScheduled || tk.From != customer || tk.To != store {
		t.Fatalf("return task must go customer → store: %+v", tk)
	}
	tk = created(t, delivery.Return)
	if tk.Status != delivery.StatusCourierAssigned {
		t.Fatalf("status = %s", tk.Status)
	}
	_, _ = tk.ApplyCarrierEvent(event("r1", delivery.StatusPickupConfirmed, now.Add(time.Hour)))
	_, _ = tk.ApplyCarrierEvent(event("r2", delivery.StatusDelivered, now.Add(3*time.Hour)))
	if tk.Status == delivery.StatusReceivedAtStore {
		t.Fatal("carrier delivery to the store is not staff receipt")
	}
	if err := tk.ReceiveAtStore(staff, now.Add(3*time.Hour)); err != nil || tk.Status != delivery.StatusReceivedAtStore {
		t.Fatalf("receive = %v %s", err, tk.Status)
	}

	lost := created(t, delivery.Return)
	_, _ = lost.ApplyCarrierEvent(event("r1", delivery.StatusPickupConfirmed, now))
	if out, _ := lost.ApplyCarrierEvent(event("r2", delivery.StatusLost, now.Add(time.Hour))); out != delivery.OutcomeApplied || lost.Status != delivery.StatusLost {
		t.Fatalf("lost = %s", lost.Status)
	}
	if err := lost.ReceiveAtStore(staff, now.Add(2*time.Hour)); !errors.Is(err, delivery.ErrInvalidTransition) {
		t.Fatalf("receiving a lost parcel err = %v", err)
	}
}

// Policy test 13.
func TestCancelNeedsCarrierConfirmation(t *testing.T) {
	tk := created(t, delivery.Outbound)
	if err := tk.RequestCancel(staff, "khách hủy đơn"); err != nil {
		t.Fatal(err)
	}
	if err := tk.CancelRejected("đã bàn giao cho bưu cục"); err != nil || tk.Status != delivery.StatusCreated {
		t.Fatalf("rejected cancel = %v %s", err, tk.Status)
	}
	if err := tk.RequestCancel(staff, "khách hủy đơn"); err != nil {
		t.Fatal(err)
	}
	if out, _ := tk.ApplyCarrierEvent(event("c1", delivery.StatusCancelled, now)); out != delivery.OutcomeApplied || tk.Status != delivery.StatusCancelled {
		t.Fatalf("confirmed cancel = %s", tk.Status)
	}
	picked := created(t, delivery.Outbound)
	_, _ = picked.ApplyCarrierEvent(event("p1", delivery.StatusPickedUp, now))
	if err := picked.RequestCancel(staff, "x"); !errors.Is(err, delivery.ErrInvalidTransition) {
		t.Fatalf("cancel after pickup err = %v", err)
	}
}

// Policy test 12.
func TestFinalFeeIsReconciledWithoutChangingCustomerFee(t *testing.T) {
	tk := created(t, delivery.Outbound) // estimate 25,000; customer pays 30,000
	r, err := tk.RecordFinalFee(vnd(27_000), vnd(5_000))
	if err != nil || !r.WithinTolerance || r.Variance.Amount() != 2_000 || tk.CustomerFee.Amount() != 30_000 {
		t.Fatalf("reconcile = %+v, %v", r, err)
	}
	r, _ = tk.RecordFinalFee(vnd(40_000), vnd(5_000))
	if r.WithinTolerance || tk.CustomerFee.Amount() != 30_000 {
		t.Fatalf("large variance must go to review without touching the customer fee: %+v", r)
	}
}

// Policy tests 8 and 15.
func TestAccessAndManualEvents(t *testing.T) {
	matrix := identity.ReferencePermissionMatrix()
	owner, _ := identity.NewPrincipal("cust-a", []identity.Role{identity.RoleCustomer}, matrix)
	other, _ := identity.NewPrincipal("cust-b", []identity.Role{identity.RoleCustomer}, matrix)
	clerk, _ := identity.NewPrincipal("staff1", []identity.Role{identity.RoleRentalStaff}, matrix)
	tk := created(t, delivery.Outbound)
	if err := delivery.AuthorizeRead(owner, tk); err != nil {
		t.Fatalf("owner read: %v", err)
	}
	if err := delivery.AuthorizeRead(other, tk); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("other customer read err = %v", err)
	}
	if _, err := tk.ManualEvent(owner, delivery.StatusDelivered, "x", "", now); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("customer manual event err = %v", err)
	}
	if _, err := tk.ManualEvent(clerk, delivery.StatusPickedUp, "", "", now); !errors.Is(err, delivery.ErrReasonRequired) {
		t.Fatalf("manual event without reason err = %v", err)
	}
	if out, err := tk.ManualEvent(clerk, delivery.StatusPickedUp, "shipper lấy hàng, app lỗi", "photo/1.jpg", now); err != nil || out != delivery.OutcomeApplied {
		t.Fatalf("manual = %s %v", out, err)
	}
	last := tk.Events[len(tk.Events)-1]
	if last.Source != delivery.SourceAdmin || last.Actor == nil || last.Actor.ID != "staff1" || last.Reason == "" {
		t.Fatalf("audit fields = %+v", last)
	}
}

func gaveUp(key string, s delivery.Status, at time.Time) delivery.CarrierEvent {
	e := event(key, s, at)
	e.Reason, e.GaveUp = "hết số lần giao", true
	return e
}

// Decision D42: the carrier re-attempts on the same shipment; once it stops,
// staff close the shipment with a follow-up.
func TestCarrierStopsAttempting(t *testing.T) {
	matrix := identity.ReferencePermissionMatrix()
	owner, _ := identity.NewPrincipal("cust-a", []identity.Role{identity.RoleCustomer}, matrix)
	clerk, _ := identity.NewPrincipal("staff1", []identity.Role{identity.RoleRentalStaff}, matrix)

	tk := created(t, delivery.Outbound)
	_, _ = tk.ApplyCarrierEvent(event("p1", delivery.StatusPickedUp, now))
	if _, err := tk.ApplyCarrierEvent(gaveUp("x", delivery.StatusInTransit, now)); err == nil {
		t.Fatal("only a failed attempt can end the attempts")
	}
	if out, _ := tk.ApplyCarrierEvent(event("f1", delivery.StatusDeliveryFailed, now.Add(time.Hour))); out != delivery.OutcomeApplied || tk.AwaitingFollowUp() {
		t.Fatalf("a failed attempt the carrier will retry: %s, awaiting %v", out, tk.AwaitingFollowUp())
	}
	if err := tk.CloseUnfulfilled(clerk, delivery.FollowUpNewShipment, "x", now); !errors.Is(err, delivery.ErrInvalidTransition) {
		t.Fatalf("closing while the carrier still tries err = %v", err)
	}
	if out, _ := tk.ApplyCarrierEvent(gaveUp("f2", delivery.StatusDeliveryFailed, now.Add(25*time.Hour))); out != delivery.OutcomeReview ||
		tk.Status != delivery.StatusManualReview || !tk.AwaitingFollowUp() || len(tk.Attempts) != 2 || tk.Attempts[1].TaskID != tk.ID {
		t.Fatalf("carrier gave up: %s, task %+v", out, tk)
	}
	if a, ok := tk.FinalAttempt(); !ok || a.ID != "f2" {
		t.Fatalf("final attempt = %+v", a)
	}
	// Carrier events while staff decide are history only.
	if out, _ := tk.ApplyCarrierEvent(event("f3", delivery.StatusInTransit, now.Add(26*time.Hour))); out != delivery.OutcomeStale || !tk.AwaitingFollowUp() {
		t.Fatalf("event during review = %s", out)
	}

	if err := tk.CloseUnfulfilled(owner, delivery.FollowUpNewShipment, "x", now); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("customer closing err = %v", err)
	}
	if err := tk.CloseUnfulfilled(clerk, delivery.FollowUpNewShipment, " ", now); !errors.Is(err, delivery.ErrReasonRequired) {
		t.Fatalf("closing without a reason err = %v", err)
	}
	if err := tk.CloseUnfulfilled(clerk, delivery.FollowUpNewShipment, "giao lại", now); !errors.Is(err, delivery.ErrAttemptUnclassified) {
		t.Fatalf("closing before classifying err = %v", err)
	}
	if err := tk.ClassifyAttempt(staff, "f2", delivery.FaultCustomer, "khách vắng 3 lần"); err != nil {
		t.Fatal(err)
	}
	back := now.Add(48 * time.Hour)
	if err := tk.CloseUnfulfilled(clerk, delivery.FollowUpNewShipment, "khách hẹn giao lại thứ Bảy", back); err != nil {
		t.Fatal(err)
	}
	if tk.Status != delivery.StatusClosedUnfulfilled || tk.ReceivedAt == nil || !tk.ReceivedAt.Equal(back) || tk.ClosedBy == nil || tk.ClosedBy.ID != "staff1" {
		t.Fatalf("closed outbound = %+v", tk)
	}
	if err := tk.ClassifyAttempt(staff, "f2", delivery.FaultCarrier, "đổi ý"); !errors.Is(err, delivery.ErrInvalidTransition) {
		t.Fatalf("reclassifying after closing err = %v", err)
	}
	if out, _ := tk.ApplyCarrierEvent(event("f4", delivery.StatusLost, now.Add(50*time.Hour))); out != delivery.OutcomeStale || tk.Status != delivery.StatusClosedUnfulfilled {
		t.Fatalf("event after closing = %s, status %s", out, tk.Status)
	}

	w, _ := period.New(now.Add(72*time.Hour), now.Add(74*time.Hour))
	req := delivery.NewTaskRequest{ID: "task-OUTBOUND-2", BookingRef: "bk-1", CustomerID: "cust-a", Direction: delivery.Outbound, ProviderCode: "ghn",
		Store: store, Customer: customer, Window: w, Parcel: tk.Parcel, CustomerFee: vnd(30_000), Now: back}
	if _, err := tk.FollowUpTask(req); err == nil {
		t.Fatal("the new shipment must be free of charge")
	}
	req.CustomerFee = vnd(0)
	other := req
	other.BookingRef = "bk-2"
	if _, err := tk.FollowUpTask(other); !errors.Is(err, delivery.ErrShipmentMismatch) {
		t.Fatalf("follow-up for another booking err = %v", err)
	}
	n, err := tk.FollowUpTask(req)
	if err != nil || n.Replaces != tk.ID || tk.ReplacedBy != n.ID || n.ClientReference == tk.ClientReference || n.Status != delivery.StatusPendingCreation {
		t.Fatalf("follow-up = %+v, %v", n, err)
	}
	if _, err := tk.FollowUpTask(req); !errors.Is(err, delivery.ErrInvalidTransition) {
		t.Fatalf("second follow-up err = %v", err)
	}
}

func TestCarrierStopsCollecting(t *testing.T) {
	clerk, _ := identity.NewPrincipal("staff1", []identity.Role{identity.RoleRentalStaff}, identity.ReferencePermissionMatrix())
	tk := created(t, delivery.Return)
	e := gaveUp("r1", delivery.StatusPickupFailed, now)
	e.Fault = delivery.FaultCarrier
	if out, _ := tk.ApplyCarrierEvent(e); out != delivery.OutcomeReview || !tk.AwaitingFollowUp() {
		t.Fatalf("pickup given up = %s", out)
	}
	if err := tk.CloseUnfulfilled(clerk, delivery.FollowUpEndBooking, "x", now); err == nil {
		t.Fatal("a return shipment cannot end the booking")
	}
	if err := tk.CloseUnfulfilled(clerk, delivery.FollowUpAtStore, "khách tự mang trả", now); err != nil {
		t.Fatal(err)
	}
	if tk.Status != delivery.StatusClosedUnfulfilled || tk.ReceivedAt != nil {
		t.Fatalf("closed return keeps the items with the customer: %+v", tk)
	}
	if _, err := tk.FollowUpTask(delivery.NewTaskRequest{ID: "r2", BookingRef: "bk-1", CustomerID: "cust-a", Direction: delivery.Return}); !errors.Is(err, delivery.ErrInvalidTransition) {
		t.Fatalf("follow-up shipment after AT_STORE err = %v", err)
	}
}

// Decision D43: in manual mode staff record failed attempts seen in the
// carrier's app, with the fault when known.
func TestManualAttemptsAndActions(t *testing.T) {
	matrix := identity.ReferencePermissionMatrix()
	owner, _ := identity.NewPrincipal("cust-a", []identity.Role{identity.RoleCustomer}, matrix)
	clerk, _ := identity.NewPrincipal("staff1", []identity.Role{identity.RoleRentalStaff}, matrix)
	tk := task(t, delivery.Outbound)
	if got := tk.Actions(); len(got) != 1 || got[0] != delivery.DoRecordShipment {
		t.Fatalf("new task actions = %v", got)
	}
	if _, err := tk.ManualAttempt(clerk, delivery.StatusDeliveryFailed, delivery.FaultCustomer, false, "x", "", now); !errors.Is(err, delivery.ErrInvalidTransition) {
		t.Fatalf("attempt before the shipment exists err = %v", err)
	}
	tk = created(t, delivery.Outbound)
	_, _ = tk.ApplyCarrierEvent(event("p1", delivery.StatusPickedUp, now))
	if _, err := tk.ManualAttempt(owner, delivery.StatusDeliveryFailed, delivery.FaultCustomer, false, "x", "", now); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("customer recording an attempt err = %v", err)
	}
	if _, err := tk.ManualAttempt(clerk, delivery.StatusDelivered, delivery.FaultCustomer, false, "x", "", now); err == nil {
		t.Fatal("a delivery is not a failed attempt")
	}
	out, err := tk.ManualAttempt(clerk, delivery.StatusDeliveryFailed, delivery.FaultCustomer, false, "khách vắng", "", now.Add(time.Hour))
	if err != nil || out != delivery.OutcomeApplied || tk.Attempts[0].Fault != delivery.FaultCustomer || tk.Attempts[0].ClassifiedBy == nil {
		t.Fatalf("classified attempt = %s %v %+v", out, err, tk.Attempts)
	}
	out, err = tk.ManualAttempt(clerk, delivery.StatusDeliveryFailed, "", true, "hết số lần", "", now.Add(25*time.Hour))
	if err != nil || out != delivery.OutcomeReview || tk.Attempts[1].Fault != delivery.FaultUnknown || tk.Attempts[1].ClassifiedBy != nil {
		t.Fatalf("given up = %s %v %+v", out, err, tk.Attempts)
	}
	if got := tk.Actions(); slices.Contains(got, delivery.DoClose) || !slices.Contains(got, delivery.DoClassifyAttempt) {
		t.Fatalf("unclassified final attempt actions = %v", got)
	}
	if _, err := tk.ManualAttempt(clerk, delivery.StatusDeliveryFailed, "", false, "x", "", now.Add(26*time.Hour)); !errors.Is(err, delivery.ErrInvalidTransition) {
		t.Fatalf("attempt while awaiting staff err = %v", err)
	}
	_ = tk.ClassifyAttempt(staff, tk.GaveUpAttempt, delivery.FaultCarrier, "shipper báo sai")
	if got := tk.Actions(); !slices.Contains(got, delivery.DoClose) || slices.Contains(got, delivery.DoClassifyAttempt) {
		t.Fatalf("classified final attempt actions = %v", got)
	}
}
