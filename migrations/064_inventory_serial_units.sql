-- 064_inventory_serial_units.sql
-- Phase F1: serial-tracked stock foundation.
--
-- One row per (company, item, serial_number). Each serial represents
-- exactly ONE unit; there is no quantity concept — quantity for a
-- serial is always 1. current_state tracks lifecycle: received →
-- on_hand → (reserved|issued) → void-restored → on_hand, etc.
--
-- Why a state machine rather than just "remaining_quantity"
-- ---------------------------------------------------------
-- A serial's identity persists through issue and reversal. When a
-- customer returns a serialized item, we need to bring back the SAME
-- serial, not "a unit of this item". The state column tracks that
-- identity across the lifecycle.
--
-- Uniqueness — concurrent on-hand
-- -------------------------------
-- Two rows may coexist with the same (company_id, item_id, serial_number)
-- ONLY if at most one of them is in a "live" state (on_hand | reserved).
-- Issued or void-archived rows may remain alongside the re-received row
-- for history. Enforced via a partial unique index below.
--
-- Expiry
-- ------
-- Expiry lives per-serial (not at a higher unit) because each serial is
-- its own identity. Nullable for items without shelf-life.

CREATE TABLE IF NOT EXISTS inventory_serial_units (
    id              BIGSERIAL PRIMARY KEY,
    company_id      BIGINT NOT NULL,
    item_id         BIGINT NOT NULL,
    serial_number   TEXT NOT NULL,
    current_state   TEXT NOT NULL,
    expiry_date     DATE,
    received_date   DATE NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'chk_inventory_serial_units_state'
    ) THEN
        ALTER TABLE inventory_serial_units
            ADD CONSTRAINT chk_inventory_serial_units_state
            CHECK (current_state IN ('on_hand', 'reserved', 'issued', 'void_archived'));
    END IF;
END$$;

-- A serial_number may appear at most once in a "live" state per item.
-- Historical rows (issued, void_archived) are not blocked — they remain
-- for traceability alongside any re-received row.
CREATE UNIQUE INDEX IF NOT EXISTS uq_inventory_serial_units_live
    ON inventory_serial_units (company_id, item_id, serial_number)
    WHERE current_state IN ('on_hand', 'reserved');

CREATE INDEX IF NOT EXISTS idx_inventory_serial_units_item
    ON inventory_serial_units (company_id, item_id, current_state);

CREATE INDEX IF NOT EXISTS idx_inventory_serial_units_expiry
    ON inventory_serial_units (company_id, item_id, expiry_date)
    WHERE expiry_date IS NOT NULL AND current_state IN ('on_hand', 'reserved');
