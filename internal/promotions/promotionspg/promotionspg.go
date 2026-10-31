// Package promotionspg stores discount campaigns and their redemptions.
//
// Usage limits hold under concurrency because pricing for a real booking
// locks the campaign rows first (see ForPricing): two customers competing for
// the last use are serialised, and the second one counts the first's
// redemption. Campaign rows are locked in id order, before any asset.
package promotionspg

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/money"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/promotions"
)

const columns = `id, coalesce(code, ''), name, type, status, valid_from, valid_until, timezone, currency, value_type,
	percent, amount, max_discount, minimum_eligible_amount, eligible_item_ids, eligible_bundle_ids, eligible_segment,
	usage_limit_total, usage_limit_per_customer, combinable, funding, policy_version, version`

func scan(r pgx.CollectableRow) (promotions.Campaign, error) {
	var c promotions.Campaign
	var cur money.Currency
	var percent *int
	var amount, maxDiscount *int64
	var minimum int64
	var items []catalog.ItemID
	var bundles []catalog.BundleID
	if err := r.Scan(&c.ID, &c.Code, &c.Name, &c.Type, &c.Status, &c.ValidFrom, &c.ValidUntil, &c.Timezone, &cur, &c.ValueType,
		&percent, &amount, &maxDiscount, &minimum, &items, &bundles, &c.EligibleSegment,
		&c.UsageLimitTotal, &c.UsageLimitPerCustomer, &c.Combinable, &c.Funding, &c.PolicyVersion, &c.Version); err != nil {
		return c, err
	}
	var err error
	c.ValidFrom, c.ValidUntil = c.ValidFrom.UTC(), c.ValidUntil.UTC()
	if c.MinimumEligibleAmount, err = money.New(minimum, cur); err != nil {
		return c, err
	}
	if percent != nil {
		c.Percent = *percent
	}
	if amount != nil {
		if c.Amount, err = money.New(*amount, cur); err != nil {
			return c, err
		}
	}
	if maxDiscount != nil {
		m, err := money.New(*maxDiscount, cur)
		if err != nil {
			return c, err
		}
		c.MaxDiscount = &m
	}
	if len(items) > 0 {
		c.EligibleItemIDs = items
	}
	if len(bundles) > 0 {
		c.EligibleBundleIDs = bundles
	}
	return c, nil
}

