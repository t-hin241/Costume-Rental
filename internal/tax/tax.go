// Package tax keeps the revenue ledger and the annual threshold watch of an
// individual business that the owner runs under the annual revenue threshold
// (decision D41, docs/note/tax-policy.md).
//
// The system computes no tax: there is no rate, no VAT line at checkout and no
// invoice. It classifies every revenue amount, records it in an append-only
// ledger and warns when the calendar-year revenue reaches the configured marks
// or passes the threshold. Passing it changes nothing automatically: the owner
// reviews the tax position with an accountant.
package tax

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"webee_rentor/internal/businesstime"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
)

type TaxpayerType string

const IndividualBusiness TaxpayerType = "INDIVIDUAL_BUSINESS"

// Category classifies a revenue amount (tax-policy §4). It is for reporting
// and reconciliation; the system attaches no tax treatment to it.
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

// RevenueCategories lists the categories a revenue entry may have.
var RevenueCategories = []Category{
	CatRentalRevenue, CatDeliveryService, CatCleaningService, CatLateReturnCharge,
	CatDamageCharge, CatLostItemCompensation, CatCancellationCharge, CatOtherCharge,
}

func (c Category) revenue() bool { return slices.Contains(RevenueCategories, c) }

const (
	maxVersion  = 64
	maxWarnings = 5
)

var (
	ErrProfileInvalid = domainerr.New(domainerr.KindInvalid, "tax.profile_invalid", "tax profile is invalid")
	ErrNoProfile      = domainerr.New(domainerr.KindNotFound, "tax.no_profile", "no tax profile is effective at that time")
	ErrNotRevenue     = domainerr.New(domainerr.KindInvalid, "tax.not_revenue", "deposits are not revenue")
	ErrRetroactive    = domainerr.New(domainerr.KindConflict, "tax.profile_retroactive", "a new tax profile version cannot start in the past")
)

// Profile is one effective version of the store tax configuration. Entries
// keep the Version they were recognised under.
type Profile struct {
	Version                string
	TaxpayerType           TaxpayerType
	AnnualRevenueThreshold money.Money
	ThresholdEffectiveFrom businesstime.Date
	// WarningPercents are the ascending shares of the threshold at which the
	// internal warning rises (decision D41: 70, 85, 95). Passing the threshold
	// itself is always a warning.
	WarningPercents []int
	EffectiveFrom   time.Time
	// EffectiveUntil is set when a newer version takes over.
	EffectiveUntil *time.Time
}

// Validate checks a profile.
func (p Profile) Validate() error {
	bad := func(why string) error { return fmt.Errorf("tax profile %s: %s: %w", p.Version, why, ErrProfileInvalid) }
	switch {
	case strings.TrimSpace(p.Version) == "" || utf8.RuneCountInString(p.Version) > maxVersion:
		return bad("version is required, at most 64 characters")
	case p.TaxpayerType != IndividualBusiness:
		return bad("only an individual business is supported")
	case p.AnnualRevenueThreshold.Currency() != money.VND || !p.AnnualRevenueThreshold.IsPositive():
		return bad("annual revenue threshold must be a positive VND amount")
	case !p.ThresholdEffectiveFrom.Valid():
		return bad("threshold effective date is required")
	case len(p.WarningPercents) == 0 || len(p.WarningPercents) > maxWarnings:
		return bad("one to five warning marks are required")
	case p.EffectiveFrom.IsZero() || (p.EffectiveUntil != nil && !p.EffectiveUntil.After(p.EffectiveFrom)):
		return bad("effective period is invalid")
	}
	for i, w := range p.WarningPercents {
		if w < 1 || w > 99 || (i > 0 && w <= p.WarningPercents[i-1]) {
			return bad("warning marks must be ascending percentages between 1 and 99")
		}
	}
	return nil
}

func (p Profile) covers(t time.Time) bool {
	return !t.Before(p.EffectiveFrom) && (p.EffectiveUntil == nil || t.Before(*p.EffectiveUntil))
}

// History is the effective-dated list of profiles, oldest first. Versions are
// appended, never edited; the only change to a stored version is the end date
// a newer one gives it.
type History struct {
	Profiles []Profile
}

// Add appends a new version (tax.manage) that runs from its EffectiveFrom with
// no end, and returns the version it closes, if any. The first version may
// start in the past so that bookings that ended before it are recognised;
// later versions start now or later, so recognised entries never change
// profile.
func (h *History) Add(p identity.Principal, prof Profile, now time.Time) (*Profile, error) {
	if err := p.Require(identity.PermTaxManage); err != nil {
		return nil, err
	}
	prof.EffectiveFrom, prof.EffectiveUntil = prof.EffectiveFrom.UTC(), nil
	prof.WarningPercents = slices.Clone(prof.WarningPercents)
	if err := prof.Validate(); err != nil {
		return nil, err
	}
	for _, e := range h.Profiles {
		if e.Version == prof.Version {
			return nil, fmt.Errorf("version %s exists: %w", prof.Version, ErrProfileInvalid)
		}
	}
	var closed *Profile
	if n := len(h.Profiles); n > 0 {
		if prof.EffectiveFrom.Before(now) {
			return nil, ErrRetroactive
		}
		last := &h.Profiles[n-1]
		if last.EffectiveUntil != nil || !last.EffectiveFrom.Before(prof.EffectiveFrom) {
			return nil, fmt.Errorf("version %s must start after %s: %w", prof.Version, last.Version, ErrProfileInvalid)
		}
		until := prof.EffectiveFrom
		last.EffectiveUntil = &until
		c := *last
		closed = &c
	}
	h.Profiles = append(h.Profiles, prof)
	return closed, nil
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
