-- 057_drop_inventory_movement_journal_entry_id.sql
-- Phase D.0 slice 8: retire the legacy InventoryMovement.JournalEntryID
-- reverse coupling to the General Ledger.
--
-- Why drop it
-- -----------
-- The original design had inventory rows pointing at their companion JE
-- directly. That makes inventory aware of GL internals — the wrong
-- dependency direction for a bounded context. As of Phase D.0 slices
-- 2-6 every mutation goes through the new services/inventory IN verbs
-- which never populate journal_entry_id; the field is already zero on
-- all new rows. Readers that need "what JE matches this movement?"
-- should resolve via source_type + source_id -> business document
-- (bill / invoice / warehouse_transfer / etc.) -> document.journal_entry_id.
--
-- Safety
-- ------
-- - The column has been nullable from day one; dropping it loses only
--   the legacy linkage and does not lose any business data.
-- - The companion Go struct field is removed in the same commit.
-- - The partial index on the column (if present) is dropped alongside.

DROP INDEX IF EXISTS idx_inventory_movements_journal_entry_id;
DROP INDEX IF EXISTS idx_inventory_movement_je_id;

ALTER TABLE inventory_movements
    DROP COLUMN IF EXISTS journal_entry_id;
