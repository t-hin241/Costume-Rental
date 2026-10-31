// Package catalogapp runs the catalog use cases: what guests browse and what
// staff with catalog.manage maintain. Bookings keep their own snapshot, so
// nothing here changes a booking already made.
package catalogapp

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"webee_rentor/internal/audit"
	"webee_rentor/internal/audit/auditpg"
	"webee_rentor/internal/catalog"
	"webee_rentor/internal/catalog/catalogpg"
	"webee_rentor/internal/domainerr"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
	"webee_rentor/internal/platform/ids"
	"webee_rentor/internal/platform/postgres"
)

// Service holds the dependencies of the catalog use cases.
type Service struct {
	pool  *pgxpool.Pool
	now   func() time.Time
	newID func() string
}

// New builds the service.
func New(pool *pgxpool.Pool, opts ...Option) *Service {
	s := &Service{pool: pool, now: time.Now, newID: ids.New}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Option configures the service.
type Option func(*Service)

// WithClock replaces time.Now, for tests.
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// PackagePrice is the rent of an offering for one active package.
type PackagePrice struct {
	Package catalog.RentalPackage
	Price   money.Money
}

// Offering is what a customer browses: an item rented alone or a bundle.
type Offering struct {
	Kind            catalog.OfferingKind
	ID              string
	Slug            string
	Name            string
	Category        *catalog.Category
	Images          []catalog.Image
	Prices          []PackagePrice
	Sizes           []string
	Colors          []string
	ComponentCount  int
	Description     string
	Variants        []catalog.Variant
	Components      []Component
	SecurityDeposit money.Money

	bookings int
	updated  time.Time
}

// Component is one slot of a bundle with the variants a customer may pick.
type Component struct {
	Key      string
	ItemName string
	Quantity int
	Options  []catalog.Variant
}

// CategoryView is a category with how many offerings it shows.
type CategoryView struct {
	catalog.Category
	OfferingCount int
}

// Query filters browsing; empty fields do not filter.
type Query struct {
	Term     string
	Category string
	Kind     catalog.OfferingKind
	Size     string
	Color    string
	Sort     string
	Page     int
	PageSize int
}

// Result is one page of offerings and the sizes and colours to filter by.
type Result struct {
	Offerings []Offering
	Total     int
	Sizes     []string
	Colors    []string
}

// Packages lists rental packages. Guests only see active ones.
func (s *Service) Packages(ctx context.Context, p *identity.Principal) ([]catalog.RentalPackage, error) {
	all, err := catalogpg.Packages(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	if p != nil && p.Can(identity.PermCatalogManage) {
		return all, nil
	}
	return slices.DeleteFunc(all, func(x catalog.RentalPackage) bool { return x.Status != catalog.StatusActive }), nil
}

// catalogRead is one consistent view of what is on sale.
type catalogRead struct {
	items      map[catalog.ItemID]catalog.Item
	bundles    []catalog.Bundle
	categories []catalog.Category
	packages   map[catalog.PackageID]catalog.RentalPackage
	stats      map[string]catalogpg.OfferingStats
}

func (s *Service) read(ctx context.Context) (catalogRead, error) {
	var c catalogRead
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		if c.items, err = catalogpg.AllItems(ctx, tx); err != nil {
			return err
		}
		if c.bundles, err = catalogpg.AllBundles(ctx, tx); err != nil {
			return err
		}
		if c.categories, err = catalogpg.Categories(ctx, tx); err != nil {
			return err
		}
		pkgs, err := catalogpg.Packages(ctx, tx)
		if err != nil {
			return err
		}
		c.packages = make(map[catalog.PackageID]catalog.RentalPackage, len(pkgs))
		for _, p := range pkgs {
			c.packages[p.ID] = p
		}
		c.stats, err = catalogpg.Stats(ctx, tx)
		return err
	})
	if err != nil {
		return catalogRead{}, fmt.Errorf("read catalog: %w", err)
	}
	return c, nil
}

func (c catalogRead) category(id catalog.CategoryID) *catalog.Category {
	i := slices.IndexFunc(c.categories, func(x catalog.Category) bool { return x.ID == id })
	if i < 0 {
		return nil
	}
	return &c.categories[i]
}

// prices lists a price per active package, cheapest first.
func (c catalogRead) prices(ps catalog.Prices) []PackagePrice {
	out := make([]PackagePrice, 0, len(ps))
	for id, price := range ps {
		if pkg, ok := c.packages[id]; ok && pkg.Status == catalog.StatusActive {
			out = append(out, PackagePrice{Package: pkg, Price: price})
		}
	}
	slices.SortFunc(out, func(a, b PackagePrice) int {
		return cmp.Or(cmp.Compare(a.Price.Amount(), b.Price.Amount()), cmp.Compare(a.Package.Duration, b.Package.Duration))
	})
	return out
}

