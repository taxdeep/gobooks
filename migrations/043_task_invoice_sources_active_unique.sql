-- Migration 043: task_invoice_sources active-row uniqueness
--
-- Batch 1 created task_invoice_sources with a full-row UNIQUE(source_type, source_id).
-- Batch 1.5 narrows that rule to active rows only:
--   - history rows are preserved
--   - voided rows remain in place
--   - only one current active linkage may exist per source at a time

ALTER TABLE task_invoice_sources
    ADD COLUMN IF NOT EXISTS voided_at TIMESTAMPTZ NULL;

ALTER TABLE task_invoice_sources
    DROP CONSTRAINT IF EXISTS uq_task_invoice_sources;

DROP INDEX IF EXISTS uq_task_invoice_sources_active;

CREATE UNIQUE INDEX IF NOT EXISTS uq_task_invoice_sources_active
    ON task_invoice_sources(source_type, source_id)
    WHERE voided_at IS NULL;
