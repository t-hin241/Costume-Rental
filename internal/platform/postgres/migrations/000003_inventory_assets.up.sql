-- Physical assets and their status movement log. Allocations (the
-- scheduling axis) are created in 000005 because they reference bookings.

CREATE TABLE assets (
    id                text PRIMARY KEY,
    code              text NOT NULL UNIQUE CHECK (code <> ''),
    variant_id        text NOT NULL REFERENCES item_variants (id),
    status            text NOT NULL CHECK (status IN
        ('READY', 'RENTED', 'NEEDS_INSPECTION', 'CLEANING', 'REPAIR', 'MISSING', 'RETIRED')),
    status_changed_at timestamptz NOT NULL,
    created_at        timestamptz NOT NULL,
    -- Lets allocations prove they hold an asset of the variant they claim.
    UNIQUE (id, variant_id)
);
-- Allocation planning locks the assets of the demanded variants in id order.
CREATE INDEX assets_by_variant ON assets (variant_id, id);

CREATE TABLE asset_status_changes (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    asset_id    text NOT NULL REFERENCES assets (id),
    action      text NOT NULL,
    from_status text,
    to_status   text NOT NULL,
    actor_kind  text NOT NULL CHECK (actor_kind IN ('user', 'system')),
    actor_id    text NOT NULL,
    at          timestamptz NOT NULL,
    reason      text NOT NULL DEFAULT ''
);
CREATE INDEX asset_status_changes_by_asset ON asset_status_changes (asset_id, at);
CREATE TRIGGER asset_status_changes_append_only BEFORE UPDATE OR DELETE ON asset_status_changes
    FOR EACH ROW EXECUTE FUNCTION reject_history_change();
