package rentalsapp_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"webee_rentor/internal/delivery"
	"webee_rentor/internal/delivery/deliverypg"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/payments"
	"webee_rentor/internal/platform/postgres/pgtest"
	"webee_rentor/internal/rentals"
	"webee_rentor/internal/rentals/rentalsapp"
)

// Fake test address only.
var home = &rentalsapp.AddressInput{RecipientName: "Khách Test", Phone: "0911111111", Line: "2 Đường B", WardCode: "W1"}

// courier places a booking delivered and collected by courier on Tuesday
// 2026-11-10: delivered 07:00–09:00, due 09:00 the next day, collected in the
// 07:00–09:00 window that ends at due.
func (e *env) courier(ward string) (*rentals.Booking, error) {
	in := e.at("2026-11-10", "deliver-am", e.item(e.c.JacketM))
	in.Outbound, in.Return = rentals.LegCourier, rentals.LegCourier
	if ward != "" {
		a := *home
		a.WardCode = ward
		in.Address = &a
	}
	buyers++
	return e.svc.PlaceBooking(context.Background(), e.f.User(fmt.Sprintf("buyer-%d", buyers), "customer"), in)
}

var buyers int

func (e *env) tasks(b rentals.BookingID) (out, ret *delivery.Task) {
	e.t.Helper()
	stored, err := deliverypg.TasksOfBooking(context.Background(), e.f.Pool, string(b), false)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, st := range stored {
		open := st.Task.Status != delivery.StatusCancelled && st.Task.Status != delivery.StatusClosedUnfulfilled
		switch {
		case st.Task.Direction == delivery.Outbound && (out == nil || open):
			out = st.Task
		case st.Task.Direction == delivery.Return && (ret == nil || open):
			ret = st.Task
		}
	}
	return out, ret
}

// dispatch pays, confirms, books the outbound shipment and hands the parcel
// to the courier.
func (e *env) dispatch(b *rentals.Booking) *delivery.Task {
	e.t.Helper()
	ctx := context.Background()
	rest, _ := b.RentTotal.Sub(b.ReservationDeposit)
	rest, _ = rest.Add(b.Fulfillment.DeliveryFees(b.Currency))
	if _, err := e.svc.RecordPayment(ctx, e.staff, b.ID, rentalsapp.PaymentInput{IdempotencyKey: "in-full", Method: payments.MethodCash,
		Portions: []payments.Portion{{Bucket: payments.BucketReservation, Amount: b.ReservationDeposit},
			{Bucket: payments.BucketRental, Amount: rest}, {Bucket: payments.BucketSecurity, Amount: b.SecurityDepositTotal}}}); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.svc.Confirm(ctx, e.staff, b.ID); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.svc.StartPreparation(ctx, e.staff, b.ID); err != nil {
		e.t.Fatal(err)
	}
	out, _ := e.tasks(b.ID)
	if _, err := e.svc.RecordShipment(ctx, e.staff, out.ID, rentalsapp.ShipmentInput{Code: "GHN-" + string(b.ID)[:8]}); err != nil {
		e.t.Fatal(err)
	}
	e.clock.Set(b.Schedule.PickupAt.Add(-3 * time.Hour))
	var delivered []rentals.DeliveredItem
	for _, a := range e.active(b.ID) {
		delivered = append(delivered, rentals.DeliveredItem{AssetID: a.AssetID})
	}
	if _, err := e.svc.RecordHandover(ctx, e.staff, b.ID, rentalsapp.HandoverInput{Delivered: delivered, ChecklistCompleted: true}); err != nil {
		e.t.Fatalf("handover: %v", err)
	}
	out, _ = e.tasks(b.ID)
	return out
}

// Decision D43: courier legs are offered from the loaded area; each leg gets
// its shipment with the booking, and the address must be in the area.
func TestCourierBookingCreatesItsShipments(t *testing.T) {
	e := setup(t)
	e.f.Assets(e.c.JacketM, 1)
	if _, err := e.courier("W9"); !errors.Is(err, delivery.ErrOutOfServiceArea) {
		t.Fatalf("ward outside the area err = %v", err)
	}
	if _, err := e.courier(""); err == nil {
		t.Fatal("a courier booking without an address was placed")
	}
	b, err := e.courier("W1")
	if err != nil {
		t.Fatal(err)
	}
	out, ret := e.tasks(b.ID)
	if out == nil || out.Status != delivery.StatusPendingCreation || out.To.WardCode != "W1" || out.To.ProvinceCode != "P1" || out.CustomerFee.Amount() != 30_000 ||
		ret == nil || ret.Status != delivery.StatusScheduled || ret.From.WardCode != "W1" || !ret.Window.End().Equal(b.Schedule.DueAt) {
		t.Fatalf("tasks = %+v / %+v", out, ret)
	}
	if b.Fulfillment.OutboundFee.Amount() != 30_000 || b.Fulfillment.ReturnFee.Amount() != 30_000 || b.Fulfillment.RedeliveryFee.Amount() != 20_000 {
		t.Fatalf("fees = %+v", b.Fulfillment)
	}
	ctx := context.Background()
	if _, err := e.svc.RecordShipment(ctx, e.staff, out.ID, rentalsapp.ShipmentInput{Code: "GHN1"}); !errors.Is(err, rentals.ErrInvalidTransition) {
		t.Fatalf("booking a shipment for an unconfirmed booking err = %v", err)
	}
	if _, err := e.svc.Cancel(ctx, e.staff, b.ID, rentalsapp.CancelInput{Reason: rentals.CancelStaffDecision, Note: "khách đổi ý"}); err != nil {
		t.Fatal(err)
	}
	if out, ret = e.tasks(b.ID); out.Status != delivery.StatusCancelled || ret.Status != delivery.StatusCancelled {
		t.Fatalf("cancelled booking kept its shipments: %s / %s", out.Status, ret.Status)
	}
}

