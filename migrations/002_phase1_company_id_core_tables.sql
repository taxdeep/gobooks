-- Phase 1 — Add company_id to core business tables + tenant-scoped uniqueness.
--
-- BACKFILL ASSUMPTIONS (single existing company):
--   - All existing business rows belong to the same logical tenant as companies.id = MIN(id).
--   - Rows in accounts/customers/vendors/invoices/bills/journal_entries/reconciliations are
--     updated directly to that company id.
--   - journal_lines.company_id is copied from journal_entries when the FK exists; any line
--     whose journal_entry_id is missing (orphan) is still assigned MIN(company id) so
--     NOT NULL + FK can succeed — ORPHANS SHOULD BE FIXED IN APP/DB BEFORE PRODUCTION
--     (see 000_phase1_precheck.sql); this migration can FAIL EARLY if orphans are detected.
--   - reconciliations.company_id is set to MIN(id) without joining accounts; in a healthy
--     single-company DB this matches the account’s company.
--
-- FAILURE MODES THIS FILE GUARDS:
--   - duplicate (company_id, code) on accounts after backfill
--   - NULL company_id after backfill (before NOT NULL)
--   - orphan journal_lines (optional strict: see DO block below)
--
-- INDEX / CONSTRAINT NAME ASSUMPTIONS:
--   - GORM may create UNIQUE on code as a constraint OR as a unique index (uni_accounts_code,
--     idx_accounts_code, etc.). This script drops known patterns and single-column UNIQUE
--     constraints on accounts(code) before creating uq_accounts_company_code.

BEGIN;

-- -----------------------------------------------------------------------------
-- 1) Nullable company_id columns
-- -----------------------------------------------------------------------------
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS company_id BIGINT;
ALTER TABLE customers ADD COLUMN IF NOT EXISTS company_id BIGINT;
ALTER TABLE vendors ADD COLUMN IF NOT EXISTS company_id BIGINT;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS company_id BIGINT;
ALTER TABLE bills ADD COLUMN IF NOT EXISTS company_id BIGINT;
ALTER TABLE journal_entries ADD COLUMN IF NOT EXISTS company_id BIGINT;
ALTER TABLE journal_lines ADD COLUMN IF NOT EXISTS company_id BIGINT;
ALTER TABLE reconciliations ADD COLUMN IF NOT EXISTS company_id BIGINT;

CREATE INDEX IF NOT EXISTS idx_accounts_company_id ON accounts (company_id);
CREATE INDEX IF NOT EXISTS idx_customers_company_id ON customers (company_id);
CREATE INDEX IF NOT EXISTS idx_vendors_company_id ON vendors (company_id);
CREATE INDEX IF NOT EXISTS idx_invoices_company_id ON invoices (company_id);
CREATE INDEX IF NOT EXISTS idx_bills_company_id ON bills (company_id);
CREATE INDEX IF NOT EXISTS idx_journal_entries_company_id ON journal_entries (company_id);
CREATE INDEX IF NOT EXISTS idx_journal_lines_company_id ON journal_lines (company_id);
CREATE INDEX IF NOT EXISTS idx_reconciliations_company_id ON reconciliations (company_id);

-- -----------------------------------------------------------------------------
-- 2) Backfill from first company (MIN(id) = single-tenant default)
-- -----------------------------------------------------------------------------
DO $$
DECLARE
  cid BIGINT;
BEGIN
  SELECT id INTO cid FROM companies ORDER BY id ASC LIMIT 1;
  IF cid IS NULL THEN
    -- Fresh install: companies is empty, AutoMigrate already created tables
    -- with company_id; nothing to backfill. Skip safely.
    RETURN;
  END IF;

  UPDATE accounts SET company_id = cid WHERE company_id IS NULL;
  UPDATE customers SET company_id = cid WHERE company_id IS NULL;
  UPDATE vendors SET company_id = cid WHERE company_id IS NULL;
  UPDATE invoices SET company_id = cid WHERE company_id IS NULL;
  UPDATE bills SET company_id = cid WHERE company_id IS NULL;
  UPDATE journal_entries SET company_id = cid WHERE company_id IS NULL;
  UPDATE reconciliations SET company_id = cid WHERE company_id IS NULL;
END $$;

