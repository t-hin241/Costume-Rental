package promotions

import (
	"cmp"
	"fmt"
	"math/bits"
	"slices"
	"strings"
	"time"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
)

// Line is a rental line to price: its rent before discounts (package or
// bundle price × quantity). Deposits and fees are never passed in.
type Line struct {
	ID         string
	Kind       catalog.OfferingKind
	OfferingID string
	Subtotal   money.Money
}

// Customer is who the booking is for, with the segments they belong to.
type Customer struct {
	ID       identity.UserID
	Segments []string
}

// Usage counts active redemptions of a campaign, read under lock.
type Usage struct {
	Total       int
	ForCustomer int
}

// ManualAdjustment is a staff price reduction on one line.
type ManualAdjustment struct {
	LineID string
	Amount money.Money
	Reason string
}

// Request is everything the pricing pass needs. Campaigns are loaded by the
// backend; the client only supplies a coupon code.
type Request struct {
	Currency money.Currency
	Lines    []Line
	Customer Customer
	// Automatic are the store's live line, order and member campaigns.
	Automatic []Campaign
	// Coupon is the campaign found for the code the customer entered (nil
	// when none was entered); CouponCode is what they typed.
	CouponCode string
	Coupon     *Campaign
	Usage      map[CampaignID]Usage
	Manual     []ManualAdjustment
	ManualBy   *identity.Principal
	Now        time.Time
}

// Allocation is the part of a discount carried by one line.
type Allocation struct {
	LineID string
	Amount money.Money
}

// Applied is the snapshot of a campaign as applied to a booking.
type Applied struct {
	CampaignID      CampaignID
	CampaignVersion int
	PolicyVersion   string
	Code            string
	Name            string
	Type            Type
	ValueType       ValueType
	Percent         int
	Amount          money.Money
	Funding         Funding
	Total           money.Money
	Allocations     []Allocation
}

// ManualApplied records a manual adjustment with before/after amounts.
type ManualApplied struct {
	LineID string
	Amount money.Money
	Reason string
	By     identity.Actor
	Before money.Money
	After  money.Money
}

// LineResult is one priced line.
type LineResult struct {
	LineID   string
	Subtotal money.Money
	Discount money.Money
	Net      money.Money
}

// Result is the pricing outcome; Net is the commercial total after
// discounts (rent only).
type Result struct {
	Lines    []LineResult
	Applied  []Applied
	Manual   []ManualApplied
	Gross    money.Money
	Discount money.Money
	Net      money.Money
}

// Line returns the result for a line.
func (r Result) Line(id string) (LineResult, bool) {
	i := slices.IndexFunc(r.Lines, func(l LineResult) bool { return l.LineID == id })
	if i < 0 {
		return LineResult{}, false
	}
	return r.Lines[i], true
}

// Price applies discounts in a fixed order (policy §4.4): line discounts,
// then member, order and coupon discounts on the remaining eligible total
// (allocated to lines), then staff manual adjustments. Percentages round down
// to the đồng; allocations use largest remainders so line amounts always add
// up to the booking total. No line goes below zero. These rules are owner
// decision D12.
func Price(req Request) (Result, error) {
	if !req.Currency.Valid() || len(req.Lines) == 0 {
		return Result{}, domainerr.Invalid("pricing", "currency and at least one line are required")
	}
	nets := make([]int64, len(req.Lines))
	seen := map[string]bool{}
	for i, l := range req.Lines {
		if l.ID == "" || seen[l.ID] || l.Subtotal.Currency() != req.Currency || l.Subtotal.IsNegative() {
			return Result{}, domainerr.Invalid("lines", "unique ids and non-negative subtotals in the booking currency are required")
		}
		seen[l.ID] = true
		nets[i] = l.Subtotal.Amount()
	}

	campaigns, err := applicable(req)
	if err != nil {
		return Result{}, err
	}
	var applied []Applied
	for _, c := range campaigns {
		allocs := make([]int64, len(req.Lines))
		if c.Type == TypeLine {
			for i, l := range req.Lines {
				if c.eligibleLine(l) {
					allocs[i] = min(c.discountOn(nets[i]), nets[i])
				}
			}
		} else {
			var base int64
			for i, l := range req.Lines {
				if c.eligibleLine(l) {
					base += nets[i]
				}
			}
			allocs = allocate(min(c.discountOn(base), base), req.Lines, nets, c.eligibleLine)
		}
		a := Applied{
			CampaignID: c.ID, CampaignVersion: c.Version, PolicyVersion: c.PolicyVersion, Code: c.Code, Name: c.Name,
			Type: c.Type, ValueType: c.ValueType, Percent: c.Percent, Amount: c.Amount, Funding: c.Funding,
		}
		var total int64
		for i, v := range allocs {
			if v == 0 {
				continue
			}
			nets[i] -= v
			total += v
			a.Allocations = append(a.Allocations, Allocation{LineID: req.Lines[i].ID, Amount: mustVND(v, req.Currency)})
		}
		if total == 0 {
			continue
		}
		a.Total = mustVND(total, req.Currency)
		applied = append(applied, a)
	}

	var manual []ManualApplied
	if len(req.Manual) > 0 {
		if req.ManualBy == nil {
			return Result{}, identity.ErrNotPrincipal
		}
		if err := req.ManualBy.Require(identity.PermPricingManualAdjust); err != nil {
			return Result{}, err
		}
	}
	for _, m := range req.Manual {
		i := slices.IndexFunc(req.Lines, func(l Line) bool { return l.ID == m.LineID })
		if i < 0 || strings.TrimSpace(m.Reason) == "" || m.Amount.Currency() != req.Currency || !m.Amount.IsPositive() || m.Amount.Amount() > nets[i] {
			return Result{}, fmt.Errorf("line %s: %w", m.LineID, ErrManualAdjustment)
		}
		before := nets[i]
		nets[i] -= m.Amount.Amount()
		manual = append(manual, ManualApplied{
			LineID: m.LineID, Amount: m.Amount, Reason: m.Reason, By: req.ManualBy.Actor(),
			Before: mustVND(before, req.Currency), After: mustVND(nets[i], req.Currency),
		})
	}

	res := Result{Applied: applied, Manual: manual}
	var gross, net int64
	for i, l := range req.Lines {
		gross += l.Subtotal.Amount()
		net += nets[i]
		res.Lines = append(res.Lines, LineResult{
			LineID: l.ID, Subtotal: l.Subtotal,
			Discount: mustVND(l.Subtotal.Amount()-nets[i], req.Currency), Net: mustVND(nets[i], req.Currency),
		})
	}
	res.Gross, res.Net, res.Discount = mustVND(gross, req.Currency), mustVND(net, req.Currency), mustVND(gross-net, req.Currency)
	return res, nil
}

