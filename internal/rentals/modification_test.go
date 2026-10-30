package rentals_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/period"
	"webee_rentor/internal/rentals"
)

var pkg7 = catalog.RentalPackage{ID: "p7d", Code: "7D", Label: "7 ngày", Duration: 7 * day, Status: catalog.StatusActive}

// modCatalog extends the fixture with a 7-day price, a headband and a
// deluxe bundle (jacket + pants + headband) priced on its own.
func modCatalog() (map[catalog.ItemID]catalog.Item, catalog.Bundle, catalog.Bundle) {
	items, bundle := catalogFixture()
	bundle.Prices["p7d"] = vnd(400_000)
	items["headband"] = catalog.Item{
		ID: "headband", Slug: "bang-do", Name: "Băng đô", Status: catalog.StatusActive,
		Variants:        []catalog.Variant{{ID: "hb-std", SKU: "HB", Status: catalog.StatusActive}},
		SecurityDeposit: vnd(50_000), DailyRate: vnd(10_000), BufferClass: "accessory", Version: 1,
	}
	deluxe := catalog.Bundle{
		ID: "naruto-deluxe", Slug: "bo-naruto-deluxe", Name: "Bộ Naruto Deluxe", Status: catalog.StatusActive,
		Components: []catalog.BundleComponent{
			{Key: "jacket", ItemID: "jacket", Options: []catalog.VariantID{"jacket-m", "jacket-l"}, Quantity: 1},
			{Key: "pants", ItemID: "pants", Options: []catalog.VariantID{"pants-m", "pants-l"}, Quantity: 1},
			{Key: "headband", ItemID: "headband", Options: []catalog.VariantID{"hb-std"}, Quantity: 1},
		},
		Prices: catalog.Prices{"p3d": vnd(260_000)}, Version: 1,
	}
	return items, bundle, deluxe
}

