-- 084_shipments_add_journal_entry_id.sql
-- IN.7: Patch historical schema gap — `shipments.journal_entry_id`.
--
-- Why this migration exists
-- -------------------------
-- The Phase I.3 slice wired ShipmentPost to create a JE under
-- `shipment_required=true` and write the JE id back to the
-- Shipment row via `s.JournalEntryID = postedJEID`. The Go model
-- (models.Shipment) carries a JournalEntryID *uint field.
--
-- However, the original 076_shipments_and_lines.sql migration did
-- NOT include the `journal_entry_id` column on the `shipments`
-- table, and `Shipment` / `ShipmentLine` were never added to the
-- main `db.AutoMigrate` list in `internal/db/migrate.go`. In
-- development + tests this is invisible — test harnesses call
-- AutoMigrate on the model directly, which quietly adds the
-- column on SQLite. In production (where only SQL migrations
-- run), the column is missing.
--
-- The bug is silent until the first real company flips
-- `shipment_required=true`. At that point the first Shipment post
-- fails with PostgreSQL's "column ... does not exist" error,
-- rolling back the transaction and blocking all downstream work
-- on that rail (Phase I pilot + Phase I.6 AR-side Return Receipt
-- layered pilot both depend on this).
--
-- This patch is analogous to 070_receipt_post_wiring.sql, which
-- did this same ALTER on the `receipts` table when H.3 wired JE
-- formation. That migration shipped; this one for Shipment never
-- did. IN.7 closes the gap.
--
-- Defense-in-depth
-- ----------------
-- Alongside this migration, IN.7 also adds Shipment / ShipmentLine
-- / Receipt / ReceiptLine / WaitingForInvoiceItem to the main
-- `db.AutoMigrate` list in migrate.go so a future column addition
-- on these physical-truth documents is picked up automatically on
-- fresh installs even if a dedicated SQL migration is omitted.
--
-- Safety
-- ------
-- - `ADD COLUMN IF NOT EXISTS` is idempotent. Databases that
--   somehow already have the column (e.g. AutoMigrate ran in a
--   staging env before this patch) no-op safely.
-- - Adding a nullable BIGINT column has zero data-migration cost
--   and zero read impact on existing queries.
-- - No FK constraint here. Convention (bills / receipts /
--   shipments / ar_return_receipts / vendor_return_shipments) is
--   service-layer enforcement of the JE link; the column is plain
--   storage.

ALTER TABLE shipments
    ADD COLUMN IF NOT EXISTS journal_entry_id BIGINT;

CREATE INDEX IF NOT EXISTS idx_shipments_journal_entry_id
    ON shipments(journal_entry_id);
