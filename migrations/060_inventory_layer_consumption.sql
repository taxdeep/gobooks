-- 060_inventory_layer_consumption.sql
-- Phase E2.1: FIFO correctness gate.
--
-- Per-layer draws for every FIFO-costed outbound event. One issue
-- movement typically produces N consumption rows, one per layer it
-- touched. Reversal of the issue reads these rows, restores each
-- layer's remaining_quantity, then (in a follow-up row on the
-- consumption table) marks them as reversed so we never double-restore
-- on re-reversal attempts.
--
-- Why this is the FIFO correctness gate
-- -------------------------------------
-- E2 shipped FIFO consumption but not FIFO reversal — a voided sale
-- restored on-hand via snapshot cost but left the layer remainings
-- stale. Under sustained void traffic the invariant
-- SUM(remaining_quantity) == quantity_on_hand
-- would drift for FIFO companies and the next FIFO draw would skip
-- the "restored" units. This table closes that loop.
--
-- Schema choices
-- --------------
-- - quantity_drawn is the signed delta from the caller's perspective:
--   always positive on the original consume row, always negative on a
--   reversal-restoration row. SUM(quantity_drawn) per (layer_id) equals
--   the net draw against that layer at any point in time; layer
--   remaining = original - SUM(quantity_drawn WHERE reversed_by IS NULL).
-- - No ON DELETE cascade intentionally: layers are immutable history,
--   and a failing cascade is better than silent data loss.

CREATE TABLE IF NOT EXISTS inventory_layer_consumption (
    id                  BIGSERIAL PRIMARY KEY,
    company_id          BIGINT NOT NULL,
    issue_movement_id   BIGINT NOT NULL,
    layer_id            BIGINT NOT NULL,
    quantity_drawn      NUMERIC(18, 4) NOT NULL,
    unit_cost_base      NUMERIC(18, 4) NOT NULL,

    -- When this consumption is undone by a reversal, this points at the
    -- reversal movement. NULL = still live. A consumption row + its
    -- reversal row together net to zero against the layer.
    reversed_by_movement_id BIGINT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Primary access pattern: "what layers did movement X draw from?" —
-- needed both for reversal and for historical FIFO valuation.
CREATE INDEX IF NOT EXISTS idx_layer_consumption_issue
    ON inventory_layer_consumption (issue_movement_id);

-- Secondary: "what consumption rows touched layer Y?" — useful for
-- reconciliation jobs that cross-check remaining against net draws.
CREATE INDEX IF NOT EXISTS idx_layer_consumption_layer
    ON inventory_layer_consumption (layer_id);
