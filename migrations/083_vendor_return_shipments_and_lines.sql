-- 083_vendor_return_shipments_and_lines.sql
-- Phase I slice I.6b.1: VendorReturnShipment and
-- VendorReturnShipmentLine as first-class documents — the AP-side
-- outbound stock-return document (we ship goods back to the vendor).
-- Buy-side mirror of Shipment (I.3) in document shape, specialised
-- for the return direction.
--
-- Naming note (charter Q2)
-- ------------------------
-- Internal identifiers use `VendorReturnShipment` /
-- `vendor_return_shipments` / `source_type='vendor_return_shipment'`.
-- The UI surface label is **"Return to Vendor"** (not "Vendor
-- Return Shipment"). This split avoids collision with the pre-
-- existing `models.VendorReturn` (a different AP business-semantic
-- concept already linked from VendorCreditNote.VendorReturnID).
--
-- Scope authority
-- ---------------
-- INVENTORY_MODULE_API.md §7 Phase I.6; PHASE_I6_CHARTER.md §6 slice
-- table row I.6b.1. Charter Q1 ordering: AR-side (I.6a) ships
-- end-to-end before AP-side (I.6b) starts. I.6b.1 ships the AP
-- document shell; the controlled-mode retrofit that wires it to VCN
-- coverage enforcement lands in I.6b.3 and uses the dedicated
-- narrow inventory verb from I.6b.2a.
--
-- Role in Phase I.6
-- -----------------
-- Under controlled mode (receipt_required=true), VendorReturnShipment
-- becomes the Rule #4 movement owner for AP-return stock lines.
-- That dispatch flip lands in I.6b.3 (Rule4DocVendorCreditNote
-- surrenders ownership to Rule4DocVendorReturnShipment under
-- receipt_required=true). In legacy mode (receipt_required=false),
-- VendorReturnShipment is always OPTIONAL — IN.6a's VCN stock-
-- reversal path remains the legacy movement owner.
--
-- Identity chain (wired at post time in I.6b.2)
-- ---------------------------------------------
--   BillLine → VendorCreditNoteLine → VendorReturnShipmentLine → inventory_movement
--
-- The per-line link is
-- `vendor_return_shipment_lines.vendor_credit_note_line_id`. The
-- header-level link is `vendor_return_shipments.vendor_credit_note_id`.
--
-- Per charter Q7 hard rules, BOTH FKs are NULLABLE at schema level.
-- Legality is enforced at service layer — Q8's "no standalone
-- Return Shipment" is a save-time service check, and Q6's "exact
-- per-line coverage" is a VCN-post-time service check (wired in
-- I.6b.3). Schema nullability keeps orphan rows recoverable per Q7
-- mitigation #4: if a VCN is voided after the physical movement
-- posted, the VendorReturnShipment stays and its own void reverses
-- its own movement independently per Q5 document-local rule.
--
-- What this migration does NOT do (deliberately — I.6b.1 scope lock)
-- ------------------------------------------------------------------
-- No inventory movement is produced by a VendorReturnShipment yet.
-- No journal entry. No VCN coupling logic. No new inventory verb.
-- The `status` column can hold 'draft', 'posted', or 'voided', but
-- `posted` is purely a document-layer state in I.6b.1 — no outflow
-- truth into inventory_movements / inventory_cost_layers /
-- inventory_balances, and no GL touch. That consumer lands in
-- I.6b.2 (CreateVendorReturnShipment / PostVendorReturnShipment /
-- VoidVendorReturnShipment — calls the narrow traced-cost outflow
-- verb from I.6b.2a). The VCN-side retrofit (rejection → acceptance
-- with coverage check) lands in I.6b.3.
--
-- The `receipt_required` capability rail is NOT checked anywhere
-- in I.6b.1. VendorReturnShipment creation and posting are gate-
-- agnostic at the document layer. Gate wiring lands with I.6b.3.
--
-- Per-line unit_cost: deliberately NOT captured
-- ----------------------------------------------
-- Like ShipmentLine (I.2) and ARReturnReceiptLine (I.6a.1),
-- VendorReturnShipmentLine has NO unit_cost column. Per the
-- authoritative-cost principle in INVENTORY_MODULE_API.md §2,
-- outbound cost is authoritative from the inventory module. For
-- AP return-at-traced-cost specifically, charter Q3 defines a
-- dedicated narrow-semantic verb (`IssueVendorReturn` /
-- `ReturnToVendorAtTracedCost`, shipping in I.6b.2a) that accepts
-- lineage (OriginalMovementID via the VCN-line's original_bill_line_id
-- chain) + intent, reads `unit_cost_base` from the source movement
-- internally, and writes the outflow at that exact cost. The
-- business-document layer never declares cost — that's the whole
-- point of the narrow verb.
--
-- Rejected alternatives (preserved for audit clarity)
-- ---------------------------------------------------
-- The "let callers extend IssueStock with a UnitCostOverride"
-- shortcut was explicitly rejected by charter Q3 to preserve
-- inventory engine cost authority. If that instinct resurfaces
-- during I.6b.2 / I.6b.2a implementation, stop — that path is
-- closed by Q3.

