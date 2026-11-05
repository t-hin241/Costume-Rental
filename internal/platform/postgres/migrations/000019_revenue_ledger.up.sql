-- Decision D41: the revenue ledger is filled from ended bookings and from
-- revenue earned outside the website, and finance reconciles succeeded
-- payments and refunds with the bank statement or cash book. The system
-- computes no tax: the tax profile only holds the annual threshold and its
-- warning marks (the JSON document of tax_profiles changes, not its columns).

-- Manual entries have no booking or charge; they say where the revenue was
-- earned, the document behind it and who entered it.
ALTER TABLE revenue_entries
    ALTER COLUMN booking_id DROP NOT NULL,
    ALTER COLUMN source_ref DROP NOT NULL,
    ADD COLUMN source text NOT NULL DEFAULT 'BOOKING' CHECK (source IN ('BOOKING', 'MANUAL')),
    ADD COLUMN channel text NOT NULL DEFAULT '',
    ADD COLUMN note text NOT NULL DEFAULT '',
    ADD COLUMN document_ref text NOT NULL DEFAULT '',
    ADD COLUMN recorded_by_kind text CHECK (recorded_by_kind IN ('user', 'system')),
    ADD COLUMN recorded_by_id text,
    ADD COLUMN created_at timestamptz NOT NULL DEFAULT now(),
    ADD CONSTRAINT revenue_entries_source_fields CHECK (CASE source
        WHEN 'BOOKING' THEN booking_id IS NOT NULL AND source_ref IS NOT NULL AND channel = '' AND recorded_by_id IS NULL
        ELSE booking_id IS NULL AND source_ref IS NULL AND channel <> '' AND recorded_by_id IS NOT NULL END),
    -- Reversals take away exactly what they reverse; originals only add.
    ADD CONSTRAINT revenue_entries_sign CHECK (CASE WHEN reverses IS NULL
        THEN list_amount >= 0 AND discount >= 0 AND net >= 0 AND reason = ''
        ELSE list_amount <= 0 AND discount <= 0 AND net <= 0 AND reason <> '' END);
ALTER TABLE revenue_entries ALTER COLUMN source DROP DEFAULT;
CREATE INDEX revenue_entries_by_booking ON revenue_entries (booking_id) WHERE booking_id IS NOT NULL;
-- A ledger charge is recognised at most once.
CREATE UNIQUE INDEX revenue_entries_charge_once ON revenue_entries (source_ref) WHERE source_ref IS NOT NULL AND reverses IS NULL;
-- Only the end date of a version changes, when a newer one takes over.
CREATE TRIGGER tax_profiles_versions_fixed BEFORE UPDATE OF version, document, effective_from, created_at ON tax_profiles
    FOR EACH ROW EXECUTE FUNCTION reject_history_change();

-- A reconciliation in force per transaction; a revoked one stays as history.
CREATE TABLE transaction_reconciliations (
    id                 text PRIMARY KEY,
    kind               text NOT NULL CHECK (kind IN ('PAYMENT', 'REFUND')),
    transaction_id     text NOT NULL,
    booking_id         text NOT NULL REFERENCES payment_ledgers (booking_id),
    statement_ref      text NOT NULL CHECK (statement_ref <> ''),
    note               text NOT NULL DEFAULT '',
    reconciled_by_kind text NOT NULL CHECK (reconciled_by_kind IN ('user', 'system')),
    reconciled_by_id   text NOT NULL,
    reconciled_at      timestamptz NOT NULL,
    revoke_reason      text,
    revoked_by_kind    text CHECK (revoked_by_kind IN ('user', 'system')),
    revoked_by_id      text,
    revoked_at         timestamptz,
    CHECK ((revoked_at IS NULL) = (revoke_reason IS NULL)),
    CHECK ((revoked_at IS NULL) = (revoked_by_id IS NULL))
);
CREATE UNIQUE INDEX transaction_reconciliations_in_force ON transaction_reconciliations (kind, transaction_id) WHERE revoked_at IS NULL;
CREATE TRIGGER transaction_reconciliations_kept BEFORE DELETE ON transaction_reconciliations
    FOR EACH ROW EXECUTE FUNCTION reject_history_change();
CREATE INDEX payments_by_settlement ON payments (settled_at) WHERE status = 'SUCCEEDED';
CREATE INDEX refunds_by_completion ON refunds (completed_at) WHERE status = 'SUCCEEDED';

-- Finance and admin gain revenue.record and payment.reconcile; as in 000016,
-- a new matrix version is built from the active one and nothing else changes.
INSERT INTO policy_documents (kind, version, document, created_at, created_by_kind, created_by_id)
SELECT kind, 'reference-2026-11-05.2',
    (SELECT jsonb_object_agg(role, CASE WHEN role IN ('finance', 'admin') THEN
            (SELECT jsonb_agg(p ORDER BY p COLLATE "C") FROM (SELECT DISTINCT p
             FROM jsonb_array_elements_text(perms || '["revenue.record", "payment.reconcile"]'::jsonb) AS added (p)) d)
        ELSE perms END)
     FROM jsonb_each(document) AS roles (role, perms)),
    now(), 'system', 'migration-000019'
FROM policy_documents WHERE kind = 'PERMISSION_MATRIX' AND active;

UPDATE policy_documents SET active = false WHERE kind = 'PERMISSION_MATRIX' AND active;
UPDATE policy_documents SET active = true WHERE kind = 'PERMISSION_MATRIX' AND version = 'reference-2026-11-05.2';
