-- 061_inventory_cost_layer_provenance.sql
-- Phase H2: make cost-layer provenance an explicit first-class property.
--
-- Why this is needed
-- ------------------
-- Before this migration, every inventory_cost_layers row carries a
-- source_movement_id pointing at "the inbound movement that created this
-- layer." RepairFIFOLayerDrift's genesis-repair path re-uses that column
-- as a mere FK anchor (pointing at whichever movement was convenient),
-- leaving the field silently overloaded between two meanings:
--
--   1. receipt layers: source_movement_id really IS the receipt that
--      produced these units
--   2. synthetic genesis layers: source_movement_id is just an FK target
--      to satisfy NOT NULL; it does NOT represent provenance
--
-- Any report/audit/traceability reader that joined InventoryCostLayer ->
-- InventoryMovement would unknowingly misattribute synthesized layers to
-- the anchor movement. This migration splits the two meanings so readers
-- can branch on provenance_type instead of guessing.
--
-- Semantics after this migration
-- ------------------------------
-- - is_synthetic = false AND provenance_type = 'receipt':
--     source_movement_id is authoritative provenance (the inbound event
--     that created the layer). This is every layer written by
--     ReceiveStock on or after Phase E2.
-- - is_synthetic = true AND provenance_type = 'synthetic_genesis':
--     source_movement_id is FK anchor only (the oldest inbound movement
--     for the cell, chosen so the FK passes). Readers interested in real
--     provenance MUST check provenance_type first; source_movement_id is
--     meaningless for true historical attribution on these rows.
--
-- Defaults are set to receipt/false so the NOT NULL promotion does not
-- require a backfill pass — every pre-existing row is a real receipt
-- layer by construction (only RepairFIFOLayerDrift synthesizes, and it
-- hadn't run before this migration).

ALTER TABLE inventory_cost_layers
    ADD COLUMN IF NOT EXISTS is_synthetic BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS provenance_type TEXT NOT NULL DEFAULT 'receipt';

-- Enforce the enum at the database layer so a stray INSERT can never
-- invent a new provenance class without a migration.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'chk_inventory_cost_layers_provenance_type'
    ) THEN
        ALTER TABLE inventory_cost_layers
            ADD CONSTRAINT chk_inventory_cost_layers_provenance_type
            CHECK (provenance_type IN ('receipt', 'synthetic_genesis'));
    END IF;
END$$;

-- The two fields must stay in lock-step. Synthetic rows MUST say they
-- are synthetic; receipt rows MUST NOT.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'chk_inventory_cost_layers_provenance_consistency'
    ) THEN
        ALTER TABLE inventory_cost_layers
            ADD CONSTRAINT chk_inventory_cost_layers_provenance_consistency
            CHECK (
                (is_synthetic = FALSE AND provenance_type = 'receipt')
                OR (is_synthetic = TRUE  AND provenance_type = 'synthetic_genesis')
            );
    END IF;
END$$;
