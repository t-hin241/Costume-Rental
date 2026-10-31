package catalogpg

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/platform/postgres"
)

func encodeImages(images []catalog.Image) (string, error) {
	if images == nil {
		images = []catalog.Image{}
	}
	b, err := json.Marshal(images)
	return string(b), err
}

func decodeImages(b []byte) ([]catalog.Image, error) {
	var images []catalog.Image
	if err := json.Unmarshal(b, &images); err != nil {
		return nil, err
	}
	if len(images) == 0 {
		return nil, nil
	}
	return images, nil
}

func nullCategory(id catalog.CategoryID) *catalog.CategoryID {
	if id == "" {
		return nil
	}
	return &id
}

// Packages lists every rental package, shortest first.
func Packages(ctx context.Context, q postgres.Querier) ([]catalog.RentalPackage, error) {
	rows, err := q.Query(ctx, `SELECT id, code, label, duration_hours, status FROM rental_packages ORDER BY duration_hours, id`)
	if err != nil {
		return nil, fmt.Errorf("list packages: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (catalog.RentalPackage, error) {
		var p catalog.RentalPackage
		var hours int
		err := r.Scan(&p.ID, &p.Code, &p.Label, &hours, &p.Status)
		p.Duration = time.Duration(hours) * time.Hour
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("list packages: %w", err)
	}
	return out, nil
}

// AllItems loads the whole item catalog.
func AllItems(ctx context.Context, q postgres.Querier) (map[catalog.ItemID]catalog.Item, error) {
	rows, err := q.Query(ctx, `SELECT id FROM catalog_items`)
	if err != nil {
		return nil, fmt.Errorf("list items: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[catalog.ItemID])
	if err != nil {
		return nil, fmt.Errorf("list items: %w", err)
	}
	return Items(ctx, q, ids)
}

// AllBundles loads every bundle.
func AllBundles(ctx context.Context, q postgres.Querier) ([]catalog.Bundle, error) {
	rows, err := q.Query(ctx, `SELECT id FROM bundles ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("list bundles: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[catalog.BundleID])
	if err != nil {
		return nil, fmt.Errorf("list bundles: %w", err)
	}
	out := make([]catalog.Bundle, 0, len(ids))
	for _, id := range ids {
		b, err := Bundle(ctx, q, id)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// Categories lists categories in display order.
func Categories(ctx context.Context, q postgres.Querier) ([]catalog.Category, error) {
	rows, err := q.Query(ctx, `SELECT id, slug, name, image, position FROM catalog_categories ORDER BY position, name, id`)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (catalog.Category, error) {
		var c catalog.Category
		var image []byte
		if err := r.Scan(&c.ID, &c.Slug, &c.Name, &image, &c.Position); err != nil {
			return c, err
		}
		if image != nil {
			c.Image = &catalog.Image{}
			return c, json.Unmarshal(image, c.Image)
		}
		return c, nil
	})
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}
	return out, nil
}

// SaveCategory inserts or updates a category.
func SaveCategory(ctx context.Context, q postgres.Querier, c catalog.Category, now time.Time) error {
	if err := c.Validate(); err != nil {
		return err
	}
	var image *string
	if c.Image != nil {
		b, err := json.Marshal(c.Image)
		if err != nil {
			return fmt.Errorf("save category %s: %w", c.ID, err)
		}
		v := string(b)
		image = &v
	}
	_, err := q.Exec(ctx, `
		INSERT INTO catalog_categories (id, slug, name, image, position, updated_at) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO UPDATE SET slug = EXCLUDED.slug, name = EXCLUDED.name, image = EXCLUDED.image,
			position = EXCLUDED.position, updated_at = EXCLUDED.updated_at`,
		c.ID, c.Slug, c.Name, image, c.Position, now.UTC())
	if postgres.IsUniqueViolation(err, "catalog_categories_slug_key") {
		return fmt.Errorf("category slug %s: %w", c.Slug, postgres.ErrDuplicate)
	}
	if err != nil {
		return fmt.Errorf("save category %s: %w", c.ID, err)
	}
	return nil
}

// OfferingStats is what browsing sorts by.
type OfferingStats struct {
	Bookings  int
	UpdatedAt time.Time
}

// Stats returns, per offering id, how often it was booked and when it last changed.
func Stats(ctx context.Context, q postgres.Querier) (map[string]OfferingStats, error) {
	rows, err := q.Query(ctx, `
		SELECT o.id, o.updated_at, coalesce(l.n, 0)
		FROM (SELECT id, updated_at FROM catalog_items UNION ALL SELECT id, updated_at FROM bundles) o
		LEFT JOIN (SELECT offering_id, count(*) AS n FROM booking_lines GROUP BY offering_id) l ON l.offering_id = o.id`)
	if err != nil {
		return nil, fmt.Errorf("offering stats: %w", err)
	}
	defer rows.Close()
	out := map[string]OfferingStats{}
	for rows.Next() {
		var id string
		var s OfferingStats
		if err := rows.Scan(&id, &s.UpdatedAt, &s.Bookings); err != nil {
			return nil, fmt.Errorf("offering stats: %w", err)
		}
		out[id] = s
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("offering stats: %w", err)
	}
	return out, nil
}

// AssetCounts returns how many assets each item has.
func AssetCounts(ctx context.Context, q postgres.Querier) (map[catalog.ItemID]int, error) {
	rows, err := q.Query(ctx, `SELECT v.item_id, count(*) FROM assets a JOIN item_variants v ON v.id = a.variant_id GROUP BY v.item_id`)
	if err != nil {
		return nil, fmt.Errorf("asset counts: %w", err)
	}
	defer rows.Close()
	out := map[catalog.ItemID]int{}
	for rows.Next() {
		var id catalog.ItemID
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("asset counts: %w", err)
		}
		out[id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("asset counts: %w", err)
	}
	return out, nil
}

// VariantInfo names a variant for reading: bookings keep only its id and SKU,
// and variants are never deleted.
type VariantInfo struct {
	ItemName string
	SKU      string
	Size     string
	Color    string
}

// Variants describes the given variants.
func Variants(ctx context.Context, q postgres.Querier, ids []catalog.VariantID) (map[catalog.VariantID]VariantInfo, error) {
	rows, err := q.Query(ctx, `SELECT v.id, i.name, v.sku, v.size, v.color FROM item_variants v JOIN catalog_items i ON i.id = v.item_id WHERE v.id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("describe variants: %w", err)
	}
	defer rows.Close()
	out := make(map[catalog.VariantID]VariantInfo, len(ids))
	for rows.Next() {
		var id catalog.VariantID
		var v VariantInfo
		if err := rows.Scan(&id, &v.ItemName, &v.SKU, &v.Size, &v.Color); err != nil {
			return nil, fmt.Errorf("describe variants: %w", err)
		}
		out[id] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("describe variants: %w", err)
	}
	return out, nil
}

// VariantIDByRef finds a variant by id or SKU.
func VariantIDByRef(ctx context.Context, q postgres.Querier, ref string) (catalog.VariantID, error) {
	var id catalog.VariantID
	err := q.QueryRow(ctx, `SELECT id FROM item_variants WHERE id = $1 OR sku = $1 LIMIT 1`, ref).Scan(&id)
	if err != nil {
		return "", postgres.NotFound(err, fmt.Sprintf("variant %s", ref))
	}
	return id, nil
}

// OfferingImages returns the first image of each offering, for booking lists.
func OfferingImages(ctx context.Context, q postgres.Querier, ids []string) (map[string]catalog.Image, error) {
	rows, err := q.Query(ctx, `
		SELECT id, images FROM catalog_items WHERE id = ANY($1) AND jsonb_array_length(images) > 0
		UNION ALL SELECT id, images FROM bundles WHERE id = ANY($1) AND jsonb_array_length(images) > 0`, ids)
	if err != nil {
		return nil, fmt.Errorf("offering images: %w", err)
	}
	defer rows.Close()
	out := map[string]catalog.Image{}
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, fmt.Errorf("offering images: %w", err)
		}
		images, err := decodeImages(raw)
		if err != nil {
			return nil, fmt.Errorf("offering %s images: %w", id, err)
		}
		if len(images) > 0 {
			out[id] = images[0]
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("offering images: %w", err)
	}
	return out, nil
}
