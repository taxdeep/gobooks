-- Migration 036: Payment Gateway foundation.
-- Platform-agnostic payment processing layer: gateway accounts, accounting
-- mappings, payment requests, and transaction events.

CREATE TABLE IF NOT EXISTS payment_gateway_accounts (
    id                   BIGSERIAL   PRIMARY KEY,
    company_id           BIGINT      NOT NULL REFERENCES companies(id) ON DELETE RESTRICT,
    provider_type        TEXT        NOT NULL,
    display_name         TEXT        NOT NULL DEFAULT '',
    external_account_ref TEXT        NOT NULL DEFAULT '',
    auth_status          TEXT        NOT NULL DEFAULT 'pending',
    webhook_status       TEXT        NOT NULL DEFAULT 'not_configured',
    is_active            BOOLEAN     NOT NULL DEFAULT true,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_pg_accounts_company ON payment_gateway_accounts(company_id);

CREATE TABLE IF NOT EXISTS payment_accounting_mappings (
    id                    BIGSERIAL   PRIMARY KEY,
    company_id            BIGINT      NOT NULL REFERENCES companies(id) ON DELETE RESTRICT,
    gateway_account_id    BIGINT      NOT NULL REFERENCES payment_gateway_accounts(id) ON DELETE RESTRICT,
    clearing_account_id   BIGINT      REFERENCES accounts(id) ON DELETE RESTRICT,
    fee_expense_account_id BIGINT     REFERENCES accounts(id) ON DELETE RESTRICT,
    refund_account_id     BIGINT      REFERENCES accounts(id) ON DELETE RESTRICT,
    payout_bank_account_id BIGINT     REFERENCES accounts(id) ON DELETE RESTRICT,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_payment_acct_mapping UNIQUE (gateway_account_id)
);
CREATE INDEX IF NOT EXISTS idx_pg_mappings_company ON payment_accounting_mappings(company_id);

CREATE TABLE IF NOT EXISTS payment_requests (
    id                 BIGSERIAL      PRIMARY KEY,
    company_id         BIGINT         NOT NULL REFERENCES companies(id) ON DELETE RESTRICT,
    gateway_account_id BIGINT         NOT NULL REFERENCES payment_gateway_accounts(id) ON DELETE RESTRICT,
    invoice_id         BIGINT         REFERENCES invoices(id) ON DELETE SET NULL,
    customer_id        BIGINT         REFERENCES customers(id) ON DELETE SET NULL,
    amount             NUMERIC(18,2)  NOT NULL DEFAULT 0,
    currency_code      TEXT           NOT NULL DEFAULT '',
    status             TEXT           NOT NULL DEFAULT 'draft',
    description        TEXT           NOT NULL DEFAULT '',
    external_ref       TEXT           NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ    NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ    NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_pg_requests_company ON payment_requests(company_id);
CREATE INDEX IF NOT EXISTS idx_pg_requests_gateway ON payment_requests(gateway_account_id);

CREATE TABLE IF NOT EXISTS payment_transactions (
    id                   BIGSERIAL      PRIMARY KEY,
    company_id           BIGINT         NOT NULL REFERENCES companies(id) ON DELETE RESTRICT,
    gateway_account_id   BIGINT         NOT NULL REFERENCES payment_gateway_accounts(id) ON DELETE RESTRICT,
    payment_request_id   BIGINT         REFERENCES payment_requests(id) ON DELETE SET NULL,
    transaction_type     TEXT           NOT NULL,
    amount               NUMERIC(18,2)  NOT NULL DEFAULT 0,
    currency_code        TEXT           NOT NULL DEFAULT '',
    status               TEXT           NOT NULL DEFAULT 'completed',
    external_txn_ref     TEXT           NOT NULL DEFAULT '',
    raw_payload          JSONB          NOT NULL DEFAULT '{}',
    created_at           TIMESTAMPTZ    NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ    NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_pg_txn_company ON payment_transactions(company_id);
CREATE INDEX IF NOT EXISTS idx_pg_txn_gateway ON payment_transactions(gateway_account_id);
