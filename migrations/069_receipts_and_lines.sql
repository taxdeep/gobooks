-- 069_receipts_and_lines.sql
-- Phase H slice H.2: Receipt and ReceiptLine as first-class documents.
--
-- What this migration installs
-- ----------------------------
-- Two new tables, `receipts` and `receipt_lines`, persisting the
-- Receipt document layer. Each Receipt represents an inbound event
-- (vendor delivery landing at a warehouse). Each ReceiptLine is one
-- item / qty / cost on that Receipt, with optional lot-tracking
-- capture fields and source-identity reservation fields for Phase I
-- PO-line linkage.
--
-- What this migration does NOT do (deliberately — H.2 scope lock)
-- ---------------------------------------------------------------
-- No inventory movement is produced by a Receipt yet. No journal
-- entry. No GR/IR. No Bill coupling. The `status` column can hold
-- 'draft', 'posted', or 'voided', but `posted` is purely a document-
-- layer state in H.2 — it does not drive any inbound truth into
-- inventory_movements / inventory_cost_layers / inventory_balances,
-- and it does not touch the GL. That consumer lands in H.3.
--
-- The `receipt_required` capability rail (migration 068) is NOT
-- checked anywhere in H.2. Receipt creation and posting are gate-
-- agnostic. Gate wiring lands with H.4 (Bill decoupling).
--
-- Source-identity reservation
-- ---------------------------
-- `receipts.purchase_order_id` and `receipt_lines.purchase_order_line_id`
-- are nullable reservation columns for Phase I's PO → Receipt → Bill
-- identity chain. They are accepted on create/update but not read or
-- enforced anywhere in H.2. No FK constraint yet — PO lines are not
-- yet lifted to first-class either; FKs land when the linkage has
-- real consumers.
--
-- Tracking capture
-- ----------------
-- `receipt_lines.lot_number` / `.lot_expiry_date` are the Phase H
-- semantic home for lot-tracked inbound data. In H.2 the columns are
-- written but not read; H.3's ReceiveStockFromReceipt will consume
-- them. The Phase G.4 columns on `bill_lines` remain as the legacy
-- transitional home for `receipt_required=false` companies
-- (INVENTORY_MODULE_API.md §Phase H Scope item 4).

CREATE TABLE IF NOT EXISTS receipts (
    id                  BIGSERIAL PRIMARY KEY,
    company_id          BIGINT       NOT NULL,
    receipt_number      TEXT         NOT NULL DEFAULT '',
    vendor_id           BIGINT,
    warehouse_id        BIGINT       NOT NULL,
    receipt_date        DATE         NOT NULL,
    status              TEXT         NOT NULL DEFAULT 'draft',
    memo                TEXT         NOT NULL DEFAULT '',
    reference           TEXT         NOT NULL DEFAULT '',
    purchase_order_id   BIGINT,
    posted_at           TIMESTAMPTZ,
    voided_at           TIMESTAMPTZ,
    created_at          TIMESTAMPTZ,
    updated_at          TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_receipts_company_id           ON receipts(company_id);
CREATE INDEX IF NOT EXISTS idx_receipts_vendor_id            ON receipts(vendor_id);
CREATE INDEX IF NOT EXISTS idx_receipts_warehouse_id         ON receipts(warehouse_id);
CREATE INDEX IF NOT EXISTS idx_receipts_status               ON receipts(status);
CREATE INDEX IF NOT EXISTS idx_receipts_receipt_date         ON receipts(receipt_date);
CREATE INDEX IF NOT EXISTS idx_receipts_company_number       ON receipts(company_id, receipt_number);
CREATE INDEX IF NOT EXISTS idx_receipts_purchase_order_id    ON receipts(purchase_order_id);

CREATE TABLE IF NOT EXISTS receipt_lines (
    id                       BIGSERIAL PRIMARY KEY,
    company_id               BIGINT          NOT NULL,
    receipt_id               BIGINT          NOT NULL,
    sort_order               INTEGER         NOT NULL DEFAULT 0,
    product_service_id       BIGINT          NOT NULL,
    description              TEXT            NOT NULL DEFAULT '',
    qty                      NUMERIC(18,6)   NOT NULL DEFAULT 0,
    unit                     TEXT            NOT NULL DEFAULT '',
    unit_cost                NUMERIC(18,6)   NOT NULL DEFAULT 0,
    lot_number               TEXT            NOT NULL DEFAULT '',
    lot_expiry_date          DATE,
    purchase_order_line_id   BIGINT,
    created_at               TIMESTAMPTZ,
    updated_at               TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_receipt_lines_company_id             ON receipt_lines(company_id);
CREATE INDEX IF NOT EXISTS idx_receipt_lines_receipt_id             ON receipt_lines(receipt_id);
CREATE INDEX IF NOT EXISTS idx_receipt_lines_product_service_id     ON receipt_lines(product_service_id);
CREATE INDEX IF NOT EXISTS idx_receipt_lines_purchase_order_line_id ON receipt_lines(purchase_order_line_id);
