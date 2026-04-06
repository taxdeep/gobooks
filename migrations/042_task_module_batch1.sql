-- Migration 042: Task + Billable Expense module — Batch 1: data foundation.
--
-- Creates the tasks, expenses, and task_invoice_sources tables.
-- Extends bill_lines and product_services with task-linkage and system-item fields.
-- No UI, no business logic, no posting-engine changes in this migration.
--
-- All CREATE TABLE / ALTER TABLE statements use IF NOT EXISTS / IF NOT EXISTS guards
-- for idempotency on replay. All new columns on existing tables have safe defaults
-- so existing rows are completely unaffected.

-- ═══════════════════════════════════════════════════════════════════════════════
-- PART 1: tasks table
-- ═══════════════════════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS tasks (
    id                  BIGSERIAL       PRIMARY KEY,
    company_id          BIGINT          NOT NULL REFERENCES companies(id)  ON DELETE RESTRICT,
    customer_id         BIGINT          NOT NULL REFERENCES customers(id)  ON DELETE RESTRICT,

    -- Work description — used as invoice line description when task is billed.
    title               TEXT            NOT NULL DEFAULT '',

    task_date           DATE            NOT NULL,

    -- Snapshot fields: fixed at creation time, never updated by downstream changes.
    quantity            NUMERIC(18,6)   NOT NULL DEFAULT 1,
    unit_type           TEXT            NOT NULL DEFAULT 'hour',   -- hour|day|unit|fixed
    rate                NUMERIC(18,6)   NOT NULL DEFAULT 0,
    currency_code       TEXT            NOT NULL DEFAULT '',

    is_billable         BOOLEAN         NOT NULL DEFAULT true,

    -- Status machine: open → completed → invoiced | cancelled
    status              TEXT            NOT NULL DEFAULT 'open',

    notes               TEXT            NOT NULL DEFAULT '',

    -- Quick-lookup cache for current invoice linkage.
    -- Authoritative source is task_invoice_sources.
    -- Cleared to NULL when the linked invoice is voided.
    invoice_id          BIGINT          REFERENCES invoices(id)      ON DELETE SET NULL,
    invoice_line_id     BIGINT          REFERENCES invoice_lines(id) ON DELETE SET NULL,

    created_at          TIMESTAMPTZ     NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ     NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_tasks_company         ON tasks(company_id);
CREATE INDEX IF NOT EXISTS idx_tasks_customer        ON tasks(customer_id);
CREATE INDEX IF NOT EXISTS idx_tasks_status          ON tasks(company_id, status);
CREATE INDEX IF NOT EXISTS idx_tasks_invoice         ON tasks(invoice_id)
    WHERE invoice_id IS NOT NULL;

-- ═══════════════════════════════════════════════════════════════════════════════
-- PART 2: expenses table (standalone expense records, distinct from vendor bills)
-- ═══════════════════════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS expenses (
    id                      BIGSERIAL       PRIMARY KEY,
    company_id              BIGINT          NOT NULL REFERENCES companies(id)  ON DELETE RESTRICT,

    -- Task linkage (optional; triggers task-body rules when set).
    task_id                 BIGINT          REFERENCES tasks(id)      ON DELETE SET NULL,

    -- billable_customer_id: who this expense is billed to.
    -- When task_id IS NOT NULL, must equal tasks.customer_id (enforced by service layer).
    billable_customer_id    BIGINT          REFERENCES customers(id)  ON DELETE SET NULL,

    is_billable             BOOLEAN         NOT NULL DEFAULT false,

    -- reinvoice_status: '' (not applicable) | uninvoiced | invoiced | excluded
    -- Only meaningful when is_billable = true AND task_id IS NOT NULL.
    reinvoice_status        TEXT            NOT NULL DEFAULT '',

    -- Quick-lookup cache for current invoice linkage (cleared on invoice void).
    invoice_id              BIGINT          REFERENCES invoices(id)      ON DELETE SET NULL,
    invoice_line_id         BIGINT          REFERENCES invoice_lines(id) ON DELETE SET NULL,

    -- Expense details.
    expense_date            DATE            NOT NULL,
    description             TEXT            NOT NULL DEFAULT '',
    amount                  NUMERIC(18,2)   NOT NULL DEFAULT 0,
    currency_code           TEXT            NOT NULL DEFAULT '',

    -- Optional vendor and GL account references.
    vendor_id               BIGINT          REFERENCES vendors(id)   ON DELETE SET NULL,
    expense_account_id      BIGINT          REFERENCES accounts(id)  ON DELETE SET NULL,

    -- Reserved for future markup-pricing support; UI not exposed in v1.
    markup_percent          NUMERIC(8,4)    NOT NULL DEFAULT 0,

    notes                   TEXT            NOT NULL DEFAULT '',

    created_at              TIMESTAMPTZ     NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ     NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_expenses_company       ON expenses(company_id);
CREATE INDEX IF NOT EXISTS idx_expenses_task          ON expenses(task_id)
    WHERE task_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_expenses_customer      ON expenses(billable_customer_id)
    WHERE billable_customer_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_expenses_reinvoice     ON expenses(company_id, reinvoice_status)
    WHERE reinvoice_status = 'uninvoiced';

-- ═══════════════════════════════════════════════════════════════════════════════
-- PART 3: Extend bill_lines with task linkage (line-level, not header-level)
-- ═══════════════════════════════════════════════════════════════════════════════
--
-- One bill may have lines belonging to different tasks / different customers.
-- Line-level is the correct granularity; header-level would be too coarse.

ALTER TABLE bill_lines ADD COLUMN IF NOT EXISTS task_id
    BIGINT REFERENCES tasks(id) ON DELETE SET NULL;

ALTER TABLE bill_lines ADD COLUMN IF NOT EXISTS billable_customer_id
    BIGINT REFERENCES customers(id) ON DELETE SET NULL;

ALTER TABLE bill_lines ADD COLUMN IF NOT EXISTS is_billable
    BOOLEAN NOT NULL DEFAULT false;

-- reinvoice_status: '' | uninvoiced | invoiced | excluded
-- Only meaningful when is_billable = true AND task_id IS NOT NULL.
ALTER TABLE bill_lines ADD COLUMN IF NOT EXISTS reinvoice_status
    TEXT NOT NULL DEFAULT '';

-- Quick-lookup cache (cleared on invoice void; authoritative source is task_invoice_sources).
ALTER TABLE bill_lines ADD COLUMN IF NOT EXISTS invoice_id
    BIGINT REFERENCES invoices(id) ON DELETE SET NULL;

ALTER TABLE bill_lines ADD COLUMN IF NOT EXISTS invoice_line_id
    BIGINT REFERENCES invoice_lines(id) ON DELETE SET NULL;

-- Reserved for future markup support; not exposed in v1 UI.
ALTER TABLE bill_lines ADD COLUMN IF NOT EXISTS markup_percent
    NUMERIC(8,4) NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_bill_lines_task    ON bill_lines(task_id)
    WHERE task_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_bill_lines_reinvoice ON bill_lines(bill_id, reinvoice_status)
    WHERE reinvoice_status = 'uninvoiced';

-- ═══════════════════════════════════════════════════════════════════════════════
-- PART 4: Extend product_services with system-item support
-- ═══════════════════════════════════════════════════════════════════════════════

-- system_code identifies system-reserved items (e.g. 'TASK_LABOR', 'TASK_REIM').
-- NULL for all user-created items.
ALTER TABLE product_services ADD COLUMN IF NOT EXISTS system_code
    TEXT;

-- is_system = true prevents deletion and type mutation of reserved items.
ALTER TABLE product_services ADD COLUMN IF NOT EXISTS is_system
    BOOLEAN NOT NULL DEFAULT false;

-- Enforce uniqueness of system_code within a company (only when system_code is set).
-- Partial index: ignores NULL / empty rows so user items are unaffected.
CREATE UNIQUE INDEX IF NOT EXISTS uq_product_services_company_system_code
    ON product_services(company_id, system_code)
    WHERE system_code IS NOT NULL AND system_code <> '';

-- ═══════════════════════════════════════════════════════════════════════════════
-- PART 5: task_invoice_sources bridge table (audit truth for invoice generation)
-- ═══════════════════════════════════════════════════════════════════════════════
--
-- This table is the authoritative record of which task / expense / bill_line
-- ended up in which invoice line.  Records are NEVER deleted — even after an
-- invoice is voided — so the full generation history is always recoverable.
-- The quick-lookup cache columns on tasks / expenses / bill_lines are cleared
-- by the service layer on void; this table is not touched.
--
-- NOTE: the initial Batch 1 schema below enforced uniqueness across all rows.
-- Batch 1.5 (migration 043) narrows this to "active rows only" so historical
-- bridge rows may be preserved while the same source is re-billed later.

CREATE TABLE IF NOT EXISTS task_invoice_sources (
    id              BIGSERIAL       PRIMARY KEY,
    company_id      BIGINT          NOT NULL REFERENCES companies(id)      ON DELETE RESTRICT,
    invoice_id      BIGINT          NOT NULL REFERENCES invoices(id)       ON DELETE RESTRICT,
    invoice_line_id BIGINT          NOT NULL REFERENCES invoice_lines(id)  ON DELETE RESTRICT,

    -- source_type: 'task' | 'expense' | 'bill_line'
    source_type     TEXT            NOT NULL,
    -- source_id: primary key of the source row in its respective table.
    source_id       BIGINT          NOT NULL,

    -- Amount snapshot at generation time (immutable; not affected by later edits).
    amount_snapshot NUMERIC(18,2)   NOT NULL DEFAULT 0,

    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),

    -- Initial uniqueness across all rows.
    -- Batch 1.5 replaces this with an active-row-only unique index so voided
    -- history rows can coexist with a later re-billing row for the same source.
    CONSTRAINT uq_task_invoice_sources UNIQUE (source_type, source_id)
);

CREATE INDEX IF NOT EXISTS idx_task_invoice_sources_company ON task_invoice_sources(company_id);
CREATE INDEX IF NOT EXISTS idx_task_invoice_sources_invoice ON task_invoice_sources(invoice_id);