func activeVariants(vs []catalog.Variant) []catalog.Variant {
	return slices.DeleteFunc(slices.Clone(vs), func(v catalog.Variant) bool { return v.Status != catalog.StatusActive })
}

func collect(sizes, colors []string, vs []catalog.Variant) ([]string, []string) {
	for _, v := range vs {
		if v.Size != "" && !slices.Contains(sizes, v.Size) {
			sizes = append(sizes, v.Size)
		}
		if v.Color != "" && !slices.Contains(colors, v.Color) {
			colors = append(colors, v.Color)
		}
	}
	return sizes, colors
}

func (c catalogRead) itemOffering(it catalog.Item) Offering {
	vs := activeVariants(it.Variants)
	o := Offering{
		Kind: catalog.KindItem, ID: string(it.ID), Slug: it.Slug, Name: it.Name, Category: c.category(it.CategoryID), Images: it.Images,
		Prices: c.prices(it.Prices), ComponentCount: 1, Description: it.Description, Variants: vs, SecurityDeposit: it.SecurityDeposit,
		bookings: c.stats[string(it.ID)].Bookings, updated: c.stats[string(it.ID)].UpdatedAt,
	}
	o.Sizes, o.Colors = collect(nil, nil, vs)
	return o
}

func (c catalogRead) bundleOffering(b catalog.Bundle) (Offering, error) {
	deposit, err := b.UnitDeposit(c.items)
	if err != nil {
		return Offering{}, err
	}
	o := Offering{
		Kind: catalog.KindBundle, ID: string(b.ID), Slug: b.Slug, Name: b.Name, Category: c.category(b.CategoryID), Images: b.Images,
		Prices: c.prices(b.Prices), ComponentCount: len(b.Components), Description: b.Description, SecurityDeposit: deposit,
		bookings: c.stats[string(b.ID)].Bookings, updated: c.stats[string(b.ID)].UpdatedAt,
	}
	for _, comp := range b.Components {
		it := c.items[comp.ItemID]
		var options []catalog.Variant
		for _, id := range comp.Options {
			if v, ok := it.Variant(id); ok && v.Status == catalog.StatusActive {
				options = append(options, v)
			}
		}
		o.Components = append(o.Components, Component{Key: comp.Key, ItemName: it.Name, Quantity: comp.Quantity, Options: options})
		o.Sizes, o.Colors = collect(o.Sizes, o.Colors, options)
	}
	return o, nil
}

// onSale lists offerings guests may see: active, individually rentable items
// and active bundles that have a price for an active package.
func (c catalogRead) onSale() ([]Offering, error) {
	var out []Offering
	for _, it := range c.items {
		if o := c.itemOffering(it); it.PubliclyListed() && len(o.Prices) > 0 && len(o.Variants) > 0 {
			out = append(out, o)
		}
	}
	for _, b := range c.bundles {
		o, err := c.bundleOffering(b)
		if err != nil {
			return nil, err
		}
		if b.Status == catalog.StatusActive && len(o.Prices) > 0 && componentsAvailable(o) {
			out = append(out, o)
		}
	}
	return out, nil
}

func componentsAvailable(o Offering) bool {
	return !slices.ContainsFunc(o.Components, func(c Component) bool { return len(c.Options) == 0 })
}

// fold makes search ignore case and Vietnamese accents.
func fold(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	out, _, err := transform.String(t, s)
	if err != nil {
		out = s
	}
	return strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(out), "đ", "d"), "Đ", "d")
}

func matches(o Offering, q Query) bool {
	switch {
	case q.Term != "" && !strings.Contains(fold(o.Name), fold(q.Term)):
		return false
	case q.Category != "" && (o.Category == nil || o.Category.Slug != q.Category):
		return false
	case q.Kind != "" && o.Kind != q.Kind:
		return false
	case q.Size != "" && !slices.Contains(o.Sizes, q.Size):
		return false
	case q.Color != "" && !slices.Contains(o.Colors, q.Color):
		return false
	}
	return true
}

