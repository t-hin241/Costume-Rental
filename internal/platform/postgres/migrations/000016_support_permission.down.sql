-- Development rollback only: policy versions are immutable, so the trigger is
-- paused to remove this version. The newest version left becomes active.
ALTER TABLE policy_documents DISABLE TRIGGER policy_documents_immutable;
DELETE FROM policy_documents WHERE kind = 'PERMISSION_MATRIX' AND version = 'reference-2026-11-02';
ALTER TABLE policy_documents ENABLE TRIGGER policy_documents_immutable;

UPDATE policy_documents SET active = false WHERE kind = 'PERMISSION_MATRIX' AND active;
UPDATE policy_documents SET active = true WHERE kind = 'PERMISSION_MATRIX' AND version = (
    SELECT version FROM policy_documents WHERE kind = 'PERMISSION_MATRIX' ORDER BY created_at DESC, version DESC LIMIT 1);
