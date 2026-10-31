-- Decision D29: a waived or lowered fee gives back what the deposit covered of
-- it, kept as a reversed deduction, and a refund records how the money really
-- went back, which at the counter can be entered after the fact.

ALTER TABLE security_deductions DROP CONSTRAINT security_deductions_status_check;
ALTER TABLE security_deductions ADD CONSTRAINT security_deductions_status_check
    CHECK (status IN ('PROPOSED', 'APPROVED', 'REJECTED', 'REVERSED'));
ALTER TABLE security_deductions
    ADD COLUMN reversed_by_kind text CHECK (reversed_by_kind IN ('user', 'system')),
    ADD COLUMN reversed_by_id   text,
    ADD COLUMN reversed_at      timestamptz,
    ADD COLUMN reversal_reason  text NOT NULL DEFAULT '';

ALTER TABLE refunds
    ADD COLUMN method           text CHECK (method IS NULL OR method IN ('CASH', 'BANK_TRANSFER')),
    ADD COLUMN reference        text NOT NULL DEFAULT '',
    ADD COLUMN recorded_by_kind text CHECK (recorded_by_kind IN ('user', 'system')),
    ADD COLUMN recorded_by_id   text,
    ADD COLUMN recorded_at      timestamptz;
