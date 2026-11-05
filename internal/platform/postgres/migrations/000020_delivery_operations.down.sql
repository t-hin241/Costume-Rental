-- Development rollback only: policy versions are immutable, so the trigger
-- is paused to remove the delivery area versions before restoring the check.
ALTER TABLE delivery_attempts DROP CONSTRAINT delivery_attempts_status_check;
DELETE FROM delivery_attempts WHERE status = 'RETURN_FAILED';
ALTER TABLE delivery_attempts ADD CONSTRAINT delivery_attempts_status_check
    CHECK (status IN ('DELIVERY_FAILED', 'PICKUP_FAILED'));

DROP INDEX delivery_tasks_open;
DROP INDEX delivery_tasks_one_open_leg;
DROP INDEX delivery_tasks_one_follow_up;
ALTER TABLE delivery_tasks
    DROP COLUMN replaced_by,
    DROP COLUMN replaces,
    DROP COLUMN closed_reason,
    DROP COLUMN closed_by_id,
    DROP COLUMN closed_by_kind,
    DROP COLUMN follow_up,
    DROP COLUMN gave_up_attempt,
    DROP COLUMN gave_up_at;

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

ALTER TABLE policy_documents DISABLE TRIGGER policy_documents_immutable;
DELETE FROM policy_documents WHERE kind = 'DELIVERY_AREA';
ALTER TABLE policy_documents ENABLE TRIGGER policy_documents_immutable;
ALTER TABLE policy_documents DROP CONSTRAINT policy_documents_kind_check;
ALTER TABLE policy_documents ADD CONSTRAINT policy_documents_kind_check
    CHECK (kind IN ('RENTAL', 'INVENTORY_ALLOCATION', 'PERMISSION_MATRIX', 'PAYMENT_APPROVAL'));
