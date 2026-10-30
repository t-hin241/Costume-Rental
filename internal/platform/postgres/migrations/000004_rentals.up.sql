-- Bookings and their snapshots. Columns are relational wherever the system
-- queries or constrains them; immutable snapshots (policy, fulfillment,
-- discounts, credits) are JSONB written by the Go domain types. Snapshot
-- rows do not reference the catalog: a booking keeps what was sold even if
-- the catalog changes.

CREATE TABLE bookings (
    id                        text PRIMARY KEY,
    code                      text NOT NULL UNIQUE CHECK (code <> ''),
    customer_id               text NOT NULL REFERENCES users (id),
    -- Client retry key: one booking per customer and key.
    request_key               text CHECK (request_key <> ''),
    status                    text NOT NULL CHECK (status IN ('PENDING_CONFIRMATION', 'CONFIRMED', 'PREPARING',
        'RENTED', 'RETURN_INSPECTION', 'COMPLETED', 'CANCELLED', 'EXPIRED')),
    currency                  text NOT NULL CHECK (currency = 'VND'),
    pickup_at                 timestamptz NOT NULL,
    due_at                    timestamptz NOT NULL CHECK (due_at > pickup_at),
    package_id                text NOT NULL,
    package_code              text NOT NULL,
    package_label             text NOT NULL,
    package_duration_hours    integer NOT NULL CHECK (package_duration_hours > 0),
    gross_rent_total          bigint NOT NULL CHECK (gross_rent_total >= 0),
    discount_total            bigint NOT NULL CHECK (discount_total >= 0),
    rent_total                bigint NOT NULL CHECK (rent_total >= 0),
    security_deposit_total    bigint NOT NULL CHECK (security_deposit_total >= 0),
    reservation_deposit       bigint NOT NULL CHECK (reservation_deposit >= 0),
    policy_version            text NOT NULL,
    policy                    jsonb NOT NULL,
    fulfillment               jsonb NOT NULL,
    discounts                 jsonb NOT NULL DEFAULT '[]',
    manual_adjustments        jsonb NOT NULL DEFAULT '[]',
    service_credits           jsonb NOT NULL DEFAULT '[]',
    open_modification_id      text,
    confirmed_modification_ids text[] NOT NULL DEFAULT '{}',
    self_service_size_changes integer NOT NULL DEFAULT 0 CHECK (self_service_size_changes >= 0),
    created_at                timestamptz NOT NULL,
    hold_expires_at           timestamptz NOT NULL,
    confirmed_at              timestamptz,
    preparation_started_at    timestamptz,
    handed_over_at            timestamptz,
    returned_at               timestamptz,
    inspection_completed_at   timestamptz,
    completed_at              timestamptz,
    cancelled_at              timestamptz,
    expired_at                timestamptz,
    cancel_reason             text CHECK (cancel_reason IN ('CUSTOMER_REQUEST', 'NO_SHOW', 'STAFF_DECISION', 'PAYMENT_FAILED')),
    cancel_note               text NOT NULL DEFAULT '',
    -- Incremented on every write; updates must name the version they read.
    version                   integer NOT NULL DEFAULT 1,
    updated_at                timestamptz NOT NULL,
    CHECK ((status = 'CANCELLED') = (cancelled_at IS NOT NULL)),
    CHECK ((status = 'CANCELLED') = (cancel_reason IS NOT NULL)),
    CHECK ((status = 'EXPIRED') = (expired_at IS NOT NULL)),
    CHECK (status <> 'COMPLETED' OR completed_at IS NOT NULL)
);
CREATE UNIQUE INDEX bookings_request_key ON bookings (customer_id, request_key) WHERE request_key IS NOT NULL;
CREATE INDEX bookings_by_customer ON bookings (customer_id, created_at DESC);
CREATE INDEX bookings_pending_holds ON bookings (hold_expires_at) WHERE status = 'PENDING_CONFIRMATION';
CREATE INDEX bookings_by_status_pickup ON bookings (status, pickup_at);

