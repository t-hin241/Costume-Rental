DROP TABLE store_closures;
DROP TABLE store_slots;
DROP TABLE policy_documents;
DROP FUNCTION reject_policy_document_change();
DROP TABLE audit_events;
DROP TABLE password_reset_tokens;
DROP TABLE sessions;
DROP TABLE user_roles;
DROP TABLE users;
DROP FUNCTION reject_history_change();
-- btree_gist is left installed: other objects in the database may use it.
