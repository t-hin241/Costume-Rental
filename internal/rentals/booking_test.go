package rentals_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/rentals"
)

func TestPlaceBundleBookingSnapshotsAndAllocatesEveryComponent(t *testing.T) {
	p := mustPlace(t)
	b := p.Booking
	if b.Status != rentals.StatusPendingConfirmation || !b.HoldExpiresAt.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("booking = %s hold %s", b.Status, b.HoldExpiresAt)
	}
	if b.RentTotal.Amount() != 200_000 || b.SecurityDepositTotal.Amount() != 550_000 {
		t.Fatalf("totals rent=%d deposit=%d", b.RentTotal.Amount(), b.SecurityDepositTotal.Amount())
	}
	if b.Policy.Version != "test-v1" || b.Package.Duration != 3*day || b.Lines[0].OfferingVersion != 1 {
		t.Fatalf("snapshot missing: %+v %+v", b.Policy.Version, b.Package)
	}
	got := map[string]inventory.PlannedAllocation{}
	for _, a := range p.Allocations {
		got[a.ComponentKey] = a
	}
	if got["jacket"].AssetID != "JK-M-1" || got["pants"].AssetID != "PT-L-1" || got["belt"].AssetID != "BL-1" || len(got) != 3 {
		t.Fatalf("allocations = %+v", p.Allocations)
	}
	// The belt uses the accessory buffer: 1h before, 4h after.
	if !got["belt"].Block.Start().Equal(b.Schedule.PickupAt.Add(-time.Hour)) || !got["belt"].Block.End().Equal(b.Schedule.DueAt.Add(4*time.Hour)) {
		t.Fatalf("belt block = %s", got["belt"].Block)
	}
	if !got["jacket"].Block.End().Equal(b.Schedule.DueAt.Add(day)) {
		t.Fatalf("jacket block = %s", got["jacket"].Block)
	}
}

func TestPlaceIsAllOrNothing(t *testing.T) {
	noBelt := slices.DeleteFunc(readyAssets(), func(a inventory.Asset) bool { return a.VariantID == "belt-std" })
	p, err := place(t, func(r *rentals.PlaceRequest) { r.Inventory.Assets = noBelt })
	if !errors.Is(err, inventory.ErrInsufficientInventory) {
		t.Fatalf("err = %v", err)
	}
	if p.Booking != nil || len(p.Allocations) != 0 {
		t.Fatal("nothing may be returned when any component is short")
	}
}

