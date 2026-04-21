-- 082_ar_return_receipts_and_lines.sql
-- Phase I slice I.6a.1: ARReturnReceipt and ARReturnReceiptLine as
-- first-class documents — the AR-side inbound stock-return document
-- (customer returns goods, warehouse books them back in). Sell-side
-- mirror of Receipt (H.3) in document shape, specialised for the
-- return direction.
--
-- Scope authority
-- ---------------
-- INVENTORY_MODULE_API.md §7 Phase I.6; PHASE_I6_CHARTER.md §6 slice
-- table row I.6a.1. Charter Q1 locks AR-first ordering (I.6a ships
-- end-to-end before I.6b starts).
--
-- Role in Phase I.6
-- -----------------
-- Under controlled mode (shipment_required=true), ARReturnReceipt
-- becomes the Rule #4 movement owner for AR-return stock lines. That
-- dispatch flip lands in I.6a.3 (Rule4DocCreditNote surrenders
-- ownership to Rule4DocARReturnReceipt under shipment_required=true).
-- In legacy mode (shipment_required=false), ARReturnReceipt is
-- always OPTIONAL — IN.5's CreditNote stock-return path remains the
-- legacy movement owner.
--
-- Identity chain (wired at post time in I.6a.2)
-- ---------------------------------------------
--   InvoiceLine → CreditNoteLine → ARReturnReceiptLine → inventory_movement
--
-- The per-line link is ar_return_receipt_lines.credit_note_line_id.
-- The header-level link is ar_return_receipts.credit_note_id.
--
-- Per charter Q7 hard rules, BOTH FKs are NULLABLE at schema level.
-- Legality is enforced at service layer — Q8's "no standalone Return
-- Receipt" is a save-time service check, and Q6's "exact per-line
-- coverage" is a CreditNote-post-time service check (wired in I.6a.3).
-- Schema nullability keeps orphan rows recoverable per Q7 mitigation
-- #4: if a CreditNote is voided after the physical movement posted,
-- the ARReturnReceipt stays (its own void reverses its own movement
-- independently per Q5 document-local rule).
--
-- What this migration does NOT do (deliberately — I.6a.1 scope lock)
-- ------------------------------------------------------------------
-- No inventory movement is produced by an ARReturnReceipt yet. No
-- journal entry. No CreditNote coupling logic. The `status` column
-- can hold 'draft', 'posted', or 'voided', but `posted` is purely
-- a document-layer state in I.6a.1 — it does not drive receive
-- truth into inventory_movements / inventory_cost_layers /
-- inventory_balances, and it does not touch the GL. That consumer
-- lands in I.6a.2 (CreateARReturnReceipt / PostARReturnReceipt /
-- VoidARReturnReceipt — uses inventory.ReceiveStock at traced cost
-- via CreditNoteLine.OriginalInvoiceLineID). The CreditNote-side
-- retrofit (rejection → acceptance with coverage check) lands in
-- I.6a.3.
--
-- The `shipment_required` capability rail is NOT checked anywhere
-- in I.6a.1. ARReturnReceipt creation and posting are gate-agnostic
-- at the document layer. Gate wiring lands with I.6a.3.
--
-- Per-line unit_cost: deliberately NOT captured
-- ----------------------------------------------
-- Like ShipmentLine (I.2), ARReturnReceiptLine has NO unit_cost
-- column. Per the authoritative-cost principle in
-- INVENTORY_MODULE_API.md §2, inventory-return cost is authoritative
-- from the inventory module at post time — I.6a.2 reads it from the
-- original Invoice's inventory_movement via the
-- CreditNoteLine.OriginalInvoiceLineID lineage (March's COGS reverses
-- at March's cost, never at today's drifted weighted average).
-- Adding a unit_cost column here would create a second source of
-- truth and is forbidden by the same principle that keeps it off
-- ShipmentLine.
--
-- Per charter Q3, the traced-cost OUTBOUND verb (return-to-vendor)
-- ships as a dedicated narrow inventory verb in I.6b.2a; traced-cost
-- INBOUND (this document) uses the existing inventory.ReceiveStock
-- verb at the cost read from the source movement. No new verb needed
-- on the AR side.
--
-- Naming note
-- -----------
-- The number column is `return_receipt_number` (not
-- `ar_return_receipt_number`) because the table prefix `ar_return_`
-- already carries the AR / return context. Matches the Receipt /
-- Shipment precedent (receipt_number, shipment_number).

CREATE TABLE IF NOT EXISTS ar_return_receipts (
    id                      BIGSERIAL    PRIMARY KEY,
    company_id              BIGINT       NOT NULL,
    return_receipt_number   TEXT         NOT NULL DEFAULT '',
    customer_id             BIGINT,
    warehouse_id            BIGINT       NOT NULL,
    return_date             DATE         NOT NULL,
    status                  TEXT         NOT NULL DEFAULT 'draft',
    memo                    TEXT         NOT NULL DEFAULT '',
    reference               TEXT         NOT NULL DEFAULT '',
    credit_note_id          BIGINT,
    posted_at               TIMESTAMPTZ,
    voided_at               TIMESTAMPTZ,
    created_at              TIMESTAMPTZ,
    updated_at              TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_ar_return_receipts_company_id
    ON ar_return_receipts(company_id);
CREATE INDEX IF NOT EXISTS idx_ar_return_receipts_customer_id
    ON ar_return_receipts(customer_id);
CREATE INDEX IF NOT EXISTS idx_ar_return_receipts_warehouse_id
    ON ar_return_receipts(warehouse_id);
CREATE INDEX IF NOT EXISTS idx_ar_return_receipts_status
    ON ar_return_receipts(status);
CREATE INDEX IF NOT EXISTS idx_ar_return_receipts_return_date
    ON ar_return_receipts(return_date);
CREATE INDEX IF NOT EXISTS idx_ar_return_receipts_company_number
    ON ar_return_receipts(company_id, return_receipt_number);
CREATE INDEX IF NOT EXISTS idx_ar_return_receipts_credit_note_id
    ON ar_return_receipts(credit_note_id);

CREATE TABLE IF NOT EXISTS ar_return_receipt_lines (
    id                        BIGSERIAL      PRIMARY KEY,
    company_id                BIGINT         NOT NULL,
    ar_return_receipt_id      BIGINT         NOT NULL,
    sort_order                INTEGER        NOT NULL DEFAULT 0,
    product_service_id        BIGINT         NOT NULL,
    description               TEXT           NOT NULL DEFAULT '',
    qty                       NUMERIC(18,6)  NOT NULL DEFAULT 0,
    unit                      TEXT           NOT NULL DEFAULT '',
    credit_note_line_id       BIGINT,
    created_at                TIMESTAMPTZ,
    updated_at                TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_ar_return_receipt_lines_company_id
    ON ar_return_receipt_lines(company_id);
CREATE INDEX IF NOT EXISTS idx_ar_return_receipt_lines_ar_return_receipt_id
    ON ar_return_receipt_lines(ar_return_receipt_id);
CREATE INDEX IF NOT EXISTS idx_ar_return_receipt_lines_product_service_id
    ON ar_return_receipt_lines(product_service_id);
CREATE INDEX IF NOT EXISTS idx_ar_return_receipt_lines_credit_note_line_id
    ON ar_return_receipt_lines(credit_note_line_id);
