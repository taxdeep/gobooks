-- Migration 025: invoice email logs
-- Logs all email send attempts for invoices (successful and failed).
-- Company-scoped for audit and troubleshooting purposes.
-- Immutable record: once created, never modified (status reflects final state).

CREATE TABLE IF NOT EXISTS invoices_email_logs (
    id              BIGSERIAL       PRIMARY KEY,
    company_id      BIGINT          NOT NULL REFERENCES companies(id)   ON DELETE CASCADE,
    invoice_id      BIGINT          NOT NULL REFERENCES invoices(id)    ON DELETE CASCADE,
    
    -- Recipient information
    to_email        TEXT            NOT NULL,
    cc_emails       TEXT            NOT NULL DEFAULT '',  -- comma-separated
    
    -- Send attempt result
    send_status     TEXT            NOT NULL DEFAULT 'pending',  -- pending|sent|failed
    error_message   TEXT,
    smtp_response   TEXT,
    
    -- Message content reference
    subject         TEXT,
    template_type   TEXT            NOT NULL DEFAULT 'invoice',  -- invoice|reminder|reminder2|etc
    
    -- Audit information
    triggered_by    BIGINT,         -- user_id who triggered send, NULL if automatic
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    sent_at         TIMESTAMPTZ,
    
    -- Metadata
    metadata_json   JSONB           NOT NULL DEFAULT '{}'  -- e.g., {"retry_count": 0, "attachment_size": 102400}
);

-- Indexes for filtering and auditing
CREATE INDEX IF NOT EXISTS idx_invoices_email_logs_company     ON invoices_email_logs(company_id);
CREATE INDEX IF NOT EXISTS idx_invoices_email_logs_invoice     ON invoices_email_logs(invoice_id);
CREATE INDEX IF NOT EXISTS idx_invoices_email_logs_status      ON invoices_email_logs(company_id, send_status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_invoices_email_logs_template    ON invoices_email_logs(company_id, template_type);
