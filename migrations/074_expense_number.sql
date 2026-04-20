-- 074_expense_number.sql
-- Add a human-facing reference number to Expense so it joins PO / SO /
-- Quote / Bill / Invoice as a numbered document. Configurable via
-- Settings → Company → Numbering (module key "expense"), auto-assigned
-- on create; no unique constraint at the DB layer (matches the pattern
-- for other document numbers) — the service-level settings-counter
-- handles uniqueness for new rows.
--
-- NOT NULL DEFAULT '' lets existing rows stay valid with an empty
-- number. They'll render without a reference on detail pages; an
-- admin can backfill by editing individual rows if needed.

ALTER TABLE expenses
    ADD COLUMN IF NOT EXISTS expense_number TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_expenses_company_number
    ON expenses(company_id, expense_number);
