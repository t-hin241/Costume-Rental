-- Development rollback only: policy versions and revenue entries are
-- append-only, so their triggers are paused to remove what 000019 added.
ALTER TABLE policy_documents DISABLE TRIGGER policy_documents_immutable;
DELETE FROM policy_documents WHERE kind = 'PERMISSION_MATRIX' AND version = 'reference-2026-11-05.2';
ALTER TABLE policy_documents ENABLE TRIGGER policy_documents_immutable;

UPDATE policy_documents SET active = false WHERE kind = 'PERMISSION_MATRIX' AND active;
UPDATE policy_documents SET active = true WHERE kind = 'PERMISSION_MATRIX' AND version = (
    SELECT version FROM policy_documents WHERE kind = 'PERMISSION_MATRIX' ORDER BY created_at DESC, version DESC LIMIT 1);

DROP INDEX refunds_by_completion;
DROP INDEX payments_by_settlement;
DROP TABLE transaction_reconciliations;
DROP TRIGGER tax_profiles_versions_fixed ON tax_profiles;

ALTER TABLE revenue_entries DISABLE TRIGGER revenue_entries_append_only;
DELETE FROM revenue_entries WHERE source = 'MANUAL';
ALTER TABLE revenue_entries ENABLE TRIGGER revenue_entries_append_only;
DROP INDEX revenue_entries_charge_once;
DROP INDEX revenue_entries_by_booking;
ALTER TABLE revenue_entries
    DROP CONSTRAINT revenue_entries_sign,
    DROP CONSTRAINT revenue_entries_source_fields,
    DROP COLUMN created_at,
    DROP COLUMN recorded_by_id,
    DROP COLUMN recorded_by_kind,
    DROP COLUMN document_ref,
    DROP COLUMN note,
    DROP COLUMN channel,
    DROP COLUMN source,
    ALTER COLUMN source_ref SET NOT NULL,
    ALTER COLUMN booking_id SET NOT NULL;
