-- 081_vendor_credit_note_lines.sql
-- IN.6a: Vendor Credit Note becomes line-aware and Rule #4 compliant
-- for stock-item returns.
--
-- Pre-IN.6 state
-- --------------
-- VendorCreditNote was header-only: a single Amount field and a 2-
-- line JE (Dr AP / Cr PurchaseReturns) on post. No line granularity,
-- no stock-item awareness, no inventory interaction. A VCN on a
-- stock-item return posted successfully, decremented AP, and left
-- the inventory balance UN-reduced. That is:
--   (1) a Rule #4 silent-swallow (stock-item leaving the warehouse
--       with no inventory movement),
--   (2) an accounting imbalance (inventory asset overstated against
--       a matching AP reversal that never removed the asset).
--
-- What IN.6a installs
-- -------------------
-- A new `vendor_credit_note_lines` table mirroring credit_note_lines
-- (IN.5) in shape. Each row carries:
--
--   product_service_id      — link to catalogue; drives the stock/
--                             service dispatch at post time.
--   original_bill_line_id   — nullable FK back to the BillLine that
--                             originally received this qty in. Used
--                             to locate the original bill's inventory
--                             movement and read its snapshot
--                             unit_cost_base as the authoritative
--                             cost for the return.
--   qty, unit_price, amount — standard per-line economics. Partial
--                             returns (qty < original) supported.
--
-- Backward compat
-- ---------------
-- The existing VendorCreditNote.amount field stays. VCNs with zero
-- rows in this new table continue through the legacy header-only
-- posting path (Dr AP / Cr Offset) unchanged — treat those as pure
-- price-adjustment credits, not physical stock returns.
--
-- Controlled-mode deferral (mirror of IN.5 Q2)
-- --------------------------------------------
-- No schema support for controlled-mode (receipt_required=true) VCN
-- stock returns. Phase AP/I.6 Vendor Return Receipt (future) will
-- own the inbound stock-return path. Until it ships, VCN posting
-- under receipt_required=true REJECTS any stock-item line with
-- ErrVendorCreditNoteStockItemRequiresReturnReceipt.

CREATE TABLE IF NOT EXISTS vendor_credit_note_lines (
    id                      BIGSERIAL PRIMARY KEY,
    company_id              BIGINT NOT NULL,
    vendor_credit_note_id   BIGINT NOT NULL,
    sort_order              BIGINT NOT NULL DEFAULT 1,

    product_service_id      BIGINT,
    original_bill_line_id   BIGINT,

    description             TEXT NOT NULL DEFAULT '',
    qty                     NUMERIC(10,4) NOT NULL DEFAULT 1,
    unit_price              NUMERIC(18,4) NOT NULL DEFAULT 0,
    amount                  NUMERIC(18,2) NOT NULL DEFAULT 0,

    created_at              TIMESTAMPTZ,
    updated_at              TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_vendor_credit_note_lines_company_id
    ON vendor_credit_note_lines(company_id);
CREATE INDEX IF NOT EXISTS idx_vendor_credit_note_lines_vcn_id
    ON vendor_credit_note_lines(vendor_credit_note_id);
CREATE INDEX IF NOT EXISTS idx_vendor_credit_note_lines_product_service_id
    ON vendor_credit_note_lines(product_service_id);
CREATE INDEX IF NOT EXISTS idx_vendor_credit_note_lines_original_bill_line_id
    ON vendor_credit_note_lines(original_bill_line_id);
