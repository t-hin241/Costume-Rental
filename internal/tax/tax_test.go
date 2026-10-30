package tax_test

import (
	"errors"
	"testing"
	"time"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
	"webee_rentor/internal/payments"
	"webee_rentor/internal/tax"
)

func vnd(v int64) money.Money {
	m, err := money.New(v, money.VND)
	if err != nil {
		panic(err)
	}
	return m
}

var (
	loc, _ = businesstime.LoadLocation()
	jan1   = time.Date(2025, 12, 31, 17, 0, 0, 0, time.UTC) // 2026-01-01 00:00 +07
)

// profile is the configuration chosen in the tax policy; the rate and the
// category treatments stay unconfirmed until the accountant sets them.
func profile() tax.Profile {
	return tax.Profile{
		Version: "tax-2026-01", TaxpayerType: tax.IndividualBusiness, VATMethod: tax.DirectOnRevenue,
		AnnualRevenueThreshold: vnd(1_000_000_000), ThresholdEffectiveFrom: businesstime.Date{Year: 2026, Month: 1, Day: 1},
		DiscountBasis: tax.DiscountBasisUnconfirmed, NearThresholdPercent: 90, EffectiveFrom: jan1,
	}
}

func entry(t *testing.T, id string, cat tax.Category, list, discount int64, at time.Time) tax.RevenueEntry {
	t.Helper()
	e, err := tax.NewEntry(tax.RevenueEntry{
		ID: tax.EntryID(id), BookingRef: "bk-" + id, SourceRef: "ch-" + id, Category: cat,
		List: vnd(list), Discount: vnd(discount), Net: vnd(list - discount), RecognizedAt: at, ProfileVersion: "tax-2026-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestProfileHistoryIsEffectiveDated(t *testing.T) {
	owner, _ := identity.NewPrincipal("owner1", []identity.Role{identity.RoleAdmin}, identity.ReferencePermissionMatrix())
	finance, _ := identity.NewPrincipal("fin1", []identity.Role{identity.RoleFinance}, identity.ReferencePermissionMatrix())
	var h tax.History
	p1 := profile()
	until := time.Date(2026, 6, 30, 17, 0, 0, 0, time.UTC)
	p1.EffectiveUntil = &until
	if err := h.Add(finance, p1); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("finance changing tax profile err = %v", err)
	}
	if err := h.Add(owner, p1); err != nil {
		t.Fatal(err)
	}
	p2 := profile()
	p2.Version, p2.EffectiveFrom = "tax-2026-07", until
	p2.VATRevenueRate = &tax.Rate{BasisPoints: 500}
	if err := h.Add(owner, p2); err != nil {
		t.Fatal(err)
	}
	overlap := profile()
	overlap.Version = "tax-bad"
	if err := h.Add(owner, overlap); !errors.Is(err, tax.ErrProfileInvalid) {
		t.Fatalf("overlapping profile err = %v", err)
	}
	if got, _ := h.At(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)); got.Version != "tax-2026-01" || got.VATRevenueRate != nil {
		t.Fatalf("March profile = %+v", got)
	}
	if got, _ := h.At(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)); got.Version != "tax-2026-07" {
		t.Fatalf("August profile = %+v", got)
	}
	if _, err := h.At(time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)); !errors.Is(err, tax.ErrNoProfile) {
		t.Fatalf("before any profile err = %v", err)
	}
}

func TestProfileRejectsOtherModels(t *testing.T) {
	p := profile()
	p.VATMethod = "DEDUCTION"
	if err := p.Validate(); !errors.Is(err, tax.ErrProfileInvalid) {
		t.Fatalf("deduction method err = %v", err)
	}
}

func TestDepositsAreNeverRevenue(t *testing.T) {
	for _, c := range []tax.Category{tax.CatRentalSecurityDeposit, tax.CatBookingDeposit} {
		_, err := tax.NewEntry(tax.RevenueEntry{ID: "x", BookingRef: "b", SourceRef: "s", Category: c,
			List: vnd(1), Discount: vnd(0), Net: vnd(1), RecognizedAt: jan1, ProfileVersion: "v"})
		if !errors.Is(err, tax.ErrNotRevenue) {
			t.Errorf("%s err = %v", c, err)
		}
	}
}