func TestCourierRentalFromDispatchToReturn(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	b, err := e.courier("W1")
	if err != nil {
		t.Fatal(err)
	}
	e.payInFull(b) // rent and deposit only: the delivery fees are still owed
	if _, err := e.svc.Confirm(ctx, e.staff, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StartPreparation(ctx, e.staff, b.ID); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(b.Schedule.PickupAt.Add(-3 * time.Hour))
	delivered := []rentals.DeliveredItem{{AssetID: e.active(b.ID)[0].AssetID}}
	if _, err := e.svc.RecordHandover(ctx, e.staff, b.ID, rentalsapp.HandoverInput{Delivered: delivered, ChecklistCompleted: true}); !errors.Is(err, rentalsapp.ErrShipmentRequired) {
		t.Fatalf("handover before the shipment is booked err = %v", err)
	}

	e.f.Assets(e.c.JacketM, 1)
	b, err = e.courier("W1")
	if err != nil {
		t.Fatal(err)
	}
	out := e.dispatch(b)
	if out.Status != delivery.StatusPickedUp || out.Events[len(out.Events)-1].Actor == nil {
		t.Fatalf("handover did not dispatch the parcel: %+v", out)
	}
	// Delivered 3 hours late because of the carrier: the due time and the
	// allocation move by 3 hours.
	blockEnd := e.active(b.ID)[0].Block.End()
	late := b.Schedule.PickupAt.Add(3 * time.Hour)
	e.clock.Set(late.Add(time.Minute))
	if _, err := e.svc.RecordDeliveryEvent(ctx, e.staff, out.ID, rentalsapp.DeliveryEventInput{Status: delivery.StatusDelivered, At: &late}); err != nil {
		t.Fatal(err)
	}
	if got := e.active(b.ID)[0].Block.End(); !got.Equal(blockEnd.Add(3 * time.Hour)) {
		t.Fatalf("block end %s, want %s + 3h", got, blockEnd)
	}

	_, ret := e.tasks(b.ID)
	if _, err := e.svc.RecordShipment(ctx, e.staff, ret.ID, rentalsapp.ShipmentInput{Code: "GHN-R"}); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(b.Schedule.DueAt.Add(time.Hour))
	if _, err := e.svc.RecordDeliveryEvent(ctx, e.staff, ret.ID, rentalsapp.DeliveryEventInput{Status: delivery.StatusPickupConfirmed}); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(b.Schedule.DueAt.Add(4 * time.Hour))
	if _, err := e.svc.ReceiveReturnParcel(ctx, e.staff, ret.ID); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.Booking(ctx, e.staff, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	asset := got.Items[0].AssetID
	if got.Status != rentals.StatusReturnInspection || e.assetStatus(asset) != inventory.AssetNeedsInspection {
		t.Fatalf("after the parcel is back: %s, asset %s", got.Status, e.assetStatus(asset))
	}
	// Handed to the courier 1h after due, inside the 3h credited: no late fee.
	for _, f := range e.fees(b.ID) {
		if f.Kind == rentals.FeeLateReturn && f.Amount.IsPositive() {
			t.Fatalf("late fee charged for a carrier delay: %+v", f)
		}
	}
}

// failedFinally records a failed delivery the carrier gave up on.
func (e *env) failedFinally(out *delivery.Task, fault delivery.Fault) *delivery.Task {
	e.t.Helper()
	t, err := e.svc.RecordDeliveryEvent(context.Background(), e.staff, out.ID, rentalsapp.DeliveryEventInput{
		Status: delivery.StatusDeliveryFailed, Fault: fault, GaveUp: true, Reason: "hết số lần giao"})
	if err != nil {
		e.t.Fatal(err)
	}
	return t
}

// Decision D42: the rental ends undelivered; a customer-caused failure keeps
// the no-show share of the rent and the outbound fee.
func TestCarrierGivesUpAndTheRentalEnds(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	b, err := e.courier("W1")
	if err != nil {
		t.Fatal(err)
	}
	out := e.dispatch(b)
	e.clock.Set(b.Schedule.PickupAt.Add(2 * time.Hour))
	if out = e.failedFinally(out, delivery.FaultUnknown); !out.AwaitingFollowUp() {
		t.Fatalf("task = %s", out.Status)
	}
	end := rentalsapp.CloseInput{FollowUp: delivery.FollowUpEndBooking, Reason: "kiện đã về kho"}
	if _, err := e.svc.CloseShipment(ctx, e.staff, out.ID, end); !errors.Is(err, delivery.ErrAttemptUnclassified) {
		t.Fatalf("closing before classifying err = %v", err)
	}
	if _, err := e.svc.ClassifyDeliveryAttempt(ctx, e.staff, out.ID, out.GaveUpAttempt, delivery.FaultCustomer, "khách tắt máy"); err != nil {
		t.Fatal(err)
	}
	redelivery := rentalsapp.FeeInput{Kind: rentals.FeeRedelivery, Task: out.ID, Attempt: out.GaveUpAttempt}
	fee, err := e.svc.ProposeFee(ctx, e.staff, b.ID, redelivery)
	if err != nil || fee.Amount.Amount() != 20_000 || fee.Ref != string(out.ID) {
		t.Fatalf("redelivery fee = %+v, %v", fee, err)
	}
	if _, err := e.svc.ProposeFee(ctx, e.staff, b.ID, redelivery); !errors.Is(err, rentals.ErrDuplicateFee) {
		t.Fatalf("second redelivery fee for one shipment err = %v", err)
	}
	if _, err := e.svc.ProposeFee(ctx, e.staff, b.ID, rentalsapp.FeeInput{Kind: rentals.FeeRedelivery, Task: out.ID, Attempt: "no-such"}); err == nil {
		t.Fatal("a fee for an unknown attempt was proposed")
	}
	if _, err := e.svc.CloseShipment(ctx, e.staff, out.ID, end); err != nil {
		t.Fatal(err)
	}
	got, _ := e.svc.Booking(ctx, e.staff, b.ID)
	asset := got.Items[0].AssetID
	if got.Status != rentals.StatusReturnInspection || got.Fulfillment.Undelivered == nil || got.RentTotal.Amount() != b.RentTotal.Amount()*70/100 ||
		e.assetStatus(asset) != inventory.AssetNeedsInspection {
		t.Fatalf("booking = %s rent %s, asset %s", got.Status, got.RentTotal, e.assetStatus(asset))
	}
	l := e.ledger(b.ID)
	var rent, outbound, ret int64
	for _, c := range l.Charges {
		if c.Reversal != nil {
			continue
		}
		switch {
		case c.Kind == payments.ChargeRent:
			rent += c.Amount.Amount()
		case c.Kind == payments.ChargeDeliveryFee && c.DeliveryLeg == payments.LegOutbound:
			outbound += c.Amount.Amount()
		case c.Kind == payments.ChargeDeliveryFee && c.DeliveryLeg == payments.LegReturn:
			ret += c.Amount.Amount()
		}
	}
	if rent != got.RentTotal.Amount() || outbound != 30_000 || ret != 0 {
		t.Fatalf("ledger rent %d outbound %d return %d", rent, outbound, ret)
	}
	if _, r := e.tasks(b.ID); r.Status != delivery.StatusCancelled {
		t.Fatalf("return shipment = %s", r.Status)
	}
	if _, err := e.svc.CloseShipment(ctx, e.staff, out.ID, end); !errors.Is(err, delivery.ErrInvalidTransition) {
		t.Fatalf("closing twice err = %v", err)
	}
}

// Decision D42: a free new shipment for the same leg, created once, even when
// two people decide at the same moment.
func TestCarrierGivesUpAndANewShipmentGoesOut(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.f.Assets(e.c.JacketM, 1)
	b, err := e.courier("W1")
	if err != nil {
		t.Fatal(err)
	}
	out := e.dispatch(b)
	e.clock.Set(b.Schedule.PickupAt.Add(2 * time.Hour))
	out = e.failedFinally(out, delivery.FaultCarrier)
	again := rentalsapp.CloseInput{FollowUp: delivery.FollowUpNewShipment, Reason: "giao lại chiều mai", Date: pgtest.Date(t, "2026-11-11"), WindowID: "deliver-pm"}
	other := e.f.User("staff2", "rental_staff")
	errs := e.all(
		func() error { _, err := e.svc.CloseShipment(ctx, e.staff, out.ID, again); return err },
		func() error { _, err := e.svc.CloseShipment(ctx, other, out.ID, again); return err },
	)
	if succeeded(errs) != 1 {
		t.Fatalf("both closed the shipment: %v", errs)
	}
	n, _ := e.tasks(b.ID)
	closed, _, err := deliverypg.LoadTask(ctx, e.f.Pool, out.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if n.ID == out.ID || n.Replaces != out.ID || closed.ReplacedBy != n.ID || !n.CustomerFee.IsZero() || n.Status != delivery.StatusPendingCreation ||
		n.Window.End().Hour() != 8 { // 15:00 local
		t.Fatalf("follow-up = %+v, closed = %+v", n, closed)
	}
	if e.count(`SELECT count(*) FROM delivery_tasks WHERE booking_id = $1 AND direction = 'OUTBOUND'`, string(b.ID)) != 2 {
		t.Fatal("more than one follow-up shipment")
	}
}
