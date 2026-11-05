-- Decision D39: support conversations are kept 24 months after they are
-- resolved, so the time of resolution is stored. A resolved request takes no
-- more replies, so its last update is when it was resolved.
ALTER TABLE support_requests ADD COLUMN resolved_at timestamptz;
UPDATE support_requests SET resolved_at = updated_at WHERE status = 'RESOLVED';
ALTER TABLE support_requests ADD CONSTRAINT support_requests_resolved_at
    CHECK ((status = 'RESOLVED') = (resolved_at IS NOT NULL));
CREATE INDEX support_requests_by_resolution ON support_requests (resolved_at) WHERE status = 'RESOLVED';

-- Erasing the profile of an unused customer account is its own permission.
-- As in 000016, a new matrix version is built from the active one: admin
-- gains customer.profile_erase and nothing else changes.
INSERT INTO policy_documents (kind, version, document, created_at, created_by_kind, created_by_id)
SELECT kind, 'reference-2026-11-05',
    (SELECT jsonb_object_agg(role, CASE WHEN role = 'admin' THEN
            (SELECT jsonb_agg(p ORDER BY p COLLATE "C") FROM jsonb_array_elements_text(perms || '["customer.profile_erase"]'::jsonb) AS added (p))
        ELSE perms END)
     FROM jsonb_each(document) AS roles (role, perms)),
    now(), 'system', 'migration-000018'
FROM policy_documents WHERE kind = 'PERMISSION_MATRIX' AND active;

UPDATE policy_documents SET active = false WHERE kind = 'PERMISSION_MATRIX' AND active;
UPDATE policy_documents SET active = true WHERE kind = 'PERMISSION_MATRIX' AND version = 'reference-2026-11-05';
