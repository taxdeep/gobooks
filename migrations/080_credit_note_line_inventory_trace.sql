-- 080_credit_note_line_inventory_trace.sql
-- IN.5: Credit Note becomes Rule #4 compliant for stock-item lines.
--
-- Pre-IN.5 state
-- --------------
-- CreditNoteLine.ProductServiceID existed but credit_note_post.go
-- never consulted it for inventory purposes. Posting a credit note
-- for returned stock items booked Dr Revenue / Cr AR (revenue
-- reversal) but left inventory unchanged — a Rule #4 silent-swallow
-- AND a real accounting imbalance (COGS remained on the P&L for
-- goods that came physically back on the shelf).
--
-- What IN.5 installs
-- ------------------
-- `credit_note_lines.original_invoice_line_id BIGINT` — nullable FK
-- back to the InvoiceLine that originally sold this qty out. The
-- link is what lets credit-note posting reach back to the specific
-- inventory_movements row the sale created and reverse it via
-- inventory.ReverseMovement, which preserves the ORIGINAL unit cost
-- (March's COGS reverses at March's cost, not today's drifted
-- weighted average). This is the Q1 authoritative-cost decision
-- from the IN.5 design discussion.
--
-- Nullable because
-- ----------------
-- Pure-service or pure-fee lines on a credit note do not point at a
-- sold inventory item. Those lines leave this column NULL and the
-- service layer treats them as legacy revenue-reversal-only rows.
--
-- What IN.5 does NOT install (Q2 deferral)
-- ----------------------------------------
-- No schema support for controlled-mode (shipment_required=true)
-- credit-note inventory returns. The Phase I.6 Return Receipt
-- charter is the planned owner for outbound stock returns under
-- controlled mode; until I.6 ships, credit-note posting under
-- shipment_required=true REJECTS any stock-item line with
-- ErrCreditNoteStockItemRequiresReturnReceipt. No column added here
-- for that path; when I.6 lands it will install its own schema.

ALTER TABLE credit_note_lines
    ADD COLUMN IF NOT EXISTS original_invoice_line_id BIGINT;

CREATE INDEX IF NOT EXISTS idx_credit_note_lines_original_invoice_line_id
    ON credit_note_lines(original_invoice_line_id);
