-- 089_pdf_templates.sql
-- Phase 3 PDF template system — block-based JSONB schema, multi-document.
--
-- Why
-- ---
-- Previous PDF rendering (invoice_render_service.go) hard-coded "classic" /
-- "modern" templates as 571 lines of Go string concatenation. Customisation
-- was limited to a few feature flags and one accent colour. Other documents
-- (Quote, SO, Bill, PO, Shipment) had no PDF output at all.
--
-- Phase 3 introduces a single block-based template system shared across all
-- six document types. The schema_json column stores the full layout
-- (page setup + theme + ordered blocks) and the renderer walks blocks,
-- looking up data values via a per-doc-type field registry.
--
-- The pre-existing `invoices_templates` table is intentionally NOT touched
-- by this migration. Migration 089 only adds the new table; the data
-- migration (visual config → schema_json) and the rename of the old table
-- to `invoice_workflow_defaults` happens in a later commit (G4) once the
-- new renderer has been validated end-to-end.
--
-- Schema
-- ------
-- company_id NULL  → system-supplied preset, available to every company.
-- company_id set   → company-owned template (cloned from a system preset
--                    or future custom build).
-- is_default       → at most one per (company_id, document_type); the
--                    partial unique index enforces it.
-- is_system        → true for the seeded preset rows; UI must prevent
--                    edit/delete on system rows (clone-to-edit pattern).
-- preview_png      → small thumbnail rendered async after save (Phase B).

CREATE TABLE IF NOT EXISTS pdf_templates (
    id            BIGSERIAL PRIMARY KEY,
    company_id    BIGINT,
    document_type VARCHAR(32) NOT NULL,
    name          VARCHAR(128) NOT NULL,
    description   TEXT NOT NULL DEFAULT '',
    schema_json   JSONB NOT NULL,
    is_default    BOOLEAN NOT NULL DEFAULT FALSE,
    is_active     BOOLEAN NOT NULL DEFAULT TRUE,
    is_system     BOOLEAN NOT NULL DEFAULT FALSE,
    preview_png   BYTEA,
    created_at    TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_pdf_templates_company_doc
    ON pdf_templates(company_id, document_type);

-- Single-default-per-company-per-doctype invariant. NULL company_id rows are
-- system presets and may include their own "is_default" hint that the
-- renderer falls back to when a company has no own default.
CREATE UNIQUE INDEX IF NOT EXISTS uk_pdf_templates_company_doc_default
    ON pdf_templates(company_id, document_type)
    WHERE is_default = true AND company_id IS NOT NULL;