func TestConcurrentLastItemOnlyOneWins(t *testing.T) {
	// Planning is deterministic over committed state: once the first booking's
	// allocations are committed, the second request for the last jacket M
	// fails. The cross-transaction guarantee is the DB exclusion constraint.
	first := mustPlace(t)
	committed := materialize(t, first.Allocations, "a")
	items, _ := catalogFixture()
	q, err := catalog.QuoteItem(items["jacket"], "jacket-m", pkg3, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = place(t, func(r *rentals.PlaceRequest) {
		r.BookingID, r.Code, r.CustomerID = "bk-2", "WB-0002", "cust-b"
		r.Lines = []rentals.LineRequest{{ID: "l1", Quote: q}}
		r.Inventory.Allocations = committed
	})
	if !errors.Is(err, inventory.ErrInsufficientInventory) {
		t.Fatalf("second booking for the last jacket err = %v", err)
	}
}

func TestPlaceRejectsInvalidRequests(t *testing.T) {
	items, _ := catalogFixture()
	jacket1d, _ := catalog.QuoteItem(items["jacket"], "jacket-l", pkg1, 1)
	jacket3d, _ := catalog.QuoteItem(items["jacket"], "jacket-l", pkg3, 6)
	cases := []struct {
		name string
		opt  placeOpt
		want error
	}{
		{"duration differs from package", func(r *rentals.PlaceRequest) { r.Schedule = schedule(t, "2026-11-10", "morning", 2*day) }, rentals.ErrDurationMismatch},
		{"mixed packages", func(r *rentals.PlaceRequest) {
			r.Lines = append(r.Lines, rentals.LineRequest{ID: "l2", Quote: jacket1d})
		}, rentals.ErrMixedPackages},
		{"pickup inside lead time", func(r *rentals.PlaceRequest) { r.Now = r.Schedule.PickupAt.Add(-time.Hour) }, rentals.ErrPickupTooSoon},
		{"pickup beyond horizon", func(r *rentals.PlaceRequest) { r.Now = r.Schedule.PickupAt.Add(-91 * day) }, rentals.ErrPickupTooFarAhead},
		{"schedule not on store slots", func(r *rentals.PlaceRequest) {
			r.Schedule = rentals.Schedule{PickupAt: r.Schedule.PickupAt.Add(time.Hour), DueAt: r.Schedule.DueAt.Add(time.Hour)}
		}, rentals.ErrSlotNotFound},
	}
	for _, tc := range cases {
		if _, err := place(t, tc.opt); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if _, err := place(t, func(r *rentals.PlaceRequest) { r.Lines = []rentals.LineRequest{{ID: "l1", Quote: jacket3d}} }); err == nil {
		t.Error("quantity above policy max must be rejected")
	}
	if _, err := place(t, func(r *rentals.PlaceRequest) { r.Calendar = nil }); err == nil {
		t.Error("missing calendar must be rejected")
	}
}

func TestSnapshotIsIndependentOfLaterCatalogChanges(t *testing.T) {
	q := bundleQuote(t, pkg3)
	p := mustPlace(t, func(r *rentals.PlaceRequest) { r.Lines = []rentals.LineRequest{{ID: "l1", Quote: q}} })
	// Mutating the quote/catalog afterwards (price change, recomposition) must
	// not reach the booking.
	q.Components[0].VariantID = "jacket-l"
	q.RentAmount = vnd(999_000)
	_, bundle := catalogFixture()
	bundle.Prices["p3d"] = vnd(250_000)
	bundle.Components = bundle.Components[:1]
	line := p.Booking.Lines[0]
	if line.Components[0].VariantID != "jacket-m" || line.RentAmount.Amount() != 200_000 || len(line.Components) != 3 {
		t.Fatalf("snapshot changed: %+v", line)
	}
}

func TestLifecycleHappyPath(t *testing.T) {
	p := mustPlace(t)
	b := p.Booking
	if _, err := b.Confirm(system, funds(60_000, 0, 60_000), now.Add(5*time.Minute)); err != nil {
		t.Fatalf("auto confirm with reservation: %v", err)
	}
	if _, err := b.StartPreparation(staff, b.Schedule.PickupAt.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	assets := allocatedAssets(p)
	if _, err := b.RecordHandover(rentals.HandoverRequest{
		Actor: staff, Allocated: allocated(p), Delivered: delivered("BL-1", "PT-L-1", "JK-M-1"),
		ChecklistCompleted: true, Funds: funds(200_000, 550_000, 60_000), Now: b.Schedule.PickupAt,
	}); err != nil {
		t.Fatal(err)
	}
	if len(b.Items) != 3 || b.Items[0].DailyRate.Amount() != 50_000 {
		t.Fatalf("items = %+v", b.Items)
	}
	if b.IsOverdue(b.Schedule.DueAt.Add(29 * time.Minute)) {
		t.Fatal("within grace the booking is not overdue")
	}
	if _, err := b.RecordReturn(rentals.ReturnRequest{Actor: staff, Assets: assets, Now: b.Schedule.DueAt.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if b.Status != rentals.StatusReturnInspection {
		t.Fatalf("status = %s", b.Status)
	}
	if _, err := b.Complete(staff, rentals.CompletionCheck{LedgerSettled: true}, b.Schedule.DueAt); !errors.Is(err, rentals.ErrInspectionIncomplete) {
		t.Fatalf("complete before inspection err = %v", err)
	}
	for _, a := range assets {
		if _, err := b.RecordInspection(staff, a, rentals.InspectionFinding{Conditions: []rentals.Condition{rentals.CondRoutineSoiling}}, b.Schedule.DueAt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Complete(staff, rentals.CompletionCheck{LedgerSettled: false}, b.Schedule.DueAt); !errors.Is(err, rentals.ErrNotSettled) {
		t.Fatalf("complete before settlement err = %v", err)
	}
	if _, err := b.Complete(staff, rentals.CompletionCheck{LedgerSettled: true, OpenFeeAssessments: 1}, b.Schedule.DueAt); !errors.Is(err, rentals.ErrOpenFeeAssessments) {
		t.Fatalf("complete with an open fee err = %v", err)
	}
	tr, err := b.Complete(staff, rentals.CompletionCheck{LedgerSettled: true}, b.Schedule.DueAt)
	if err != nil || b.Status != rentals.StatusCompleted || tr.From != rentals.StatusReturnInspection {
		t.Fatalf("complete = %+v, %v", tr, err)
	}
	if b.ConfirmedAt == nil || b.HandedOverAt == nil || b.ReturnedAt == nil || b.CompletedAt == nil {
		t.Fatal("lifecycle timestamps must be recorded")
	}
}

func TestConfirmRules(t *testing.T) {
	b := mustPlace(t).Booking
	if _, err := b.Confirm(system, funds(59_999, 0, 59_999), now); !errors.Is(err, rentals.ErrInsufficientFunds) {
		t.Fatalf("insufficient funds err = %v", err)
	}
	if _, err := b.Confirm(system, funds(200_000, 0, 60_000), now.Add(30*time.Minute)); !errors.Is(err, rentals.ErrHoldExpired) {
		t.Fatalf("expired hold err = %v", err)
	}

	staffOnly := testPolicy()
	staffOnly.Confirmation = rentals.ConfirmationRule{Mode: rentals.ConfirmByStaff, Funds: rentals.FundsRequirement{Kind: rentals.FundsNone}}
	b = mustPlace(t, func(r *rentals.PlaceRequest) { r.Policy = staffOnly }).Booking
	if _, err := b.Confirm(system, funds(0, 0, 0), now); !errors.Is(err, rentals.ErrStaffConfirmationNeeded) {
		t.Fatalf("system confirm in staff mode err = %v", err)
	}
	if _, err := b.Confirm(staff, funds(0, 0, 0), now); err != nil {
		t.Fatalf("staff confirm: %v", err)
	}
	if _, err := b.Confirm(staff, funds(0, 0, 0), now); !errors.Is(err, rentals.ErrInvalidTransition) {
		t.Fatalf("double confirm err = %v", err)
	}
}

func TestExpireIsIdempotentAndRaceSafe(t *testing.T) {
	b := mustPlace(t).Booking
	if _, _, err := b.Expire(system, now.Add(29*time.Minute)); !errors.Is(err, rentals.ErrHoldNotExpired) {
		t.Fatalf("early expire err = %v", err)
	}
	tr, changed, err := b.Expire(system, now.Add(30*time.Minute))
	if err != nil || !changed || !tr.ReleaseAllocations || b.Status != rentals.StatusExpired {
		t.Fatalf("expire = %+v %v %v", tr, changed, err)
	}
	if _, changed, err := b.Expire(system, now.Add(time.Hour)); err != nil || changed {
		t.Fatalf("second expire = %v, %v; want no-op", changed, err)
	}
	// A late payment success cannot confirm an expired booking; it goes to
	// reconciliation instead.
	if _, err := b.Confirm(system, funds(200_000, 550_000, 60_000), now.Add(time.Hour)); !errors.Is(err, rentals.ErrInvalidTransition) {
		t.Fatalf("confirm expired err = %v", err)
	}

	confirmed := mustPlace(t).Booking
	if _, err := confirmed.Confirm(system, funds(60_000, 0, 60_000), now); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := confirmed.Expire(system, now.Add(time.Hour)); err != nil || changed || confirmed.Status != rentals.StatusConfirmed {
		t.Fatalf("expiring a confirmed booking must be a no-op: %v %v", changed, err)
	}
}

func TestCancelRules(t *testing.T) {
	own := identity.UserActor(customer)
	b := mustPlace(t).Booking
	pickup := b.Schedule.PickupAt
	noShowAt := pickup.Add(2 * time.Hour)
	if _, err := b.Cancel(rentals.CancelRequest{Actor: own, Scope: identity.ScopeOwn, Reason: rentals.CancelCustomerRequest, Now: noShowAt}); !errors.Is(err, rentals.ErrCancelWindowClosed) {
		t.Fatalf("customer cancel at the no-show deadline err = %v", err)
	}
	if _, err := b.Cancel(rentals.CancelRequest{Actor: staff, Scope: identity.ScopeAny, Reason: rentals.CancelCustomerRequest, Note: "khách gọi", Now: noShowAt}); !errors.Is(err, rentals.ErrCancelWindowClosed) {
		t.Fatalf("staff customer-request cancel after the deadline err = %v", err)
	}
	if _, err := b.Cancel(rentals.CancelRequest{Actor: system, Reason: rentals.CancelStaffDecision, Now: now}); !errors.Is(err, rentals.ErrInvalidTransition) {
		t.Fatalf("system cancel for non-payment reason err = %v", err)
	}
	if _, err := b.Cancel(rentals.CancelRequest{Actor: staff, Scope: identity.ScopeAny, Reason: rentals.CancelNoShow, Note: "khách không đến", Now: noShowAt.Add(-time.Minute)}); !errors.Is(err, rentals.ErrNoShowTooEarly) {
		t.Fatalf("no-show before the deadline err = %v", err)
	}
	if _, err := b.Cancel(rentals.CancelRequest{Actor: staff, Scope: identity.ScopeAny, Reason: rentals.CancelStaffDecision, Now: now}); !errors.Is(err, rentals.ErrReasonRequired) {
		t.Fatalf("staff cancel without note err = %v", err)
	}
	// Customers may cancel themselves right up to the no-show deadline.
	tr, err := b.Cancel(rentals.CancelRequest{Actor: own, Scope: identity.ScopeOwn, Reason: rentals.CancelCustomerRequest, Now: noShowAt.Add(-time.Minute)})
	if err != nil || !tr.ReleaseAllocations || b.Status != rentals.StatusCancelled {
		t.Fatalf("customer cancel = %+v, %v", tr, err)
	}
	if _, err := b.Confirm(system, funds(200_000, 550_000, 60_000), now); !errors.Is(err, rentals.ErrInvalidTransition) {
		t.Fatalf("payment success must not confirm a cancelled booking: %v", err)
	}

	preparing := mustPlace(t).Booking
	_, _ = preparing.Confirm(system, funds(60_000, 0, 60_000), now)
	_, _ = preparing.StartPreparation(staff, now)
	if _, err := preparing.Cancel(rentals.CancelRequest{Actor: own, Scope: identity.ScopeOwn, Reason: rentals.CancelCustomerRequest, Now: now}); err != nil {
		t.Fatalf("customer cancel while preparing: %v", err)
	}

	noShow := mustPlace(t).Booking
	if _, err := noShow.Cancel(rentals.CancelRequest{Actor: staff, Scope: identity.ScopeAny, Reason: rentals.CancelNoShow, Note: "không đến", Now: noShow.Schedule.PickupAt.Add(3 * time.Hour)}); err != nil {
		t.Fatalf("staff no-show: %v", err)
	}
}

func handedOver(t *testing.T) (*rentals.Booking, []inventory.AssetID) {
	t.Helper()
	p := mustPlace(t)
	b := p.Booking
	if _, err := b.Confirm(system, funds(60_000, 0, 60_000), now); err != nil {
		t.Fatal(err)
	}
	if _, err := b.StartPreparation(staff, now); err != nil {
		t.Fatal(err)
	}
	assets := allocatedAssets(p)
	if _, err := b.RecordHandover(rentals.HandoverRequest{Actor: staff, Allocated: allocated(p), Delivered: delivered(assets...),
		ChecklistCompleted: true, Funds: funds(200_000, 550_000, 60_000), Now: b.Schedule.PickupAt}); err != nil {
		t.Fatal(err)
	}
	return b, assets
}

func TestHandoverChecks(t *testing.T) {
	p := mustPlace(t)
	b := p.Booking
	_, _ = b.Confirm(system, funds(60_000, 0, 60_000), now)
	_, _ = b.StartPreparation(staff, now)
	assets := allocatedAssets(p)
	base := rentals.HandoverRequest{Actor: staff, Allocated: allocated(p), Delivered: delivered(assets...),
		ChecklistCompleted: true, Funds: funds(200_000, 550_000, 60_000), Now: b.Schedule.PickupAt}

	wrong := base
	wrong.Delivered = delivered("JK-L-1", "PT-L-1", "BL-1")
	if _, err := b.RecordHandover(wrong); !errors.Is(err, rentals.ErrAssetMismatch) {
		t.Errorf("substituted asset err = %v", err)
	}
	dup := base
	dup.Delivered = delivered("JK-M-1", "JK-M-1", "PT-L-1")
	if _, err := b.RecordHandover(dup); !errors.Is(err, rentals.ErrAssetMismatch) {
		t.Errorf("duplicate asset err = %v", err)
	}
	unpaid := base
	unpaid.Funds = funds(200_000, 549_999, 60_000)
	if _, err := b.RecordHandover(unpaid); !errors.Is(err, rentals.ErrInsufficientFunds) {
		t.Errorf("deposit short err = %v", err)
	}
	noCheck := base
	noCheck.ChecklistCompleted = false
	if _, err := b.RecordHandover(noCheck); !errors.Is(err, rentals.ErrChecklistIncomplete) {
		t.Errorf("checklist err = %v", err)
	}
	late := base
	late.Now = b.Schedule.DueAt
	if _, err := b.RecordHandover(late); !errors.Is(err, rentals.ErrHandoverTooLate) {
		t.Errorf("handover after due err = %v", err)
	}
	if b.Status != rentals.StatusPreparing || len(b.Items) != 0 {
		t.Fatalf("failed handovers must not change the booking, got %s", b.Status)
	}
}

func TestPartialReturnsAndMissingItems(t *testing.T) {
	b, assets := handedOver(t)
	due := b.Schedule.DueAt
	if !b.IsOverdue(due.Add(30 * time.Minute)) {
		t.Fatal("past due + grace with items out must be overdue")
	}
	// Jacket and pants come back first; the belt is missing.
	if _, err := b.RecordReturn(rentals.ReturnRequest{Actor: staff, Assets: assets[:2], Now: due.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if b.Status != rentals.StatusRented {
		t.Fatalf("booking must stay RENTED while an item is out, got %s", b.Status)
	}
	if _, err := b.RecordReturn(rentals.ReturnRequest{Actor: staff, Assets: assets[:1], Now: due.Add(2 * time.Hour)}); !errors.Is(err, rentals.ErrAssetMismatch) {
		t.Fatalf("returning twice err = %v", err)
	}
	// Returned items can be inspected while others are still out.
	if _, err := b.RecordInspection(staff, assets[0], rentals.InspectionFinding{}, due.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.RecordInspection(staff, assets[2], rentals.InspectionFinding{}, due.Add(time.Hour)); !errors.Is(err, rentals.ErrAssetMismatch) {
		t.Fatalf("inspecting an item still out err = %v", err)
	}

	reported := due.Add(2 * time.Hour)
	if _, err := b.ReportMissing(staff, assets[2], reported); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ConfirmLost(staff, assets[2], "không tìm thấy", reported.Add(47*time.Hour)); !errors.Is(err, rentals.ErrVerificationWindowOpen) {
		t.Fatalf("confirming loss inside 48h err = %v", err)
	}
	if _, err := b.ConfirmLost(staff, assets[2], "không tìm thấy sau 48 giờ", reported.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if b.Status != rentals.StatusReturnInspection {
		t.Fatalf("all items resolved → RETURN_INSPECTION, got %s", b.Status)
	}
	if b.InspectionCompletedAt != nil {
		t.Fatal("pants are not inspected yet")
	}
	if _, err := b.RecordInspection(staff, assets[1], rentals.InspectionFinding{Conditions: []rentals.Condition{rentals.CondNeedsVerification}}, reported); err != nil {
		t.Fatal(err)
	}
	if b.InspectionCompletedAt != nil {
		t.Fatal("NEEDS_VERIFICATION keeps inspection open")
	}
	if _, err := b.RecordInspection(staff, assets[1], rentals.InspectionFinding{Conditions: []rentals.Condition{rentals.CondNormalWear}}, reported); err != nil {
		t.Fatal(err)
	}
	if b.InspectionCompletedAt == nil {
		t.Fatal("inspection completes when returned items are inspected and the rest confirmed lost")
	}
}

func TestMissingItemFoundBeforeLossConfirmation(t *testing.T) {
	b, assets := handedOver(t)
	due := b.Schedule.DueAt
	if _, err := b.RecordReturn(rentals.ReturnRequest{Actor: staff, Assets: assets[:2], Now: due}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ReportMissing(staff, assets[2], due); err != nil {
		t.Fatal(err)
	}
	if _, err := b.RecordReturn(rentals.ReturnRequest{Actor: staff, Assets: assets[2:], Now: due.Add(20 * time.Hour)}); err != nil {
		t.Fatalf("a missing item that turns up is returned normally: %v", err)
	}
	if _, err := b.ConfirmLost(staff, assets[2], "x", due.Add(72*time.Hour)); !errors.Is(err, rentals.ErrInvalidTransition) {
		t.Fatalf("confirming loss of a returned item err = %v", err)
	}
	if b.Status != rentals.StatusReturnInspection {
		t.Fatalf("status = %s", b.Status)
	}
}

func TestInspectionEvidenceRules(t *testing.T) {
	b, assets := handedOver(t)
	_, _ = b.RecordReturn(rentals.ReturnRequest{Actor: staff, Assets: assets, Now: b.Schedule.DueAt})
	if _, err := b.RecordInspection(staff, assets[0], rentals.InspectionFinding{Conditions: []rentals.Condition{rentals.CondMinorRepairable}}, now); err == nil {
		t.Fatal("chargeable damage without evidence must be rejected")
	}
	if _, err := b.RecordInspection(staff, assets[0], rentals.InspectionFinding{Conditions: []rentals.Condition{rentals.CondNeedsVerification, rentals.CondNormalWear}}, now); err == nil {
		t.Fatal("NEEDS_VERIFICATION combined with other conditions must be rejected")
	}
}

func TestRescheduleKeepsOldReservationOnFailure(t *testing.T) {
	first := mustPlace(t)
	b := first.Booking
	own := materialize(t, first.Allocations, "own")

	// Another booking holds jacket M on 2026-11-17..18.
	items, _ := catalogFixture()
	q, _ := catalog.QuoteItem(items["jacket"], "jacket-m", pkg1, 1)
	other := mustPlace(t, func(r *rentals.PlaceRequest) {
		r.BookingID, r.Code, r.CustomerID = "bk-2", "WB-0002", "cust-b"
		r.Schedule = schedule(t, "2026-11-17", "morning", day)
		r.Lines = []rentals.LineRequest{{ID: "l1", Quote: q}}
	})
	allocs := append(slices.Clone(own), materialize(t, other.Allocations, "other")...)
	old := b.Schedule

	_, err := b.Reschedule(rentals.RescheduleRequest{
		Actor: identity.UserActor(customer), Scope: identity.ScopeOwn, Calendar: calendar(t),
		Schedule:  schedule(t, "2026-11-16", "morning", 3*day),
		Inventory: rentals.InventoryView{Assets: readyAssets(), Allocations: allocs}, Now: now,
	})
	if !errors.Is(err, inventory.ErrInsufficientInventory) {
		t.Fatalf("conflicting reschedule err = %v", err)
	}
	if b.Schedule != old {
		t.Fatal("a failed reschedule must leave the booking unchanged")
	}

	// Moving one day later only overlaps the booking's own allocations, which
	// are excluded and released.
	res, err := b.Reschedule(rentals.RescheduleRequest{
		Actor: identity.UserActor(customer), Scope: identity.ScopeOwn, Calendar: calendar(t),
		Schedule:  schedule(t, "2026-11-11", "morning", 3*day),
		Inventory: rentals.InventoryView{Assets: readyAssets(), Allocations: allocs}, Now: now,
	})
	if err != nil {
		t.Fatalf("reschedule: %v", err)
	}
	if len(res.Release) != 3 || len(res.Allocations) != 3 || b.Schedule.PickupAt.Equal(old.PickupAt) {
		t.Fatalf("reschedule result = %+v", res)
	}

	if _, err := b.Reschedule(rentals.RescheduleRequest{
		Actor: identity.UserActor(customer), Scope: identity.ScopeOwn, Calendar: calendar(t),
		Schedule:  schedule(t, "2026-11-12", "morning", day),
		Inventory: rentals.InventoryView{Assets: readyAssets()}, Now: now,
	}); !errors.Is(err, rentals.ErrDurationMismatch) {
		t.Fatalf("changing duration err = %v", err)
	}
	if _, err := b.Reschedule(rentals.RescheduleRequest{
		Actor: identity.UserActor(customer), Scope: identity.ScopeOwn, Calendar: calendar(t),
		Schedule:  schedule(t, "2026-11-24", "morning", 3*day),
		Inventory: rentals.InventoryView{Assets: readyAssets()}, Now: b.Schedule.PickupAt.Add(-time.Hour),
	}); !errors.Is(err, rentals.ErrRescheduleCutoffPassed) {
		t.Fatalf("customer reschedule after cutoff err = %v", err)
	}
}

func TestAuthorization(t *testing.T) {
	matrix := identity.ReferencePermissionMatrix()
	mk := func(id identity.UserID, roles ...identity.Role) identity.Principal {
		p, err := identity.NewPrincipal(id, roles, matrix)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	b := mustPlace(t).Booking
	a, other := mk(customer, identity.RoleCustomer), mk("cust-b", identity.RoleCustomer)
	clerk, stock := mk("staff1", identity.RoleRentalStaff), mk("inv1", identity.RoleInventoryManager)

	if s, err := rentals.Authorize(a, rentals.ActionRead, b); err != nil || s != identity.ScopeOwn {
		t.Fatalf("owner read = %v, %v", s, err)
	}
	for _, action := range []rentals.Action{rentals.ActionRead, rentals.ActionCancel, rentals.ActionReschedule} {
		if _, err := rentals.Authorize(other, action, b); !errors.Is(err, identity.ErrForbidden) {
			t.Errorf("customer B %s on A's booking err = %v", action, err)
		}
	}
	for _, action := range []rentals.Action{rentals.ActionConfirm, rentals.ActionHandover, rentals.ActionComplete} {
		if _, err := rentals.Authorize(a, action, b); !errors.Is(err, identity.ErrForbidden) {
			t.Errorf("customer %s err = %v", action, err)
		}
	}
	if s, err := rentals.Authorize(clerk, rentals.ActionCancel, b); err != nil || s != identity.ScopeAny {
		t.Fatalf("staff cancel = %v, %v", s, err)
	}
	if _, err := rentals.Authorize(stock, rentals.ActionHandover, b); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("inventory manager handover err = %v", err)
	}
	if _, err := rentals.Authorize(stock, rentals.ActionInspectAsset, b); err != nil {
		t.Fatalf("inventory manager inspection: %v", err)
	}
	if err := rentals.AuthorizeCreate(clerk); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("staff without customer role creating own booking err = %v", err)
	}
}
