package rentals_test

import (
	"fmt"
	"testing"
	"time"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/catalog"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/money"
	"webee_rentor/internal/rentals"
)

const day = 24 * time.Hour

var (
	// 2026-11-01 09:00 Asia/Ho_Chi_Minh.
	now      = time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)
	customer = identity.UserID("cust-a")
	staff    = identity.UserActor("staff1")
	system   = identity.SystemActor("payments-webhook")
	pkg1     = catalog.RentalPackage{ID: "p1d", Code: "1D", Label: "1 ngày", Duration: day, Status: catalog.StatusActive}
	pkg3     = catalog.RentalPackage{ID: "p3d", Code: "3D", Label: "3 ngày", Duration: 3 * day, Status: catalog.StatusActive}
)

func vnd(v int64) money.Money {
	m, err := money.New(v, money.VND)
	if err != nil {
		panic(err)
	}
	return m
}

// funds mirrors what payments.Summary reports; with the reservation credited
// to rent, rental already includes the held reservation.
func funds(rental, security, reservation int64) rentals.Funds {
	return rentals.Funds{RentalCovered: vnd(rental), SecurityHeld: vnd(security), ReservationHeld: vnd(reservation)}
}

// testPolicy is a fixture only; real values are pending owner approval.
func testPolicy() rentals.Policy {
	return rentals.Policy{
		Version:            "test-v1",
		HoldTTL:            30 * time.Minute,
		MinLeadTime:        2 * time.Hour,
		MaxAdvance:         90 * day,
		MaxLinesPerBooking: 10,
		MaxQuantityPerLine: 5,
		Confirmation: rentals.ConfirmationRule{
			Mode:  rentals.ConfirmAuto,
			Funds: rentals.FundsRequirement{Kind: rentals.FundsReservationDeposit},
		},
		Handover:                 rentals.FundsRequirement{Kind: rentals.FundsFullRentAndSecurity},
		CustomerRescheduleCutoff: day,
		Buffers: rentals.BufferPolicy{
			Default: rentals.Buffer{Preparation: 2 * time.Hour, Turnaround: day},
			ByClass: map[string]rentals.Buffer{"accessory": {Preparation: time.Hour, Turnaround: 4 * time.Hour}},
		},
		// 30% of rent, deducted from rent at handover (fixture value).
		Reservation: rentals.ReservationPolicy{
			Kind: rentals.ReservationRentPercent, RentPercent: 30,
			Treatment: rentals.ReservationAppliedToRent,
		},
		Cancellation: approvedCancellation(),
		LateFee:      approvedLateFee(),
		Loss:         rentals.LossPolicy{VerificationWindow: 48 * time.Hour},
		// Fixture threshold; the real value is pending owner confirmation.
		FeeApproval: rentals.FeeApprovalPolicy{ManagerApprovalAbove: vnd(500_000)},
		// 24h self-service window and one online size change (decision D10);
		// the 30-minute hold is a fixture value.
		Modification: rentals.ModificationPolicy{SelfServiceCutoff: 24 * time.Hour, SelfServiceSizeChanges: 1, HoldTTL: 30 * time.Minute},
		// Fixture transit buffers; real values come from the route and SLA.
		Delivery: rentals.DeliveryPolicy{OutboundLead: time.Hour, ReturnTransit: 3 * time.Hour, ComplaintWindow: 2 * time.Hour},
	}
}

// approvedLateFee is the late-return table of docs/note/fee-policy.md §2.2
// (decision D9).
func approvedLateFee() rentals.LateFeePolicy {
	return rentals.LateFeePolicy{
		Grace:                   30 * time.Minute,
		GraceRequiresNoConflict: true,
		Tiers: []rentals.LateFeeTier{
			{UpTo: 4 * time.Hour, Percent: 25},
			{UpTo: 8 * time.Hour, Percent: 50},
			{UpTo: 12 * time.Hour, Percent: 75},
			{UpTo: 24 * time.Hour, Percent: 100},
		},
		ExtraPeriod:  24 * time.Hour,
		ExtraPercent: 100,
	}
}