-- journal_lines: copy from parent journal entry when possible
UPDATE journal_lines jl
SET company_id = je.company_id
FROM journal_entries je
WHERE jl.journal_entry_id = je.id
  AND jl.company_id IS NULL
  AND je.company_id IS NOT NULL;

-- Fallback: same company id as step 2 (covers orphans / edge cases)
DO $$
DECLARE
  cid BIGINT;
BEGIN
  SELECT id INTO cid FROM companies ORDER BY id ASC LIMIT 1;
  UPDATE journal_lines SET company_id = cid WHERE company_id IS NULL;
END $$;

-- -----------------------------------------------------------------------------
-- 2b) Orphan journal_lines: lines with no matching journal_entries row.
--     Fails fast so you can clean data before migrate. If you must proceed (not recommended),
--     comment out this entire DO $$ ... $$ block — you accept inconsistent FK data.
-- -----------------------------------------------------------------------------
DO $$
DECLARE
  n BIGINT;
BEGIN
  SELECT count(*) INTO n
  FROM journal_lines jl
  LEFT JOIN journal_entries je ON je.id = jl.journal_entry_id
  WHERE je.id IS NULL;

  IF n > 0 THEN
    RAISE EXCEPTION 'journal_lines orphan rows: % — fix or delete (see 000_phase1_precheck.sql query #3)', n;
  END IF;
END $$;

-- -----------------------------------------------------------------------------
-- 2c) No NULL company_id remaining (all tables above must be filled)
-- -----------------------------------------------------------------------------
DO $$
DECLARE
  n BIGINT;
BEGIN
  -- Skip validation on fresh installs (no companies = no business rows to check).
  IF NOT EXISTS (SELECT 1 FROM companies LIMIT 1) THEN
    RETURN;
  END IF;

  SELECT count(*) INTO n FROM accounts WHERE company_id IS NULL;
  IF n > 0 THEN RAISE EXCEPTION 'accounts still has % NULL company_id', n; END IF;
  SELECT count(*) INTO n FROM customers WHERE company_id IS NULL;
  IF n > 0 THEN RAISE EXCEPTION 'customers still has % NULL company_id', n; END IF;
  SELECT count(*) INTO n FROM vendors WHERE company_id IS NULL;
  IF n > 0 THEN RAISE EXCEPTION 'vendors still has % NULL company_id', n; END IF;
  SELECT count(*) INTO n FROM invoices WHERE company_id IS NULL;
  IF n > 0 THEN RAISE EXCEPTION 'invoices still has % NULL company_id', n; END IF;
  SELECT count(*) INTO n FROM bills WHERE company_id IS NULL;
  IF n > 0 THEN RAISE EXCEPTION 'bills still has % NULL company_id', n; END IF;
  SELECT count(*) INTO n FROM journal_entries WHERE company_id IS NULL;
  IF n > 0 THEN RAISE EXCEPTION 'journal_entries still has % NULL company_id', n; END IF;
  SELECT count(*) INTO n FROM journal_lines WHERE company_id IS NULL;
  IF n > 0 THEN RAISE EXCEPTION 'journal_lines still has % NULL company_id', n; END IF;
  SELECT count(*) INTO n FROM reconciliations WHERE company_id IS NULL;
  IF n > 0 THEN RAISE EXCEPTION 'reconciliations still has % NULL company_id', n; END IF;
END $$;

-- -----------------------------------------------------------------------------
-- 3) NOT NULL
-- -----------------------------------------------------------------------------
ALTER TABLE accounts ALTER COLUMN company_id SET NOT NULL;
ALTER TABLE customers ALTER COLUMN company_id SET NOT NULL;
ALTER TABLE vendors ALTER COLUMN company_id SET NOT NULL;
ALTER TABLE invoices ALTER COLUMN company_id SET NOT NULL;
ALTER TABLE bills ALTER COLUMN company_id SET NOT NULL;
ALTER TABLE journal_entries ALTER COLUMN company_id SET NOT NULL;
ALTER TABLE journal_lines ALTER COLUMN company_id SET NOT NULL;
ALTER TABLE reconciliations ALTER COLUMN company_id SET NOT NULL;

-- -----------------------------------------------------------------------------
-- 4) Foreign keys to companies (idempotent names for first run)
-- -----------------------------------------------------------------------------
ALTER TABLE accounts DROP CONSTRAINT IF EXISTS fk_accounts_company;
ALTER TABLE accounts
  ADD CONSTRAINT fk_accounts_company FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE RESTRICT;

