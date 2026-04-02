-- Migration 039: Payment transaction application state.
-- Tracks when a charge/capture transaction has been applied to an invoice.

ALTER TABLE payment_transactions
    ADD COLUMN IF NOT EXISTS applied_invoice_id BIGINT REFERENCES invoices(id) ON DELETE SET NULL;

ALTER TABLE payment_transactions
    ADD COLUMN IF NOT EXISTS applied_at TIMESTAMPTZ;