func sortOfferings(all []Offering, by string) {
	price := func(o Offering) int64 { return o.Prices[0].Price.Amount() }
	slices.SortFunc(all, func(a, b Offering) int {
		var c int
		switch by {
		case "price_asc":
			c = cmp.Compare(price(a), price(b))
		case "price_desc":
			c = cmp.Compare(price(b), price(a))
		case "newest":
			c = b.updated.Compare(a.updated)
		default:
			c = cmp.Compare(b.bookings, a.bookings)
		}
		return cmp.Or(c, cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
}

// Offerings lists what is on sale. The catalog of one shop fits in memory, so
// filters run here and search can ignore Vietnamese accents.
func (s *Service) Offerings(ctx context.Context, q Query) (Result, error) {
	c, err := s.read(ctx)
	if err != nil {
		return Result{}, err
	}
	all, err := c.onSale()
	if err != nil {
		return Result{}, err
	}
	var res Result
	for _, o := range all {
		res.Sizes, res.Colors = collect(res.Sizes, res.Colors, append(slices.Clone(o.Variants), optionVariants(o)...))
	}
	slices.Sort(res.Sizes)
	slices.Sort(res.Colors)
	found := slices.DeleteFunc(all, func(o Offering) bool { return !matches(o, q) })
	sortOfferings(found, q.Sort)
	res.Total = len(found)
	start := min((q.Page-1)*q.PageSize, len(found))
	res.Offerings = found[start:min(start+q.PageSize, len(found))]
	return res, nil
}

func optionVariants(o Offering) []catalog.Variant {
	var out []catalog.Variant
	for _, c := range o.Components {
		out = append(out, c.Options...)
	}
	return out
}

// Offering reads one offering on sale by slug.
func (s *Service) Offering(ctx context.Context, kind catalog.OfferingKind, slug string) (Offering, error) {
	c, err := s.read(ctx)
	if err != nil {
		return Offering{}, err
	}
	all, err := c.onSale()
	if err != nil {
		return Offering{}, err
	}
	i := slices.IndexFunc(all, func(o Offering) bool { return o.Kind == kind && o.Slug == slug })
	if i < 0 {
		return Offering{}, fmt.Errorf("offering %s %s: %w", kind, slug, postgres.ErrNotFound)
	}
	return all[i], nil
}

// Categories lists categories with how many offerings are on sale in each.
func (s *Service) Categories(ctx context.Context) ([]CategoryView, error) {
	c, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	all, err := c.onSale()
	if err != nil {
		return nil, err
	}
	out := make([]CategoryView, 0, len(c.categories))
	for _, cat := range c.categories {
		n := 0
		for _, o := range all {
			if o.Category != nil && o.Category.ID == cat.ID {
				n++
			}
		}
		out = append(out, CategoryView{Category: cat, OfferingCount: n})
	}
	return out, nil
}

// AdminItem is an item with its asset count, for catalog management.
type AdminItem struct {
	catalog.Item
	AssetCount int
}

// AdminItems lists every item, whatever its status.
func (s *Service) AdminItems(ctx context.Context, p identity.Principal) ([]AdminItem, error) {
	if err := p.Require(identity.PermCatalogManage); err != nil {
		return nil, err
	}
	items, err := catalogpg.AllItems(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	counts, err := catalogpg.AssetCounts(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	out := make([]AdminItem, 0, len(items))
	for _, it := range items {
		out = append(out, AdminItem{Item: it, AssetCount: counts[it.ID]})
	}
	slices.SortFunc(out, func(a, b AdminItem) int { return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID)) })
	return out, nil
}

// AdminBundles lists every bundle, whatever its status.
func (s *Service) AdminBundles(ctx context.Context, p identity.Principal) ([]catalog.Bundle, error) {
	if err := p.Require(identity.PermCatalogManage); err != nil {
		return nil, err
	}
	return catalogpg.AllBundles(ctx, s.pool)
}

func (s *Service) audit(ctx context.Context, q postgres.Querier, p identity.Principal, action, kind, id string, now time.Time) error {
	return auditpg.Append(ctx, q, audit.Event{
		ID: s.newID(), OccurredAt: now, Actor: p.Actor(), Action: action, ResourceType: kind, ResourceID: id, Result: audit.ResultSuccess,
	})
}

// SaveItem creates an item (id empty) or stores a new version of it. Variants
// are never removed because assets and bookings point at them: one left out
// of the input is archived. New variants get their id here.
func (s *Service) SaveItem(ctx context.Context, p identity.Principal, id catalog.ItemID, in catalog.Item) (catalog.Item, error) {
	if err := p.Require(identity.PermCatalogManage); err != nil {
		return catalog.Item{}, err
	}
	var out catalog.Item
	err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		now := s.now().UTC()
		in.Version = 1
		in.ID = id
		if id == "" {
			in.ID = catalog.ItemID(s.newID())
		} else {
			current, err := catalogpg.Items(ctx, tx, []catalog.ItemID{id})
			if err != nil {
				return err
			}
			in.Version = current[id].Version + 1
			for _, v := range current[id].Variants {
				if !slices.ContainsFunc(in.Variants, func(x catalog.Variant) bool { return x.ID == v.ID }) {
					v.Status = catalog.StatusArchived
					in.Variants = append(in.Variants, v)
				}
			}
		}
		for i := range in.Variants {
			if in.Variants[i].ID == "" {
				in.Variants[i].ID = catalog.VariantID(s.newID())
			}
		}
		if err := catalogpg.SaveItem(ctx, tx, in, now); err != nil {
			return err
		}
		out = in
		return s.audit(ctx, tx, p, "catalog.item_saved", "catalog_item", string(in.ID), now)
	})
	if err != nil {
		return catalog.Item{}, fmt.Errorf("save item: %w", err)
	}
	return out, nil
}

// SaveBundle creates a bundle (id empty) or stores a new version of it.
func (s *Service) SaveBundle(ctx context.Context, p identity.Principal, id catalog.BundleID, in catalog.Bundle) (catalog.Bundle, error) {
	if err := p.Require(identity.PermCatalogManage); err != nil {
		return catalog.Bundle{}, err
	}
	var out catalog.Bundle
	err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		now := s.now().UTC()
		in.Version = 1
		in.ID = id
		if id == "" {
			in.ID = catalog.BundleID(s.newID())
		} else {
			current, err := catalogpg.Bundle(ctx, tx, id)
			if err != nil {
				return err
			}
			in.Version = current.Version + 1
		}
		itemIDs := make([]catalog.ItemID, 0, len(in.Components))
		for _, c := range in.Components {
			if c.ItemID == "" {
				return domainerr.Invalid("components", "every component needs an item")
			}
			itemIDs = append(itemIDs, c.ItemID)
		}
		items, err := catalogpg.Items(ctx, tx, itemIDs)
		if err != nil {
			return err
		}
		if err := catalogpg.SaveBundle(ctx, tx, in, items, now); err != nil {
			return err
		}
		out = in
		return s.audit(ctx, tx, p, "catalog.bundle_saved", "bundle", string(in.ID), now)
	})
	if err != nil {
		return catalog.Bundle{}, fmt.Errorf("save bundle: %w", err)
	}
	return out, nil
}

