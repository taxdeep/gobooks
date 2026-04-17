-- 056_inventory_movement_api_fields.sql
-- Phase D.0: schema foundation for the Inventory Module API contract.
-- See INVENTORY_MODULE_API.md §6 for the full rationale.
--
-- Adds the fields that IN events carry but the current schema does not yet
-- capture. All columns are nullable so this migration is a no-op on existing
-- rows; the new services/inventory package populates them going forward.
--
-- InventoryMovement.JournalEntryID (the reverse coupling to GL) is kept for
-- now; it will be dropped in a follow-up cleanup once all readers migrate to
-- source_type + source_id -> business document -> document.journal_entry_id.

ALTER TABLE inventory_movements
    ADD COLUMN IF NOT EXISTS currency_code         VARCHAR(3),
    ADD COLUMN IF NOT EXISTS exchange_rate         NUMERIC(20, 8),
    ADD COLUMN IF NOT EXISTS unit_cost_base        NUMERIC(18, 4),
    ADD COLUMN IF NOT EXISTS landed_cost_allocation NUMERIC(18, 2),
    ADD COLUMN IF NOT EXISTS idempotency_key       TEXT,
    ADD COLUMN IF NOT EXISTS actor_user_id         BIGINT,
    ADD COLUMN IF NOT EXISTS source_line_id        BIGINT,
    ADD COLUMN IF NOT EXISTS reversed_by_movement_id BIGINT,
    ADD COLUMN IF NOT EXISTS reversal_of_movement_id BIGINT;

-- Partial unique index: idempotency must be globally unique per company when
-- set, but legacy / in-progress rows with NULL key must not collide.
CREATE UNIQUE INDEX IF NOT EXISTS uq_inventory_movement_idempotency
    ON inventory_movements (company_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- Helps lookups "what reversed this?" and "what did this reverse?".
CREATE INDEX IF NOT EXISTS idx_inventory_movement_reversed_by
    ON inventory_movements (reversed_by_movement_id)
    WHERE reversed_by_movement_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_inventory_movement_reversal_of
    ON inventory_movements (reversal_of_movement_id)
    WHERE reversal_of_movement_id IS NOT NULL;
