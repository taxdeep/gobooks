-- 059_inventory_cost_layers.sql
-- Phase E2: FIFO cost layers.
--
-- One row per inbound event (ReceiveStock) tracks the original quantity
-- and unit cost of that receipt plus a running remaining_quantity that
-- decrements as outbound events draw from the oldest layers first.
--
-- The weighted-average costing method ignores this table (continues to
-- read directly from inventory_balances.average_cost). Layers are still
-- written on every receipt so that switching a company to FIFO later
-- doesn't start from an empty layer set — historical receipts are
-- preserved as the initial FIFO stack.
--
-- Ordering rule
-- -------------
-- FIFO draws from the lowest (received_date, id) tuple first. The tie-
-- break on id handles same-day receipts deterministically.
--
-- Invariants (enforced by Go code; not by DB constraints so backfills and
-- reversals stay flexible)
-- ------------------------
-- - 0 ≤ remaining_quantity ≤ original_quantity at all times.
-- - SUM(remaining_quantity) per (item, warehouse) equals the
--   (item, warehouse) inventory_balances.quantity_on_hand under FIFO.
--   Under weighted-avg this invariant MAY drift temporarily because
--   weighted-avg issues don't touch layers — a future slice can opt into
--   enforcing the invariant or add a reconcile job.

CREATE TABLE IF NOT EXISTS inventory_cost_layers (
    id                  BIGSERIAL PRIMARY KEY,
    company_id          BIGINT NOT NULL,
    item_id             BIGINT NOT NULL,
    warehouse_id        BIGINT,
    source_movement_id  BIGINT NOT NULL,
    original_quantity   NUMERIC(18, 4) NOT NULL,
    remaining_quantity  NUMERIC(18, 4) NOT NULL,
    unit_cost_base      NUMERIC(18, 4) NOT NULL,
    received_date       DATE NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- FIFO draws from the oldest (received_date, id) tuple first — the order
-- is written here once so query planners always pick it up.
CREATE INDEX IF NOT EXISTS idx_cost_layers_fifo_order
    ON inventory_cost_layers (company_id, item_id, warehouse_id, received_date, id)
    WHERE remaining_quantity > 0;

-- Lookup by the movement that created the layer (for reconciliation and
-- a future reversal-of-receipt path that needs to retire the layer).
CREATE INDEX IF NOT EXISTS idx_cost_layers_source_movement
    ON inventory_cost_layers (source_movement_id);
