DROP INDEX fee_assessments_one_late;

ALTER TABLE booking_items DROP COLUMN affected_next_booking;

-- Development rollback only: policy versions are immutable, so the trigger
-- is paused to remove the approval versions before restoring the old check.
ALTER TABLE policy_documents DISABLE TRIGGER policy_documents_immutable;
DELETE FROM policy_documents WHERE kind = 'PAYMENT_APPROVAL';
ALTER TABLE policy_documents ENABLE TRIGGER policy_documents_immutable;
ALTER TABLE policy_documents DROP CONSTRAINT policy_documents_kind_check;
ALTER TABLE policy_documents ADD CONSTRAINT policy_documents_kind_check
    CHECK (kind IN ('RENTAL', 'INVENTORY_ALLOCATION', 'PERMISSION_MATRIX'));
