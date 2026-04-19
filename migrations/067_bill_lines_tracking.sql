-- 067_bill_lines_tracking.sql
-- Phase G.4: Bill line carries lot-tracked receipt data so
-- CreatePurchaseMovements can forward tracking truth to ReceiveStock.
--
-- What this allows
-- ----------------
-- For lot-tracked ProductService lines, the bill now captures
-- lot_number and (optional) expiry_date at the line level. On bill
-- post, CreatePurchaseMovements reads these and feeds the
-- inventory.ReceiveStock LotNumber / ExpiryDate fields. The lot
-- persists in inventory_lots per F2's rules (create-or-top-up).
--
-- What this does NOT allow
-- ------------------------
-- - Serial-tracked items via Bill: serials are not captured here.
--   The bill format has no natural N-serials-per-line surface, and
--   serialized items typically arrive through a dedicated receipt
--   flow (Phase H Receipt). A serial-tracked line on a bill will
--   continue to fail at post time with ErrTrackingDataMissing from
--   the F2 inventory guard — this is intended, not a bug.
-- - Removing Bill-forms-inventory: still active in Phase G.
--   C.G.1 / §C.G.1 of the authority baseline binds Phase G to
--   transition status. Phase H is where Receipt takes over and
--   these columns should eventually move to receipt_lines.
--
-- Schema choice
-- -------------
-- - lot_number TEXT NOT NULL DEFAULT '' — consistent with the
--   convention used elsewhere in this codebase (nullable strings
--   avoided; empty string = "not set"). Non-tracked lines write
--   '' and inventory.validateInboundTracking treats '' as absent.
-- - expiry_date DATE (nullable). F2 already treats null expiry as
--   "no shelf life specified" and adopts any later value at
--   first top-up where the existing row has none.

ALTER TABLE bill_lines
    ADD COLUMN IF NOT EXISTS lot_number      TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS lot_expiry_date DATE;
