ALTER TABLE refunds
    DROP COLUMN method,
    DROP COLUMN reference,
    DROP COLUMN recorded_by_kind,
    DROP COLUMN recorded_by_id,
    DROP COLUMN recorded_at;

ALTER TABLE security_deductions
    DROP COLUMN reversed_by_kind,
    DROP COLUMN reversed_by_id,
    DROP COLUMN reversed_at,
    DROP COLUMN reversal_reason;
ALTER TABLE security_deductions DROP CONSTRAINT security_deductions_status_check;
UPDATE security_deductions SET status = 'REJECTED' WHERE status = 'REVERSED';
ALTER TABLE security_deductions ADD CONSTRAINT security_deductions_status_check
    CHECK (status IN ('PROPOSED', 'APPROVED', 'REJECTED'));
