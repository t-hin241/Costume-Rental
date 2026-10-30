// Package promotions owns discounts: campaigns (line, coupon, order and
// member discounts), staff manual adjustments, the pricing pass that applies
// them to rental lines, and coupon redemptions
// (docs/note/tax-direct-discount-policy.md §4).
//
// Discounts only ever reduce rent. They never touch the security deposit,
// the reservation deposit or post-rental fees. A bundle's own price is a
// catalog price, not a discount.
package promotions

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
)

type CampaignID string

// Type is the kind of discount program.
type Type string

const (
	// TypeLine discounts each eligible line on its own.
	TypeLine Type = "LINE_DISCOUNT"
	// TypeCoupon is entered by the customer; at most one per booking.
	TypeCoupon Type = "COUPON"
	// TypeOrder discounts the eligible total and is allocated to lines.
	TypeOrder Type = "ORDER_DISCOUNT"
	// TypeMember applies to customers in EligibleSegment, at order level.
	TypeMember Type = "MEMBER_DISCOUNT"
)

// Status of a campaign.
type Status string

const (
	StatusDraft  Status = "DRAFT"
	StatusActive Status = "ACTIVE"
	StatusPaused Status = "PAUSED"
	StatusEnded  Status = "ENDED"
)

// ValueType is how the discount value is expressed.
type ValueType string

const (
	ValuePercent ValueType = "PERCENT"
	ValueFixed   ValueType = "FIXED_AMOUNT"
)

// Funding records who pays for the discount.
type Funding string

const FundingMerchant Funding = "MERCHANT"

var (
	ErrCampaignInvalid  = domainerr.New(domainerr.KindInvalid, "promotions.campaign_invalid", "campaign definition is invalid")
	ErrCouponNotFound   = domainerr.New(domainerr.KindInvalid, "promotions.coupon_not_found", "the coupon code is not valid")
	ErrCouponInactive   = domainerr.New(domainerr.KindInvalid, "promotions.coupon_inactive", "the coupon is not active at this time")
	ErrCouponIneligible = domainerr.New(domainerr.KindInvalid, "promotions.coupon_ineligible", "the booking does not meet the coupon conditions")
	ErrUsageLimit       = domainerr.New(domainerr.KindConflict, "promotions.usage_limit_reached", "the coupon has no uses left")
	ErrNotCombinable    = domainerr.New(domainerr.KindInvalid, "promotions.not_combinable", "these discounts cannot be combined")
	ErrManualAdjustment = domainerr.New(domainerr.KindInvalid, "promotions.manual_adjustment_invalid", "a manual adjustment needs a reason and cannot exceed the line amount")
)

// Campaign is a discount program. Changing it never alters bookings
// already confirmed: they keep the Applied snapshot.
type Campaign struct {
	ID     CampaignID
	Code   string // coupon code, stored upper-case; empty for automatic types
	Name   string
	Type   Type
	Status Status
	// ValidFrom/ValidUntil bound use as [from, until) instants; Timezone is
	// for display of the campaign terms.
	ValidFrom  time.Time
	ValidUntil time.Time
	Timezone   string
	ValueType  ValueType
	Percent    int         // ValuePercent, 1..100
	Amount     money.Money // ValueFixed
	// MaxDiscount caps a percent discount (whole campaign; per line for
	// line discounts).
	MaxDiscount *money.Money
	// MinimumEligibleAmount is checked on the eligible subtotal.
	MinimumEligibleAmount money.Money
	// Eligible items and bundles; both empty means every line.
	EligibleItemIDs   []catalog.ItemID
	EligibleBundleIDs []catalog.BundleID
	// EligibleSegment is required for member discounts.
	EligibleSegment string
	// Usage limits; 0 means unlimited.
	UsageLimitTotal       int
	UsageLimitPerCustomer int
	// Combinable campaigns may stack with others; a non-combinable one only
	// applies alone. Nothing stacks implicitly.
	Combinable    bool
	Funding       Funding
	PolicyVersion string
	Version       int
}

// NormalizeCode upper-cases and trims a coupon code.
func NormalizeCode(code string) string { return strings.ToUpper(strings.TrimSpace(code)) }

// Validate checks a campaign definition.
func (c Campaign) Validate() error {
	bad := func(why string) error { return fmt.Errorf("campaign %s: %s: %w", c.ID, why, ErrCampaignInvalid) }
	switch {
	case c.ID == "" || strings.TrimSpace(c.Name) == "":
		return bad("id and name are required")
	case c.Type != TypeLine && c.Type != TypeCoupon && c.Type != TypeOrder && c.Type != TypeMember:
		return bad("unknown type")
	case c.Type == TypeCoupon && (c.Code == "" || c.Code != NormalizeCode(c.Code)):
		return bad("coupons need an upper-case code")
	case c.Type != TypeCoupon && c.Code != "":
		return bad("only coupons have codes")
	case c.Type == TypeMember && strings.TrimSpace(c.EligibleSegment) == "":
		return bad("member discounts need a segment")
	case c.Status != StatusDraft && c.Status != StatusActive && c.Status != StatusPaused && c.Status != StatusEnded:
		return bad("unknown status")
	case c.ValidFrom.IsZero() || !c.ValidUntil.After(c.ValidFrom):
		return bad("valid_until must be after valid_from")
	case c.UsageLimitTotal < 0 || c.UsageLimitPerCustomer < 0:
		return bad("usage limits must not be negative")
	case c.Funding != FundingMerchant:
		return bad("unknown funding source")
	case strings.TrimSpace(c.PolicyVersion) == "" || c.Version < 1:
		return bad("policy version and version are required")
	case !c.MinimumEligibleAmount.Currency().Valid() || c.MinimumEligibleAmount.IsNegative():
		return bad("minimum eligible amount must be zero or positive")
	}
	switch c.ValueType {
	case ValuePercent:
		if c.Percent < 1 || c.Percent > 100 {
			return bad("percent must be 1..100")
		}
		if c.MaxDiscount != nil && !c.MaxDiscount.IsPositive() {
			return bad("max discount must be positive")
		}
	case ValueFixed:
		if !c.Amount.Currency().Valid() || !c.Amount.IsPositive() {
			return bad("fixed amount must be positive")
		}
	default:
		return bad("unknown value type")
	}
	return nil
}

// Create authorises and validates a new campaign.
func Create(p identity.Principal, c Campaign) (Campaign, error) {
	if err := p.Require(identity.PermPromotionManage); err != nil {
		return Campaign{}, err
	}
	if err := c.Validate(); err != nil {
		return Campaign{}, err
	}
	c.EligibleItemIDs, c.EligibleBundleIDs = slices.Clone(c.EligibleItemIDs), slices.Clone(c.EligibleBundleIDs)
	return c, nil
}

func (c Campaign) live(now time.Time) bool {
	return c.Status == StatusActive && !now.Before(c.ValidFrom) && now.Before(c.ValidUntil)
}

func (c Campaign) eligibleLine(l Line) bool {
	if len(c.EligibleItemIDs) == 0 && len(c.EligibleBundleIDs) == 0 {
		return true
	}
	switch l.Kind {
	case catalog.KindItem:
		return slices.Contains(c.EligibleItemIDs, catalog.ItemID(l.OfferingID))
	case catalog.KindBundle:
		return slices.Contains(c.EligibleBundleIDs, catalog.BundleID(l.OfferingID))
	}
	return false
}