CREATE TABLE booking_lines (
    booking_id       text NOT NULL REFERENCES bookings (id),
    line_id          text NOT NULL CHECK (line_id <> ''),
    position         integer NOT NULL,
    kind             text NOT NULL CHECK (kind IN ('ITEM', 'BUNDLE')),
    offering_id      text NOT NULL,
    offering_name    text NOT NULL,
    offering_version integer NOT NULL,
    quantity         integer NOT NULL CHECK (quantity >= 1),
    unit_rent        bigint NOT NULL CHECK (unit_rent >= 0),
    list_rent        bigint NOT NULL CHECK (list_rent >= 0),
    discount         bigint NOT NULL CHECK (discount >= 0),
    rent_amount      bigint NOT NULL CHECK (rent_amount >= 0),
    unit_deposit     bigint NOT NULL CHECK (unit_deposit >= 0),
    deposit_amount   bigint NOT NULL CHECK (deposit_amount >= 0),
    PRIMARY KEY (booking_id, line_id),
    CHECK (rent_amount = list_rent - discount)
);

-- The physical variants a line needs, with the snapshot daily rate.
CREATE TABLE booking_line_components (
    booking_id      text NOT NULL,
    line_id         text NOT NULL,
    component_key   text NOT NULL CHECK (component_key <> ''),
    position        integer NOT NULL,
    item_id         text NOT NULL,
    variant_id      text NOT NULL,
    sku             text NOT NULL,
    quantity        integer NOT NULL CHECK (quantity >= 1),
    buffer_class    text NOT NULL,
    unit_daily_rate bigint NOT NULL CHECK (unit_daily_rate >= 0),
    PRIMARY KEY (booking_id, line_id, component_key),
    FOREIGN KEY (booking_id, line_id) REFERENCES booking_lines (booking_id, line_id) ON DELETE CASCADE
);

-- Assets handed over under the booking, each with its own return, loss and
-- inspection record.
CREATE TABLE booking_items (
    booking_id              text NOT NULL REFERENCES bookings (id),
    asset_id                text NOT NULL REFERENCES assets (id),
    position                integer NOT NULL,
    line_id                 text NOT NULL,
    component_key           text NOT NULL,
    variant_id              text NOT NULL,
    daily_rate              bigint NOT NULL CHECK (daily_rate >= 0),
    preexisting_issues      text NOT NULL DEFAULT '',
    returned_at             timestamptz,
    received_at             timestamptz,
    missing_reported_at     timestamptz,
    lost_confirmed_at       timestamptz,
    carrier_liable          boolean NOT NULL DEFAULT false,
    carrier_damage_reported boolean NOT NULL DEFAULT false,
    not_received            boolean NOT NULL DEFAULT false,
    arrival_issue           boolean NOT NULL DEFAULT false,
    collect_by_store        boolean NOT NULL DEFAULT false,
    replacement_for         text,
    replaced_by             text,
    finding                 jsonb,
    PRIMARY KEY (booking_id, asset_id)
);

CREATE TABLE booking_transitions (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    booking_id  text NOT NULL REFERENCES bookings (id),
    action      text NOT NULL,
    from_status text,
    to_status   text NOT NULL,
    actor_kind  text NOT NULL CHECK (actor_kind IN ('user', 'system')),
    actor_id    text NOT NULL,
    at          timestamptz NOT NULL,
    reason      text NOT NULL DEFAULT ''
);
CREATE INDEX booking_transitions_by_booking ON booking_transitions (booking_id, id);
CREATE TRIGGER booking_transitions_append_only BEFORE UPDATE OR DELETE ON booking_transitions
    FOR EACH ROW EXECUTE FUNCTION reject_history_change();

