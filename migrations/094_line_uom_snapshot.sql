-- 094_line_uom_snapshot.sql
-- UOM phase U2 — line-level UOM snapshot columns on the four primary
-- doc-line tables: invoice_lines, bill_lines, sales_order_lines,
-- purchase_order_lines.
--
-- Why three columns per line:
--   line_uom         — the UOM the operator picked at save time. Snapshot.
--   line_uom_factor  — how many ProductService.StockUOM equal one line_uom.
--                      Snapshotted so historical docs print correctly even
--                      if the catalog changes after save.
--   qty_in_stock_uom — Qty × line_uom_factor, computed once at save time
--                      and stored. Lets every downstream consumer (inventory
--                      module, reports, JE build) read the stock-quantity
--                      directly without redoing the multiply (and risking
--                      rounding drift across joins).
--
-- Defaults: line_uom='EA', factor=1, qty_in_stock_uom=qty.  Backfill keeps
-- pre-U2 lines correct (every existing line has factor=1 implicitly).
--
-- See UOM_DESIGN.md §3.2 for the full snapshot rationale.
-- Vendor-side line tables (vendor_credit_note_lines, expense_lines, etc.)
-- and AR/AP return-line tables are NOT touched in this slice — defer to
-- a follow-up.  They don't post inventory directly except via the same
-- ProductService.StockUOM path which already has the right defaults.

-- Helper: each ALTER + UPDATE pair is identical apart from the table name.

ALTER TABLE invoice_lines
    ADD COLUMN IF NOT EXISTS line_uom         VARCHAR(16)   NOT NULL DEFAULT 'EA';
ALTER TABLE invoice_lines
    ADD COLUMN IF NOT EXISTS line_uom_factor  NUMERIC(18,6) NOT NULL DEFAULT 1;
ALTER TABLE invoice_lines
    ADD COLUMN IF NOT EXISTS qty_in_stock_uom NUMERIC(18,4) NOT NULL DEFAULT 0;

UPDATE invoice_lines
   SET qty_in_stock_uom = qty
 WHERE qty_in_stock_uom = 0
   AND qty > 0;

ALTER TABLE bill_lines
    ADD COLUMN IF NOT EXISTS line_uom         VARCHAR(16)   NOT NULL DEFAULT 'EA';
ALTER TABLE bill_lines
    ADD COLUMN IF NOT EXISTS line_uom_factor  NUMERIC(18,6) NOT NULL DEFAULT 1;
ALTER TABLE bill_lines
    ADD COLUMN IF NOT EXISTS qty_in_stock_uom NUMERIC(18,4) NOT NULL DEFAULT 0;

UPDATE bill_lines
   SET qty_in_stock_uom = qty
 WHERE qty_in_stock_uom = 0
   AND qty > 0;

ALTER TABLE sales_order_lines
    ADD COLUMN IF NOT EXISTS line_uom         VARCHAR(16)   NOT NULL DEFAULT 'EA';
ALTER TABLE sales_order_lines
    ADD COLUMN IF NOT EXISTS line_uom_factor  NUMERIC(18,6) NOT NULL DEFAULT 1;
ALTER TABLE sales_order_lines
    ADD COLUMN IF NOT EXISTS qty_in_stock_uom NUMERIC(18,4) NOT NULL DEFAULT 0;

UPDATE sales_order_lines
   SET qty_in_stock_uom = quantity
 WHERE qty_in_stock_uom = 0
   AND quantity > 0;

ALTER TABLE purchase_order_lines
    ADD COLUMN IF NOT EXISTS line_uom         VARCHAR(16)   NOT NULL DEFAULT 'EA';
ALTER TABLE purchase_order_lines
    ADD COLUMN IF NOT EXISTS line_uom_factor  NUMERIC(18,6) NOT NULL DEFAULT 1;
ALTER TABLE purchase_order_lines
    ADD COLUMN IF NOT EXISTS qty_in_stock_uom NUMERIC(18,4) NOT NULL DEFAULT 0;

UPDATE purchase_order_lines
   SET qty_in_stock_uom = qty
 WHERE qty_in_stock_uom = 0
   AND qty > 0;
