-- Migration 038: Payment transaction posting state.

ALTER TABLE payment_transactions
    ADD COLUMN IF NOT EXISTS posted_journal_entry_id BIGINT REFERENCES journal_entries(id) ON DELETE SET NULL;

ALTER TABLE payment_transactions
    ADD COLUMN IF NOT EXISTS posted_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_payment_txn_posted
    ON payment_transactions(posted_journal_entry_id) WHERE posted_journal_entry_id IS NOT NULL;
