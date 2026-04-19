-- 058_inventory_balance_reserved.sql
-- Phase E1: reservations. Adds a live counter on inventory_balances that
-- tracks units committed by upstream documents (SO confirmed, not yet
-- shipped) so GetOnHand can expose qty_available = qty_on_hand - reserved.
--
-- Why a counter (not a ledger table) for E1
-- -----------------------------------------
-- The Phase E design treats a reservation as a live counter, not as a
-- movement. Shipping / release simply decrements it. If later phases need
-- per-source traceability ("which reservations outstanding on SO #42?")
-- they can introduce a ledger table and reconcile this counter against
-- SUM(ledger). For E1 the counter is authoritative.
--
-- Safety
-- ------
-- - NULL-safe: the column defaults to 0 on all existing rows, so no
--   reader that ignores it changes behavior.
-- - Cheap: single numeric column, no index required yet (Phase E queries
--   filter by (company_id, item_id, warehouse_id) which already hits the
--   existing indexes).

ALTER TABLE inventory_balances
    ADD COLUMN IF NOT EXISTS quantity_reserved NUMERIC(18, 4) NOT NULL DEFAULT 0;
