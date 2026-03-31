-- Migration 024: invoices extended fields
-- Extends invoices table with template linkage, state tracking timestamps,
-- and snapshot fields for historical preservation.
-- All snapshots preserve customer/account details at posting time (immutable).

ALTER TABLE invoices
    ADD COLUMN IF NOT EXISTS template_id          BIGINT               REFERENCES invoices_templates(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS issued_at            TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS sent_at              TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS voided_at            TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS balance_due          NUMERIC(18,2)        NOT NULL DEFAULT 0,
    
    -- Snapshots: preserve customer state at posting time (never updated)
    ADD COLUMN IF NOT EXISTS customer_name_snapshot    TEXT,
    ADD COLUMN IF NOT EXISTS customer_email_snapshot   TEXT,
    ADD COLUMN IF NOT EXISTS customer_address_snapshot TEXT,
    
    -- Snapshots: preserve revenue account details at posting time (for audit trail)
    ADD COLUMN IF NOT EXISTS principal_account_id_snapshot BIGINT,
    ADD COLUMN IF NOT EXISTS principal_account_name_snapshot TEXT,
    ADD COLUMN IF NOT EXISTS principal_account_code_snapshot TEXT;

-- Index for filtering by invoice state and dates
CREATE INDEX IF NOT EXISTS idx_invoices_status_issued         ON invoices(company_id, status, issued_at);
CREATE INDEX IF NOT EXISTS idx_invoices_status_sent           ON invoices(company_id, status, sent_at);
CREATE INDEX IF NOT EXISTS idx_invoices_template              ON invoices(company_id, template_id);
CREATE INDEX IF NOT EXISTS idx_invoices_balance_due           ON invoices(company_id, balance_due) WHERE balance_due > 0;
