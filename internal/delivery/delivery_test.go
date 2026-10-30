package delivery_test

import (
	"errors"
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
	area := delivery.ServiceArea{ProviderCode: "ghn", Version: "area-v1", Zones: []delivery.Zone{
		{ID: "inner", ProvinceCode: "P1", DistrictCodes: []string{"D1", "D3"}, OutboundFee: vnd(30_000), ReturnFee: vnd(30_000), RedeliveryFee: vnd(20_000), Active: true},
	}}
	if err := area.Validate(); err != nil {
		t.Fatal(err)
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