CREATE TABLE IF NOT EXISTS vendor_return_shipments (
    id                              BIGSERIAL    PRIMARY KEY,
    company_id                      BIGINT       NOT NULL,
    vendor_return_shipment_number   TEXT         NOT NULL DEFAULT '',
    vendor_id                       BIGINT,
    warehouse_id                    BIGINT       NOT NULL,
    ship_date                       DATE         NOT NULL,
    status                          TEXT         NOT NULL DEFAULT 'draft',
    memo                            TEXT         NOT NULL DEFAULT '',
    reference                       TEXT         NOT NULL DEFAULT '',
    vendor_credit_note_id           BIGINT,
    posted_at                       TIMESTAMPTZ,
    voided_at                       TIMESTAMPTZ,
    created_at                      TIMESTAMPTZ,
    updated_at                      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_vendor_return_shipments_company_id
    ON vendor_return_shipments(company_id);
CREATE INDEX IF NOT EXISTS idx_vendor_return_shipments_vendor_id
    ON vendor_return_shipments(vendor_id);
CREATE INDEX IF NOT EXISTS idx_vendor_return_shipments_warehouse_id
    ON vendor_return_shipments(warehouse_id);
CREATE INDEX IF NOT EXISTS idx_vendor_return_shipments_status
    ON vendor_return_shipments(status);
CREATE INDEX IF NOT EXISTS idx_vendor_return_shipments_ship_date
    ON vendor_return_shipments(ship_date);
CREATE INDEX IF NOT EXISTS idx_vendor_return_shipments_company_number
    ON vendor_return_shipments(company_id, vendor_return_shipment_number);
CREATE INDEX IF NOT EXISTS idx_vendor_return_shipments_vcn_id
    ON vendor_return_shipments(vendor_credit_note_id);

CREATE TABLE IF NOT EXISTS vendor_return_shipment_lines (
    id                              BIGSERIAL      PRIMARY KEY,
    company_id                      BIGINT         NOT NULL,
    vendor_return_shipment_id       BIGINT         NOT NULL,
    sort_order                      INTEGER        NOT NULL DEFAULT 0,
    product_service_id              BIGINT         NOT NULL,
    description                     TEXT           NOT NULL DEFAULT '',
    qty                             NUMERIC(18,6)  NOT NULL DEFAULT 0,
    unit                            TEXT           NOT NULL DEFAULT '',
    vendor_credit_note_line_id      BIGINT,
    created_at                      TIMESTAMPTZ,
    updated_at                      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_vrs_lines_company_id
    ON vendor_return_shipment_lines(company_id);
CREATE INDEX IF NOT EXISTS idx_vrs_lines_vrs_id
    ON vendor_return_shipment_lines(vendor_return_shipment_id);
CREATE INDEX IF NOT EXISTS idx_vrs_lines_product_service_id
    ON vendor_return_shipment_lines(product_service_id);
CREATE INDEX IF NOT EXISTS idx_vrs_lines_vcn_line_id
    ON vendor_return_shipment_lines(vendor_credit_note_line_id);
