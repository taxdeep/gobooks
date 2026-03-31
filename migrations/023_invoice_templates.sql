-- Migration 023: invoice templates
-- Stores company-scoped invoice template configurations.
-- Templates define default line item layouts, terms, and rendering rules.
-- All templates are company-isolated via FK constraint.

CREATE TABLE IF NOT EXISTS invoices_templates (
    id              BIGSERIAL       PRIMARY KEY,
    company_id      BIGINT          NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    name            TEXT            NOT NULL,
    description     TEXT            NOT NULL DEFAULT '',
    
    -- Template configuration (stored as JSONB for flexibility)
    -- Example: {"default_terms": "net_30", "line_item_template": [...]}
    config_json     JSONB           NOT NULL DEFAULT '{}',
    
    -- Business logic flags
    is_default      BOOLEAN         NOT NULL DEFAULT false,
    
    -- Audit and versioning
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    
    -- Audit and versioning handled above
    CONSTRAINT uq_invoices_templates_company_name UNIQUE (company_id, name)
);

-- Enforce: only one default template per company (partial unique index)
CREATE UNIQUE INDEX IF NOT EXISTS uk_invoices_templates_company_default
    ON invoices_templates(company_id)
    WHERE is_default = true;

-- Indexes for filtering and joins
CREATE INDEX IF NOT EXISTS idx_invoices_templates_company   ON invoices_templates(company_id);
CREATE INDEX IF NOT EXISTS idx_invoices_templates_default   ON invoices_templates(company_id, is_default);
