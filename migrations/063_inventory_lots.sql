-- 063_inventory_lots.sql
-- Phase F1: lot-tracked stock foundation.
--
-- One row per (company, item, lot_number). A lot represents a bucket
-- of units received together sharing a lot identifier and (optionally)
-- an expiry date. The remaining_quantity counter decrements as
-- outbound events consume from the lot.
--
-- Scope
-- -----
-- - Company-scoped and item-scoped. A lot number is unique only within
--   a (company_id, item_id) pair; two companies may legitimately share
--   the string "LOT-2026-01".
-- - Tracking truth ONLY. The lot's cost attribution continues to live
--   on inventory_cost_layers / inventory_balances per the company's
--   costing method. This table does not carry cost.
-- - Expiry is nullable so industries without shelf-life (hardware,
--   durable goods) don't have to invent a date. Industries that need
--   it (pharma, food) populate it at inbound time.
--
-- Uniqueness
-- ----------
-- UNIQUE (company_id, item_id, lot_number) — a second inbound of the
-- same lot_number for the same item is treated as a top-up: the
-- service layer increments remaining_quantity instead of creating a
-- second row (F2 implements this).

CREATE TABLE IF NOT EXISTS inventory_lots (
    id                  BIGSERIAL PRIMARY KEY,
    company_id          BIGINT NOT NULL,
    item_id             BIGINT NOT NULL,
    lot_number          TEXT NOT NULL,
    expiry_date         DATE,
    received_date       DATE NOT NULL,
    original_quantity   NUMERIC(18, 4) NOT NULL,
    remaining_quantity  NUMERIC(18, 4) NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_inventory_lots_company_item_lot
    ON inventory_lots (company_id, item_id, lot_number);

CREATE INDEX IF NOT EXISTS idx_inventory_lots_expiry
    ON inventory_lots (company_id, item_id, expiry_date)
    WHERE expiry_date IS NOT NULL AND remaining_quantity > 0;
