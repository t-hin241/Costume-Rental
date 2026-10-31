-- Wires the ledger into the rental flow: the approval policy of refunds and
-- deductions becomes a versioned document, and each returned item keeps
-- whether its late return disrupted the next booking of the asset, which can
-- only be read before the allocation is marked returned.

ALTER TABLE policy_documents DROP CONSTRAINT policy_documents_kind_check;
ALTER TABLE policy_documents ADD CONSTRAINT policy_documents_kind_check
    CHECK (kind IN ('RENTAL', 'INVENTORY_ALLOCATION', 'PERMISSION_MATRIX', 'PAYMENT_APPROVAL'));

ALTER TABLE booking_items ADD COLUMN affected_next_booking boolean NOT NULL DEFAULT false;

-- One late fee per item; an adjustment supersedes it with a linked record.
CREATE UNIQUE INDEX fee_assessments_one_late ON fee_assessments (booking_id, asset_id)
    WHERE kind = 'LATE_RETURN' AND supersedes IS NULL;
