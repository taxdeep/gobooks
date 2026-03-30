-- COA Template system: default Chart of Accounts templates and per-company flag.
-- Idempotent: uses IF NOT EXISTS / ADD COLUMN IF NOT EXISTS throughout.

-- Template registry ---------------------------------------------------------
CREATE TABLE IF NOT EXISTS coa_templates (
    id         BIGSERIAL   PRIMARY KEY,
    name       TEXT        NOT NULL,
    is_default BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_coa_templates_is_default
    ON coa_templates (is_default);

-- Template account rows ------------------------------------------------------
CREATE TABLE IF NOT EXISTS coa_template_accounts (
    id                  BIGSERIAL   PRIMARY KEY,
    template_id         BIGINT      NOT NULL REFERENCES coa_templates(id) ON DELETE CASCADE,
    account_code        TEXT        NOT NULL,
    name                TEXT        NOT NULL,
    root_account_type   TEXT        NOT NULL,
    detail_account_type TEXT        NOT NULL,
    normal_balance      TEXT        NOT NULL,
    sort_order          INTEGER     NOT NULL DEFAULT 0,
    metadata            TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_coa_template_accounts_template_id
    ON coa_template_accounts (template_id);

-- is_system_default flag on existing accounts table -------------------------
ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS is_system_default BOOLEAN NOT NULL DEFAULT FALSE;
