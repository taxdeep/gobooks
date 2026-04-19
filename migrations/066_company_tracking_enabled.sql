-- 066_company_tracking_enabled.sql
-- Phase G slice G.1: company-level capability gate for tracking.
--
-- Why this gate exists
-- --------------------
-- Phase F made inventory IN/OUT tracked-item-aware, but no business
-- document flow (bill / invoice / opening balance / transfer / build)
-- can yet supply the required tracking data. Without a gate, an admin
-- could flip product_services.tracking_mode to 'lot' or 'serial' on an
-- item that sees real bill / invoice traffic, and every subsequent
-- post would fail with ErrLotSelectionMissing / ErrSerialSelectionMissing
-- bubbled up from the IN-verb layer — no actionable remediation path.
--
-- This column is the company-level switch that governs whether any item
-- owned by this company may be moved out of tracking_mode='none'. It
-- defaults to FALSE so all existing companies remain safe; enabling it
-- is a deliberate admin action (see ChangeCompanyTrackingCapability in
-- services/product_service_tracking.go), audited in AuditLog.
--
-- Semantic anchor: INVENTORY_MODULE_API.md §F.7 defines this as the
-- first of four company capability gates. The remaining three
-- (receipt_required, shipment_required, manufacturing_enabled) land in
-- their respective phases.

ALTER TABLE companies
    ADD COLUMN IF NOT EXISTS tracking_enabled BOOLEAN NOT NULL DEFAULT FALSE;