// SaveCategory creates a category (id empty) or updates it.
func (s *Service) SaveCategory(ctx context.Context, p identity.Principal, id catalog.CategoryID, in catalog.Category) (catalog.Category, error) {
	if err := p.Require(identity.PermCatalogManage); err != nil {
		return catalog.Category{}, err
	}
	in.ID = id
	if id == "" {
		in.ID = catalog.CategoryID(s.newID())
	}
	err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		now := s.now().UTC()
		if id != "" {
			all, err := catalogpg.Categories(ctx, tx)
			if err != nil {
				return err
			}
			i := slices.IndexFunc(all, func(c catalog.Category) bool { return c.ID == id })
			if i < 0 {
				return fmt.Errorf("category %s: %w", id, postgres.ErrNotFound)
			}
			in.Image, in.Position = all[i].Image, all[i].Position
		}
		if err := catalogpg.SaveCategory(ctx, tx, in, now); err != nil {
			return err
		}
		return s.audit(ctx, tx, p, "catalog.category_saved", "catalog_category", string(in.ID), now)
	})
	if err != nil {
		return catalog.Category{}, fmt.Errorf("save category: %w", err)
	}
	return in, nil
}

// SavePackage creates a rental package (id empty) or updates it. Bookings
// already made keep the duration they were sold with.
func (s *Service) SavePackage(ctx context.Context, p identity.Principal, id catalog.PackageID, in catalog.RentalPackage) (catalog.RentalPackage, error) {
	if err := p.Require(identity.PermCatalogManage); err != nil {
		return catalog.RentalPackage{}, err
	}
	in.ID = id
	if id == "" {
		in.ID = catalog.PackageID(s.newID())
	}
	err := postgres.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		now := s.now().UTC()
		if err := catalogpg.SavePackage(ctx, tx, in, now); err != nil {
			return err
		}
		return s.audit(ctx, tx, p, "catalog.package_saved", "rental_package", string(in.ID), now)
	})
	if err != nil {
		return catalog.RentalPackage{}, fmt.Errorf("save package: %w", err)
	}
	return in, nil
}
