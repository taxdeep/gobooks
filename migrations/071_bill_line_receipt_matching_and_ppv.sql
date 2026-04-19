-- 071_bill_line_receipt_matching_and_ppv.sql
-- Phase H slice H.5: Bill ↔ Receipt matching linkage + Purchase Price
-- Variance (PPV) account wiring.
--
-- What this migration adds
-- ------------------------
-- 1. `bill_lines.receipt_line_id` — a nullable FK that lets one Bill
--    line point to the Receipt line that originally accrued GR/IR
--    for the same goods. Matching is line-to-line from the bill side:
--    each bill line can reference AT MOST ONE receipt line; a receipt
--    line MAY be referenced by multiple bill lines over time (partial
--    settlement). No reverse pointer on receipt_lines — authoritative
--    direction is bill → receipt.
--
--    FK action ON DELETE SET NULL: defensive only. In practice,
--    receipt_lines are only deletable while on a draft Receipt, and
--    bill_lines can only reference posted Receipt lines (service-layer
--    invariant), so this FK action rarely triggers.
--
-- 2. `companies.purchase_price_variance_account_id` — the P&L account
--    that receives the Dr / Cr delta between Bill amount and matched
--    Receipt amount on stock-backed lines. Required before PostBill
--    can complete a matched flow; the absence of it fails the post
--    loud (ErrPPVAccountNotConfigured), mirroring the GR/IR config
--    symmetry from H.4.
--
-- What this migration does NOT add
-- --------------------------------
-- - No reverse pointer on `receipt_lines` — keeps Receipt
--   authoritative and forward-looking.
-- - No cached matched_qty on `receipt_lines` — cumulative matching
--   is computed dynamically by summing posted bill_lines.qty grouped
--   by receipt_line_id. Avoids dual-write consistency bugs at the
--   cost of one extra aggregate query per post.
-- - No FK from `companies.purchase_price_variance_account_id` to
--   `accounts(id)` at the schema layer, matching the pattern used by
--   `gr_ir_clearing_account_id` (migration 070). Service-layer
--   validation is the boundary.

ALTER TABLE bill_lines
    ADD COLUMN IF NOT EXISTS receipt_line_id BIGINT;

CREATE INDEX IF NOT EXISTS idx_bill_lines_receipt_line_id ON bill_lines(receipt_line_id);

-- FK kept separate from the column add so ADD COLUMN IF NOT EXISTS
-- stays idempotent. The constraint is named so re-runs can detect it
-- via pg_constraint and skip (done via the DO block so the migration
-- tool's all-or-nothing per-file semantics stay safe).
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'fk_bill_lines_receipt_line'
    ) THEN
        ALTER TABLE bill_lines
            ADD CONSTRAINT fk_bill_lines_receipt_line
            FOREIGN KEY (receipt_line_id) REFERENCES receipt_lines(id) ON DELETE SET NULL;
    END IF;
END$$;

ALTER TABLE companies
    ADD COLUMN IF NOT EXISTS purchase_price_variance_account_id BIGINT;
