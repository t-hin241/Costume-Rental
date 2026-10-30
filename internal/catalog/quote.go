package catalog

import (
	"fmt"
	"slices"

	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/money"
)

// ItemComponentKey is the component key used for individually rented items.
const ItemComponentKey = "item"

// ComponentDemand is a physical variant a quoted line needs from inventory.
type ComponentDemand struct {
	Key       string
	ItemID    ItemID
	VariantID VariantID
	SKU       string
	// Quantity is the total units for the whole line (component quantity ×
	// line quantity).
	Quantity    int
	BufferClass string
	// UnitDailyRate is the late-fee base per unit, snapshotted with the line.
	UnitDailyRate money.Money
}

// LineQuote is a priced line computed from the current catalog. Rentals
// copies it into the booking snapshot; it shares no mutable state with the
// catalog entries it came from.
type LineQuote struct {
	Kind            OfferingKind
	OfferingID      string
	OfferingName    string
	OfferingVersion int
	Package         RentalPackage
	Quantity        int
	UnitRent        money.Money
	RentAmount      money.Money
	UnitDeposit     money.Money
	DepositAmount   money.Money
	Components      []ComponentDemand
}

// QuoteItem prices an individually rented item variant.
func QuoteItem(item Item, variantID VariantID, pkg RentalPackage, quantity int) (LineQuote, error) {
	if err := checkQuantityAndPackage(pkg, quantity); err != nil {
		return LineQuote{}, err
	}
	if item.Status != StatusActive {
		return LineQuote{}, fmt.Errorf("item %s: %w", item.ID, ErrOfferingUnavailable)
	}
	if !item.IndividuallyRentable {
		return LineQuote{}, fmt.Errorf("item %s: %w", item.ID, ErrNotIndividually)
	}
	v, ok := item.Variant(variantID)
	if !ok || v.Status != StatusActive {
		return LineQuote{}, fmt.Errorf("item %s variant %s: %w", item.ID, variantID, ErrVariantUnavailable)
	}
	unit, ok := item.Prices[pkg.ID]
	if !ok {
		return LineQuote{}, fmt.Errorf("item %s package %s: %w", item.ID, pkg.ID, ErrNoPriceForPackage)
	}
	return buildQuote(KindItem, string(item.ID), item.Name, item.Version, pkg, quantity, unit, item.SecurityDeposit,
		[]ComponentDemand{{
			Key: ItemComponentKey, ItemID: item.ID, VariantID: v.ID, SKU: v.SKU,
			Quantity: quantity, BufferClass: item.BufferClass, UnitDailyRate: item.DailyRate,
		}})
}

