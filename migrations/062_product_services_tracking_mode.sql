-- 062_product_services_tracking_mode.sql
-- Phase F1: tracking mode foundation.
--
-- Adds tracking_mode to product_services. Values constrained to
-- ('none','lot','serial'). Every existing row defaults to 'none' so the
-- migration is a no-op for the current population.
--
-- Hard invariants enforced by the service layer (NOT by DB — the
-- stock-item relationship would require a function/trigger and we want
-- to keep this migration declarative):
--
--   - tracking_mode may only be 'lot' or 'serial' when the item is a
--     stock item (is_stock_item = TRUE). Non-stock / service items MUST
--     stay on 'none'. See ValidateTrackingModeForItem in
--     internal/models/product_service.go and ChangeTrackingMode in
--     internal/services/product_service_tracking.go.
--
--   - Changing tracking_mode while the item has on-hand or layer
--     remaining > 0 is rejected. Phase F1 does not ship a data
--     conversion tool; operators who need to switch modes must first
--     zero out stock (or wait for a future migration tool).
--
-- Tracking truth vs cost truth
-- ----------------------------
-- tracking_mode governs lot/serial/expiry CAPTURE. It does NOT influence
-- costing — that is still controlled by companies.inventory_costing_method
-- (moving_average | fifo). Phase F deliberately keeps the two orthogonal
-- so lot/serial does not become specific-identification costing by the
-- back door.

ALTER TABLE product_services
    ADD COLUMN IF NOT EXISTS tracking_mode TEXT NOT NULL DEFAULT 'none';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'chk_product_services_tracking_mode'
    ) THEN
        ALTER TABLE product_services
            ADD CONSTRAINT chk_product_services_tracking_mode
            CHECK (tracking_mode IN ('none', 'lot', 'serial'));
    END IF;
END$$;
