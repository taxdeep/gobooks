-- 086_inventory_balance_warehouse_unique_fix.sql
-- Fix uq_inv_balances_item_location so multi-warehouse posts work.
--
-- Background
-- ----------
-- Migration 028 created inventory_balances with unique index
--   uq_inv_balances_item_location (company_id, item_id, location_type, location_ref)
-- Later, Phase B added `warehouse_id BIGINT` to the table via GORM
-- AutoMigrate — but the unique index was never updated. Result:
--
--   1. A legacy bill post (no warehouse routing) creates a balance row
--      at warehouse_id=NULL.
--   2. After the operator sets up a default warehouse, a new bill post
--      routes to that warehouse. readOrCreateBalance looks up by
--      warehouse_id=X, misses the NULL row, tries to INSERT a new row.
--   3. The INSERT violates the old unique index because the
--      (company, item, location_type, location_ref) tuple already
--      exists on the NULL-warehouse row, regardless of warehouse_id.
--
-- Operator symptom:
--   "Could not submit bill: inventory purchase movements: receive
--    stock for item 12: inventory: create balance: ERROR: duplicate
--    key value violates unique constraint 'uq_inv_balances_item_location'"
--
-- What this migration does
-- ------------------------
-- 1. Drops the old unique index.
-- 2. Migrates NULL-warehouse balance rows to the company's default
--    warehouse where one exists AND no default-warehouse row for the
--    same item already exists. Otherwise leaves the NULL row alone
--    (new index allows it to coexist with specific-warehouse rows).
-- 3. Recreates the unique index with warehouse_id included, using
--    COALESCE(warehouse_id, 0) so NULL rows collapse to a single
--    bucket per (item, location) instead of PG's default "each NULL
--    is distinct" behavior — which would let unbounded duplicate
--    NULL-warehouse rows creep in.
--
-- Safety
-- ------
-- Fresh installs: inventory_balances is created empty; the UPDATE
-- is a no-op, the index swap is idempotent (DROP IF EXISTS /
-- CREATE IF NOT EXISTS).
--
-- Existing data: rows with NULL warehouse_id keep semantic meaning.
-- The conditional migration in step 2 is a best-effort consolidation
-- for single-default-warehouse companies — it will NOT overwrite a
-- pre-existing default-warehouse row for the same item, to avoid
-- losing qty/avg state. Companies with more complex layouts can
-- reconcile manually after the schema fix lands.

-- 1. Drop the old unique index.
DROP INDEX IF EXISTS uq_inv_balances_item_location;

-- 2. Best-effort migration of NULL-warehouse rows to the default
--    warehouse. Skips rows where a default-warehouse row already
--    exists for the same (company, item, location_type, location_ref)
--    — those will coexist with the legacy NULL row under the new
--    unique index, and can be reconciled manually.
UPDATE inventory_balances AS ib
   SET warehouse_id = w.id
  FROM warehouses AS w
 WHERE ib.warehouse_id IS NULL
   AND ib.company_id = w.company_id
   AND w.is_default = true
   AND w.is_active = true
   AND NOT EXISTS (
        SELECT 1 FROM inventory_balances AS ib2
         WHERE ib2.company_id     = ib.company_id
           AND ib2.item_id        = ib.item_id
           AND ib2.location_type  = ib.location_type
           AND ib2.location_ref   = ib.location_ref
           AND ib2.warehouse_id   = w.id
   );

-- 3. Recreate the unique index with warehouse_id included.
--    COALESCE collapses NULL to 0 so at most ONE NULL-warehouse row
--    can exist per (company, item, location). Non-NULL warehouse_id
--    values keep their natural distinct semantics.
CREATE UNIQUE INDEX IF NOT EXISTS uq_inv_balances_item_location
    ON inventory_balances(
        company_id,
        item_id,
        location_type,
        location_ref,
        COALESCE(warehouse_id, 0)
    );
