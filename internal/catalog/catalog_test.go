package catalog_test

import (
	"errors"
	"testing"
	"time"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/money"
)

const day = 24 * time.Hour

func vnd(v int64) money.Money {
	m, err := money.New(v, money.VND)
	if err != nil {
		panic(err)
	}
	return m
}

var (
	pkg1 = catalog.RentalPackage{ID: "p1d", Code: "1D", Label: "1 ngày", Duration: day, Status: catalog.StatusActive}
	pkg3 = catalog.RentalPackage{ID: "p3d", Code: "3D", Label: "3 ngày", Duration: 3 * day, Status: catalog.StatusActive}
)

// Example figures from docs/about_costume_rental.md §5.2 and §6.1.
func fixture() (map[catalog.ItemID]catalog.Item, catalog.Bundle) {
	jacket := catalog.Item{
		ID: "jacket", Slug: "ao-khoac-naruto", Name: "Áo khoác Naruto", Status: catalog.StatusActive,
		IndividuallyRentable: true,
		Variants: []catalog.Variant{
			{ID: "jacket-m", SKU: "NAR-JK-M", Size: "M", Status: catalog.StatusActive},
			{ID: "jacket-l", SKU: "NAR-JK-L", Size: "L", Status: catalog.StatusActive},
		},
		Prices:          catalog.Prices{"p1d": vnd(50_000)},
		SecurityDeposit: vnd(300_000), DailyRate: vnd(50_000), BufferClass: "garment", Version: 1,
	}
	pants := catalog.Item{
		ID: "pants", Slug: "quan-naruto", Name: "Quần Naruto", Status: catalog.StatusActive,
		IndividuallyRentable: true,
		Variants: []catalog.Variant{
			{ID: "pants-m", SKU: "NAR-PT-M", Size: "M", Status: catalog.StatusActive},
			{ID: "pants-l", SKU: "NAR-PT-L", Size: "L", Status: catalog.StatusActive},
		},
		Prices:          catalog.Prices{"p1d": vnd(30_000)},
		SecurityDeposit: vnd(150_000), DailyRate: vnd(30_000), BufferClass: "garment", Version: 1,
	}
	belt := catalog.Item{
		ID: "belt", Slug: "that-lung-naruto", Name: "Thắt lưng Naruto", Status: catalog.StatusActive,
		IndividuallyRentable: false,
		Variants:             []catalog.Variant{{ID: "belt-std", SKU: "NAR-BL", Status: catalog.StatusActive}},
		SecurityDeposit:      vnd(100_000), DailyRate: vnd(10_000), BufferClass: "accessory", Version: 1,
	}
	items := map[catalog.ItemID]catalog.Item{"jacket": jacket, "pants": pants, "belt": belt}
	bundle := catalog.Bundle{
		ID: "naruto", Slug: "bo-naruto", Name: "Bộ Naruto", Status: catalog.StatusActive,
		Components: []catalog.BundleComponent{
			{Key: "jacket", ItemID: "jacket", Options: []catalog.VariantID{"jacket-m", "jacket-l"}, Quantity: 1},
			{Key: "pants", ItemID: "pants", Options: []catalog.VariantID{"pants-m", "pants-l"}, Quantity: 1},
			{Key: "belt", ItemID: "belt", Options: []catalog.VariantID{"belt-std"}, Quantity: 1},
		},
		Prices:  catalog.Prices{"p1d": vnd(80_000), "p3d": vnd(200_000)},
		Version: 1,
	}
	return items, bundle
}

func TestFixtureIsValid(t *testing.T) {
	items, bundle := fixture()
	for id, it := range items {
		if err := it.Validate(); err != nil {
			t.Fatalf("item %s: %v", id, err)
		}
	}
	if err := bundle.Validate(items); err != nil {
		t.Fatalf("bundle: %v", err)
	}
}

func TestValidationRejectsBadDefinitions(t *testing.T) {
	items, bundle := fixture()

	bad := items["jacket"]
	bad.Prices = catalog.Prices{"p1d": vnd(0)}
	if err := bad.Validate(); !errors.Is(err, domainerr.ErrValidation) {
		t.Errorf("zero price err = %v", err)
	}
	bad = items["jacket"]
	bad.Variants = append(bad.Variants, catalog.Variant{ID: "jacket-xl", SKU: "NAR-JK-M", Status: catalog.StatusActive})
	if err := bad.Validate(); !errors.Is(err, domainerr.ErrValidation) {
		t.Errorf("duplicate SKU err = %v", err)
	}
	bad = items["jacket"]
	bad.Slug = "Áo Khoác"
	if err := bad.Validate(); !errors.Is(err, domainerr.ErrValidation) {
		t.Errorf("bad slug err = %v", err)
	}

	b := bundle
	b.Components = []catalog.BundleComponent{{Key: "jacket", ItemID: "jacket", Options: []catalog.VariantID{"pants-m"}, Quantity: 1}}
	if err := b.Validate(items); !errors.Is(err, domainerr.ErrValidation) {
		t.Errorf("option from another item err = %v", err)
	}
	b = bundle
	b.Components = append([]catalog.BundleComponent{}, bundle.Components...)
	b.Components[0].Quantity = 0
	if err := b.Validate(items); !errors.Is(err, domainerr.ErrValidation) {
		t.Errorf("zero component quantity err = %v", err)
	}

	p := pkg1
	p.Duration = 90 * time.Minute
	if err := p.Validate(); !errors.Is(err, domainerr.ErrValidation) {
		t.Errorf("non-hour package err = %v", err)
	}
}