// Save inserts a campaign or replaces it with a newer version.
func Save(ctx context.Context, q postgres.Querier, c promotions.Campaign, now time.Time) error {
	if err := c.Validate(); err != nil {
		return err
	}
	var code *string
	if c.Code != "" {
		code = &c.Code
	}
	var percent *int
	var amount, maxDiscount *int64
	switch c.ValueType {
	case promotions.ValuePercent:
		percent = &c.Percent
	case promotions.ValueFixed:
		v := c.Amount.Amount()
		amount = &v
	}
	if c.MaxDiscount != nil {
		v := c.MaxDiscount.Amount()
		maxDiscount = &v
	}
	items, bundles := c.EligibleItemIDs, c.EligibleBundleIDs
	if items == nil {
		items = []catalog.ItemID{}
	}
	if bundles == nil {
		bundles = []catalog.BundleID{}
	}
	tag, err := q.Exec(ctx, `
		INSERT INTO promotion_campaigns (id, code, name, type, status, valid_from, valid_until, timezone, currency, value_type,
			percent, amount, max_discount, minimum_eligible_amount, eligible_item_ids, eligible_bundle_ids, eligible_segment,
			usage_limit_total, usage_limit_per_customer, combinable, funding, policy_version, version, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24)
		ON CONFLICT (id) DO UPDATE SET code = EXCLUDED.code, name = EXCLUDED.name, type = EXCLUDED.type, status = EXCLUDED.status,
			valid_from = EXCLUDED.valid_from, valid_until = EXCLUDED.valid_until, value_type = EXCLUDED.value_type,
			percent = EXCLUDED.percent, amount = EXCLUDED.amount, max_discount = EXCLUDED.max_discount,
			minimum_eligible_amount = EXCLUDED.minimum_eligible_amount, eligible_item_ids = EXCLUDED.eligible_item_ids,
			eligible_bundle_ids = EXCLUDED.eligible_bundle_ids, eligible_segment = EXCLUDED.eligible_segment,
			usage_limit_total = EXCLUDED.usage_limit_total, usage_limit_per_customer = EXCLUDED.usage_limit_per_customer,
			combinable = EXCLUDED.combinable, policy_version = EXCLUDED.policy_version, version = EXCLUDED.version,
			updated_at = EXCLUDED.updated_at
		WHERE promotion_campaigns.version < EXCLUDED.version`,
		c.ID, code, c.Name, c.Type, c.Status, c.ValidFrom.UTC(), c.ValidUntil.UTC(), c.Timezone, c.MinimumEligibleAmount.Currency(), c.ValueType,
		percent, amount, maxDiscount, c.MinimumEligibleAmount.Amount(), items, bundles, c.EligibleSegment,
		c.UsageLimitTotal, c.UsageLimitPerCustomer, c.Combinable, c.Funding, c.PolicyVersion, c.Version, now.UTC())
	if postgres.IsUniqueViolation(err, "promotion_campaigns_code") {
		return fmt.Errorf("coupon code %s: %w", c.Code, postgres.ErrDuplicate)
	}
	if err != nil {
		return fmt.Errorf("save campaign %s: %w", c.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("save campaign %s version %d: %w", c.ID, c.Version, postgres.ErrStale)
	}
	return nil
}

// Load reads one campaign.
func Load(ctx context.Context, q postgres.Querier, id promotions.CampaignID) (promotions.Campaign, error) {
	rows, err := q.Query(ctx, `SELECT `+columns+` FROM promotion_campaigns WHERE id = $1`, id)
	if err != nil {
		return promotions.Campaign{}, fmt.Errorf("load campaign %s: %w", id, err)
	}
	c, err := pgx.CollectExactlyOneRow(rows, scan)
	if err != nil {
		return promotions.Campaign{}, postgres.NotFound(err, fmt.Sprintf("campaign %s", id))
	}
	return c, nil
}

// All lists every campaign, newest validity first.
func All(ctx context.Context, q postgres.Querier) ([]promotions.Campaign, error) {
	rows, err := q.Query(ctx, `SELECT `+columns+` FROM promotion_campaigns ORDER BY valid_from DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("list campaigns: %w", err)
	}
	out, err := pgx.CollectRows(rows, scan)
	if err != nil {
		return nil, fmt.Errorf("list campaigns: %w", err)
	}
	return out, nil
}

// ForPricing reads the active automatic campaigns and the coupon with code,
// whatever its status so a paused coupon can be told apart from a wrong code.
// With lock set the rows are locked, which serialises usage limits.
func ForPricing(ctx context.Context, q postgres.Querier, code string, lock bool) ([]promotions.Campaign, error) {
	query := `SELECT ` + columns + ` FROM promotion_campaigns
		WHERE (code IS NULL AND status = 'ACTIVE') OR code = $1 ORDER BY id`
	if lock {
		query += ` FOR UPDATE`
	}
	rows, err := q.Query(ctx, query, code)
	if err != nil {
		return nil, fmt.Errorf("campaigns for pricing: %w", err)
	}
	out, err := pgx.CollectRows(rows, scan)
	if err != nil {
		return nil, fmt.Errorf("campaigns for pricing: %w", err)
	}
	return out, nil
}

// Usage counts active redemptions of the campaigns, total and for customer.
func Usage(ctx context.Context, q postgres.Querier, customer identity.UserID) (map[promotions.CampaignID]promotions.Usage, error) {
	rows, err := q.Query(ctx, `SELECT campaign_id, count(*), count(*) FILTER (WHERE customer_id = $1)
		FROM promotion_redemptions WHERE status = 'ACTIVE' GROUP BY campaign_id`, customer)
	if err != nil {
		return nil, fmt.Errorf("campaign usage: %w", err)
	}
	defer rows.Close()
	out := map[promotions.CampaignID]promotions.Usage{}
	for rows.Next() {
		var id promotions.CampaignID
		var u promotions.Usage
		if err := rows.Scan(&id, &u.Total, &u.ForCustomer); err != nil {
			return nil, fmt.Errorf("campaign usage: %w", err)
		}
		out[id] = u
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("campaign usage: %w", err)
	}
	return out, nil
}

// InsertRedemptions records the campaigns a booking used.
func InsertRedemptions(ctx context.Context, q postgres.Querier, rs []promotions.Redemption) error {
	for _, r := range rs {
		if _, err := q.Exec(ctx, `INSERT INTO promotion_redemptions (id, campaign_id, booking_id, customer_id, status, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)`, r.ID, r.CampaignID, r.BookingRef, r.CustomerID, r.Status, r.CreatedAt); err != nil {
			return fmt.Errorf("redeem campaign %s: %w", r.CampaignID, err)
		}
	}
	return nil
}

// ReleaseOfBooking frees what a cancelled or expired booking used (decision
// D12). Running it again changes nothing.
func ReleaseOfBooking(ctx context.Context, q postgres.Querier, bookingID, reason string, now time.Time) error {
	if _, err := q.Exec(ctx, `UPDATE promotion_redemptions SET status = 'RELEASED', released_at = $3, reason = $2
		WHERE booking_id = $1 AND status = 'ACTIVE'`, bookingID, reason, now.UTC()); err != nil {
		return fmt.Errorf("release redemptions of booking %s: %w", bookingID, err)
	}
	return nil
}
