package promotions_test

import (
	"errors"
	"testing"
	"time"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
	"webee_rentor/internal/promotions"
)

var now = time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)

func vnd(v int64) money.Money {
	m, err := money.New(v, money.VND)
	if err != nil {
		panic(err)
	}
	return m
}

func coupon(code string, percent int) promotions.Campaign {
	return promotions.Campaign{
		ID: promotions.CampaignID("c-" + code), Code: code, Name: "Giảm " + code, Type: promotions.TypeCoupon,
		Status: promotions.StatusActive, ValidFrom: now.Add(-24 * time.Hour), ValidUntil: now.Add(24 * time.Hour),
		Timezone: "Asia/Ho_Chi_Minh", ValueType: promotions.ValuePercent, Percent: percent,
		MinimumEligibleAmount: vnd(0), Funding: promotions.FundingMerchant, PolicyVersion: "promo-v1", Version: 1,
	}
}

func lines(amounts ...int64) []promotions.Line {
	var out []promotions.Line
	for i, a := range amounts {
		out = append(out, promotions.Line{ID: string(rune('a' + i)), Kind: catalog.KindBundle, OfferingID: "naruto", Subtotal: vnd(a)})
	}
	return out
}

func price(t *testing.T, req promotions.Request) promotions.Result {
	t.Helper()
	req.Currency, req.Now = money.VND, now
	r, err := promotions.Price(req)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Policy §8: 300,000đ bundle with a 10% coupon.
func TestCouponExample(t *testing.T) {
	c := coupon("TET10", 10)
	r := price(t, promotions.Request{Lines: lines(300_000), CouponCode: " tet10 ", Coupon: &c})
	if r.Gross.Amount() != 300_000 || r.Discount.Amount() != 30_000 || r.Net.Amount() != 270_000 {
		t.Fatalf("result = %+v", r)
	}
	if len(r.Applied) != 1 || r.Applied[0].CampaignVersion != 1 || r.Applied[0].PolicyVersion != "promo-v1" {
		t.Fatalf("snapshot = %+v", r.Applied)
	}
}

func TestCouponValidityAndLimits(t *testing.T) {
	c := coupon("SALE", 10)
	expired := c
	expired.ValidUntil = now
	future := c
	future.ValidFrom = now.Add(time.Hour)
	minimum := c
	minimum.MinimumEligibleAmount = vnd(500_000)
	cases := []struct {
		name  string
		c     *promotions.Campaign
		usage promotions.Usage
		limit func(*promotions.Campaign)
		want  error
	}{
		{"unknown code", nil, promotions.Usage{}, nil, promotions.ErrCouponNotFound},
		{"expired", &expired, promotions.Usage{}, nil, promotions.ErrCouponInactive},
		{"not yet valid", &future, promotions.Usage{}, nil, promotions.ErrCouponInactive},
		{"below minimum", &minimum, promotions.Usage{}, nil, promotions.ErrCouponIneligible},
		{"total limit", &c, promotions.Usage{Total: 100}, func(x *promotions.Campaign) { x.UsageLimitTotal = 100 }, promotions.ErrUsageLimit},
		{"per-customer limit", &c, promotions.Usage{Total: 3, ForCustomer: 1}, func(x *promotions.Campaign) { x.UsageLimitPerCustomer = 1 }, promotions.ErrUsageLimit},
	}
	for _, tc := range cases {
		var cp *promotions.Campaign
		if tc.c != nil {
			v := *tc.c
			if tc.limit != nil {
				tc.limit(&v)
			}
			cp = &v
		}
		usage := map[promotions.CampaignID]promotions.Usage{c.ID: tc.usage}
		_, err := promotions.Price(promotions.Request{Currency: money.VND, Lines: lines(300_000), CouponCode: "SALE", Coupon: cp, Usage: usage, Now: now})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestRoundingAndAllocationAddUp(t *testing.T) {
	c := coupon("TEN", 10)
	r := price(t, promotions.Request{Lines: lines(33_333, 33_333, 33_334), CouponCode: "TEN", Coupon: &c})
	// 10% of 100,000 = 10,000, split by largest remainder: 3,333 + 3,333 + 3,334.
	var sum int64
	for _, l := range r.Lines {
		sum += l.Discount.Amount()
		if l.Net.Amount() != l.Subtotal.Amount()-l.Discount.Amount() {
			t.Fatalf("line = %+v", l)
		}
	}
	if r.Discount.Amount() != 10_000 || sum != 10_000 || r.Net.Amount() != 90_000 {
		t.Fatalf("discount = %d, lines sum %d", r.Discount.Amount(), sum)
	}
	again := price(t, promotions.Request{Lines: lines(33_333, 33_333, 33_334), CouponCode: "TEN", Coupon: &c})
	for i := range r.Lines {
		if r.Lines[i] != again.Lines[i] {
			t.Fatal("pricing must be deterministic")
		}
	}
	odd := price(t, promotions.Request{Lines: lines(33_333), CouponCode: "TEN", Coupon: &c})
	if odd.Discount.Amount() != 3_333 {
		t.Fatalf("10%% of 33,333 = %d, want 3,333 (rounded down)", odd.Discount.Amount())
	}
}

func TestDiscountNeverBelowZeroAndCapped(t *testing.T) {
	fixed := coupon("BIG", 10)
	fixed.ValueType, fixed.Percent, fixed.Amount = promotions.ValueFixed, 0, vnd(500_000)
	r := price(t, promotions.Request{Lines: lines(300_000), CouponCode: "BIG", Coupon: &fixed})
	if r.Net.Amount() != 0 || r.Discount.Amount() != 300_000 {
		t.Fatalf("result = %+v", r)
	}
	capped := coupon("HALF", 50)
	ceiling := vnd(40_000)
	capped.MaxDiscount = &ceiling
	r = price(t, promotions.Request{Lines: lines(300_000), CouponCode: "HALF", Coupon: &capped})
	if r.Discount.Amount() != 40_000 {
		t.Fatalf("capped discount = %d", r.Discount.Amount())
	}
}

func TestStackingMustBeDeclared(t *testing.T) {
	member := promotions.Campaign{
		ID: "member", Name: "Thành viên", Type: promotions.TypeMember, Status: promotions.StatusActive,
		ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour), ValueType: promotions.ValuePercent, Percent: 5,
		MinimumEligibleAmount: vnd(0), EligibleSegment: "gold", Funding: promotions.FundingMerchant, PolicyVersion: "promo-v1", Version: 1,
	}
	c := coupon("TEN", 10)
	gold := promotions.Customer{ID: "u1", Segments: []string{"gold"}}
	if _, err := promotions.Price(promotions.Request{Currency: money.VND, Lines: lines(200_000), Customer: gold,
		Automatic: []promotions.Campaign{member}, CouponCode: "TEN", Coupon: &c, Now: now}); !errors.Is(err, promotions.ErrNotCombinable) {
		t.Fatalf("non-combinable stack err = %v", err)
	}
	member.Combinable, c.Combinable = true, true
	r := price(t, promotions.Request{Lines: lines(200_000), Customer: gold, Automatic: []promotions.Campaign{member}, CouponCode: "TEN", Coupon: &c})
	// Member 5% first (10,000), then coupon 10% of 190,000 (19,000).
	if r.Discount.Amount() != 29_000 || len(r.Applied) != 2 || r.Applied[0].Type != promotions.TypeMember {
		t.Fatalf("stacked = %+v", r)
	}
	plain := price(t, promotions.Request{Lines: lines(200_000), Customer: promotions.Customer{ID: "u2"}, Automatic: []promotions.Campaign{member}})
	if !plain.Discount.IsZero() {
		t.Fatal("member discount needs the segment")
	}
}

func TestEligibilityByOffering(t *testing.T) {
	line := promotions.Campaign{
		ID: "jacket-week", Name: "Áo khoác", Type: promotions.TypeLine, Status: promotions.StatusActive,
		ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour), ValueType: promotions.ValueFixed, Amount: vnd(20_000),
		MinimumEligibleAmount: vnd(0), EligibleItemIDs: []catalog.ItemID{"jacket"}, Funding: promotions.FundingMerchant,
		PolicyVersion: "promo-v1", Version: 1,
	}
	ls := []promotions.Line{
		{ID: "a", Kind: catalog.KindItem, OfferingID: "jacket", Subtotal: vnd(130_000)},
		{ID: "b", Kind: catalog.KindBundle, OfferingID: "naruto", Subtotal: vnd(200_000)},
	}
	r := price(t, promotions.Request{Lines: ls, Automatic: []promotions.Campaign{line}})
	a, _ := r.Line("a")
	b, _ := r.Line("b")
	if a.Discount.Amount() != 20_000 || !b.Discount.IsZero() {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
}

func TestManualAdjustment(t *testing.T) {
	matrix := identity.ReferencePermissionMatrix()
	clerk, _ := identity.NewPrincipal("staff1", []identity.Role{identity.RoleRentalStaff}, matrix)
	owner, _ := identity.NewPrincipal("owner1", []identity.Role{identity.RoleAdmin}, matrix)
	adj := []promotions.ManualAdjustment{{LineID: "a", Amount: vnd(15_000), Reason: "khách quen"}}
	if _, err := promotions.Price(promotions.Request{Currency: money.VND, Lines: lines(200_000), Manual: adj, ManualBy: &clerk, Now: now}); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("staff without permission err = %v", err)
	}
	noReason := []promotions.ManualAdjustment{{LineID: "a", Amount: vnd(15_000)}}
	if _, err := promotions.Price(promotions.Request{Currency: money.VND, Lines: lines(200_000), Manual: noReason, ManualBy: &owner, Now: now}); !errors.Is(err, promotions.ErrManualAdjustment) {
		t.Fatalf("no reason err = %v", err)
	}
	r := price(t, promotions.Request{Lines: lines(200_000), Manual: adj, ManualBy: &owner})
	if r.Net.Amount() != 185_000 || r.Manual[0].Before.Amount() != 200_000 || r.Manual[0].After.Amount() != 185_000 || r.Manual[0].By.ID != "owner1" {
		t.Fatalf("manual = %+v", r.Manual)
	}
}

func TestCreateRequiresPermissionAndRedemptionReleaseIsIdempotent(t *testing.T) {
	matrix := identity.ReferencePermissionMatrix()
	finance, _ := identity.NewPrincipal("fin1", []identity.Role{identity.RoleFinance}, matrix)
	owner, _ := identity.NewPrincipal("owner1", []identity.Role{identity.RoleAdmin}, matrix)
	if _, err := promotions.Create(finance, coupon("X", 10)); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("create without permission err = %v", err)
	}
	c, err := promotions.Create(owner, coupon("X", 10))
	if err != nil {
		t.Fatal(err)
	}
	r := price(t, promotions.Request{Lines: lines(100_000), CouponCode: "x", Coupon: &c})
	rs, err := promotions.Redeem(func(id promotions.CampaignID) promotions.RedemptionID { return promotions.RedemptionID("r-" + id) }, r.Applied, "bk-1", "u1", now)
	if err != nil || len(rs) != 1 {
		t.Fatal(err)
	}
	if u := promotions.CountUsage(rs, c.ID, "u1"); u.Total != 1 || u.ForCustomer != 1 {
		t.Fatalf("usage = %+v", u)
	}
	if changed, err := rs[0].Release("booking cancelled", now); err != nil || !changed {
		t.Fatal(err)
	}
	if changed, err := rs[0].Release("retry", now); err != nil || changed {
		t.Fatalf("second release = %v, %v", changed, err)
	}
	if u := promotions.CountUsage(rs, c.ID, "u1"); u.Total != 0 {
		t.Fatalf("usage after release = %+v", u)
	}
}
