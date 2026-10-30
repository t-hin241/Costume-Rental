// Package catalogpg stores the catalog in PostgreSQL. Bookings only read it
// and keep their own snapshot; writes are for admin use cases and seeding.
package catalogpg

import (
	"context"
	"fmt"
	"slices"
	"time"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/money"
	"webee_rentor/internal/platform/postgres"
)

// SavePackage inserts or updates a rental package.
func SavePackage(ctx context.Context, q postgres.Querier, p catalog.RentalPackage, now time.Time) error {
	if err := p.Validate(); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `
		INSERT INTO rental_packages (id, code, label, duration_hours, status, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO UPDATE SET code = EXCLUDED.code, label = EXCLUDED.label,
			duration_hours = EXCLUDED.duration_hours, status = EXCLUDED.status, updated_at = EXCLUDED.updated_at`,
		p.ID, p.Code, p.Label, int(p.Duration/time.Hour), p.Status, now.UTC())
	if err != nil {
		return fmt.Errorf("save package %s: %w", p.ID, err)
	}
	return nil
}

// Package loads one rental package.
func Package(ctx context.Context, q postgres.Querier, id catalog.PackageID) (catalog.RentalPackage, error) {
	var p catalog.RentalPackage
	var hours int
	err := q.QueryRow(ctx, `SELECT id, code, label, duration_hours, status FROM rental_packages WHERE id = $1`, id).
		Scan(&p.ID, &p.Code, &p.Label, &hours, &p.Status)
	if err != nil {
		return catalog.RentalPackage{}, postgres.NotFound(err, fmt.Sprintf("package %s", id))
	}
	p.Duration = time.Duration(hours) * time.Hour
	return p, nil
}

