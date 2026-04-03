-- Migration 040: Business-layer payment receipt headers.
-- Stores receipt metadata such as payment method without changing JE logic.

CREATE TABLE IF NOT EXISTS payment_receipts (
    id               BIGSERIAL      PRIMARY KEY,
    company_id       BIGINT         NOT NULL REFERENCES companies(id) ON DELETE RESTRICT,
    customer_id      BIGINT         NOT NULL REFERENCES customers(id) ON DELETE RESTRICT,
    invoice_id       BIGINT         REFERENCES invoices(id) ON DELETE SET NULL,
    journal_entry_id BIGINT         NOT NULL REFERENCES journal_entries(id) ON DELETE RESTRICT,
    bank_account_id  BIGINT         NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
    payment_method   TEXT           NOT NULL DEFAULT 'other',
    amount_base      NUMERIC(18,2)  NOT NULL DEFAULT 0,
    memo             TEXT           NOT NULL DEFAULT '',
    entry_date       TIMESTAMPTZ    NOT NULL,
    created_at       TIMESTAMPTZ    NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ    NOT NULL DEFAULT now(),
    CONSTRAINT uq_payment_receipts_journal_entry UNIQUE (journal_entry_id)
);

CREATE INDEX IF NOT EXISTS idx_payment_receipts_company ON payment_receipts(company_id);
CREATE INDEX IF NOT EXISTS idx_payment_receipts_customer ON payment_receipts(customer_id);
CREATE INDEX IF NOT EXISTS idx_payment_receipts_invoice ON payment_receipts(invoice_id);
CREATE INDEX IF NOT EXISTS idx_payment_receipts_bank_account ON payment_receipts(bank_account_id);