// applicable selects live, eligible campaigns in application order and
// enforces usage limits and stacking. A coupon that does not apply is an
// error the customer sees; automatic campaigns that do not apply are skipped.
func applicable(req Request) ([]Campaign, error) {
	var out []Campaign
	for _, c := range req.Automatic {
		if c.Type == TypeCoupon || !c.live(req.Now) || c.Validate() != nil || c.Currency() != req.Currency {
			continue
		}
		if c.Type == TypeMember && !slices.Contains(req.Customer.Segments, c.EligibleSegment) {
			continue
		}
		if !c.meetsMinimum(req.Lines) || limitReached(c, req.Usage[c.ID]) {
			continue
		}
		out = append(out, c)
	}
	if code := NormalizeCode(req.CouponCode); code != "" {
		c := req.Coupon
		switch {
		case c == nil || c.Type != TypeCoupon || c.Code != code:
			return nil, ErrCouponNotFound
		case c.Validate() != nil || !c.live(req.Now):
			return nil, ErrCouponInactive
		case c.Currency() != req.Currency || !c.meetsMinimum(req.Lines):
			return nil, ErrCouponIneligible
		case limitReached(*c, req.Usage[c.ID]):
			return nil, ErrUsageLimit
		}
		out = append(out, *c)
	}
	if len(out) > 1 {
		for _, c := range out {
			if !c.Combinable {
				return nil, fmt.Errorf("campaign %s: %w", c.ID, ErrNotCombinable)
			}
		}
	}
	order := map[Type]int{TypeLine: 0, TypeMember: 1, TypeOrder: 2, TypeCoupon: 3}
	slices.SortStableFunc(out, func(a, b Campaign) int { return cmp.Compare(order[a.Type], order[b.Type]) })
	return out, nil
}

func limitReached(c Campaign, u Usage) bool {
	return (c.UsageLimitTotal > 0 && u.Total >= c.UsageLimitTotal) ||
		(c.UsageLimitPerCustomer > 0 && u.ForCustomer >= c.UsageLimitPerCustomer)
}

func (c Campaign) meetsMinimum(lines []Line) bool {
	var eligible int64
	found := false
	for _, l := range lines {
		if c.eligibleLine(l) {
			eligible += l.Subtotal.Amount()
			found = true
		}
	}
	return found && eligible >= c.MinimumEligibleAmount.Amount()
}

// Currency is the campaign's money currency.
func (c Campaign) Currency() money.Currency {
	if c.ValueType == ValueFixed {
		return c.Amount.Currency()
	}
	return c.MinimumEligibleAmount.Currency()
}

func (c Campaign) discountOn(base int64) int64 {
	var d int64
	if c.ValueType == ValuePercent {
		d = base * int64(c.Percent) / 100
		if c.MaxDiscount != nil {
			d = min(d, c.MaxDiscount.Amount())
		}
	} else {
		d = c.Amount.Amount()
	}
	return max(d, 0)
}

// allocate splits amount over eligible lines in proportion to their nets,
// largest remainder first (ties by line order), so the parts sum exactly.
func allocate(amount int64, lines []Line, nets []int64, eligible func(Line) bool) []int64 {
	out := make([]int64, len(lines))
	var base int64
	for i, l := range lines {
		if eligible(l) {
			base += nets[i]
		}
	}
	if amount <= 0 || base <= 0 {
		return out
	}
	type rem struct {
		i int
		r int64
	}
	var rems []rem
	var given int64
	for i, l := range lines {
		if !eligible(l) {
			continue
		}
		q, r := mulDiv(amount, nets[i], base)
		out[i] = q
		given += q
		rems = append(rems, rem{i, r})
	}
	slices.SortStableFunc(rems, func(a, b rem) int { return cmp.Compare(b.r, a.r) })
	for k := 0; given < amount; k++ {
		out[rems[k%len(rems)].i]++
		given++
	}
	return out
}

// mulDiv returns a*b/c and its remainder without overflowing; a <= c and all
// values are non-negative, so the quotient fits.
func mulDiv(a, b, c int64) (int64, int64) {
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	q, r := bits.Div64(hi, lo, uint64(c))
	return int64(q), int64(r)
}

func mustVND(v int64, c money.Currency) money.Money {
	m, _ := money.New(v, c) // bounded by the line subtotals
	return m
}