func TestRecognizeSettledLedger(t *testing.T) {
	sys := identity.SystemActor("booking")
	l, err := payments.OpenLedger(payments.OpenRequest{
		BookingRef: "bk-1", Rent: vnd(270_000), Security: vnd(500_000), Reservation: vnd(0),
		RentChargeID: "ch-rent", SecurityChargeID: "ch-dep", Actor: sys, Now: jan1,
	})
	if err != nil {
		t.Fatal(err)
	}
	staff := identity.UserActor("staff1")
	if _, err := l.AddFee("ch-late", payments.ChargeLateFee, vnd(25_000), "trễ 5 giờ", nil, staff, jan1); err != nil {
		t.Fatal(err)
	}
	entries, err := tax.RecognizeLedger(tax.RecognitionInput{Ledger: l, RentList: vnd(300_000), RentDiscount: vnd(30_000), IDPrefix: "rev", Profile: profile(), At: jan1})
	if err != nil {
		t.Fatal(err)
	}
	// Rent and the late fee only; the 500,000đ security deposit is not revenue.
	if len(entries) != 2 || entries[0].Category != tax.CatRentalRevenue || entries[0].List.Amount() != 300_000 ||
		entries[0].Discount.Amount() != 30_000 || entries[0].Net.Amount() != 270_000 || entries[1].Category != tax.CatLateReturnCharge {
		t.Fatalf("entries = %+v", entries)
	}
	if _, err := tax.RecognizeLedger(tax.RecognitionInput{Ledger: l, RentList: vnd(300_000), RentDiscount: vnd(0), IDPrefix: "rev", Profile: profile(), At: jan1}); err == nil {
		t.Fatal("rent breakdown must match the rent charge")
	}
}

func TestThresholdLevelsAndBusinessYear(t *testing.T) {
	p := profile()
	mid := time.Date(2026, 6, 1, 3, 0, 0, 0, time.UTC)
	near := []tax.RevenueEntry{entry(t, "1", tax.CatRentalRevenue, 900_000_000, 0, mid)}
	if s := tax.Summarize(near, p, 2026, loc); s.Level != tax.ThresholdNear {
		t.Fatalf("90%% level = %s", s.Level)
	}
	// 2026-12-31 18:00 UTC is 2027-01-01 01:00 in Vietnam.
	newYear := entry(t, "2", tax.CatRentalRevenue, 200_000_000, 0, time.Date(2026, 12, 31, 18, 0, 0, 0, time.UTC))
	if s := tax.Summarize(append(near, newYear), p, 2026, loc); s.Level != tax.ThresholdNear || s.Revenue.Amount() != 900_000_000 {
		t.Fatalf("2026 = %+v; the late entry belongs to 2027", s)
	}
	over := append(near, entry(t, "3", tax.CatRentalRevenue, 200_000_000, 0, mid))
	if s := tax.Summarize(over, p, 2026, loc); s.Level != tax.ThresholdExceeded {
		t.Fatalf("level = %s", s.Level)
	}
}

func TestVATEstimateNeedsConfirmedRules(t *testing.T) {
	mid := time.Date(2026, 6, 1, 3, 0, 0, 0, time.UTC)
	entries := []tax.RevenueEntry{
		entry(t, "1", tax.CatRentalRevenue, 1_100_000_000, 100_000_000, mid),
		entry(t, "2", tax.CatLateReturnCharge, 50_000_000, 0, mid),
	}
	p := profile()
	if e := tax.EstimateVAT(entries[1:], p, 2026, loc); e.Status != tax.EstimateBelowThreshold || e.VAT != nil {
		t.Fatalf("below threshold = %+v", e)
	}
	e := tax.EstimateVAT(entries, p, 2026, loc)
	if e.Status != tax.EstimateUnconfirmed || e.VAT != nil || len(e.Missing) == 0 {
		t.Fatalf("unconfirmed = %+v", e)
	}

	// Example configuration only; real values come from the accountant.
	p.VATRevenueRate = &tax.Rate{BasisPoints: 500}
	p.Categories = map[tax.Category]tax.Treatment{tax.CatRentalRevenue: tax.Taxable, tax.CatLateReturnCharge: tax.NotTaxable}
	p.DiscountBasis = tax.DiscountBasisNet
	e = tax.EstimateVAT(entries, p, 2026, loc)
	// Whole-year taxable rental revenue (1,000,000,000 net), not only the
	// part above the threshold.
	if e.Status != tax.EstimateReady || e.TaxableRevenue.Amount() != 1_000_000_000 || e.VAT.Amount() != 50_000_000 {
		t.Fatalf("estimate = %+v", e)
	}
	p.DiscountBasis = tax.DiscountBasisGross
	if e := tax.EstimateVAT(entries, p, 2026, loc); e.TaxableRevenue.Amount() != 1_100_000_000 {
		t.Fatalf("gross basis = %+v", e)
	}
}

func TestReversalKeepsHistory(t *testing.T) {
	e := entry(t, "1", tax.CatRentalRevenue, 300_000, 30_000, jan1)
	r, err := e.Reverse("1-rev", "booking cancelled after recognition", jan1.Add(time.Hour))
	if err != nil || r.Net.Amount() != -270_000 || r.Reverses != "1" {
		t.Fatalf("reversal = %+v, %v", r, err)
	}
	if s := tax.Summarize([]tax.RevenueEntry{e, r}, profile(), 2026, loc); !s.Revenue.IsZero() {
		t.Fatalf("revenue after reversal = %s", s.Revenue)
	}
}
