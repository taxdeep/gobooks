-- Migration 051: Per-line task linkage for expense lines.
--
-- Adds task_id and is_billable to expense_lines so each cost-category row can
-- be independently linked to a customer task and marked billable.
-- The header-level Expense.task_id / Expense.is_billable remain as a derived
-- summary (first non-null line task) maintained by the service layer.

ALTER TABLE expense_lines ADD COLUMN IF NOT EXISTS task_id BIGINT REFERENCES tasks(id);
ALTER TABLE expense_lines ADD COLUMN IF NOT EXISTS is_billable BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS idx_expense_lines_task_id ON expense_lines(task_id);
