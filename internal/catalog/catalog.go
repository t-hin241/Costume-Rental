// Package catalog owns what customers can rent: items with size/colour
// variants, bundle definitions composed of variants, rental packages and
// their prices, and security deposit amounts.
//
// Catalog never decides availability or payment outcomes; it resolves a
// customer's selection into priced lines and the physical variants they need.
package catalog

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/money"
)

type (
	ItemID    string
	VariantID string
	BundleID  string
	PackageID string
)

// Status controls whether an entry can be rented. Only ACTIVE entries are
// public; ARCHIVED keeps history linkable instead of deleting.
type Status string

const (
	StatusDraft    Status = "DRAFT"
	StatusActive   Status = "ACTIVE"
	StatusArchived Status = "ARCHIVED"
)

func (s Status) valid() bool {
	return s == StatusDraft || s == StatusActive || s == StatusArchived
}

// OfferingKind distinguishes a single item line from a bundle line.
type OfferingKind string

const (
	KindItem   OfferingKind = "ITEM"
	KindBundle OfferingKind = "BUNDLE"
)

var (
	ErrOfferingUnavailable = domainerr.New(domainerr.KindInvalid, "catalog.offering_unavailable", "offering is not active")
	ErrNotIndividually     = domainerr.New(domainerr.KindInvalid, "catalog.not_individually_rentable", "item can only be rented as part of a bundle")
	ErrVariantUnavailable  = domainerr.New(domainerr.KindInvalid, "catalog.variant_unavailable", "variant does not exist or is not active")
	ErrPackageUnavailable  = domainerr.New(domainerr.KindInvalid, "catalog.package_unavailable", "rental package is not active")
	ErrNoPriceForPackage   = domainerr.New(domainerr.KindInvalid, "catalog.no_price_for_package", "offering has no price for this package")
	ErrInvalidSelection    = domainerr.New(domainerr.KindInvalid, "catalog.invalid_selection", "bundle component selection is invalid")
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

const (
	maxNameLength = 200
	maxSlugLength = 120
)

// RentalPackage is a rental duration offered for pricing, e.g. "3 ngày" =
// 72h. Duration, not Label, is the definition. Booking durations must match
// a package exactly (owner decision); nothing is rounded.
type RentalPackage struct {
	ID       PackageID
	Code     string
	Label    string
	Duration time.Duration
	Status   Status
}

// Validate checks a package definition.
func (p RentalPackage) Validate() error {
	switch {
	case p.ID == "":
		return domainerr.Invalid("package.id", "required")
	case strings.TrimSpace(p.Code) == "":
		return domainerr.Invalid("package.code", "required")
	case strings.TrimSpace(p.Label) == "":
		return domainerr.Invalid("package.label", "required")
	case p.Duration <= 0 || p.Duration%time.Hour != 0:
		return domainerr.Invalid("package.duration", "must be a positive whole number of hours")
	case !p.Status.valid():
		return domainerr.Invalid("package.status", "unknown status")
	}
	return nil
}

// Prices maps a package to the rental price for one unit of an offering.
type Prices map[PackageID]money.Money

func (ps Prices) validate(field string) error {
	for id, price := range ps {
		if id == "" {
			return domainerr.Invalid(field, "package id is required")
		}
		if !price.Currency().Valid() || !price.IsPositive() {
			return domainerr.Invalid(field, fmt.Sprintf("price for %s must be a positive amount in a supported currency", id))
		}
	}
	return nil
}

// Variant is a SKU: a non-interchangeable size/colour of an item and its own
// inventory source.
type Variant struct {
	ID     VariantID
	SKU    string
	Size   string
	Color  string
	Status Status
}

// Item is a rentable garment or accessory family, e.g. "Áo khoác Naruto".
type Item struct {
	ID     ItemID
	Slug   string
	Name   string
	Status Status
	// IndividuallyRentable=false means the item is only rentable inside a
	// bundle.
	IndividuallyRentable bool
	Variants             []Variant
	// Prices are per unit when rented individually.
	Prices Prices
	// SecurityDeposit is the refundable deposit per unit, charged whether the
	// item is rented alone or as a bundle component (unless the bundle
	// overrides it).
	SecurityDeposit money.Money
	// DailyRate is the item's rental price per day, the base for late-return
	// fees (fee policy §2.1). It is set even for items only rented in bundles.
	DailyRate money.Money
	// BufferClass selects the preparation/turnaround buffer in rental policy.
	BufferClass string
	// Version increments on every change; bookings snapshot it.
	Version int
}

// Validate checks an item's own invariants. Slug/SKU uniqueness across the
// catalog is enforced by the repository.
func (it Item) Validate() error {
	if err := validateHeader(string(it.ID), it.Slug, it.Name, it.Status, it.Version); err != nil {
		return err
	}
	if len(it.Variants) == 0 {
		return domainerr.Invalid("variants", "at least one variant is required")
	}
	ids := map[VariantID]bool{}
	skus := map[string]bool{}
	for _, v := range it.Variants {
		switch {
		case v.ID == "" || strings.TrimSpace(v.SKU) == "":
			return domainerr.Invalid("variants", "id and sku are required")
		case ids[v.ID] || skus[v.SKU]:
			return domainerr.Invalid("variants", "variant ids and skus must be unique")
		case !v.Status.valid():
			return domainerr.Invalid("variants", "unknown status")
		}
		ids[v.ID], skus[v.SKU] = true, true
	}
	if err := it.Prices.validate("prices"); err != nil {
		return err
	}
	if it.Status == StatusActive && it.IndividuallyRentable && len(it.Prices) == 0 {
		return domainerr.Invalid("prices", "an active individually rentable item needs at least one price")
	}
	if !it.SecurityDeposit.Currency().Valid() || it.SecurityDeposit.IsNegative() {
		return domainerr.Invalid("security_deposit", "must be zero or positive in a supported currency")
	}
	if !it.DailyRate.Currency().Valid() || !it.DailyRate.IsPositive() {
		return domainerr.Invalid("daily_rate", "must be positive in a supported currency")
	}
	if strings.TrimSpace(it.BufferClass) == "" {
		return domainerr.Invalid("buffer_class", "required")
	}
	return nil
}

// Variant finds a variant by ID.
func (it Item) Variant(id VariantID) (Variant, bool) {
	i := slices.IndexFunc(it.Variants, func(v Variant) bool { return v.ID == id })
	if i < 0 {
		return Variant{}, false
	}
	return it.Variants[i], true
}

// PubliclyListed reports whether guests may see the item as its own offering.
func (it Item) PubliclyListed() bool {
	return it.Status == StatusActive && it.IndividuallyRentable
}

// BundleComponent is one slot of a bundle: which item, which variants the
// customer may pick for it (e.g. sizes), and how many units one bundle needs.
type BundleComponent struct {
	Key      string
	ItemID   ItemID
	Options  []VariantID
	Quantity int
}

// Bundle is a recipe over shared inventory. Renting a bundle allocates each
// component's variant; it is never an extra stock pool.
type Bundle struct {
	ID         BundleID
	Slug       string
	Name       string
	Status     Status
	Components []BundleComponent
	// Prices are the bundle's own package prices (owner decision): renting
	// the bundle never adds component prices on top.
	Prices Prices
	// DepositOverride, when set, replaces the sum of component deposits for
	// one bundle. Component deposits are then not charged, so the same
	// responsibility is never charged twice.
	DepositOverride *money.Money
	Version         int
}

// Validate checks the bundle against the items it references.
func (b Bundle) Validate(items map[ItemID]Item) error {
	if err := validateHeader(string(b.ID), b.Slug, b.Name, b.Status, b.Version); err != nil {
		return err
	}
	if len(b.Components) == 0 {
		return domainerr.Invalid("components", "at least one component is required")
	}
	keys := map[string]bool{}
	for _, c := range b.Components {
		if strings.TrimSpace(c.Key) == "" || keys[c.Key] {
			return domainerr.Invalid("components", "component keys must be present and unique")
		}
		keys[c.Key] = true
		if c.Quantity < 1 {
			return domainerr.Invalid("components", fmt.Sprintf("component %s quantity must be at least 1", c.Key))
		}
		item, ok := items[c.ItemID]
		if !ok {
			return domainerr.Invalid("components", fmt.Sprintf("component %s references unknown item %s", c.Key, c.ItemID))
		}
		if len(c.Options) == 0 {
			return domainerr.Invalid("components", fmt.Sprintf("component %s needs at least one variant option", c.Key))
		}
		seen := map[VariantID]bool{}
		for _, opt := range c.Options {
			if _, ok := item.Variant(opt); !ok || seen[opt] {
				return domainerr.Invalid("components", fmt.Sprintf("component %s option %s is unknown or duplicated", c.Key, opt))
			}
			seen[opt] = true
		}
	}
	if err := b.Prices.validate("prices"); err != nil {
		return err
	}
	if b.Status == StatusActive && len(b.Prices) == 0 {
		return domainerr.Invalid("prices", "an active bundle needs at least one price")
	}
	if b.DepositOverride != nil && (!b.DepositOverride.Currency().Valid() || b.DepositOverride.IsNegative()) {
		return domainerr.Invalid("deposit_override", "must be zero or positive in a supported currency")
	}
	return nil
}

func validateHeader(id, slug, name string, status Status, version int) error {
	switch {
	case id == "":
		return domainerr.Invalid("id", "required")
	case len(slug) > maxSlugLength || !slugPattern.MatchString(slug):
		return domainerr.Invalid("slug", "must be lowercase letters, digits and single hyphens")
	case strings.TrimSpace(name) == "" || len(name) > maxNameLength:
		return domainerr.Invalid("name", fmt.Sprintf("required, at most %d bytes", maxNameLength))
	case !status.valid():
		return domainerr.Invalid("status", "unknown status")
	case version < 1:
		return domainerr.Invalid("version", "must be at least 1")
	}
	return nil
}