func quoteBundle(t *testing.T, b catalog.Bundle, jacket, pants catalog.VariantID, pkg catalog.RentalPackage) catalog.LineQuote {
	t.Helper()
	items, _, _ := modCatalog()
	q, err := catalog.QuoteBundle(b, items, map[string]catalog.VariantID{"jacket": jacket, "pants": pants}, pkg, 1)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func modAssets() []inventory.Asset {
	return append(readyAssets(), inventory.Asset{ID: "HB-1", Code: "HB-1", VariantID: "hb-std", Status: inventory.AssetReady})
}

type modFixture struct {
	booking *rentals.Booking
	allocs  []inventory.Allocation
}

func placedForModification(t *testing.T) modFixture {
	t.Helper()
	p := mustPlace(t)
	return modFixture{booking: p.Booking, allocs: materialize(t, p.Allocations, "own")}
}

func (f modFixture) view(extra ...inventory.Allocation) rentals.InventoryView {
	return rentals.InventoryView{Assets: modAssets(), Allocations: append(slices.Clone(f.allocs), extra...)}
}

func request(t *testing.T, b *rentals.Booking, scope identity.Scope, changes []rentals.LineChange, pkg *catalog.RentalPackage) *rentals.Modification {
	t.Helper()
	actor := staff
	if scope == identity.ScopeOwn {
		actor = identity.UserActor(customer)
	}
	m, _, err := b.RequestModification(rentals.ModificationRequest{ID: "mod-1", Actor: actor, Scope: scope, Reason: "đổi", Changes: changes, NewPackage: pkg, Now: now})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	return m
}

func quote(m *rentals.Modification, b *rentals.Booking, scope identity.Scope, view rentals.InventoryView, at time.Time) error {
	actor := staff
	if scope == identity.ScopeOwn {
		actor = identity.UserActor(customer)
	}
	_, err := m.Quote(b, rentals.QuoteInput{Actor: actor, Scope: scope, Calendar: nil, Inventory: view, Now: at})
	return err
}

// Policy test 1: same-price size exchange.
func TestSizeExchangeSamePrice(t *testing.T) {
	f := placedForModification(t)
	b := f.booking
	_, bundle, _ := modCatalog()
	m := request(t, b, identity.ScopeOwn, []rentals.LineChange{{LineID: "l1", Quote: quoteBundle(t, bundle, "jacket-l", "pants-l", pkg3)}}, nil)
	if !slices.Equal(m.Kinds, []rentals.ChangeKind{rentals.ChangeSizeExchange}) {
		t.Fatalf("kinds = %v", m.Kinds)
	}
	if err := quote(m, b, identity.ScopeOwn, f.view(), now); err != nil {
		t.Fatal(err)
	}
	if m.Status != rentals.ModQuoted || !m.RequiredPayment.IsZero() || !m.RentDelta.IsZero() || !m.SecurityDelta.IsZero() {
		t.Fatalf("quote = %s required=%s", m.Status, m.RequiredPayment)
	}
	// Pants and belt keep their allocations; the jacket M is replaced by a held L.
	if len(m.Plan.Keep) != 2 || len(m.Plan.Release) != 1 || len(m.Plan.Hold) != 1 || m.Plan.Hold[0].AssetID != "JK-L-1" || m.Plan.Hold[0].ModificationRef != "mod-1" {
		t.Fatalf("plan = %+v", m.Plan)
	}
	if b.Lines[0].Components[0].VariantID != "jacket-m" {
		t.Fatal("the booking must not change before confirmation")
	}
	if _, err := m.Confirm(b, identity.UserActor(customer), vnd(0), now); err != nil {
		t.Fatal(err)
	}
	if b.Lines[0].Components[0].VariantID != "jacket-l" || b.OpenModification != "" || b.SelfServiceSizeChanges != 1 || len(b.Modifications) != 1 {
		t.Fatalf("booking after confirm = %+v", b)
	}
	if m.Before.Lines[0].Components[0].VariantID != "jacket-m" {
		t.Fatal("the original terms must be kept in the modification")
	}
}

// Policy test 2: the new size is not available.
func TestSizeExchangeUnavailableKeepsBooking(t *testing.T) {
	f := placedForModification(t)
	b := f.booking
	other, err := inventory.NewAllocation("other-1", inventory.PlannedAllocation{
		BookingRef: "bk-other", AssetID: "JK-L-1", VariantID: "jacket-l", Block: f.allocs[0].Block,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	_, bundle, _ := modCatalog()
	m := request(t, b, identity.ScopeOwn, []rentals.LineChange{{LineID: "l1", Quote: quoteBundle(t, bundle, "jacket-l", "pants-l", pkg3)}}, nil)
	if err := quote(m, b, identity.ScopeOwn, f.view(other), now); !errors.Is(err, inventory.ErrInsufficientInventory) {
		t.Fatalf("err = %v", err)
	}
	if m.Status != rentals.ModRequested || b.Lines[0].Components[0].VariantID != "jacket-m" {
		t.Fatalf("booking or request changed: %s", m.Status)
	}
	tr, err := m.Reject(b, staff, "hết size L", now)
	if err != nil || tr.ReleaseModificationHolds != "mod-1" || b.OpenModification != "" {
		t.Fatalf("reject = %+v, %v", tr, err)
	}
}

// Policy tests 3 and 4: bundle change priced by the new bundle; the increase
// must be paid first.
func TestBundleChangeRequiresPaymentOfIncrease(t *testing.T) {
	f := placedForModification(t)
	b := f.booking
	_, _, deluxe := modCatalog()
	m := request(t, b, identity.ScopeOwn, []rentals.LineChange{{LineID: "l1", Quote: quoteBundle(t, deluxe, "jacket-m", "pants-l", pkg3)}}, nil)
	if err := quote(m, b, identity.ScopeOwn, f.view(), now); err != nil {
		t.Fatal(err)
	}
	// 260k bundle price (not component prices); deposit 300k+150k+50k.
	if m.Status != rentals.ModAwaitingPayment || m.RentDelta.Amount() != 60_000 || m.SecurityDelta.Amount() != -50_000 || m.RequiredPayment.Amount() != 60_000 {
		t.Fatalf("quote = %s rentΔ=%s secΔ=%s required=%s", m.Status, m.RentDelta, m.SecurityDelta, m.RequiredPayment)
	}
	// Jacket and pants reused; belt released; headband held.
	if len(m.Plan.Keep) != 2 || len(m.Plan.Release) != 1 || len(m.Plan.Hold) != 1 || m.Plan.Hold[0].AssetID != "HB-1" {
		t.Fatalf("plan = %+v", m.Plan)
	}
	if _, err := m.Confirm(b, identity.UserActor(customer), vnd(59_999), now); !errors.Is(err, rentals.ErrInsufficientFunds) {
		t.Fatalf("confirm before payment err = %v", err)
	}
	if _, err := m.Confirm(b, identity.UserActor(customer), vnd(60_000), now); err != nil {
		t.Fatal(err)
	}
	if b.RentTotal.Amount() != 260_000 || b.SecurityDepositTotal.Amount() != 500_000 || b.Lines[0].OfferingID != "naruto-deluxe" {
		t.Fatalf("booking = rent %s security %s offering %s", b.RentTotal, b.SecurityDepositTotal, b.Lines[0].OfferingID)
	}
}

// Policy test 6: the increase is not paid in time.
func TestUnpaidModificationExpires(t *testing.T) {
	f := placedForModification(t)
	b := f.booking
	_, _, deluxe := modCatalog()
	m := request(t, b, identity.ScopeOwn, []rentals.LineChange{{LineID: "l1", Quote: quoteBundle(t, deluxe, "jacket-m", "pants-l", pkg3)}}, nil)
	_ = quote(m, b, identity.ScopeOwn, f.view(), now)
	if _, _, err := m.Expire(b, system, now.Add(29*time.Minute)); !errors.Is(err, rentals.ErrHoldNotExpired) {
		t.Fatalf("early expiry err = %v", err)
	}
	tr, changed, err := m.Expire(b, system, now.Add(30*time.Minute))
	if err != nil || !changed || tr.ReleaseModificationHolds != "mod-1" || m.Status != rentals.ModExpired {
		t.Fatalf("expire = %+v %v %v", tr, changed, err)
	}
	if _, changed, err := m.Expire(b, system, now.Add(time.Hour)); err != nil || changed {
		t.Fatalf("second expiry = %v %v", changed, err)
	}
	if b.RentTotal.Amount() != 200_000 || b.Lines[0].OfferingID != "naruto" || b.OpenModification != "" {
		t.Fatal("the original booking must stay valid")
	}
	if _, err := m.Confirm(b, identity.UserActor(customer), vnd(60_000), now.Add(time.Hour)); !errors.Is(err, rentals.ErrInvalidTransition) {
		t.Fatalf("confirming an expired change err = %v", err)
	}
}

func TestPackageChangeShortensAndReblocks(t *testing.T) {
	f := placedForModification(t)
	b := f.booking
	_, bundle, _ := modCatalog()
	m := request(t, b, identity.ScopeOwn, []rentals.LineChange{{LineID: "l1", Quote: quoteBundle(t, bundle, "jacket-m", "pants-l", pkg1)}}, &pkg1)
	if _, err := m.Quote(b, rentals.QuoteInput{Actor: identity.UserActor(customer), Scope: identity.ScopeOwn, Calendar: calendar(t), Inventory: f.view(), Now: now}); err != nil {
		t.Fatal(err)
	}
	if m.RentDelta.Amount() != -120_000 || !m.RequiredPayment.IsZero() || m.Status != rentals.ModQuoted {
		t.Fatalf("quote = %s %s", m.RentDelta, m.Status)
	}
	if len(m.Plan.Reblock) != 3 || len(m.Plan.Hold) != 0 || len(m.Plan.Release) != 0 {
		t.Fatalf("shrinking should reblock in place: %+v", m.Plan)
	}
	if _, err := m.Confirm(b, staff, vnd(0), now); err != nil {
		t.Fatal(err)
	}
	if b.Schedule.Duration() != day || b.Package.ID != "p1d" || b.RentTotal.Amount() != 80_000 {
		t.Fatalf("booking = %s %s %s", b.Schedule.Duration(), b.Package.ID, b.RentTotal)
	}
}

func TestExtensionAfterHandover(t *testing.T) {
	b, _ := handedOver(t)
	p := mustPlace(t) // same allocations as handedOver's booking
	own := materialize(t, p.Allocations, "own")
	for i := range own {
		if err := own[i].MarkHandedOver(b.Schedule.PickupAt); err != nil {
			t.Fatal(err)
		}
	}
	_, bundle, _ := modCatalog()
	change := []rentals.LineChange{{LineID: "l1", Quote: quoteBundle(t, bundle, "jacket-m", "pants-l", pkg7)}}
	if _, _, err := b.RequestModification(rentals.ModificationRequest{ID: "m0", Actor: identity.UserActor(customer), Scope: identity.ScopeOwn, Changes: change, NewPackage: &pkg7, Now: now}); !errors.Is(err, rentals.ErrInvalidTransition) {
		t.Fatalf("customer changing a rented booking err = %v", err)
	}
	sizeSwap := []rentals.LineChange{{LineID: "l1", Quote: quoteBundle(t, bundle, "jacket-l", "pants-l", pkg3)}}
	if _, _, err := b.RequestModification(rentals.ModificationRequest{ID: "m0", Actor: staff, Scope: identity.ScopeAny, Changes: sizeSwap, Now: now}); !errors.Is(err, rentals.ErrAfterHandover) {
		t.Fatalf("size change after handover err = %v", err)
	}

	// Another booking holds the jacket right after the current block: the
	// only jacket M cannot be extended.
	later, _ := inventory.NewAllocation("later-1", inventory.PlannedAllocation{
		BookingRef: "bk-later", AssetID: "JK-M-1", VariantID: "jacket-m",
		Block: mustRange(t, own[0].Block.End(), own[0].Block.End().Add(day)),
	}, now)
	m := request(t, b, identity.ScopeAny, change, &pkg7)
	if !slices.Equal(m.Kinds, []rentals.ChangeKind{rentals.ChangeExtension}) {
		t.Fatalf("kinds = %v", m.Kinds)
	}
	view := rentals.InventoryView{Assets: modAssets(), Allocations: append(slices.Clone(own), later)}
	if _, err := m.Quote(b, rentals.QuoteInput{Actor: staff, Scope: identity.ScopeAny, Calendar: calendar(t), Inventory: view, Now: now}); !errors.Is(err, inventory.ErrInsufficientInventory) {
		t.Fatalf("blocked extension err = %v", err)
	}

	view.Allocations = own
	if _, err := m.Quote(b, rentals.QuoteInput{Actor: staff, Scope: identity.ScopeAny, Calendar: calendar(t), Inventory: view, Now: now}); err != nil {
		t.Fatal(err)
	}
	if m.RentDelta.Amount() != 200_000 || len(m.Plan.Reblock) != 3 || len(m.Plan.Hold) != 3 || m.Plan.Hold[0].ExtendsAllocation == "" {
		t.Fatalf("extension quote = %s %+v", m.RentDelta, m.Plan)
	}
	if _, err := m.Confirm(b, staff, vnd(200_000), now); err != nil {
		t.Fatal(err)
	}
	if b.Schedule.Duration() != 7*day || b.Status != rentals.StatusRented {
		t.Fatalf("booking = %s %s", b.Schedule.Duration(), b.Status)
	}
}

func mustRange(t *testing.T, a, b time.Time) period.Range {
	t.Helper()
	r, err := period.New(a, b)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSelfServiceWindowAndAllowance(t *testing.T) {
	f := placedForModification(t)
	b := f.booking
	if _, err := b.Confirm(system, funds(60_000, 0, 60_000), now); err != nil {
		t.Fatal(err)
	}
	_, bundle, _ := modCatalog()
	toL := []rentals.LineChange{{LineID: "l1", Quote: quoteBundle(t, bundle, "jacket-l", "pants-l", pkg3)}}
	m := request(t, b, identity.ScopeOwn, toL, nil)
	late := b.Schedule.PickupAt.Add(-23 * time.Hour)
	if err := quote(m, b, identity.ScopeOwn, f.view(), late); !errors.Is(err, rentals.ErrSelfServiceClosed) {
		t.Fatalf("customer quote inside 24h err = %v", err)
	}
	if err := quote(m, b, identity.ScopeAny, f.view(), late); err != nil {
		t.Fatalf("staff handles late requests: %v", err)
	}
	if _, err := m.Cancel(b, identity.UserActor(customer), "đổi ý", late); err != nil {
		t.Fatal(err)
	}

	b.SelfServiceSizeChanges = 1 // allowance used by an earlier change
	m2, _, err := b.RequestModification(rentals.ModificationRequest{ID: "mod-2", Actor: identity.UserActor(customer), Scope: identity.ScopeOwn, Changes: toL, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := quote(m2, b, identity.ScopeOwn, f.view(), now); !errors.Is(err, rentals.ErrSelfServiceExhausted) {
		t.Fatalf("second online size change err = %v", err)
	}
}

// Policy test 9: the store cannot provide what it committed.
func TestStoreFaultNeverRaisesThePrice(t *testing.T) {
	f := placedForModification(t)
	b := f.booking
	_, _, deluxe := modCatalog()
	change := []rentals.LineChange{{LineID: "l1", Quote: quoteBundle(t, deluxe, "jacket-m", "pants-l", pkg3)}}
	if _, _, err := b.RequestModification(rentals.ModificationRequest{ID: "m0", Actor: identity.UserActor(customer), Scope: identity.ScopeOwn, StoreFault: true, Changes: change, Now: now}); err == nil {
		t.Fatal("customers cannot claim store fault")
	}
	m, _, err := b.RequestModification(rentals.ModificationRequest{ID: "mod-1", Actor: staff, Scope: identity.ScopeAny, StoreFault: true, Reason: "thắt lưng hỏng, thay bằng bộ deluxe", Changes: change, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := quote(m, b, identity.ScopeAny, f.view(), now); err != nil {
		t.Fatal(err)
	}
	if !m.RentDelta.IsZero() || !m.RequiredPayment.IsZero() {
		t.Fatalf("store-fault change rentΔ=%s required=%s", m.RentDelta, m.RequiredPayment)
	}
}

func TestUnsupportedChanges(t *testing.T) {
	f := placedForModification(t)
	b := f.booking
	items, bundle, _ := modCatalog()
	jacket, _ := catalog.QuoteItem(items["jacket"], "jacket-m", pkg3, 1)
	two := quoteBundle(t, bundle, "jacket-l", "pants-l", pkg3)
	two.Quantity = 2
	cases := map[string]struct {
		changes []rentals.LineChange
		pkg     *catalog.RentalPackage
		want    error
	}{
		"bundle broken into an item": {[]rentals.LineChange{{LineID: "l1", Quote: jacket}}, nil, rentals.ErrChangeNotSupported},
		"quantity change":            {[]rentals.LineChange{{LineID: "l1", Quote: two}}, nil, rentals.ErrChangeNotSupported},
		"nothing changes":            {[]rentals.LineChange{{LineID: "l1", Quote: quoteBundle(t, bundle, "jacket-m", "pants-l", pkg3)}}, nil, rentals.ErrNothingChanges},
		"package without the quote":  {[]rentals.LineChange{{LineID: "l1", Quote: quoteBundle(t, bundle, "jacket-m", "pants-l", pkg3)}}, &pkg1, rentals.ErrMixedPackages},
	}
	for name, c := range cases {
		if _, _, err := b.RequestModification(rentals.ModificationRequest{ID: "m", Actor: staff, Scope: identity.ScopeAny, Changes: c.changes, NewPackage: c.pkg, Now: now}); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
}

func TestOpenModificationBlocksOtherChanges(t *testing.T) {
	p := mustPlace(t)
	b := p.Booking
	_, bundle, _ := modCatalog()
	change := []rentals.LineChange{{LineID: "l1", Quote: quoteBundle(t, bundle, "jacket-l", "pants-l", pkg3)}}
	_ = request(t, b, identity.ScopeAny, change, nil)
	if _, _, err := b.RequestModification(rentals.ModificationRequest{ID: "m2", Actor: staff, Scope: identity.ScopeAny, Changes: change, Now: now}); !errors.Is(err, rentals.ErrModificationOpen) {
		t.Fatalf("second open change err = %v", err)
	}
	if _, err := b.Reschedule(rentals.RescheduleRequest{Actor: staff, Scope: identity.ScopeAny, Calendar: calendar(t),
		Schedule: schedule(t, "2026-11-11", "morning", 3*day), Inventory: rentals.InventoryView{Assets: readyAssets()}, Now: now}); !errors.Is(err, rentals.ErrModificationOpen) {
		t.Fatalf("reschedule during a change err = %v", err)
	}
	_, _ = b.Confirm(system, funds(60_000, 0, 60_000), now)
	_, _ = b.StartPreparation(staff, now)
	if _, err := b.RecordHandover(rentals.HandoverRequest{Actor: staff, Allocated: allocated(p), Delivered: delivered(allocatedAssets(p)...),
		ChecklistCompleted: true, Funds: funds(200_000, 550_000, 60_000), Now: b.Schedule.PickupAt}); !errors.Is(err, rentals.ErrModificationOpen) {
		t.Fatalf("handover during a change err = %v", err)
	}
}

// Policy test 8: unchanged lines keep their snapshot price.
func TestUnchangedLinesKeepSnapshotPrice(t *testing.T) {
	items, bundle, _ := modCatalog()
	jacketL, _ := catalog.QuoteItem(items["jacket"], "jacket-l", pkg3, 1)
	p := mustPlace(t, func(r *rentals.PlaceRequest) {
		r.Lines = append(r.Lines, rentals.LineRequest{ID: "l2", Quote: jacketL})
	})
	b := p.Booking
	// Only the bundle line is re-quoted; the jacket line is never re-priced,
	// whatever the catalog says now.
	m := request(t, b, identity.ScopeOwn, []rentals.LineChange{{LineID: "l1", Quote: quoteBundle(t, bundle, "jacket-m", "pants-m", pkg3)}}, nil)
	view := rentals.InventoryView{Assets: modAssets(), Allocations: materialize(t, p.Allocations, "own")}
	if err := quote(m, b, identity.ScopeOwn, view, now); err != nil {
		t.Fatal(err)
	}
	if m.After.Lines[1].RentAmount.Amount() != 130_000 || !m.RentDelta.IsZero() {
		t.Fatalf("unchanged line = %s, rentΔ = %s", m.After.Lines[1].RentAmount, m.RentDelta)
	}
}

// Policy test 11.
func TestOtherCustomerCannotModify(t *testing.T) {
	b := mustPlace(t).Booking
	other, _ := identity.NewPrincipal("cust-b", []identity.Role{identity.RoleCustomer}, identity.ReferencePermissionMatrix())
	if _, err := rentals.Authorize(other, rentals.ActionModify, b); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("err = %v", err)
	}
	owner, _ := identity.NewPrincipal(customer, []identity.Role{identity.RoleCustomer}, identity.ReferencePermissionMatrix())
	if s, err := rentals.Authorize(owner, rentals.ActionModify, b); err != nil || s != identity.ScopeOwn {
		t.Fatalf("owner = %v, %v", s, err)
	}
}
