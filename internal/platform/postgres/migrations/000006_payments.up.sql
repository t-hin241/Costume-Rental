-- Payment ledger per booking. Every charge, payment, refund, deduction and
-- reservation resolution is its own row; balances are derived, never
-- stored. Webhook replays are rejected by provider_events.

CREATE TABLE payment_ledgers (
    booking_id                 text PRIMARY KEY REFERENCES bookings (id),
    currency                   text NOT NULL CHECK (currency = 'VND'),
    credit_reservation_to_rent boolean NOT NULL,
    -- Settled cancellation (payments.Cancellation), when any.
    cancellation               jsonb,
    -- Re-pricing staged by an open modification (payments.Repricing).
    staged_repricing           jsonb,
    committed_repricings       text[] NOT NULL DEFAULT '{}',
    rent_reductions            text[] NOT NULL DEFAULT '{}',
    version                    integer NOT NULL DEFAULT 1,
    updated_at                 timestamptz NOT NULL
);

CREATE TABLE ledger_charges (
    id               text PRIMARY KEY,
    booking_id       text NOT NULL REFERENCES payment_ledgers (booking_id),
    position         integer NOT NULL,
    kind             text NOT NULL CHECK (kind IN ('RENT', 'SECURITY_DEPOSIT', 'RESERVATION_DEPOSIT', 'DELIVERY_FEE',
        'CANCELLATION_FEE', 'LATE_FEE', 'REDELIVERY_FEE', 'CLEANING_FEE', 'DAMAGE_FEE', 'LOSS_FEE', 'OTHER_FEE')),
    amount           bigint NOT NULL CHECK (amount >= 0),
    reason           text NOT NULL DEFAULT '',
    -- Object-storage keys of photos/documents, never the files themselves.
    evidence         text[] NOT NULL DEFAULT '{}',
    created_by_kind  text NOT NULL CHECK (created_by_kind IN ('user', 'system')),
    created_by_id    text NOT NULL,
    created_at       timestamptz NOT NULL,
    reversal_reason  text,
    reversed_by_kind text CHECK (reversed_by_kind IN ('user', 'system')),
    reversed_by_id   text,
    reversed_at      timestamptz,
    assessment_ref   text,
    approved_by_kind text CHECK (approved_by_kind IN ('user', 'system')),
    approved_by_id   text,
    delivery_leg     text CHECK (delivery_leg IN ('OUTBOUND', 'RETURN')),
    CHECK ((reversed_at IS NULL) = (reversal_reason IS NULL))
);
CREATE INDEX ledger_charges_by_booking ON ledger_charges (booking_id, position);
-- An approved fee assessment is charged at most once.
CREATE UNIQUE INDEX ledger_charges_assessment ON ledger_charges (booking_id, assessment_ref) WHERE assessment_ref IS NOT NULL;

CREATE TABLE payments (
    id               text PRIMARY KEY,
    booking_id       text NOT NULL REFERENCES payment_ledgers (booking_id),
    idempotency_key  text NOT NULL UNIQUE CHECK (idempotency_key <> ''),
    method           text NOT NULL CHECK (method IN ('CASH', 'BANK_TRANSFER', 'GATEWAY')),
    provider         text NOT NULL DEFAULT '',
    provider_txn_id  text,
    amount           bigint NOT NULL CHECK (amount > 0),
    status           text NOT NULL CHECK (status IN ('PENDING', 'SUCCEEDED', 'FAILED', 'EXPIRED')),
    created_by_kind  text NOT NULL CHECK (created_by_kind IN ('user', 'system')),
    created_by_id    text NOT NULL,
    created_at       timestamptz NOT NULL,
    settled_at       timestamptz,
    failure_reason   text NOT NULL DEFAULT '',
    needs_review     boolean NOT NULL DEFAULT false,
    modification_ref text
);
CREATE INDEX payments_by_booking ON payments (booking_id, created_at);
CREATE UNIQUE INDEX payments_provider_txn ON payments (provider, provider_txn_id) WHERE provider_txn_id IS NOT NULL;

