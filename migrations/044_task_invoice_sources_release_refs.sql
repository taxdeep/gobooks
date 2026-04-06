-- Migration 044: allow draft-deleted bridge rows to retain history
--
-- Batch 4 needs task_invoice_sources rows to survive draft deletion while the
-- source itself becomes billable again. To do that, the bridge row must be
-- allowed to outlive the draft invoice and invoice line it originally pointed
-- at. The service layer clears invoice_id / invoice_line_id to NULL before the
-- draft header and lines are deleted.

ALTER TABLE task_invoice_sources
    ALTER COLUMN invoice_id DROP NOT NULL;

ALTER TABLE task_invoice_sources
    ALTER COLUMN invoice_line_id DROP NOT NULL;
