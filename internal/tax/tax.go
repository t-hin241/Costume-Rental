// Package tax keeps the revenue ledger and the store tax profile for an
// individual business paying VAT directly on revenue
// (docs/note/tax-direct-discount-policy.md).
//
// The system never adds a VAT line at checkout and never hard-codes a rate.
// It classifies every revenue amount, tracks the annual revenue threshold and
// produces an internal VAT estimate only when the accountant has confirmed
// the rate and the treatment of every category involved. It is not a tax
// filing and does not issue invoices; e-invoicing goes through an adapter.
package tax

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
)

type (
	TaxpayerType string
	VATMethod    string
)

const (
	IndividualBusiness TaxpayerType = "INDIVIDUAL_BUSINESS"
	DirectOnRevenue    VATMethod    = "DIRECT_ON_REVENUE"
)

// Category classifies a money flow (policy §3). The code alone never decides
// tax treatment; Profile.Categories does, once confirmed by the accountant.
type Category string

const (
	CatRentalRevenue        Category = "RENTAL_REVENUE"
	CatDeliveryService      Category = "DELIVERY_SERVICE"
	CatCleaningService      Category = "CLEANING_SERVICE"
	CatLateReturnCharge     Category = "LATE_RETURN_CHARGE"
	CatDamageCharge         Category = "DAMAGE_CHARGE"
	CatLostItemCompensation Category = "LOST_ITEM_COMPENSATION"
	CatCancellationCharge   Category = "CANCELLATION_CHARGE"
	CatOtherCharge          Category = "OTHER_CHARGE"
	// Deposits are held money, never revenue entries. Retained amounts reach
	// the ledger as the fee categories above.
	CatBookingDeposit        Category = "BOOKING_DEPOSIT"
	CatRentalSecurityDeposit Category = "RENTAL_SECURITY_DEPOSIT"
)

func (c Category) revenue() bool {
	switch c {
	case CatRentalRevenue, CatDeliveryService, CatCleaningService, CatLateReturnCharge,
		CatDamageCharge, CatLostItemCompensation, CatCancellationCharge, CatOtherCharge:
		return true
	}
	return false
}

// Treatment is the accountant-confirmed tax treatment of a category.
type Treatment string

const (
	Unconfirmed Treatment = "UNCONFIRMED"
	Taxable     Treatment = "TAXABLE"
	NotTaxable  Treatment = "NOT_TAXABLE"
)

// DiscountBasis says whether discounts reduce the taxable base.
type DiscountBasis string

const (
	DiscountBasisUnconfirmed DiscountBasis = "UNCONFIRMED"
	DiscountBasisNet         DiscountBasis = "NET_OF_DISCOUNT"
	DiscountBasisGross       DiscountBasis = "GROSS"
)

// Rate is a VAT rate in basis points (500 = 5%). There is no default.
type Rate struct{ BasisPoints int }

var (
	ErrProfileInvalid = domainerr.New(domainerr.KindInvalid, "tax.profile_invalid", "tax profile is invalid")
	ErrNoProfile      = domainerr.New(domainerr.KindNotFound, "tax.no_profile", "no tax profile is effective at that time")
	ErrNotRevenue     = domainerr.New(domainerr.KindInvalid, "tax.not_revenue", "deposits are not revenue")
)

// Profile is one effective version of the store tax configuration
// (policy §2.1). Bookings and entries keep the Version they were made under.
type Profile struct {
	Version                string
	TaxpayerType           TaxpayerType
	VATMethod              VATMethod
	AnnualRevenueThreshold money.Money
	ThresholdEffectiveFrom businesstime.Date
	ActivityCode           string
	// VATRevenueRate is nil until the accountant confirms it.
	VATRevenueRate *Rate
	// Categories maps each category to its confirmed treatment; missing
	// means Unconfirmed.
	Categories    map[Category]Treatment
	DiscountBasis DiscountBasis
	// NearThresholdPercent raises the internal warning (e.g. 90 = at 90%).
	NearThresholdPercent int
	InvoiceRequiredMode  string
	EffectiveFrom        time.Time
	EffectiveUntil       *time.Time
}