-- How a payment splits over the RENTAL, SECURITY and RESERVATION buckets.
CREATE TABLE payment_portions (
    payment_id text NOT NULL REFERENCES payments (id),
    bucket     text NOT NULL CHECK (bucket IN ('RENTAL', 'SECURITY', 'RESERVATION')),
    amount     bigint NOT NULL CHECK (amount > 0),
    PRIMARY KEY (payment_id, bucket)
);

CREATE TABLE refunds (
    id                text PRIMARY KEY,
    booking_id        text NOT NULL REFERENCES payment_ledgers (booking_id),
    idempotency_key   text NOT NULL UNIQUE CHECK (idempotency_key <> ''),
    bucket            text NOT NULL CHECK (bucket IN ('RENTAL', 'SECURITY', 'RESERVATION')),
    amount            bigint NOT NULL CHECK (amount > 0),
    reason            text NOT NULL,
    status            text NOT NULL CHECK (status IN
        ('REQUESTED', 'APPROVED', 'REJECTED', 'PROCESSING', 'SUCCEEDED', 'FAILED')),
    requested_by_kind text NOT NULL CHECK (requested_by_kind IN ('user', 'system')),
    requested_by_id   text NOT NULL,
    requested_at      timestamptz NOT NULL,
    decided_by_kind   text CHECK (decided_by_kind IN ('user', 'system')),
    decided_by_id     text,
    decided_at        timestamptz,
    decision_note     text NOT NULL DEFAULT '',
    provider          text NOT NULL DEFAULT '',
    provider_ref      text,
    completed_at      timestamptz,
    failure_reason    text NOT NULL DEFAULT ''
);
CREATE INDEX refunds_by_booking ON refunds (booking_id, requested_at);

CREATE TABLE security_deductions (
    id               text PRIMARY KEY,
    booking_id       text NOT NULL REFERENCES payment_ledgers (booking_id),
    charge_id        text NOT NULL REFERENCES ledger_charges (id),
    amount           bigint NOT NULL CHECK (amount > 0),
    reason           text NOT NULL,
    evidence         text[] NOT NULL DEFAULT '{}',
    status           text NOT NULL CHECK (status IN ('PROPOSED', 'APPROVED', 'REJECTED')),
    proposed_by_kind text NOT NULL CHECK (proposed_by_kind IN ('user', 'system')),
    proposed_by_id   text NOT NULL,
    proposed_at      timestamptz NOT NULL,
    decided_by_kind  text CHECK (decided_by_kind IN ('user', 'system')),
    decided_by_id    text,
    decided_at       timestamptz,
    decision_note    text NOT NULL DEFAULT ''
);
CREATE INDEX security_deductions_by_booking ON security_deductions (booking_id);

CREATE TABLE reservation_resolutions (
    id              text PRIMARY KEY,
    booking_id      text NOT NULL REFERENCES payment_ledgers (booking_id),
    kind            text NOT NULL CHECK (kind IN ('APPLIED_TO_RENTAL')),
    amount          bigint NOT NULL CHECK (amount > 0),
    reason          text NOT NULL DEFAULT '',
    created_by_kind text NOT NULL CHECK (created_by_kind IN ('user', 'system')),
    created_by_id   text NOT NULL,
    created_at      timestamptz NOT NULL
);
CREATE INDEX reservation_resolutions_by_booking ON reservation_resolutions (booking_id);

-- Processed payment/refund provider events: the primary key makes a replayed
-- webhook a no-op even across concurrent deliveries.
CREATE TABLE provider_events (
    provider    text NOT NULL,
    event_id    text NOT NULL,
    booking_id  text NOT NULL REFERENCES payment_ledgers (booking_id),
    received_at timestamptz NOT NULL,
    PRIMARY KEY (provider, event_id)
);
CREATE TRIGGER provider_events_append_only BEFORE UPDATE OR DELETE ON provider_events
    FOR EACH ROW EXECUTE FUNCTION reject_history_change();