// approvedCancellation is the owner-approved fee table (decision D8).
func approvedCancellation() rentals.CancellationPolicy {
	return rentals.CancellationPolicy{
		Tiers: []rentals.FeeTier{
			{MinNotice: 7 * day, FeePercent: 0},
			{MinNotice: 3 * day, FeePercent: 10},
			{MinNotice: 24 * time.Hour, FeePercent: 30},
			{MinNotice: 0, FeePercent: 50},
		},
		NoShowAfter:           2 * time.Hour,
		NoShowFeePercent:      70,
		StoreCancelFeePercent: 0, // owner decision D17
	}
}

func tod(t *testing.T, s string) businesstime.TimeOfDay {
	t.Helper()
	v, err := businesstime.ParseTimeOfDay(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func date(t *testing.T, s string) businesstime.Date {
	t.Helper()
	d, err := businesstime.ParseDate(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// calendar: open Monday–Saturday with morning and afternoon slots, closed on
// Sundays and on 2026-11-20.
func calendar(t *testing.T) *rentals.StoreCalendar {
	t.Helper()
	loc, err := businesstime.LoadLocation()
	if err != nil {
		t.Fatal(err)
	}
	slots := []rentals.Slot{
		{ID: "morning", Start: tod(t, "09:00"), End: tod(t, "11:00")},
		{ID: "afternoon", Start: tod(t, "15:00"), End: tod(t, "17:00")},
	}
	weekly := map[time.Weekday][]rentals.Slot{}
	for d := time.Monday; d <= time.Saturday; d++ {
		weekly[d] = slots
	}
	c, err := rentals.NewStoreCalendar(loc, weekly, []businesstime.Date{date(t, "2026-11-20")})
	if err != nil {
		t.Fatal(err)
	}
	// Courier windows (fixture): 07-09, 09-11 and 18-20, Monday to Saturday.
	windows := []rentals.Slot{
		{ID: "early", Start: tod(t, "07:00"), End: tod(t, "09:00")},
		{ID: "mid", Start: tod(t, "09:00"), End: tod(t, "11:00")},
		{ID: "eve", Start: tod(t, "18:00"), End: tod(t, "20:00")},
	}
	delivery := map[time.Weekday][]rentals.Slot{}
	for d := time.Monday; d <= time.Saturday; d++ {
		delivery[d] = windows
	}
	if err := c.SetDeliveryWindows(delivery); err != nil {
		t.Fatal(err)
	}
	return c
}

func schedule(t *testing.T, pickupDate, slot string, d time.Duration) rentals.Schedule {
	t.Helper()
	s, err := calendar(t).ResolveSchedule(date(t, pickupDate), slot, d)
	if err != nil {
		t.Fatalf("resolve %s %s: %v", pickupDate, slot, err)
	}
	return s
}

func catalogFixture() (map[catalog.ItemID]catalog.Item, catalog.Bundle) {
	items := map[catalog.ItemID]catalog.Item{
		"jacket": {
			ID: "jacket", Slug: "ao-khoac", Name: "Áo khoác", Status: catalog.StatusActive, IndividuallyRentable: true,
			Variants: []catalog.Variant{
				{ID: "jacket-m", SKU: "JK-M", Status: catalog.StatusActive},
				{ID: "jacket-l", SKU: "JK-L", Status: catalog.StatusActive},
			},
			Prices: catalog.Prices{"p1d": vnd(50_000), "p3d": vnd(130_000)}, SecurityDeposit: vnd(300_000), DailyRate: vnd(50_000),
			BufferClass: "garment", Version: 1,
		},
		"pants": {
			ID: "pants", Slug: "quan", Name: "Quần", Status: catalog.StatusActive, IndividuallyRentable: true,
			Variants: []catalog.Variant{
				{ID: "pants-m", SKU: "PT-M", Status: catalog.StatusActive},
				{ID: "pants-l", SKU: "PT-L", Status: catalog.StatusActive},
			},
			Prices: catalog.Prices{"p1d": vnd(30_000)}, SecurityDeposit: vnd(150_000), DailyRate: vnd(30_000),
			BufferClass: "garment", Version: 1,
		},
		"belt": {
			ID: "belt", Slug: "that-lung", Name: "Thắt lưng", Status: catalog.StatusActive,
			Variants:        []catalog.Variant{{ID: "belt-std", SKU: "BL", Status: catalog.StatusActive}},
			SecurityDeposit: vnd(100_000), DailyRate: vnd(10_000), BufferClass: "accessory", Version: 1,
		},
	}
	bundle := catalog.Bundle{
		ID: "naruto", Slug: "bo-naruto", Name: "Bộ Naruto", Status: catalog.StatusActive,
		Components: []catalog.BundleComponent{
			{Key: "jacket", ItemID: "jacket", Options: []catalog.VariantID{"jacket-m", "jacket-l"}, Quantity: 1},
			{Key: "pants", ItemID: "pants", Options: []catalog.VariantID{"pants-m", "pants-l"}, Quantity: 1},
			{Key: "belt", ItemID: "belt", Options: []catalog.VariantID{"belt-std"}, Quantity: 1},
		},
		Prices: catalog.Prices{"p1d": vnd(80_000), "p3d": vnd(200_000)}, Version: 1,
	}
	return items, bundle
}

func bundleQuote(t *testing.T, pkg catalog.RentalPackage) catalog.LineQuote {
	t.Helper()
	items, bundle := catalogFixture()
	q, err := catalog.QuoteBundle(bundle, items, map[string]catalog.VariantID{"jacket": "jacket-m", "pants": "pants-l"}, pkg, 1)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func readyAssets() []inventory.Asset {
	var out []inventory.Asset
	for _, id := range []string{"JK-M-1", "JK-L-1", "PT-M-1", "PT-L-1", "BL-1"} {
		variant := map[string]catalog.VariantID{
			"JK-M-1": "jacket-m", "JK-L-1": "jacket-l", "PT-M-1": "pants-m", "PT-L-1": "pants-l", "BL-1": "belt-std",
		}[id]
		out = append(out, inventory.Asset{ID: inventory.AssetID(id), Code: id, VariantID: variant, Status: inventory.AssetReady})
	}
	return out
}

type placeOpt func(*rentals.PlaceRequest)

func place(t *testing.T, opts ...placeOpt) (rentals.Placement, error) {
	t.Helper()
	req := rentals.PlaceRequest{
		BookingID: "bk-1", Code: "WB-0001", CustomerID: customer, Actor: identity.UserActor(customer),
		Schedule: schedule(t, "2026-11-10", "morning", 3*day),
		Calendar: calendar(t),
		Lines:    []rentals.LineRequest{{ID: "l1", Quote: bundleQuote(t, pkg3)}},
		Policy:   testPolicy(),
		Inventory: rentals.InventoryView{
			Assets: readyAssets(),
		},
		Now: now,
	}
	for _, o := range opts {
		o(&req)
	}
	return rentals.PlaceBooking(req)
}

func mustPlace(t *testing.T, opts ...placeOpt) rentals.Placement {
	t.Helper()
	p, err := place(t, opts...)
	if err != nil {
		t.Fatalf("place booking: %v", err)
	}
	return p
}

func materialize(t *testing.T, planned []inventory.PlannedAllocation, prefix string) []inventory.Allocation {
	t.Helper()
	var out []inventory.Allocation
	for i, p := range planned {
		a, err := inventory.NewAllocation(inventory.AllocationID(fmt.Sprintf("%s-%d", prefix, i)), p, now)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func allocated(p rentals.Placement) []rentals.AllocatedAsset {
	var out []rentals.AllocatedAsset
	for _, a := range p.Allocations {
		out = append(out, rentals.AllocatedAsset{AssetID: a.AssetID, LineID: rentals.LineID(a.LineRef), ComponentKey: a.ComponentKey})
	}
	return out
}

func delivered(ids ...inventory.AssetID) []rentals.DeliveredItem {
	var out []rentals.DeliveredItem
	for _, id := range ids {
		out = append(out, rentals.DeliveredItem{AssetID: id})
	}
	return out
}

func allocatedAssets(p rentals.Placement) []inventory.AssetID {
	var ids []inventory.AssetID
	for _, a := range p.Allocations {
		ids = append(ids, a.AssetID)
	}
	return ids
}
