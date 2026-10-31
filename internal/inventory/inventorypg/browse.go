package inventorypg

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/identity"
	"webee_rentor/internal/inventory"
	"webee_rentor/internal/platform/postgres"
)

// AssetsByID reads assets without locking them, for display.
func AssetsByID(ctx context.Context, q postgres.Querier, ids []inventory.AssetID) (map[inventory.AssetID]inventory.Asset, error) {
	rows, err := q.Query(ctx, `SELECT `+assetColumns+` FROM assets WHERE id = ANY($1)`, slices.Compact(slices.Sorted(slices.Values(ids))))
	if err != nil {
		return nil, fmt.Errorf("read assets: %w", err)
	}
	defer rows.Close()
	out := make(map[inventory.AssetID]inventory.Asset, len(ids))
	for rows.Next() {
		var a inventory.Asset
		if err := rows.Scan(&a.ID, &a.Code, &a.VariantID, &a.Status, &a.StatusChangedAt); err != nil {
			return nil, fmt.Errorf("read assets: %w", err)
		}
		out[a.ID] = a
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read assets: %w", err)
	}
	return out, nil
}

// AssetFilter narrows the asset list; zero fields do not filter.
type AssetFilter struct {
	Term     string
	Status   inventory.AssetStatus
	Page     int
	PageSize int
}

// AssetRow is one line of the asset list.
type AssetRow struct {
	Asset       inventory.Asset
	SKU         string
	ItemName    string
	Size        string
	Color       string
	Note        string
	BookingCode string
}

// AssetPage lists assets by code with the total matching. BookingCode is the
// earliest booking still holding the asset.
func AssetPage(ctx context.Context, q postgres.Querier, f AssetFilter) ([]AssetRow, int, error) {
	term := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Term)
	rows, err := q.Query(ctx, `
		SELECT a.id, a.code, a.variant_id, a.status, a.status_changed_at, v.sku, i.name, v.size, v.color,
			coalesce((SELECT c.reason FROM asset_status_changes c WHERE c.asset_id = a.id ORDER BY c.at DESC, c.id DESC LIMIT 1), ''),
			coalesce((SELECT b.code FROM allocations al JOIN bookings b ON b.id = al.booking_id
				WHERE al.asset_id = a.id AND al.status = 'ACTIVE' ORDER BY al.blocked_from LIMIT 1), ''),
			count(*) OVER ()
		FROM assets a
		JOIN item_variants v ON v.id = a.variant_id
		JOIN catalog_items i ON i.id = v.item_id
		WHERE ($1 = '' OR a.status = $1)
			AND ($2 = '' OR a.code ILIKE '%' || $2 || '%' OR v.sku ILIKE '%' || $2 || '%' OR i.name ILIKE '%' || $2 || '%')
		ORDER BY a.code, a.id
		LIMIT $3 OFFSET $4`, string(f.Status), term, f.PageSize, (f.Page-1)*f.PageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("list assets: %w", err)
	}
	defer rows.Close()
	var out []AssetRow
	total := 0
	for rows.Next() {
		var r AssetRow
		if err := rows.Scan(&r.Asset.ID, &r.Asset.Code, &r.Asset.VariantID, &r.Asset.Status, &r.Asset.StatusChangedAt,
			&r.SKU, &r.ItemName, &r.Size, &r.Color, &r.Note, &r.BookingCode, &total); err != nil {
			return nil, 0, fmt.Errorf("list assets: %w", err)
		}
		r.Asset.StatusChangedAt = r.Asset.StatusChangedAt.UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list assets: %w", err)
	}
	return out, total, nil
}

// HistoryRow is one movement of an asset.
type HistoryRow struct {
	Action string
	From   inventory.AssetStatus
	To     inventory.AssetStatus
	Actor  identity.Actor
	At     time.Time
	Reason string
}

// History reads an asset's movements, latest first.
func History(ctx context.Context, q postgres.Querier, id inventory.AssetID) ([]HistoryRow, error) {
	rows, err := q.Query(ctx, `SELECT action, coalesce(from_status, ''), to_status, actor_kind, actor_id, at, reason
		FROM asset_status_changes WHERE asset_id = $1 ORDER BY at DESC, id DESC`, id)
	if err != nil {
		return nil, fmt.Errorf("asset %s history: %w", id, err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (HistoryRow, error) {
		var h HistoryRow
		err := r.Scan(&h.Action, &h.From, &h.To, &h.Actor.Kind, &h.Actor.ID, &h.At, &h.Reason)
		h.At = h.At.UTC()
		return h, err
	})
	if err != nil {
		return nil, fmt.Errorf("asset %s history: %w", id, err)
	}
	return out, nil
}
