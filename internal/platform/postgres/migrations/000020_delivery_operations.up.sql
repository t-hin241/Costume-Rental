-- Wires delivery into the rental flow (decisions D42, D43): the service area
-- becomes a versioned policy document instead of a zone table nothing read,
-- shipments keep what staff decided once the carrier stopped attempting, and
-- each leg of a booking has at most one shipment in progress.

ALTER TABLE policy_documents DROP CONSTRAINT policy_documents_kind_check;
ALTER TABLE policy_documents ADD CONSTRAINT policy_documents_kind_check
    CHECK (kind IN ('RENTAL', 'INVENTORY_ALLOCATION', 'PERMISSION_MATRIX', 'PAYMENT_APPROVAL', 'DELIVERY_AREA'));

DROP TABLE delivery_zones;

ALTER TABLE delivery_tasks
    ADD COLUMN gave_up_at      timestamptz,
    ADD COLUMN gave_up_attempt text NOT NULL DEFAULT '',
    ADD COLUMN follow_up       text NOT NULL DEFAULT ''
        CHECK (follow_up IN ('', 'NEW_SHIPMENT', 'AT_STORE', 'END_BOOKING')),
    ADD COLUMN closed_by_kind  text CHECK (closed_by_kind IN ('user', 'system')),
    ADD COLUMN closed_by_id    text,
    ADD COLUMN closed_reason   text NOT NULL DEFAULT '',
    ADD COLUMN replaces        text REFERENCES delivery_tasks (id),
    -- Deferred: a closed shipment links to its follow-up, saved after it.
    ADD COLUMN replaced_by     text REFERENCES delivery_tasks (id) DEFERRABLE INITIALLY DEFERRED;

-- A closed shipment is followed by at most one new shipment.
CREATE UNIQUE INDEX delivery_tasks_one_follow_up ON delivery_tasks (replaces) WHERE replaces IS NOT NULL;
-- One shipment in progress per leg; cancelled and closed ones are history.
CREATE UNIQUE INDEX delivery_tasks_one_open_leg ON delivery_tasks (booking_id, direction)
    WHERE status NOT IN ('CANCELLED', 'CLOSED_UNFULFILLED');
CREATE INDEX delivery_tasks_open ON delivery_tasks (window_from)
    WHERE status NOT IN ('DELIVERED', 'RECEIVED_AT_STORE', 'CANCELLED', 'LOST', 'CLOSED_UNFULFILLED');

ALTER TABLE delivery_attempts DROP CONSTRAINT delivery_attempts_status_check;
ALTER TABLE delivery_attempts ADD CONSTRAINT delivery_attempts_status_check
    CHECK (status IN ('DELIVERY_FAILED', 'PICKUP_FAILED', 'RETURN_FAILED'));
