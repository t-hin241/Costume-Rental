-- Allocations block one asset for one booking line component over
-- [blocked_from, blocked_until). They belong to inventory but reference
-- bookings, hence this separate step.
--
-- Concurrency contract (see internal/inventory/inventorypg):
--   1. Writers that add or extend blocks lock the candidate asset rows
--      (SELECT ... FOR UPDATE, ordered by id) before reading allocations, so
--      two bookings competing for the last asset are serialised.
--   2. allocations_no_overlap is the backstop: the database itself rejects
--      two ACTIVE allocations of one asset with overlapping blocks.
-- A late return keeps blocking past blocked_until while handed_over_at is
-- set and returned_at is not; the planner reads that state under rule 1.

CREATE TABLE allocations (
    id                    text PRIMARY KEY,
    booking_id            text NOT NULL REFERENCES bookings (id),
    line_id               text NOT NULL,
    component_key         text NOT NULL,
    asset_id              text NOT NULL,
    variant_id            text NOT NULL,
    blocked_from          timestamptz NOT NULL,
    blocked_until         timestamptz NOT NULL,
    block                 tstzrange GENERATED ALWAYS AS (tstzrange(blocked_from, blocked_until, '[)')) STORED,
    status                text NOT NULL CHECK (status IN ('ACTIVE', 'RELEASED', 'CLOSED')),
    created_at            timestamptz NOT NULL,
    handed_over_at        timestamptz,
    returned_at           timestamptz,
    released_at           timestamptz,
    release_reason        text NOT NULL DEFAULT '',
    closed_at             timestamptz,
    -- Tentative hold for a pending booking modification.
    modification_id       text REFERENCES booking_modifications (id),
    -- Hold that only covers extra time for an existing allocation.
    extends_allocation_id text REFERENCES allocations (id),
    FOREIGN KEY (asset_id, variant_id) REFERENCES assets (id, variant_id),
    CHECK (blocked_until > blocked_from),
    CHECK ((status = 'RELEASED') = (released_at IS NOT NULL)),
    CHECK ((status = 'CLOSED') = (closed_at IS NOT NULL)),
    CHECK (released_at IS NULL OR handed_over_at IS NULL),
    CHECK (returned_at IS NULL OR (handed_over_at IS NOT NULL AND returned_at >= handed_over_at)),
    CHECK (extends_allocation_id IS NULL OR modification_id IS NOT NULL),
    CONSTRAINT allocations_no_overlap EXCLUDE USING gist (asset_id WITH =, block WITH &&) WHERE (status = 'ACTIVE')
);
CREATE INDEX allocations_by_booking ON allocations (booking_id);
CREATE INDEX allocations_active_by_asset ON allocations (asset_id, blocked_from) WHERE status = 'ACTIVE';
CREATE INDEX allocations_by_modification ON allocations (modification_id) WHERE modification_id IS NOT NULL;
-- Overdue scan: handed over, not returned, still active.
CREATE INDEX allocations_in_use ON allocations (blocked_until)
    WHERE status = 'ACTIVE' AND handed_over_at IS NOT NULL AND returned_at IS NULL;
