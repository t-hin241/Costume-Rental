-- Delivery: service-area zones with the fixed customer fee table, carrier
-- tasks per leg, attempts and the deduplicated event log.
--
-- Addresses and phone numbers are PII: they live only on delivery_tasks,
-- are read under delivery.read_* permissions, and must follow the retention
-- policy still to be decided with the owner (not in logs or audit).

CREATE TABLE delivery_zones (
    id             text PRIMARY KEY,
    area_version   text NOT NULL CHECK (area_version <> ''),
    provider_code  text NOT NULL,
    name           text NOT NULL,
    province_code  text NOT NULL,
    district_codes text[] NOT NULL DEFAULT '{}',
    ward_codes     text[] NOT NULL DEFAULT '{}',
    currency       text NOT NULL CHECK (currency = 'VND'),
    outbound_fee   bigint NOT NULL CHECK (outbound_fee >= 0),
    return_fee     bigint NOT NULL CHECK (return_fee >= 0),
    redelivery_fee bigint NOT NULL CHECK (redelivery_fee >= 0),
    active         boolean NOT NULL,
    updated_at     timestamptz NOT NULL
);
CREATE INDEX delivery_zones_by_area ON delivery_zones (area_version, province_code) WHERE active;

CREATE TABLE delivery_tasks (
    id                     text PRIMARY KEY,
    booking_id             text NOT NULL REFERENCES bookings (id),
    customer_id            text NOT NULL REFERENCES users (id),
    direction              text NOT NULL CHECK (direction IN ('OUTBOUND', 'RETURN')),
    status                 text NOT NULL,
    progress               text,
    status_before_cancel   text,
    provider_code          text NOT NULL,
    -- Our idempotent reference sent to the carrier.
    client_reference       text NOT NULL UNIQUE,
    provider_shipment_code text,
    service_code           text NOT NULL DEFAULT '',
    from_address           jsonb NOT NULL,
    to_address             jsonb NOT NULL,
    window_from            timestamptz NOT NULL,
    window_until           timestamptz NOT NULL CHECK (window_until > window_from),
    expected_at            timestamptz,
    picked_up_at           timestamptz,
    delivered_at           timestamptz,
    received_at            timestamptz,
    parcel                 jsonb NOT NULL,
    currency               text NOT NULL CHECK (currency = 'VND'),
    customer_fee           bigint NOT NULL CHECK (customer_fee >= 0),
    carrier_fee_estimated  bigint CHECK (carrier_fee_estimated >= 0),
    carrier_fee_final      bigint CHECK (carrier_fee_final >= 0),
    failure_code           text NOT NULL DEFAULT '',
    failure_reason         text NOT NULL DEFAULT '',
    created_at             timestamptz NOT NULL,
    version                integer NOT NULL DEFAULT 1,
    updated_at             timestamptz NOT NULL
);
CREATE INDEX delivery_tasks_by_booking ON delivery_tasks (booking_id);
CREATE UNIQUE INDEX delivery_tasks_shipment ON delivery_tasks (provider_code, provider_shipment_code)
    WHERE provider_shipment_code IS NOT NULL;

CREATE TABLE delivery_attempts (
    task_id            text NOT NULL REFERENCES delivery_tasks (id),
    id                 text NOT NULL,
    status             text NOT NULL CHECK (status IN ('DELIVERY_FAILED', 'PICKUP_FAILED')),
    at                 timestamptz NOT NULL,
    fault              text NOT NULL CHECK (fault IN ('UNKNOWN', 'CUSTOMER', 'STORE', 'CARRIER')),
    reason             text NOT NULL DEFAULT '',
    evidence           text NOT NULL DEFAULT '',
    classified_by_kind text CHECK (classified_by_kind IN ('user', 'system')),
    classified_by_id   text,
    PRIMARY KEY (task_id, id)
);

-- Carrier and manual events; (task_id, key) rejects a replayed webhook.
CREATE TABLE delivery_events (
    task_id         text NOT NULL REFERENCES delivery_tasks (id),
    key             text NOT NULL,
    position        integer NOT NULL,
    source          text NOT NULL CHECK (source IN ('CARRIER_WEBHOOK', 'CARRIER_API', 'ADMIN')),
    status          text NOT NULL,
    provider_status text NOT NULL DEFAULT '',
    occurred_at     timestamptz NOT NULL,
    received_at     timestamptz NOT NULL,
    actor_kind      text CHECK (actor_kind IN ('user', 'system')),
    actor_id        text,
    reason          text NOT NULL DEFAULT '',
    evidence        text NOT NULL DEFAULT '',
    applied         boolean NOT NULL,
    PRIMARY KEY (task_id, key)
);
CREATE TRIGGER delivery_events_append_only BEFORE UPDATE OR DELETE ON delivery_events
    FOR EACH ROW EXECUTE FUNCTION reject_history_change();