// SaveItem inserts an item or replaces it with a newer version. Variants are
// upserted and never deleted (archive them with a status instead) because
// assets reference them; prices are replaced.
func SaveItem(ctx context.Context, q postgres.Querier, it catalog.Item, now time.Time) error {
	if err := it.Validate(); err != nil {
		return err
	}
	tag, err := q.Exec(ctx, `
		INSERT INTO catalog_items (id, slug, name, status, individually_rentable, currency,
			security_deposit, daily_rate, buffer_class, version, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (id) DO UPDATE SET slug = EXCLUDED.slug, name = EXCLUDED.name, status = EXCLUDED.status,
			individually_rentable = EXCLUDED.individually_rentable, currency = EXCLUDED.currency,
			security_deposit = EXCLUDED.security_deposit, daily_rate = EXCLUDED.daily_rate,
			buffer_class = EXCLUDED.buffer_class, version = EXCLUDED.version, updated_at = EXCLUDED.updated_at
		WHERE catalog_items.version < EXCLUDED.version`,
		it.ID, it.Slug, it.Name, it.Status, it.IndividuallyRentable, it.DailyRate.Currency(),
		it.SecurityDeposit.Amount(), it.DailyRate.Amount(), it.BufferClass, it.Version, now.UTC())
	if err != nil {
		return fmt.Errorf("save item %s: %w", it.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("save item %s version %d: %w", it.ID, it.Version, postgres.ErrStale)
	}
	for i, v := range it.Variants {
		tag, err := q.Exec(ctx, `
			INSERT INTO item_variants (id, item_id, sku, size, color, status, position)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (id) DO UPDATE SET sku = EXCLUDED.sku, size = EXCLUDED.size, color = EXCLUDED.color,
				status = EXCLUDED.status, position = EXCLUDED.position
			WHERE item_variants.item_id = EXCLUDED.item_id`,
			v.ID, it.ID, v.SKU, v.Size, v.Color, v.Status, i)
		if err != nil {
			return fmt.Errorf("save item %s variant %s: %w", it.ID, v.ID, err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("save item %s variant %s belongs to another item: %w", it.ID, v.ID, postgres.ErrDuplicate)
		}
	}
	if _, err := q.Exec(ctx, `DELETE FROM item_prices WHERE item_id = $1`, it.ID); err != nil {
		return fmt.Errorf("save item %s prices: %w", it.ID, err)
	}
	for pkg, price := range it.Prices {
		if _, err := q.Exec(ctx, `INSERT INTO item_prices (item_id, package_id, amount) VALUES ($1, $2, $3)`,
			it.ID, pkg, price.Amount()); err != nil {
			return fmt.Errorf("save item %s price %s: %w", it.ID, pkg, err)
		}
	}
	return nil
}

// Items loads items with their variants and prices. Missing IDs are
// reported as not found.
func Items(ctx context.Context, q postgres.Querier, ids []catalog.ItemID) (map[catalog.ItemID]catalog.Item, error) {
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	rows, err := q.Query(ctx, `
		SELECT id, slug, name, status, individually_rentable, currency, security_deposit, daily_rate, buffer_class, version
		FROM catalog_items WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("load items: %w", err)
	}
	out := map[catalog.ItemID]catalog.Item{}
	for rows.Next() {
		var it catalog.Item
		var cur money.Currency
		var deposit, daily int64
		if err := rows.Scan(&it.ID, &it.Slug, &it.Name, &it.Status, &it.IndividuallyRentable, &cur,
			&deposit, &daily, &it.BufferClass, &it.Version); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan item: %w", err)
		}
		if it.SecurityDeposit, err = money.New(deposit, cur); err != nil {
			rows.Close()
			return nil, fmt.Errorf("item %s deposit: %w", it.ID, err)
		}
		if it.DailyRate, err = money.New(daily, cur); err != nil {
			rows.Close()
			return nil, fmt.Errorf("item %s daily rate: %w", it.ID, err)
		}
		out[it.ID] = it
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load items: %w", err)
	}
	for _, id := range ids {
		if _, ok := out[id]; !ok {
			return nil, fmt.Errorf("item %s: %w", id, postgres.ErrNotFound)
		}
	}

	rows, err = q.Query(ctx, `
		SELECT item_id, id, sku, size, color, status FROM item_variants
		WHERE item_id = ANY($1) ORDER BY item_id, position`, ids)
	if err != nil {
		return nil, fmt.Errorf("load variants: %w", err)
	}
	for rows.Next() {
		var itemID catalog.ItemID
		var v catalog.Variant
		if err := rows.Scan(&itemID, &v.ID, &v.SKU, &v.Size, &v.Color, &v.Status); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan variant: %w", err)
		}
		it := out[itemID]
		it.Variants = append(it.Variants, v)
		out[itemID] = it
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load variants: %w", err)
	}

	rows, err = q.Query(ctx, `SELECT item_id, package_id, amount FROM item_prices WHERE item_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("load item prices: %w", err)
	}
	for rows.Next() {
		var itemID catalog.ItemID
		var pkg catalog.PackageID
		var amount int64
		if err := rows.Scan(&itemID, &pkg, &amount); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan item price: %w", err)
		}
		it := out[itemID]
		price, err := money.New(amount, it.DailyRate.Currency())
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("item %s price %s: %w", itemID, pkg, err)
		}
		if it.Prices == nil {
			it.Prices = catalog.Prices{}
		}
		it.Prices[pkg] = price
		out[itemID] = it
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load item prices: %w", err)
	}
	return out, nil
}