-- Change requests (size, bundle, package, extension). The document keeps
-- the before/after terms and the allocation plan.
CREATE TABLE booking_modifications (
    id              text PRIMARY KEY,
    booking_id      text NOT NULL REFERENCES bookings (id),
    status          text NOT NULL CHECK (status IN
        ('REQUESTED', 'QUOTED', 'AWAITING_PAYMENT', 'CONFIRMED', 'REJECTED', 'EXPIRED', 'CANCELLED')),
    hold_expires_at timestamptz,
    document        jsonb NOT NULL,
    created_at      timestamptz NOT NULL,
    version         integer NOT NULL DEFAULT 1,
    updated_at      timestamptz NOT NULL
);
CREATE UNIQUE INDEX booking_modifications_one_open ON booking_modifications (booking_id)
    WHERE status IN ('REQUESTED', 'QUOTED', 'AWAITING_PAYMENT');
CREATE INDEX booking_modifications_holds ON booking_modifications (hold_expires_at)
    WHERE status IN ('QUOTED', 'AWAITING_PAYMENT');

CREATE TABLE fee_assessments (
    id                text PRIMARY KEY,
    booking_id        text NOT NULL REFERENCES bookings (id),
    asset_id          text NOT NULL,
    kind              text NOT NULL CHECK (kind IN
        ('LATE_RETURN', 'CLEANING', 'DAMAGE_REPAIR', 'DAMAGE_REPLACEMENT', 'LOST_ITEM', 'REDELIVERY')),
    currency          text NOT NULL CHECK (currency = 'VND'),
    amount            bigint NOT NULL CHECK (amount >= 0),
    basis             text NOT NULL,
    evidence          text[] NOT NULL DEFAULT '{}',
    policy_version    text NOT NULL,
    status            text NOT NULL CHECK (status IN
        ('PROPOSED', 'APPROVED', 'REJECTED', 'DISPUTED', 'WAIVED', 'SUPERSEDED')),
    proposed_by_kind  text NOT NULL CHECK (proposed_by_kind IN ('user', 'system')),
    proposed_by_id    text NOT NULL,
    proposed_at       timestamptz NOT NULL,
    decided_by_kind   text CHECK (decided_by_kind IN ('user', 'system')),
    decided_by_id     text,
    decided_at        timestamptz,
    decision_note     text NOT NULL DEFAULT '',
    dispute_note      text NOT NULL DEFAULT '',
    ref               text NOT NULL DEFAULT '',
    supersedes        text REFERENCES fee_assessments (id),
    superseded_by     text REFERENCES fee_assessments (id),
    version           integer NOT NULL DEFAULT 1
);
CREATE INDEX fee_assessments_by_booking ON fee_assessments (booking_id);

CREATE TABLE delivery_complaints (
    id              text PRIMARY KEY,
    booking_id      text NOT NULL REFERENCES bookings (id),
    kind            text NOT NULL CHECK (kind IN ('MISSING_ITEM', 'WRONG_ITEM', 'DAMAGED_ON_ARRIVAL')),
    assets          text[] NOT NULL CHECK (cardinality(assets) > 0),
    description     text NOT NULL,
    evidence        text[] NOT NULL DEFAULT '{}',
    raised_by_kind  text NOT NULL CHECK (raised_by_kind IN ('user', 'system')),
    raised_by_id    text NOT NULL,
    raised_at       timestamptz NOT NULL,
    status          text NOT NULL CHECK (status IN
        ('OPEN', 'REJECTED', 'ACCEPTED', 'RESOLVED_REPLACEMENT', 'RESOLVED_REFUND')),
    fault           text CHECK (fault IN ('UNKNOWN', 'CUSTOMER', 'STORE', 'CARRIER')),
    decided_by_kind text CHECK (decided_by_kind IN ('user', 'system')),
    decided_by_id   text,
    decided_at      timestamptz,
    decision_note   text NOT NULL DEFAULT '',
    credit          jsonb,
    version         integer NOT NULL DEFAULT 1
);
CREATE INDEX delivery_complaints_by_booking ON delivery_complaints (booking_id);
