package rentals_test

import (
	"errors"
	"testing"
	"time"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
	"webee_rentor/internal/promotions"
	"webee_rentor/internal/rentals"
)

func tenPercentCoupon() promotions.Campaign {
	return promotions.Campaign{
		ID: "c-ten", Code: "TEN", Name: "Giảm 10%", Type: promotions.TypeCoupon, Status: promotions.StatusActive,
		ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour), ValueType: promotions.ValuePercent, Percent: 10,
		MinimumEligibleAmount: vnd(0), Funding: promotions.FundingMerchant, PolicyVersion: "promo-v1", Version: 1,
	}
}

func pricingFor(t *testing.T, c promotions.Campaign, ls ...rentals.LineRequest) *promotions.Result {
	t.Helper()
	var pl []promotions.Line
	for _, l := range ls {
		pl = append(pl, promotions.Line{ID: string(l.ID), Kind: l.Quote.Kind, OfferingID: l.Quote.OfferingID, Subtotal: l.Quote.RentAmount})
	}
	r, err := promotions.Price(promotions.Request{Currency: money.VND, Lines: pl, CouponCode: c.Code, Coupon: &c, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return &r
}

// Tax & discount policy §8 through a booking: 300,000đ bundle, 10% coupon,
// 500,000đ security deposit → 770,000đ due.
func TestBookingWithCoupon(t *testing.T) {
	items, bundle := catalogFixture()
	bundle.Prices["p3d"] = vnd(300_000)
	override := vnd(500_000)
	bundle.DepositOverride = &override
	q, err := catalog.QuoteBundle(bundle, items, map[string]catalog.VariantID{"jacket": "jacket-m", "pants": "pants-l"}, pkg3, 1)
	if err != nil {
		t.Fatal(err)
	}
	lines := []rentals.LineRequest{{ID: "l1", Quote: q}}
	c := tenPercentCoupon()
	b := mustPlace(t, func(r *rentals.PlaceRequest) { r.Lines, r.Pricing = lines, pricingFor(t, c, lines...) }).Booking

	if b.GrossRentTotal.Amount() != 300_000 || b.DiscountTotal.Amount() != 30_000 || b.RentTotal.Amount() != 270_000 {
		t.Fatalf("rent gross=%s discount=%s net=%s", b.GrossRentTotal, b.DiscountTotal, b.RentTotal)
	}
	if b.SecurityDepositTotal.Amount() != 500_000 {
		t.Fatal("discounts never touch the security deposit")
	}
	if due := b.RentTotal.Amount() + b.SecurityDepositTotal.Amount(); due != 770_000 {
		t.Fatalf("total due = %d", due)
	}
	if b.Lines[0].ListRent.Amount() != 300_000 || b.Lines[0].Discount.Amount() != 30_000 || b.Lines[0].RentAmount.Amount() != 270_000 {
		t.Fatalf("line = %+v", b.Lines[0])
	}
	// Reservation is a share of the rent actually charged.
	if b.ReservationDeposit.Amount() != 81_000 {
		t.Fatalf("reservation = %s", b.ReservationDeposit)
	}

	// Editing the campaign later does not touch the booking snapshot.
	c.Percent = 50
	if b.Discounts[0].Percent != 10 || b.Discounts[0].Total.Amount() != 30_000 {
		t.Fatalf("snapshot = %+v", b.Discounts[0])
	}
}

func TestPricingMustMatchLines(t *testing.T) {
	lines := []rentals.LineRequest{{ID: "l1", Quote: bundleQuote(t, pkg3)}}
	pricing := pricingFor(t, tenPercentCoupon(), lines...)
	pricing.Lines[0].Subtotal = vnd(1)
	if _, err := place(t, func(r *rentals.PlaceRequest) { r.Lines, r.Pricing = lines, pricing }); !errors.Is(err, rentals.ErrPricingMismatch) {
		t.Fatalf("err = %v", err)
	}
}

func TestSizeChangeKeepsDiscount(t *testing.T) {
	lines := []rentals.LineRequest{{ID: "l1", Quote: bundleQuote(t, pkg3)}}
	p := mustPlace(t, func(r *rentals.PlaceRequest) { r.Lines, r.Pricing = lines, pricingFor(t, tenPercentCoupon(), lines...) })
	b := p.Booking
	_, bundle, _ := modCatalog()
	m, _, err := b.RequestModification(rentals.ModificationRequest{ID: "mod-1", Actor: identity.UserActor(customer), Scope: identity.ScopeOwn,
		Changes: []rentals.LineChange{{LineID: "l1", Quote: quoteBundle(t, bundle, "jacket-l", "pants-l", pkg3)}}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	view := rentals.InventoryView{Assets: modAssets(), Allocations: materialize(t, p.Allocations, "own")}
	if _, err := m.Quote(b, rentals.QuoteInput{Actor: identity.UserActor(customer), Scope: identity.ScopeOwn, Inventory: view, Now: now}); err != nil {
		t.Fatal(err)
	}
	if !m.RentDelta.IsZero() || m.After.Lines[0].Discount.Amount() != 20_000 || m.After.Discount.Amount() != 20_000 {
		t.Fatalf("a same-price size swap must keep the discount: Δ=%s line=%+v", m.RentDelta, m.After.Lines[0])
	}
}