// SaveBundle inserts a bundle or replaces it with a newer version. items
// must contain every component item, for validation.
func SaveBundle(ctx context.Context, q postgres.Querier, b catalog.Bundle, items map[catalog.ItemID]catalog.Item, now time.Time) error {
	if err := b.Validate(items); err != nil {
		return err
	}
	currency := money.VND
	for _, p := range b.Prices {
		currency = p.Currency()
	}
	var override *int64
	if b.DepositOverride != nil {
		v := b.DepositOverride.Amount()
		override = &v
	}
	tag, err := q.Exec(ctx, `
		INSERT INTO bundles (id, slug, name, status, currency, deposit_override, version, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (id) DO UPDATE SET slug = EXCLUDED.slug, name = EXCLUDED.name, status = EXCLUDED.status,
			currency = EXCLUDED.currency, deposit_override = EXCLUDED.deposit_override,
			version = EXCLUDED.version, updated_at = EXCLUDED.updated_at
		WHERE bundles.version < EXCLUDED.version`,
		b.ID, b.Slug, b.Name, b.Status, currency, override, b.Version, now.UTC())
	if err != nil {
		return fmt.Errorf("save bundle %s: %w", b.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("save bundle %s version %d: %w", b.ID, b.Version, postgres.ErrStale)
	}
	// Composition rows have no dependants (bookings snapshot them), so they
	// are replaced as a whole; options cascade with their component.
	for _, stmt := range []string{`DELETE FROM bundle_components WHERE bundle_id = $1`, `DELETE FROM bundle_prices WHERE bundle_id = $1`} {
		if _, err := q.Exec(ctx, stmt, b.ID); err != nil {
			return fmt.Errorf("save bundle %s: %w", b.ID, err)
		}
	}
	for i, c := range b.Components {
		if _, err := q.Exec(ctx, `INSERT INTO bundle_components (bundle_id, key, item_id, quantity, position) VALUES ($1, $2, $3, $4, $5)`,
			b.ID, c.Key, c.ItemID, c.Quantity, i); err != nil {
			return fmt.Errorf("save bundle %s component %s: %w", b.ID, c.Key, err)
		}
		for j, v := range c.Options {
			if _, err := q.Exec(ctx, `
				INSERT INTO bundle_component_options (bundle_id, component_key, item_id, variant_id, position)
				VALUES ($1, $2, $3, $4, $5)`, b.ID, c.Key, c.ItemID, v, j); err != nil {
				return fmt.Errorf("save bundle %s component %s option %s: %w", b.ID, c.Key, v, err)
			}
		}
	}
	for pkg, price := range b.Prices {
		if _, err := q.Exec(ctx, `INSERT INTO bundle_prices (bundle_id, package_id, amount) VALUES ($1, $2, $3)`,
			b.ID, pkg, price.Amount()); err != nil {
			return fmt.Errorf("save bundle %s price %s: %w", b.ID, pkg, err)
		}
	}
	return nil
}

// Bundle loads one bundle with its composition and prices.
func Bundle(ctx context.Context, q postgres.Querier, id catalog.BundleID) (catalog.Bundle, error) {
	var b catalog.Bundle
	var cur money.Currency
	var override *int64
	err := q.QueryRow(ctx, `SELECT id, slug, name, status, currency, deposit_override, version FROM bundles WHERE id = $1`, id).
		Scan(&b.ID, &b.Slug, &b.Name, &b.Status, &cur, &override, &b.Version)
	if err != nil {
		return catalog.Bundle{}, postgres.NotFound(err, fmt.Sprintf("bundle %s", id))
	}
	if override != nil {
		m, err := money.New(*override, cur)
		if err != nil {
			return catalog.Bundle{}, fmt.Errorf("bundle %s deposit override: %w", id, err)
		}
		b.DepositOverride = &m
	}

	rows, err := q.Query(ctx, `
		SELECT c.key, c.item_id, c.quantity, o.variant_id
		FROM bundle_components c
		JOIN bundle_component_options o ON o.bundle_id = c.bundle_id AND o.component_key = c.key
		WHERE c.bundle_id = $1 ORDER BY c.position, o.position`, id)
	if err != nil {
		return catalog.Bundle{}, fmt.Errorf("load bundle %s components: %w", id, err)
	}
	for rows.Next() {
		var c catalog.BundleComponent
		var opt catalog.VariantID
		if err := rows.Scan(&c.Key, &c.ItemID, &c.Quantity, &opt); err != nil {
			rows.Close()
			return catalog.Bundle{}, fmt.Errorf("scan bundle component: %w", err)
		}
		if n := len(b.Components); n > 0 && b.Components[n-1].Key == c.Key {
			b.Components[n-1].Options = append(b.Components[n-1].Options, opt)
			continue
		}
		c.Options = []catalog.VariantID{opt}
		b.Components = append(b.Components, c)
	}
	if err := rows.Err(); err != nil {
		return catalog.Bundle{}, fmt.Errorf("load bundle %s components: %w", id, err)
	}

	rows, err = q.Query(ctx, `SELECT package_id, amount FROM bundle_prices WHERE bundle_id = $1`, id)
	if err != nil {
		return catalog.Bundle{}, fmt.Errorf("load bundle %s prices: %w", id, err)
	}
	for rows.Next() {
		var pkg catalog.PackageID
		var amount int64
		if err := rows.Scan(&pkg, &amount); err != nil {
			rows.Close()
			return catalog.Bundle{}, fmt.Errorf("scan bundle price: %w", err)
		}
		price, err := money.New(amount, cur)
		if err != nil {
			rows.Close()
			return catalog.Bundle{}, fmt.Errorf("bundle %s price %s: %w", id, pkg, err)
		}
		if b.Prices == nil {
			b.Prices = catalog.Prices{}
		}
		b.Prices[pkg] = price
	}
	if err := rows.Err(); err != nil {
		return catalog.Bundle{}, fmt.Errorf("load bundle %s prices: %w", id, err)
	}
	return b, nil
}
