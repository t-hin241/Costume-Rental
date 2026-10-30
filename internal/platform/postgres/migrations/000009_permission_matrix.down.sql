-- Development rollback only: policy versions are immutable, so the trigger
-- is paused to remove the matrix versions before restoring the old check.
ALTER TABLE policy_documents DISABLE TRIGGER policy_documents_immutable;
DELETE FROM policy_documents WHERE kind = 'PERMISSION_MATRIX';
ALTER TABLE policy_documents ENABLE TRIGGER policy_documents_immutable;
ALTER TABLE policy_documents DROP CONSTRAINT policy_documents_kind_check;
ALTER TABLE policy_documents ADD CONSTRAINT policy_documents_kind_check
    CHECK (kind IN ('RENTAL', 'INVENTORY_ALLOCATION'));
