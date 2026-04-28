-- Phase 1 — Identity & membership (no app code changes required to run this file alone).
-- Prerequisites: table `companies` must already exist (GORM AutoMigrate).
-- Assumptions: PostgreSQL; run against the same schema Balanciz uses (typically `public`).
--
-- Idempotency: CREATE TABLE IF NOT EXISTS / CREATE TYPE IF NOT EXISTS / CREATE INDEX IF NOT EXISTS
-- are safe to re-run. Extension pgcrypto is required by 003 (numbering_settings.id default) and
-- any UUID defaults using gen_random_uuid().

BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- -----------------------------------------------------------------------------
-- users (UUID PK; companies.id stays BIGINT as today)
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS users (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  email TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT '',
  is_active BOOLEAN NOT NULL DEFAULT TRUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_users_email_lower ON users (lower(email));

-- -----------------------------------------------------------------------------
-- company_role enum — role lives ONLY on membership
-- -----------------------------------------------------------------------------
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'company_role') THEN
    CREATE TYPE company_role AS ENUM (
      'owner',
      'admin',
      'bookkeeper',
      'accountant',
      'ap',
      'viewer'
    );
  END IF;
END$$;

-- -----------------------------------------------------------------------------
-- company_memberships
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS company_memberships (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  company_id BIGINT NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
  role company_role NOT NULL,
  is_active BOOLEAN NOT NULL DEFAULT TRUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id, company_id)
);

CREATE INDEX IF NOT EXISTS idx_company_memberships_user_id ON company_memberships (user_id);
CREATE INDEX IF NOT EXISTS idx_company_memberships_company_id ON company_memberships (company_id);

-- -----------------------------------------------------------------------------
-- sessions (opaque token stored as hash; cookie holds raw token only in app layer)
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS sessions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  token_hash TEXT NOT NULL UNIQUE,
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  active_company_id BIGINT NULL REFERENCES companies(id) ON DELETE SET NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  revoked_at TIMESTAMPTZ NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions (user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions (expires_at);

-- -----------------------------------------------------------------------------
-- company_invitations (minimal shell for later phases)
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS company_invitations (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  company_id BIGINT NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
  email TEXT NOT NULL,
  role company_role NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  invited_by_user_id UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  status TEXT NOT NULL DEFAULT 'pending',
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_company_invitations_company_id ON company_invitations (company_id);
CREATE INDEX IF NOT EXISTS idx_company_invitations_email_lower ON company_invitations (lower(email));

COMMIT;
