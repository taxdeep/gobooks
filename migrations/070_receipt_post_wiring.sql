-- 070_receipt_post_wiring.sql
-- Phase H slice H.3: wiring for Receipt post → receive truth → inventory effect,
-- and the business-document-layer journal Dr Inventory / Cr GR/IR.
--
-- What this migration adds
-- ------------------------
-- 1. `companies.gr_ir_clearing_account_id` — the liability account that
--    Receipt post credits (and that Bill post will later debit under
--    `receipt_required=true` in H.5). Nullable: companies that have
--    never opted into the Receipt-first flow do not need a value.
--    When `receipt_required=true` is flipped ON, PostReceipt requires
--    this to be set or fails loud with ErrGRIRAccountNotConfigured.
--
-- 2. `receipts.journal_entry_id` — linkage from a posted Receipt to
--    the JE that books its inventory accrual. Nullable. Populated
--    only when Receipt posts under `receipt_required=true`; stays
--    NULL on flag=false posts (which remain status-flip-only per
--    §Phase H Scope item 4 — byte-identical to Phase G for legacy
--    companies). VoidReceipt uses its presence as the signal that a
--    JE reversal + movement reversal is needed.
--
-- What this migration does NOT add
-- --------------------------------
-- - No new movement tables — receive truth lives in the existing
--   `inventory_movements` via `source_type='receipt'`.
-- - No new JE source type column. The existing JournalEntry.source_type
--   string accepts 'receipt' without schema change; the enum is
--   app-level (models.LedgerSource*).
-- - No FK constraints to `accounts` / `journal_entries`. Following the
--   repo convention for similar linkage columns (bills.journal_entry_id
--   is not FK'd either) so that schema-level cascade semantics are not
--   implicitly declared; the service layer is the boundary.

ALTER TABLE companies
    ADD COLUMN IF NOT EXISTS gr_ir_clearing_account_id BIGINT;

ALTER TABLE receipts
    ADD COLUMN IF NOT EXISTS journal_entry_id BIGINT;

CREATE INDEX IF NOT EXISTS idx_receipts_journal_entry_id ON receipts(journal_entry_id);