func TestBundleUsesOwnPriceNotComponentSum(t *testing.T) {
	items, bundle := fixture()
	q, err := catalog.QuoteBundle(bundle, items, map[string]catalog.VariantID{"jacket": "jacket-m", "pants": "pants-l"}, pkg3, 1)
	if err != nil {
		t.Fatal(err)
	}
	if q.RentAmount.Amount() != 200_000 {
		t.Fatalf("bundle 3-day rent = %d, want 200000 (never component prices on top)", q.RentAmount.Amount())
	}
	// Deposit per component: 300k + 150k + 100k.
	if q.DepositAmount.Amount() != 550_000 {
		t.Fatalf("bundle deposit = %d, want 550000", q.DepositAmount.Amount())
	}
	want := map[string]catalog.VariantID{"jacket": "jacket-m", "pants": "pants-l", "belt": "belt-std"}
	if len(q.Components) != 3 {
		t.Fatalf("components = %+v", q.Components)
	}
	for _, c := range q.Components {
		if want[c.Key] != c.VariantID || c.Quantity != 1 {
			t.Errorf("component %s = %s x%d, want %s x1", c.Key, c.VariantID, c.Quantity, want[c.Key])
		}
	}
	if q.Components[2].BufferClass != "accessory" {
		t.Errorf("belt buffer class = %q", q.Components[2].BufferClass)
	}
}

func TestBundleQuantityMultipliesComponents(t *testing.T) {
	items, bundle := fixture()
	q, err := catalog.QuoteBundle(bundle, items, map[string]catalog.VariantID{"jacket": "jacket-l", "pants": "pants-l"}, pkg1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if q.RentAmount.Amount() != 160_000 || q.DepositAmount.Amount() != 1_100_000 {
		t.Fatalf("rent=%d deposit=%d", q.RentAmount.Amount(), q.DepositAmount.Amount())
	}
	for _, c := range q.Components {
		if c.Quantity != 2 {
			t.Errorf("component %s quantity = %d, want 2", c.Key, c.Quantity)
		}
	}
}

func TestBundleDepositOverrideReplacesComponentDeposits(t *testing.T) {
	items, bundle := fixture()
	override := vnd(400_000)
	bundle.DepositOverride = &override
	q, err := catalog.QuoteBundle(bundle, items, map[string]catalog.VariantID{"jacket": "jacket-m", "pants": "pants-m"}, pkg1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if q.DepositAmount.Amount() != 400_000 {
		t.Fatalf("deposit = %d, want override 400000 only", q.DepositAmount.Amount())
	}
}

func TestBundleSelectionRules(t *testing.T) {
	items, bundle := fixture()
	cases := map[string]map[string]catalog.VariantID{
		"missing choice":      {"jacket": "jacket-m"},
		"variant not allowed": {"jacket": "pants-m", "pants": "pants-m"},
		"unknown component":   {"jacket": "jacket-m", "pants": "pants-m", "hat": "x"},
	}
	for name, sel := range cases {
		if _, err := catalog.QuoteBundle(bundle, items, sel, pkg1, 1); !errors.Is(err, catalog.ErrInvalidSelection) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	inactive := items["pants"]
	inactive.Variants = []catalog.Variant{
		{ID: "pants-m", SKU: "NAR-PT-M", Status: catalog.StatusArchived},
		{ID: "pants-l", SKU: "NAR-PT-L", Status: catalog.StatusActive},
	}
	items["pants"] = inactive
	if _, err := catalog.QuoteBundle(bundle, items, map[string]catalog.VariantID{"jacket": "jacket-m", "pants": "pants-m"}, pkg1, 1); !errors.Is(err, catalog.ErrVariantUnavailable) {
		t.Errorf("archived variant err = %v", err)
	}
}

func TestItemQuoteRules(t *testing.T) {
	items, _ := fixture()
	q, err := catalog.QuoteItem(items["jacket"], "jacket-m", pkg1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if q.RentAmount.Amount() != 50_000 || q.DepositAmount.Amount() != 300_000 || len(q.Components) != 1 {
		t.Fatalf("jacket quote = %+v", q)
	}
	if _, err := catalog.QuoteItem(items["belt"], "belt-std", pkg1, 1); !errors.Is(err, catalog.ErrNotIndividually) {
		t.Errorf("belt alone err = %v", err)
	}
	if _, err := catalog.QuoteItem(items["jacket"], "jacket-m", pkg3, 1); !errors.Is(err, catalog.ErrNoPriceForPackage) {
		t.Errorf("missing package price err = %v", err)
	}
	if _, err := catalog.QuoteItem(items["jacket"], "jacket-m", pkg1, 0); !errors.Is(err, domainerr.ErrValidation) {
		t.Errorf("zero quantity err = %v", err)
	}
	draft := pkg1
	draft.Status = catalog.StatusDraft
	if _, err := catalog.QuoteItem(items["jacket"], "jacket-m", draft, 1); !errors.Is(err, catalog.ErrPackageUnavailable) {
		t.Errorf("draft package err = %v", err)
	}
	archived := items["jacket"]
	archived.Status = catalog.StatusArchived
	if _, err := catalog.QuoteItem(archived, "jacket-m", pkg1, 1); !errors.Is(err, catalog.ErrOfferingUnavailable) {
		t.Errorf("archived item err = %v", err)
	}
	if archived.PubliclyListed() || items["belt"].PubliclyListed() || !items["jacket"].PubliclyListed() {
		t.Error("only active, individually rentable items are publicly listed")
	}
}
