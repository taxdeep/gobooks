-- Migration 050: Multi-line expense support.
--
-- Creates the expense_lines table, which stores one row per logical cost
-- category within an expense. Existing single-line expenses are backfilled
-- with one line row that mirrors the current header-level fields.
--
-- The expense header retains its amount/expense_account_id columns for
-- backward-compatibility with reporting queries that join against expenses
-- directly. The service layer keeps these in sync with the line totals.

-- ═══════════════════════════════════════════════════════════════════════════════
-- PART 1: expense_lines table
-- ═══════════════════════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS expense_lines (
    id                  BIGSERIAL       PRIMARY KEY,
    expense_id          BIGINT          NOT NULL REFERENCES expenses(id) ON DELETE CASCADE,
    line_order          INTEGER         NOT NULL DEFAULT 0,
    description         TEXT            NOT NULL DEFAULT '',
    amount              NUMERIC(18,2)   NOT NULL DEFAULT 0,
    expense_account_id  BIGINT          REFERENCES accounts(id),
    created_at          TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_expense_lines_expense_id ON expense_lines(expense_id);

-- ═══════════════════════════════════════════════════════════════════════════════
-- PART 2: Backfill — one line per existing expense
-- ═══════════════════════════════════════════════════════════════════════════════

-- Only insert where no lines exist yet (idempotent on replay).
INSERT INTO expense_lines (expense_id, line_order, description, amount, expense_account_id, created_at, updated_at)
SELECT
    e.id,
    0,
    e.description,
    e.amount,
    e.expense_account_id,
    NOW(),
    NOW()
FROM expenses e
WHERE NOT EXISTS (
    SELECT 1 FROM expense_lines el WHERE el.expense_id = e.id
)
AND (e.amount > 0 OR e.expense_account_id IS NOT NULL);