ALTER TABLE customers DROP CONSTRAINT IF EXISTS fk_customers_company;
ALTER TABLE customers
  ADD CONSTRAINT fk_customers_company FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE RESTRICT;

ALTER TABLE vendors DROP CONSTRAINT IF EXISTS fk_vendors_company;
ALTER TABLE vendors
  ADD CONSTRAINT fk_vendors_company FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE RESTRICT;

ALTER TABLE invoices DROP CONSTRAINT IF EXISTS fk_invoices_company;
ALTER TABLE invoices
  ADD CONSTRAINT fk_invoices_company FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE RESTRICT;

ALTER TABLE bills DROP CONSTRAINT IF EXISTS fk_bills_company;
ALTER TABLE bills
  ADD CONSTRAINT fk_bills_company FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE RESTRICT;

ALTER TABLE journal_entries DROP CONSTRAINT IF EXISTS fk_journal_entries_company;
ALTER TABLE journal_entries
  ADD CONSTRAINT fk_journal_entries_company FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE RESTRICT;

ALTER TABLE journal_lines DROP CONSTRAINT IF EXISTS fk_journal_lines_company;
ALTER TABLE journal_lines
  ADD CONSTRAINT fk_journal_lines_company FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE RESTRICT;

ALTER TABLE reconciliations DROP CONSTRAINT IF EXISTS fk_reconciliations_company;
ALTER TABLE reconciliations
  ADD CONSTRAINT fk_reconciliations_company FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE RESTRICT;

-- -----------------------------------------------------------------------------
-- 5) Duplicate (company_id, code) after backfill — must be clean before new UNIQUE
-- -----------------------------------------------------------------------------
DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM accounts
    GROUP BY company_id, code
    HAVING count(*) > 1
  ) THEN
    RAISE EXCEPTION 'accounts: duplicate (company_id, code) — resolve duplicate codes before unique index';
  END IF;
END $$;

-- -----------------------------------------------------------------------------
-- 6) Drop global uniqueness on accounts.code only (single-column UNIQUE constraints)
-- -----------------------------------------------------------------------------
DO $$
DECLARE
  r RECORD;
BEGIN
  FOR r IN
    SELECT tc.constraint_name
    FROM information_schema.table_constraints tc
    WHERE tc.table_schema = current_schema()
      AND tc.table_name = 'accounts'
      AND tc.constraint_type = 'UNIQUE'
      AND (
        SELECT count(*)::int
        FROM information_schema.key_column_usage k
        WHERE k.constraint_schema = tc.constraint_schema
          AND k.constraint_name = tc.constraint_name
          AND k.table_name = 'accounts'
      ) = 1
      AND EXISTS (
        SELECT 1
        FROM information_schema.key_column_usage k2
        WHERE k2.constraint_schema = tc.constraint_schema
          AND k2.constraint_name = tc.constraint_name
          AND k2.column_name = 'code'
      )
  LOOP
    EXECUTE format('ALTER TABLE accounts DROP CONSTRAINT IF EXISTS %I', r.constraint_name);
  END LOOP;
END $$;

-- GORM sometimes creates a UNIQUE INDEX instead of a named table constraint
DROP INDEX IF EXISTS uni_accounts_code;
DROP INDEX IF EXISTS idx_accounts_code;

-- If a unique index remains (name varies), try dropping unique indexes that only reference (code)
DO $$
DECLARE
  r RECORD;
BEGIN
  FOR r IN
    SELECT indexname
    FROM pg_indexes
    WHERE schemaname = current_schema()
      AND tablename = 'accounts'
      AND indexdef ~* 'unique'
      AND indexdef ~* '\(code\)'
      AND indexdef !~* 'company_id'
  LOOP
    EXECUTE format('DROP INDEX IF EXISTS %I', r.indexname);
  END LOOP;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS uq_accounts_company_code ON accounts (company_id, code);

-- Helpful composite indexes (non-unique)
CREATE INDEX IF NOT EXISTS idx_invoices_company_invoice_number ON invoices (company_id, invoice_number);
CREATE INDEX IF NOT EXISTS idx_bills_company_bill_number ON bills (company_id, bill_number);

COMMIT;