// QuoteBundle prices a bundle with the customer's per-component variant
// choices (e.g. jacket M, trousers L). A component with a single option may
// be omitted from selections.
func QuoteBundle(b Bundle, items map[ItemID]Item, selections map[string]VariantID, pkg RentalPackage, quantity int) (LineQuote, error) {
	if err := checkQuantityAndPackage(pkg, quantity); err != nil {
		return LineQuote{}, err
	}
	if b.Status != StatusActive {
		return LineQuote{}, fmt.Errorf("bundle %s: %w", b.ID, ErrOfferingUnavailable)
	}
	unit, ok := b.Prices[pkg.ID]
	if !ok {
		return LineQuote{}, fmt.Errorf("bundle %s package %s: %w", b.ID, pkg.ID, ErrNoPriceForPackage)
	}
	for key := range selections {
		if !slices.ContainsFunc(b.Components, func(c BundleComponent) bool { return c.Key == key }) {
			return LineQuote{}, fmt.Errorf("bundle %s unknown component %q: %w", b.ID, key, ErrInvalidSelection)
		}
	}

	demands := make([]ComponentDemand, 0, len(b.Components))
	componentDeposits := make([]money.Money, 0, len(b.Components))
	for _, c := range b.Components {
		item, ok := items[c.ItemID]
		if !ok {
			return LineQuote{}, fmt.Errorf("bundle %s component %s item %s: %w", b.ID, c.Key, c.ItemID, ErrOfferingUnavailable)
		}
		chosen, ok := selections[c.Key]
		if !ok {
			if len(c.Options) != 1 {
				return LineQuote{}, fmt.Errorf("bundle %s component %s needs a variant choice: %w", b.ID, c.Key, ErrInvalidSelection)
			}
			chosen = c.Options[0]
		}
		if !slices.Contains(c.Options, chosen) {
			return LineQuote{}, fmt.Errorf("bundle %s component %s variant %s not allowed: %w", b.ID, c.Key, chosen, ErrInvalidSelection)
		}
		v, ok := item.Variant(chosen)
		if !ok || v.Status != StatusActive {
			return LineQuote{}, fmt.Errorf("bundle %s component %s variant %s: %w", b.ID, c.Key, chosen, ErrVariantUnavailable)
		}
		total, err := mulInt(c.Quantity, quantity)
		if err != nil {
			return LineQuote{}, err
		}
		demands = append(demands, ComponentDemand{
			Key: c.Key, ItemID: item.ID, VariantID: v.ID, SKU: v.SKU,
			Quantity: total, BufferClass: item.BufferClass, UnitDailyRate: item.DailyRate,
		})
		d, err := item.SecurityDeposit.Mul(int64(c.Quantity))
		if err != nil {
			return LineQuote{}, fmt.Errorf("bundle %s component %s deposit: %w", b.ID, c.Key, err)
		}
		componentDeposits = append(componentDeposits, d)
	}

	var unitDeposit money.Money
	if b.DepositOverride != nil {
		unitDeposit = *b.DepositOverride
	} else {
		var err error
		if unitDeposit, err = money.Sum(unit.Currency(), componentDeposits...); err != nil {
			return LineQuote{}, fmt.Errorf("bundle %s deposit: %w", b.ID, err)
		}
	}
	return buildQuote(KindBundle, string(b.ID), b.Name, b.Version, pkg, quantity, unit, unitDeposit, demands)
}

func buildQuote(kind OfferingKind, id, name string, version int, pkg RentalPackage, quantity int,
	unitRent, unitDeposit money.Money, demands []ComponentDemand) (LineQuote, error) {
	rent, err := unitRent.Mul(int64(quantity))
	if err != nil {
		return LineQuote{}, fmt.Errorf("%s %s rent: %w", kind, id, err)
	}
	deposit, err := unitDeposit.Mul(int64(quantity))
	if err != nil {
		return LineQuote{}, fmt.Errorf("%s %s deposit: %w", kind, id, err)
	}
	if deposit.Currency() != rent.Currency() {
		return LineQuote{}, fmt.Errorf("%s %s: %w", kind, id, money.ErrCurrencyMismatch)
	}
	return LineQuote{
		Kind: kind, OfferingID: id, OfferingName: name, OfferingVersion: version,
		Package: pkg, Quantity: quantity,
		UnitRent: unitRent, RentAmount: rent,
		UnitDeposit: unitDeposit, DepositAmount: deposit,
		Components: demands,
	}, nil
}

func checkQuantityAndPackage(pkg RentalPackage, quantity int) error {
	if quantity < 1 {
		return domainerr.Invalid("quantity", "must be at least 1")
	}
	if err := pkg.Validate(); err != nil {
		return err
	}
	if pkg.Status != StatusActive {
		return fmt.Errorf("package %s: %w", pkg.ID, ErrPackageUnavailable)
	}
	return nil
}

func mulInt(a, b int) (int, error) {
	const limit = 1 << 20
	if a > limit || b > limit {
		return 0, domainerr.Invalid("quantity", "too large")
	}
	return a * b, nil
}

// Clone returns a deep copy so callers can store the quote safely.
func (q LineQuote) Clone() LineQuote {
	q.Components = slices.Clone(q.Components)
	return q
}
