-- Decision D36: support requests get their own permission. Stored matrix
-- versions are immutable, so this publishes a new one built from the active
-- matrix: every role that reads all bookings gains support.handle and nothing
-- else changes, including in a matrix an admin published. Nobody loses access.

INSERT INTO policy_documents (kind, version, document, created_at, created_by_kind, created_by_id)
SELECT kind, 'reference-2026-11-02',
    (SELECT jsonb_object_agg(role, CASE WHEN perms ? 'booking.read_all' THEN
            (SELECT jsonb_agg(p ORDER BY p COLLATE "C") FROM jsonb_array_elements_text(perms || '["support.handle"]'::jsonb) AS added (p))
        ELSE perms END)
     FROM jsonb_each(document) AS roles (role, perms)),
    now(), 'system', 'migration-000016'
FROM policy_documents WHERE kind = 'PERMISSION_MATRIX' AND active;

UPDATE policy_documents SET active = false WHERE kind = 'PERMISSION_MATRIX' AND active;
UPDATE policy_documents SET active = true WHERE kind = 'PERMISSION_MATRIX' AND version = 'reference-2026-11-02';
