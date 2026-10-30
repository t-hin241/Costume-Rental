-- Promotions (campaigns, redemptions) and the tax profile history with
-- revenue entries. Tax values stay UNCONFIRMED until the accountant
-- confirms them (docs/domain_decisions.md A33); the profile is a versioned
-- document, not hard-coded rates.

CREATE TABLE promotion_campaigns (
    id                       text PRIMARY KEY,
    -- Coupon code, stored upper-case; NULL for automatic campaigns.
    code                     text CHECK (code = upper(code) AND code <> ''),
    name                     text NOT NULL CHECK (name <> ''),
    type                     text NOT NULL CHECK (type IN ('LINE_DISCOUNT', 'COUPON', 'ORDER_DISCOUNT', 'MEMBER_DISCOUNT')),
    status                   text NOT NULL CHECK (status IN ('DRAFT', 'ACTIVE', 'PAUSED', 'ENDED')),
    valid_from               timestamptz NOT NULL,
    valid_until              timestamptz NOT NULL CHECK (valid_until > valid_from),
    timezone                 text NOT NULL,
    currency                 text NOT NULL CHECK (currency = 'VND'),
    value_type               text NOT NULL CHECK (value_type IN ('PERCENT', 'FIXED_AMOUNT')),
    percent                  integer CHECK (percent BETWEEN 1 AND 100),
    amount                   bigint CHECK (amount > 0),
    max_discount             bigint CHECK (max_discount > 0),
    minimum_eligible_amount  bigint NOT NULL DEFAULT 0 CHECK (minimum_eligible_amount >= 0),
    eligible_item_ids        text[] NOT NULL DEFAULT '{}',
    eligible_bundle_ids      text[] NOT NULL DEFAULT '{}',
    eligible_segment         text NOT NULL DEFAULT '',
    usage_limit_total        integer NOT NULL DEFAULT 0 CHECK (usage_limit_total >= 0),
    usage_limit_per_customer integer NOT NULL DEFAULT 0 CHECK (usage_limit_per_customer >= 0),
    combinable               boolean NOT NULL,
    funding                  text NOT NULL CHECK (funding IN ('MERCHANT')),
    policy_version           text NOT NULL,
    version                  integer NOT NULL CHECK (version >= 1),
    updated_at               timestamptz NOT NULL,
    CHECK ((value_type = 'PERCENT') = (percent IS NOT NULL)),
    CHECK ((value_type = 'FIXED_AMOUNT') = (amount IS NOT NULL)),
    CHECK ((type = 'COUPON') = (code IS NOT NULL))
);
CREATE UNIQUE INDEX promotion_campaigns_code ON promotion_campaigns (code) WHERE code IS NOT NULL;

-- Usage limits count ACTIVE redemptions; the partial unique index stops one
-- booking redeeming a campaign twice.
CREATE TABLE promotion_redemptions (
    id          text PRIMARY KEY,
    campaign_id text NOT NULL REFERENCES promotion_campaigns (id),
    booking_id  text NOT NULL REFERENCES bookings (id),
    customer_id text NOT NULL REFERENCES users (id),
    status      text NOT NULL CHECK (status IN ('ACTIVE', 'RELEASED')),
    created_at  timestamptz NOT NULL,
    released_at timestamptz,
    reason      text NOT NULL DEFAULT '',
    CHECK ((status = 'RELEASED') = (released_at IS NOT NULL))
);
CREATE UNIQUE INDEX promotion_redemptions_once ON promotion_redemptions (campaign_id, booking_id) WHERE status = 'ACTIVE';
CREATE INDEX promotion_redemptions_usage ON promotion_redemptions (campaign_id, customer_id) WHERE status = 'ACTIVE';

CREATE TABLE tax_profiles (
    version         text PRIMARY KEY CHECK (version <> ''),
    document        jsonb NOT NULL,
    effective_from  timestamptz NOT NULL,
    effective_until timestamptz CHECK (effective_until > effective_from),
    created_at      timestamptz NOT NULL,
    EXCLUDE USING gist (tstzrange(effective_from, effective_until, '[)') WITH &&)
);
CREATE TRIGGER tax_profiles_append_only BEFORE DELETE ON tax_profiles
    FOR EACH ROW EXECUTE FUNCTION reject_history_change();

-- Recognised revenue, one entry per ledger charge; corrections reverse an
-- entry with a new row instead of editing it.
CREATE TABLE revenue_entries (
    id              text PRIMARY KEY,
    booking_id      text NOT NULL REFERENCES bookings (id),
    source_ref      text NOT NULL,
    category        text NOT NULL CHECK (category IN ('RENTAL_REVENUE', 'DELIVERY_SERVICE', 'CLEANING_SERVICE',
        'LATE_RETURN_CHARGE', 'DAMAGE_CHARGE', 'LOST_ITEM_COMPENSATION', 'CANCELLATION_CHARGE', 'OTHER_CHARGE',
        'BOOKING_DEPOSIT', 'RENTAL_SECURITY_DEPOSIT')),
    currency        text NOT NULL CHECK (currency = 'VND'),
    list_amount     bigint NOT NULL,
    discount        bigint NOT NULL,
    net             bigint NOT NULL,
    recognized_at   timestamptz NOT NULL,
    profile_version text NOT NULL REFERENCES tax_profiles (version),
    reverses        text REFERENCES revenue_entries (id),
    reason          text NOT NULL DEFAULT '',
    CHECK (net = list_amount - discount)
);
CREATE INDEX revenue_entries_by_time ON revenue_entries (recognized_at);
CREATE UNIQUE INDEX revenue_entries_reversed_once ON revenue_entries (reverses) WHERE reverses IS NOT NULL;
CREATE TRIGGER revenue_entries_append_only BEFORE UPDATE OR DELETE ON revenue_entries
    FOR EACH ROW EXECUTE FUNCTION reject_history_change();