// Validate checks a profile.
func (p Profile) Validate() error {
	bad := func(why string) error { return fmt.Errorf("tax profile %s: %s: %w", p.Version, why, ErrProfileInvalid) }
	switch {
	case strings.TrimSpace(p.Version) == "":
		return bad("version is required")
	case p.TaxpayerType != IndividualBusiness || p.VATMethod != DirectOnRevenue:
		return bad("only individual business with direct VAT on revenue is supported")
	case !p.AnnualRevenueThreshold.Currency().Valid() || !p.AnnualRevenueThreshold.IsPositive():
		return bad("annual revenue threshold must be positive")
	case !p.ThresholdEffectiveFrom.Valid():
		return bad("threshold effective date is required")
	case p.VATRevenueRate != nil && (p.VATRevenueRate.BasisPoints <= 0 || p.VATRevenueRate.BasisPoints > 10_000):
		return bad("rate must be between 0 and 100%")
	case p.NearThresholdPercent < 1 || p.NearThresholdPercent > 100:
		return bad("near-threshold warning must be 1..100%")
	case p.EffectiveFrom.IsZero() || (p.EffectiveUntil != nil && !p.EffectiveUntil.After(p.EffectiveFrom)):
		return bad("effective period is invalid")
	}
	switch p.DiscountBasis {
	case DiscountBasisUnconfirmed, DiscountBasisNet, DiscountBasisGross:
	default:
		return bad("unknown discount basis")
	}
	for c, t := range p.Categories {
		if !c.revenue() || (t != Unconfirmed && t != Taxable && t != NotTaxable) {
			return bad(fmt.Sprintf("invalid treatment for %s", c))
		}
	}
	return nil
}

func (p Profile) treatment(c Category) Treatment {
	if t, ok := p.Categories[c]; ok {
		return t
	}
	return Unconfirmed
}

func (p Profile) covers(t time.Time) bool {
	return !t.Before(p.EffectiveFrom) && (p.EffectiveUntil == nil || t.Before(*p.EffectiveUntil))
}

// History is the effective-dated list of profiles. Versions are appended,
// never edited, so past bookings and entries keep their profile.
type History struct {
	Profiles []Profile
}

// Add appends a new profile version (tax.manage). Its period must not overlap
// existing ones.
func (h *History) Add(p identity.Principal, prof Profile) error {
	if err := p.Require(identity.PermTaxManage); err != nil {
		return err
	}
	if err := prof.Validate(); err != nil {
		return err
	}
	for _, e := range h.Profiles {
		if e.Version == prof.Version {
			return fmt.Errorf("version %s exists: %w", prof.Version, ErrProfileInvalid)
		}
		if overlaps(e, prof) {
			return fmt.Errorf("version %s overlaps %s: %w", prof.Version, e.Version, ErrProfileInvalid)
		}
	}
	prof.Categories = maps.Clone(prof.Categories)
	h.Profiles = append(h.Profiles, prof)
	slices.SortFunc(h.Profiles, func(a, b Profile) int { return a.EffectiveFrom.Compare(b.EffectiveFrom) })
	return nil
}

// At returns the profile effective at t.
func (h History) At(t time.Time) (Profile, error) {
	for _, p := range h.Profiles {
		if p.covers(t) {
			return p, nil
		}
	}
	return Profile{}, fmt.Errorf("at %s: %w", t.UTC(), ErrNoProfile)
}

func overlaps(a, b Profile) bool {
	aEndsAfterBStarts := a.EffectiveUntil == nil || a.EffectiveUntil.After(b.EffectiveFrom)
	bEndsAfterAStarts := b.EffectiveUntil == nil || b.EffectiveUntil.After(a.EffectiveFrom)
	return aEndsAfterBStarts && bEndsAfterAStarts
}
