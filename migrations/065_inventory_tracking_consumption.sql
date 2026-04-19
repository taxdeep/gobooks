-- 065_inventory_tracking_consumption.sql
-- Phase F3: tracked outbound consumption log.
--
-- Mirrors inventory_layer_consumption (E2.1) in design: every
-- lot/serial consumed by an outbound movement writes one row here so
-- that ReverseMovement can unwind the consumption exactly.
--
-- Invariant
-- ---------
-- For any tracked issue movement M on a lot/serial item:
--   SUM(inventory_tracking_consumption.quantity_drawn
--       WHERE issue_movement_id = M.id AND reversed_by_movement_id IS NULL)
--   == |M.quantity_delta|
--
-- A reversal row points its reversed_by_movement_id at this movement's
-- reversal, neutralising the draw. If M is reversed and the consumption
-- rows remain, the unwind logic reads them, restores lot.remaining or
-- serial.current_state, then stamps reversed_by_movement_id.
--
-- Schema choice: one table covers both lot and serial by making both
-- lot_id and serial_unit_id nullable. Exactly one must be non-null per
-- row. Enforced by CHECK.
--
-- untracked items write nothing here.

CREATE TABLE IF NOT EXISTS inventory_tracking_consumption (
    id                      BIGSERIAL PRIMARY KEY,
    company_id              BIGINT NOT NULL,
    issue_movement_id       BIGINT NOT NULL,
    item_id                 BIGINT NOT NULL,
    lot_id                  BIGINT,
    serial_unit_id          BIGINT,
    quantity_drawn          NUMERIC(18, 4) NOT NULL,
    reversed_by_movement_id BIGINT,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Exactly one of lot_id / serial_unit_id is set per row.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'chk_inventory_tracking_consumption_one_of'
    ) THEN
        ALTER TABLE inventory_tracking_consumption
            ADD CONSTRAINT chk_inventory_tracking_consumption_one_of
            CHECK (
                (lot_id IS NOT NULL AND serial_unit_id IS NULL)
                OR (lot_id IS NULL AND serial_unit_id IS NOT NULL)
            );
    END IF;
END$$;

-- "Which lots / serials did movement X consume?" — reverse path + audit
CREATE INDEX IF NOT EXISTS idx_tracking_consumption_issue_mov
    ON inventory_tracking_consumption (issue_movement_id);

-- "What consumption rows touched lot Y / serial Z?" — reconciliation
CREATE INDEX IF NOT EXISTS idx_tracking_consumption_lot
    ON inventory_tracking_consumption (lot_id)
    WHERE lot_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_tracking_consumption_serial
    ON inventory_tracking_consumption (serial_unit_id)
    WHERE serial_unit_id IS NOT NULL;
