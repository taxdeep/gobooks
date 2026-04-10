// 遵循project_guide.md
package db

import (
	"fmt"
	"strings"

	"gobooks/internal/models"

	"gorm.io/gorm"
)

// Migrate runs basic GORM auto-migrations.
// This is intentionally simple for the initial project setup.
// Company.City: added with not null + default ” so existing rows survive (see migrations/002_add_company_city.sql).
func Migrate(db *gorm.DB) error {
	if err := renameJournalEntriesDescriptionToJournalNo(db); err != nil {
		return err
	}
	// Required before tables using models.CompanyRole (gorm:"type:company_role").
	// Fresh DBs (e.g. after DROP SCHEMA public CASCADE) have no enum until this runs.
	if err := ensureCompanyRoleEnum(db); err != nil {
		return err
	}
	// Must run before AutoMigrate on &models.Account{}: existing rows with legacy `type` need
	// nullable columns + backfill before GORM adds NOT NULL root/detail columns.
	if err := migrateAccountsRootDetail(db); err != nil {
		return err
	}
	// Adds balance_due to bills table (backfills from amount). Safe on fresh installs.
	if err := migrateBillsAddBalanceDue(db); err != nil {
		return err
	}
	// Historical databases may contain stale optional foreign keys that predate
	// current constraints. Null them before AutoMigrate adds FK constraints.
	if err := clearOptionalForeignKeyOrphans(db); err != nil {
		return err
	}
	// Migrate customers.address (free-form text) to addr_street1 before AutoMigrate
	// adds the new structured columns. Safe to run on fresh databases (no-op when
	// the old column does not exist).
	if err := migrateCustomerAddressToStructured(db); err != nil {
		return err
	}
	// Introduce payment_terms master table, seed defaults, migrate Invoice/Bill
	// snapshot columns, and backfill Customer/Vendor default_payment_term_code.
	// Must run before AutoMigrate so that the old `terms` column on invoices/bills
	// is renamed before GORM tries to manage `term_code`.
	if err := migratePaymentTerms(db); err != nil {
		return err
	}
	// Phase 1 multi-currency: create currencies / company_currencies / exchange_rates
	// tables, seed the currency dictionary, and add new columns to companies and accounts
	// with safe defaults for all existing rows.
	if err := migrateCurrencyPhase1(db); err != nil {
		return err
	}
	// Phase 3 multi-currency: add currency_code, exchange_rate, and base-currency amount
	// columns to invoices and bills, backfilling existing rows with safe defaults.
	if err := migrateCurrencyPhase3(db); err != nil {
		return err
	}
	// Phase 4 multi-currency: add balance_due_base to invoices and bills;
	// create settlement_allocations table.
	if err := migrateCurrencyPhase4(db); err != nil {
		return err
	}
	// Batch 6: hosted invoice link infrastructure (share tokens, access audit).
	// Must run before AutoMigrate so the partial unique index is in place.
	if err := migrateHostedInvoicePhase1(db); err != nil {
		return err
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.User{},
		&models.CompanyMembership{},
		&models.Session{},
		&models.AIConnectionSettings{},
		&models.NumberingSetting{},
		&models.Account{},
		&models.Customer{},
		&models.Vendor{},
		&models.PaymentTerm{},
		&models.Invoice{},
		&models.Bill{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.Reconciliation{},
		&models.AuditLog{},
		&models.CompanyInvitation{},
		// Phase 2: tax infrastructure
		&models.TaxAgency{},
		&models.TaxComponent{},
		&models.TaxCode{},
		// Phase 2: product/service catalogue
		&models.ProductService{},
		// Phase 3: invoice + bill line items
		&models.InvoiceLine{},
		&models.BillLine{},
		// SysAdmin: 独立系统管理员账户与会话，与业务用户完全隔离
		&models.SysadminUser{},
		&models.SysadminSession{},
		// Phase C: 运行时结构化日志（错误/警告持久化，与业务审计日志分离）
		&models.SystemLog{},
		// Phase E: 系统级配置键值存储（维护模式持久化等）
		&models.SystemSetting{},
		// Phase E: 用户粒度权限扩展点（schema-ready，尚未接入权限检查）
		&models.UserCompanyPermission{},
		// Notifications & Security: per-company and system-level settings + event log
		&models.CompanyNotificationSettings{},
		&models.SystemNotificationSettings{},
		&models.CompanySecuritySettings{},
		&models.SystemSecuritySettings{},
		&models.SecurityEvent{},
		// Posting Engine Phase 2: accounting fact layer (projection of posted journal lines)
		&models.LedgerEntry{},
		// Phase 2: user profile verification challenges (email / password change)
		&models.UserVerificationChallenge{},
		// COA Template system: default Chart of Accounts templates
		&models.COATemplate{},
		&models.COATemplateAccount{},
		// Reconciliation match engine: suggestions, suggestion lines, and memory layer
		&models.ReconciliationMatchSuggestion{},
		&models.ReconciliationMatchSuggestionLine{},
		&models.ReconciliationMemory{},
		// Phase 1 multi-currency: global currency dictionary + per-company currencies + exchange rates
		&models.Currency{},
		&models.CompanyCurrency{},
		&models.ExchangeRate{},
		// Phase 4 multi-currency: settlement allocation records (per-invoice / per-bill)
		&models.SettlementAllocation{},
		// Business-layer customer receipt headers.
		&models.PaymentReceipt{},
		// Phase 5 multi-currency: period-end unrealized FX revaluation runs + lines
		&models.RevaluationRun{},
		&models.RevaluationLine{},
		// Items extensibility: BOM components, inventory tracking, channel integration
		&models.ItemComponent{},
		&models.InventoryMovement{},
		&models.InventoryBalance{},
		&models.SalesChannelAccount{},
		&models.ItemChannelMapping{},
		&models.ChannelOrder{},
		&models.ChannelOrderLine{},
		&models.ChannelSettlement{},
		&models.ChannelSettlementLine{},
		&models.ChannelAccountingMapping{},
		// Bank reconciliation draft (in-progress session persistence)
		&models.ReconciliationDraft{},
		// Payment Gateway foundation
		&models.PaymentGatewayAccount{},
		&models.PaymentAccountingMapping{},
		&models.PaymentRequest{},
		&models.PaymentTransaction{},
		// Batch 17: multi-invoice payment allocation
		&models.PaymentAllocation{},
		// Task + Billable Expense module (Batch 1: data foundation)
		&models.Task{},
		&models.Expense{},
		&models.TaskInvoiceSource{},
		// Invoice Template + Sending: template definitions and email audit log
		&models.InvoiceTemplate{},
		&models.InvoiceEmailLog{},
		// Batch 6: Hosted Invoice share links (token-based, no-auth public access)
		&models.InvoiceHostedLink{},
		// Batch 7: Hosted payment attempt trace (immutable; no accounting entries)
		&models.HostedPaymentAttempt{},
		// Batch 10: Webhook event deduplication and traceability
		&models.WebhookEvent{},
		// Batch 11: Payment settlement bridge — gateway-side verified payment → accounting-side truth
		&models.GatewaySettlement{},
		// Batch 14: Gateway payout bridge — clearing → bank + fee expense
		&models.GatewayPayout{},
		&models.GatewayPayoutSettlement{},
		// Batch 15: Dispute state tracking
		&models.GatewayDispute{},
		// Batch 16: Customer credit balance (overpayment + credit apply)
		&models.CustomerCredit{},
		&models.CustomerCreditApplication{},
		// Batch 18: Payout ↔ bank entry reconciliation
		&models.BankEntry{},
		&models.PayoutReconciliation{},
		// Batch 19: Payout component / composition truth (reconciliation-side)
		&models.GatewayPayoutComponent{},
		// Batch 20: Reconciliation exception truth
		&models.ReconciliationException{},
		// Batch 21: Resolution hook attempt trace
		&models.ReconciliationResolutionAttempt{},
		// Batch 22: Multi-allocated payment reverse allocation truth
		&models.PaymentReverseAllocation{},
		// Batch 23: Payment-side reverse exception truth
		&models.PaymentReverseException{},
		// Batch 26: Payment reverse hook execution trace
		&models.PaymentReverseResolutionAttempt{},
	); err != nil {
		return err
	}
	// Batch 10: add webhook_secret column to existing payment_gateway_accounts rows.
	// Safe on fresh installs (AutoMigrate will have added the column already).
	if err := migratePaymentGatewayWebhookSecret(db); err != nil {
		return err
	}
	// Batch 15: add chargeback_account_id to payment_accounting_mappings and
	// original_transaction_id to payment_transactions for existing live databases.
	// AutoMigrate handles fresh installs; these guards protect live DBs.
	if err := migrateBatch15Columns(db); err != nil {
		return err
	}
	if err := ensureCompanyAccountCodeDefaults(db); err != nil {
		return err
	}
	if err := ensureDocumentNumberIndexes(db); err != nil {
		return err
	}
	// Batch 28 task service item: link tasks to Products & Services catalogue.
	// AutoMigrate above handles fresh installs; this guard adds the column on live DBs.
	return migrateTaskServiceItem(db)
}

// migrateBatch15Columns adds Batch 15 columns to existing live databases.
// AutoMigrate handles fresh installs; this guard protects pre-Batch-15 DBs.
func migrateBatch15Columns(db *gorm.DB) error {
	type safeCol struct{ table, col, def string }
	cols := []safeCol{
		{"payment_accounting_mappings", "chargeback_account_id", "NULL"},
		{"payment_transactions", "original_transaction_id", "NULL"},
	}
	for _, c := range cols {
		sql := fmt.Sprintf(
			`ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s BIGINT DEFAULT %s`,
			c.table, c.col, c.def,
		)
		if err := db.Exec(sql).Error; err != nil {
			if !strings.Contains(err.Error(), "duplicate column") &&
				!strings.Contains(err.Error(), "already exists") {
				return fmt.Errorf("migrateBatch15Columns %s.%s: %w", c.table, c.col, err)
			}
		}
	}
	return nil
}

// migratePaymentGatewayWebhookSecret adds the webhook_secret column to
// payment_gateway_accounts for existing databases that predate Batch 10.
// AutoMigrate will have already handled fresh installs; this is a guard for live DBs.
func migratePaymentGatewayWebhookSecret(db *gorm.DB) error {
	sql := `ALTER TABLE payment_gateway_accounts ADD COLUMN IF NOT EXISTS webhook_secret TEXT NOT NULL DEFAULT ''`
	if err := db.Exec(sql).Error; err != nil {
		// SQLite used in tests does not support ADD COLUMN IF NOT EXISTS.
		// Ignore the error if the column already exists (SQLite: "duplicate column name").
		if !strings.Contains(err.Error(), "duplicate column") &&
			!strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("migratePaymentGatewayWebhookSecret: %w", err)
		}
	}
	return nil
}

// clearOptionalForeignKeyOrphans removes stale nullable references left behind by
// older schemas or manual deletes. Each target column is nullable by model design,
// so normalising bad references back to NULL is safe and preserves business data.
func clearOptionalForeignKeyOrphans(db *gorm.DB) error {
	statements := []string{
		`
UPDATE product_services ps
SET default_tax_code_id = NULL
WHERE default_tax_code_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM tax_codes tc WHERE tc.id = ps.default_tax_code_id
  );
`,
		`
UPDATE invoice_lines il
SET product_service_id = NULL
WHERE product_service_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM product_services ps WHERE ps.id = il.product_service_id
  );
`,
		`
UPDATE invoice_lines il
SET tax_code_id = NULL
WHERE tax_code_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM tax_codes tc WHERE tc.id = il.tax_code_id
  );
`,
		`
UPDATE bill_lines bl
SET product_service_id = NULL
WHERE product_service_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM product_services ps WHERE ps.id = bl.product_service_id
  );
`,
		`
UPDATE bill_lines bl
SET tax_code_id = NULL
WHERE tax_code_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM tax_codes tc WHERE tc.id = bl.tax_code_id
  );
`,
		`
UPDATE bill_lines bl
SET expense_account_id = NULL
WHERE expense_account_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM accounts a WHERE a.id = bl.expense_account_id
  );
`,
		`
UPDATE tax_codes tc
SET purchase_recoverable_account_id = NULL
WHERE purchase_recoverable_account_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM accounts a WHERE a.id = tc.purchase_recoverable_account_id
  );
`,
		`
UPDATE invoices i
SET journal_entry_id = NULL
WHERE journal_entry_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM journal_entries je WHERE je.id = i.journal_entry_id
  );
`,
		`
UPDATE bills b
SET journal_entry_id = NULL
WHERE journal_entry_id IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM journal_entries je WHERE je.id = b.journal_entry_id
  );
`,
	}

	for _, stmt := range statements {
		if err := db.Exec(stmt).Error; err != nil {
			// Fresh databases don't have these tables yet; skip safely.
			if strings.Contains(err.Error(), "42P01") {
				continue
			}
			return err
		}
	}
	return nil
}

// ensureCompanyAccountCodeDefaults backfills account_code_length and locks length for companies that already have accounts.
func ensureCompanyAccountCodeDefaults(db *gorm.DB) error {
	return db.Exec(`
UPDATE companies SET account_code_length = 4
WHERE account_code_length IS NULL OR account_code_length < 4 OR account_code_length > 12;

UPDATE companies c SET account_code_length_locked = true
WHERE EXISTS (SELECT 1 FROM accounts a WHERE a.company_id = c.id);
`).Error
}

// renameJournalEntriesDescriptionToJournalNo upgrades older databases that used
// journal_entries.description; the application model now maps to journal_no.
func renameJournalEntriesDescriptionToJournalNo(db *gorm.DB) error {
	return db.Exec(`
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = CURRENT_SCHEMA()
      AND table_name = 'journal_entries'
      AND column_name = 'description'
  ) THEN
    ALTER TABLE journal_entries RENAME COLUMN description TO journal_no;
  END IF;
END $$;
`).Error
}

func ensureCompanyRoleEnum(db *gorm.DB) error {
	return db.Exec(`
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
`).Error
}

func ensureDocumentNumberIndexes(db *gorm.DB) error {
	if err := db.Exec(`
CREATE UNIQUE INDEX IF NOT EXISTS uq_invoices_company_invoice_number_ci
ON invoices (company_id, lower(invoice_number));
`).Error; err != nil {
		return err
	}

	return db.Exec(`
CREATE UNIQUE INDEX IF NOT EXISTS uq_bills_company_vendor_bill_number_ci
ON bills (company_id, vendor_id, lower(bill_number));
`).Error
}

// migrateBillsAddBalanceDue adds the balance_due column to the bills table if it
// does not already exist, and backfills it with the existing amount value so that
// previously-posted bills remain accurate. On fresh installs (table not yet
// created) the function is a no-op; AutoMigrate will create the column via GORM.
func migrateBillsAddBalanceDue(db *gorm.DB) error {
	err := db.Exec(`
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.tables
    WHERE table_schema = CURRENT_SCHEMA()
      AND table_name = 'bills'
  ) AND NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = CURRENT_SCHEMA()
      AND table_name   = 'bills'
      AND column_name  = 'balance_due'
  ) THEN
    ALTER TABLE bills ADD COLUMN balance_due NUMERIC(18,2) NOT NULL DEFAULT 0;
    -- Backfill: existing posted bills with no payments owe their full amount.
    -- Paid bills already owe nothing; set balance_due = 0 (default).
    UPDATE bills SET balance_due = amount WHERE status IN ('posted', 'partially_paid');
  END IF;
END $$;
`).Error
	if err != nil && strings.Contains(err.Error(), "42P01") {
		return nil
	}
	return err
}

// migratePaymentTerms is the master migration for the payment_terms feature.
// It runs 7 idempotent steps:
//  1. Create payment_terms table (IF NOT EXISTS)
//  2. Seed the 5 built-in terms per company (ON CONFLICT DO NOTHING)
//  3. Set N30 as default for any company without a default term
//  4. Migrate invoices: rename terms→term_code column, remap legacy codes,
//     add snapshot columns, backfill from payment_terms, recompute due_date
//  5. Migrate bills: same as step 4
//  6. Migrate customers: add default_payment_term_code, best-effort backfill
//  7. Migrate vendors: same as step 6
//
// All steps are fully idempotent (safe to run multiple times).
func migratePaymentTerms(db *gorm.DB) error {
	steps := []string{
		// ── Step 1: create payment_terms table ────────────────────────────
		`
CREATE TABLE IF NOT EXISTS payment_terms (
  id            BIGSERIAL PRIMARY KEY,
  company_id    BIGINT NOT NULL,
  code          TEXT   NOT NULL,
  description   TEXT   NOT NULL DEFAULT '',
  discount_days INTEGER NOT NULL DEFAULT 0,
  discount_pct  NUMERIC(5,2) NOT NULL DEFAULT 0.00,
  net_days      INTEGER NOT NULL DEFAULT 0,
  is_default    BOOLEAN NOT NULL DEFAULT FALSE,
  is_active     BOOLEAN NOT NULL DEFAULT TRUE,
  sort_order    INTEGER NOT NULL DEFAULT 0,
  created_at    TIMESTAMP NOT NULL DEFAULT NOW(),
  updated_at    TIMESTAMP NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_payment_terms_company_code_ci
  ON payment_terms (company_id, lower(code));
CREATE UNIQUE INDEX IF NOT EXISTS uq_payment_terms_company_default
  ON payment_terms (company_id) WHERE is_default = TRUE;
`,
		// ── Step 2: seed 5 default terms per company ──────────────────────
		`
INSERT INTO payment_terms (company_id, code, description, discount_days, discount_pct, net_days, is_default, is_active, sort_order, created_at, updated_at)
SELECT c.id, 'DOC', 'Delivery on Cash', 0, 0.00, 0, FALSE, TRUE, 1, NOW(), NOW()
FROM companies c
ON CONFLICT DO NOTHING;

INSERT INTO payment_terms (company_id, code, description, discount_days, discount_pct, net_days, is_default, is_active, sort_order, created_at, updated_at)
SELECT c.id, 'N15', 'Net 15', 0, 0.00, 15, FALSE, TRUE, 2, NOW(), NOW()
FROM companies c
ON CONFLICT DO NOTHING;

INSERT INTO payment_terms (company_id, code, description, discount_days, discount_pct, net_days, is_default, is_active, sort_order, created_at, updated_at)
SELECT c.id, 'N30', 'Net 30', 0, 0.00, 30, FALSE, TRUE, 3, NOW(), NOW()
FROM companies c
ON CONFLICT DO NOTHING;

INSERT INTO payment_terms (company_id, code, description, discount_days, discount_pct, net_days, is_default, is_active, sort_order, created_at, updated_at)
SELECT c.id, 'N60', 'Net 60', 0, 0.00, 60, FALSE, TRUE, 4, NOW(), NOW()
FROM companies c
ON CONFLICT DO NOTHING;

INSERT INTO payment_terms (company_id, code, description, discount_days, discount_pct, net_days, is_default, is_active, sort_order, created_at, updated_at)
SELECT c.id, 'N102%', '2% discount if paid within 10 days; otherwise net 30', 10, 2.00, 30, FALSE, TRUE, 5, NOW(), NOW()
FROM companies c
ON CONFLICT DO NOTHING;
`,
		// ── Step 3: set N30 as default for companies without a default ────
		`
UPDATE payment_terms
SET    is_default = TRUE
WHERE  code = 'N30'
  AND  company_id NOT IN (
         SELECT DISTINCT company_id FROM payment_terms WHERE is_default = TRUE
       );
`,
		// ── Step 4: invoices — rename terms→term_code, remap, add snapshots ─
		`
DO $$
BEGIN
  -- 4a. Rename 'terms' column to 'term_code' if it still exists under the old name.
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'invoices' AND column_name = 'terms'
  ) AND NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'invoices' AND column_name = 'term_code'
  ) THEN
    ALTER TABLE invoices RENAME COLUMN terms TO term_code;
  END IF;

  -- 4b. Add term_code if neither old nor new column existed (fresh install).
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'invoices' AND column_name = 'term_code'
  ) THEN
    ALTER TABLE invoices ADD COLUMN term_code TEXT NOT NULL DEFAULT '';
  END IF;

  -- 4c. Remap legacy enum values to new codes.
  UPDATE invoices SET term_code = CASE term_code
    WHEN 'due_on_receipt' THEN 'DOC'
    WHEN 'net_15'         THEN 'N15'
    WHEN 'net_30'         THEN 'N30'
    WHEN 'net_60'         THEN 'N60'
    WHEN 'custom'         THEN 'N30'
    WHEN ''               THEN 'N30'
    ELSE term_code
  END
  WHERE term_code IN ('due_on_receipt','net_15','net_30','net_60','custom','');

  -- 4d. Add snapshot columns.
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'invoices' AND column_name = 'term_description_snapshot') THEN
    ALTER TABLE invoices ADD COLUMN term_description_snapshot TEXT NOT NULL DEFAULT '';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'invoices' AND column_name = 'discount_days_snapshot') THEN
    ALTER TABLE invoices ADD COLUMN discount_days_snapshot INTEGER NOT NULL DEFAULT 0;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'invoices' AND column_name = 'discount_pct_snapshot') THEN
    ALTER TABLE invoices ADD COLUMN discount_pct_snapshot NUMERIC(5,2) NOT NULL DEFAULT 0.00;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'invoices' AND column_name = 'net_days_snapshot') THEN
    ALTER TABLE invoices ADD COLUMN net_days_snapshot INTEGER NOT NULL DEFAULT 0;
  END IF;

  -- 4e. Backfill snapshots from payment_terms master.
  UPDATE invoices i
  SET    term_description_snapshot = pt.description,
         discount_days_snapshot    = pt.discount_days,
         discount_pct_snapshot     = pt.discount_pct,
         net_days_snapshot         = pt.net_days
  FROM   payment_terms pt
  WHERE  pt.company_id = i.company_id
    AND  lower(pt.code) = lower(i.term_code)
    AND  i.term_description_snapshot = '';

  -- 4f. Recompute due_date from snapshot where net_days > 0.
  UPDATE invoices
  SET    due_date = invoice_date + (net_days_snapshot * INTERVAL '1 day')
  WHERE  net_days_snapshot > 0 AND due_date IS NULL;

END $$;
`,
		// ── Step 5: bills — same as step 4 ────────────────────────────────
		`
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'bills' AND column_name = 'terms'
  ) AND NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'bills' AND column_name = 'term_code'
  ) THEN
    ALTER TABLE bills RENAME COLUMN terms TO term_code;
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'bills' AND column_name = 'term_code'
  ) THEN
    ALTER TABLE bills ADD COLUMN term_code TEXT NOT NULL DEFAULT '';
  END IF;

  UPDATE bills SET term_code = CASE term_code
    WHEN 'due_on_receipt' THEN 'DOC'
    WHEN 'net_15'         THEN 'N15'
    WHEN 'net_30'         THEN 'N30'
    WHEN 'net_60'         THEN 'N60'
    WHEN 'custom'         THEN 'N30'
    WHEN ''               THEN 'N30'
    ELSE term_code
  END
  WHERE term_code IN ('due_on_receipt','net_15','net_30','net_60','custom','');

  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'bills' AND column_name = 'term_description_snapshot') THEN
    ALTER TABLE bills ADD COLUMN term_description_snapshot TEXT NOT NULL DEFAULT '';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'bills' AND column_name = 'discount_days_snapshot') THEN
    ALTER TABLE bills ADD COLUMN discount_days_snapshot INTEGER NOT NULL DEFAULT 0;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'bills' AND column_name = 'discount_pct_snapshot') THEN
    ALTER TABLE bills ADD COLUMN discount_pct_snapshot NUMERIC(5,2) NOT NULL DEFAULT 0.00;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'bills' AND column_name = 'net_days_snapshot') THEN
    ALTER TABLE bills ADD COLUMN net_days_snapshot INTEGER NOT NULL DEFAULT 0;
  END IF;

  UPDATE bills b
  SET    term_description_snapshot = pt.description,
         discount_days_snapshot    = pt.discount_days,
         discount_pct_snapshot     = pt.discount_pct,
         net_days_snapshot         = pt.net_days
  FROM   payment_terms pt
  WHERE  pt.company_id = b.company_id
    AND  lower(pt.code) = lower(b.term_code)
    AND  b.term_description_snapshot = '';

  UPDATE bills
  SET    due_date = bill_date + (net_days_snapshot * INTERVAL '1 day')
  WHERE  net_days_snapshot > 0 AND due_date IS NULL;

END $$;
`,
		// ── Step 6: customers — add default_payment_term_code ─────────────
		`
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'customers'
      AND column_name = 'default_payment_term_code'
  ) THEN
    ALTER TABLE customers ADD COLUMN default_payment_term_code TEXT NOT NULL DEFAULT '';
    -- Best-effort mapping from old free-text payment_term.
    UPDATE customers SET default_payment_term_code =
      CASE LOWER(TRIM(COALESCE(payment_term, '')))
        WHEN 'due on receipt'     THEN 'DOC'
        WHEN 'delivery on cash'   THEN 'DOC'
        WHEN 'doc'                THEN 'DOC'
        WHEN 'net 15'             THEN 'N15'
        WHEN 'n15'                THEN 'N15'
        WHEN 'net 30'             THEN 'N30'
        WHEN 'n30'                THEN 'N30'
        WHEN 'net 60'             THEN 'N60'
        WHEN 'n60'                THEN 'N60'
        ELSE ''
      END;
  END IF;
END $$;
`,
		// ── Step 7: vendors — add default_payment_term_code ───────────────
		`
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'vendors'
      AND column_name = 'default_payment_term_code'
  ) THEN
    ALTER TABLE vendors ADD COLUMN default_payment_term_code TEXT NOT NULL DEFAULT '';
    UPDATE vendors SET default_payment_term_code =
      CASE LOWER(TRIM(COALESCE(payment_term, '')))
        WHEN 'due on receipt'     THEN 'DOC'
        WHEN 'delivery on cash'   THEN 'DOC'
        WHEN 'doc'                THEN 'DOC'
        WHEN 'net 15'             THEN 'N15'
        WHEN 'n15'                THEN 'N15'
        WHEN 'net 30'             THEN 'N30'
        WHEN 'n30'                THEN 'N30'
        WHEN 'net 60'             THEN 'N60'
        WHEN 'n60'                THEN 'N60'
        ELSE ''
      END;
  END IF;
END $$;
`,
	}

	for i, stmt := range steps {
		if err := db.Exec(stmt).Error; err != nil {
			// 42P01 = table does not exist; safe to skip on fresh installs for
			// steps that reference tables not yet created by AutoMigrate.
			if strings.Contains(err.Error(), "42P01") {
				continue
			}
			return fmt.Errorf("migratePaymentTerms step %d: %w", i+1, err)
		}
	}
	return nil
}

// migrateCurrencyPhase1 is the master migration for the multi-currency Phase 1 feature.
// It runs 5 idempotent steps:
//  1. Create the global currencies dictionary table and seed 7 ISO 4217 currencies.
//  2. Create the company_currencies join table.
//  3. Create the exchange_rates table with partial unique indexes.
//  4. Add base_currency_code and multi_currency_enabled to the companies table.
//  5. Add currency_mode, currency_code, is_system_generated, system_key to accounts.
//
// All steps are fully idempotent (safe to run multiple times).
// On fresh installs the ALTER TABLE steps are no-ops (table-existence guard);
// AutoMigrate creates the columns from GORM struct definitions.
func migrateCurrencyPhase1(db *gorm.DB) error {
	steps := []string{
		// ── Step 1: global currencies dictionary + seed ────────────────────────
		`
CREATE TABLE IF NOT EXISTS currencies (
  code           VARCHAR(3)  PRIMARY KEY,
  name           TEXT        NOT NULL,
  symbol         TEXT        NOT NULL DEFAULT '',
  decimal_places INTEGER     NOT NULL DEFAULT 2,
  is_active      BOOLEAN     NOT NULL DEFAULT TRUE
);

INSERT INTO currencies (code, name, symbol, decimal_places, is_active) VALUES
  ('CAD', 'Canadian Dollar',   '$', 2, TRUE),
  ('USD', 'US Dollar',         '$', 2, TRUE),
  ('EUR', 'Euro',              '€', 2, TRUE),
  ('GBP', 'British Pound',     '£', 2, TRUE),
  ('CNY', 'Chinese Yuan',      '¥', 2, TRUE),
  ('AUD', 'Australian Dollar', '$', 2, TRUE),
  ('JPY', 'Japanese Yen',      '¥', 0, TRUE)
ON CONFLICT (code) DO NOTHING;
`,
		// ── Step 2: company_currencies join table ──────────────────────────────
		`
CREATE TABLE IF NOT EXISTS company_currencies (
  id            BIGSERIAL  PRIMARY KEY,
  company_id    BIGINT     NOT NULL,
  currency_code VARCHAR(3) NOT NULL,
  is_active     BOOLEAN    NOT NULL DEFAULT TRUE,
  CONSTRAINT uq_company_currency UNIQUE (company_id, currency_code)
);
`,
		// ── Step 3: exchange_rates table + partial unique indexes ──────────────
		`
CREATE TABLE IF NOT EXISTS exchange_rates (
  id                   BIGSERIAL     PRIMARY KEY,
  company_id           BIGINT,
  base_currency_code   VARCHAR(3)    NOT NULL,
  target_currency_code VARCHAR(3)    NOT NULL,
  rate                 NUMERIC(20,8) NOT NULL,
  rate_type            TEXT          NOT NULL DEFAULT 'spot',
  source               TEXT          NOT NULL DEFAULT '',
  effective_date       DATE          NOT NULL,
  created_at           TIMESTAMP     NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_exchange_rates_effective_date
  ON exchange_rates (effective_date);
CREATE UNIQUE INDEX IF NOT EXISTS uq_exchange_rates_system
  ON exchange_rates (base_currency_code, target_currency_code, rate_type, effective_date)
  WHERE company_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_exchange_rates_company
  ON exchange_rates (company_id, base_currency_code, target_currency_code, rate_type, effective_date)
  WHERE company_id IS NOT NULL;
`,
		// ── Step 4: add multi-currency columns to companies ────────────────────
		`
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.tables
    WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'companies'
  ) THEN
    IF NOT EXISTS (
      SELECT 1 FROM information_schema.columns
      WHERE table_schema = CURRENT_SCHEMA()
        AND table_name  = 'companies'
        AND column_name = 'base_currency_code'
    ) THEN
      ALTER TABLE companies ADD COLUMN base_currency_code TEXT NOT NULL DEFAULT 'CAD';
    END IF;
    IF NOT EXISTS (
      SELECT 1 FROM information_schema.columns
      WHERE table_schema = CURRENT_SCHEMA()
        AND table_name  = 'companies'
        AND column_name = 'multi_currency_enabled'
    ) THEN
      ALTER TABLE companies ADD COLUMN multi_currency_enabled BOOLEAN NOT NULL DEFAULT FALSE;
    END IF;
  END IF;
END $$;
`,
		// ── Step 5: add multi-currency columns to accounts ─────────────────────
		`
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.tables
    WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'accounts'
  ) THEN
    IF NOT EXISTS (
      SELECT 1 FROM information_schema.columns
      WHERE table_schema = CURRENT_SCHEMA()
        AND table_name  = 'accounts'
        AND column_name = 'currency_mode'
    ) THEN
      ALTER TABLE accounts ADD COLUMN currency_mode TEXT NOT NULL DEFAULT 'base_only';
    END IF;
    IF NOT EXISTS (
      SELECT 1 FROM information_schema.columns
      WHERE table_schema = CURRENT_SCHEMA()
        AND table_name  = 'accounts'
        AND column_name = 'currency_code'
    ) THEN
      ALTER TABLE accounts ADD COLUMN currency_code VARCHAR(3);
    END IF;
    IF NOT EXISTS (
      SELECT 1 FROM information_schema.columns
      WHERE table_schema = CURRENT_SCHEMA()
        AND table_name  = 'accounts'
        AND column_name = 'is_system_generated'
    ) THEN
      ALTER TABLE accounts ADD COLUMN is_system_generated BOOLEAN NOT NULL DEFAULT FALSE;
    END IF;
    IF NOT EXISTS (
      SELECT 1 FROM information_schema.columns
      WHERE table_schema = CURRENT_SCHEMA()
        AND table_name  = 'accounts'
        AND column_name = 'system_key'
    ) THEN
      ALTER TABLE accounts ADD COLUMN system_key TEXT;
    END IF;
  END IF;
END $$;
`,
	}

	for i, stmt := range steps {
		if err := db.Exec(stmt).Error; err != nil {
			// 42P01 = undefined table; safe to skip on fresh installs where the
			// referenced table has not been created by AutoMigrate yet.
			if strings.Contains(err.Error(), "42P01") {
				continue
			}
			return fmt.Errorf("migrateCurrencyPhase1 step %d: %w", i+1, err)
		}
	}
	return nil
}

// migrateCustomerAddressToStructured copies the legacy free-form `address` column
// into `addr_street1` when upgrading an existing database.  On fresh installs the
// customers table doesn't exist yet, so the function is a no-op (42P01 = undefined table).
func migrateCustomerAddressToStructured(db *gorm.DB) error {
	err := db.Exec(`
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = CURRENT_SCHEMA()
      AND table_name   = 'customers'
      AND column_name  = 'address'
  ) AND NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = CURRENT_SCHEMA()
      AND table_name   = 'customers'
      AND column_name  = 'addr_street1'
  ) THEN
    ALTER TABLE customers ADD COLUMN addr_street1 TEXT NOT NULL DEFAULT '';
    UPDATE customers SET addr_street1 = address WHERE address IS NOT NULL AND address <> '';
    ALTER TABLE customers DROP COLUMN address;
  END IF;
END $$;
`).Error
	if err != nil && strings.Contains(err.Error(), "42P01") {
		return nil
	}
	return err
}

// migrateCurrencyPhase3 adds currency_code, exchange_rate, and base-currency amount columns
// to the invoices and bills tables, backfilling existing rows with safe defaults.
//
// Each table gets 5 new columns:
//   - currency_code  VARCHAR(3)    NOT NULL DEFAULT ''   (blank = company base currency)
//   - exchange_rate  NUMERIC(20,8) NOT NULL DEFAULT 1    (foreignToBase rate; 1 for base-currency docs)
//   - amount_base    NUMERIC(18,2) NOT NULL DEFAULT 0
//   - subtotal_base  NUMERIC(18,2) NOT NULL DEFAULT 0
//   - tax_total_base NUMERIC(18,2) NOT NULL DEFAULT 0
//
// Backfill: existing rows get exchange_rate=1 and base amounts equal to document amounts.
// Safe to run multiple times (idempotent).
func migrateCurrencyPhase3(db *gorm.DB) error {
	steps := []string{
		// ── invoices ─────────────────────────────────────────────────────────────
		`
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'invoices' AND column_name = 'currency_code'
  ) THEN
    ALTER TABLE invoices
      ADD COLUMN currency_code  VARCHAR(3)    NOT NULL DEFAULT '',
      ADD COLUMN exchange_rate  NUMERIC(20,8) NOT NULL DEFAULT 1,
      ADD COLUMN amount_base    NUMERIC(18,2) NOT NULL DEFAULT 0,
      ADD COLUMN subtotal_base  NUMERIC(18,2) NOT NULL DEFAULT 0,
      ADD COLUMN tax_total_base NUMERIC(18,2) NOT NULL DEFAULT 0;
    -- Backfill base amounts for all existing (base-currency) invoices.
    UPDATE invoices
      SET exchange_rate  = 1,
          amount_base    = amount,
          subtotal_base  = subtotal,
          tax_total_base = tax_total;
  END IF;
END $$;
`,
		// ── bills ─────────────────────────────────────────────────────────────────
		`
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'bills' AND column_name = 'currency_code'
  ) THEN
    ALTER TABLE bills
      ADD COLUMN currency_code  VARCHAR(3)    NOT NULL DEFAULT '',
      ADD COLUMN exchange_rate  NUMERIC(20,8) NOT NULL DEFAULT 1,
      ADD COLUMN amount_base    NUMERIC(18,2) NOT NULL DEFAULT 0,
      ADD COLUMN subtotal_base  NUMERIC(18,2) NOT NULL DEFAULT 0,
      ADD COLUMN tax_total_base NUMERIC(18,2) NOT NULL DEFAULT 0;
    -- Backfill base amounts for all existing (base-currency) bills.
    UPDATE bills
      SET exchange_rate  = 1,
          amount_base    = amount,
          subtotal_base  = subtotal,
          tax_total_base = tax_total;
  END IF;
END $$;
`,
	}

	for _, sql := range steps {
		err := db.Exec(sql).Error
		if err != nil && strings.Contains(err.Error(), "42P01") {
			// Table doesn't exist yet (fresh install); AutoMigrate will create it.
			continue
		}
		if err != nil {
			return fmt.Errorf("migrateCurrencyPhase3: %w", err)
		}
	}
	return nil
}

// migrateCurrencyPhase4 adds balance_due_base to invoices and bills, and creates the
// settlement_allocations table.
//
// balance_due_base changes:
//   - invoices: backfill from amount_base for foreign-currency docs; from balance_due for base docs.
//   - bills:    same pattern.
//
// settlement_allocations is created by GORM AutoMigrate (it is in the AutoMigrate list above).
// This function only handles the column additions that require idempotent ALTER TABLE guards.
//
// All steps are safe to run multiple times (idempotent).
func migrateCurrencyPhase4(db *gorm.DB) error {
	steps := []string{
		// ── invoices: add balance_due_base ────────────────────────────────────────
		`
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'invoices' AND column_name = 'balance_due_base'
  ) THEN
    ALTER TABLE invoices ADD COLUMN balance_due_base NUMERIC(18,2) NOT NULL DEFAULT 0;
    -- For base-currency invoices: balance_due_base = balance_due.
    UPDATE invoices
      SET balance_due_base = balance_due
      WHERE currency_code = '' OR currency_code IS NULL;
    -- For foreign-currency invoices: assume fully unpaid; balance_due_base = amount_base.
    -- (Phase-4 settlements will decrement this going forward.)
    UPDATE invoices
      SET balance_due_base = amount_base
      WHERE currency_code IS NOT NULL AND currency_code <> '' AND balance_due_base = 0;
  END IF;
END $$;
`,
		// ── bills: add balance_due_base ───────────────────────────────────────────
		`
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'bills' AND column_name = 'balance_due_base'
  ) THEN
    ALTER TABLE bills ADD COLUMN balance_due_base NUMERIC(18,2) NOT NULL DEFAULT 0;
    UPDATE bills
      SET balance_due_base = balance_due
      WHERE currency_code = '' OR currency_code IS NULL;
    UPDATE bills
      SET balance_due_base = amount_base
      WHERE currency_code IS NOT NULL AND currency_code <> '' AND balance_due_base = 0;
  END IF;
END $$;
`,
	}

	for _, sql := range steps {
		err := db.Exec(sql).Error
		if err != nil && strings.Contains(err.Error(), "42P01") {
			continue
		}
		if err != nil {
			return fmt.Errorf("migrateCurrencyPhase4: %w", err)
		}
	}
	return nil
}

// migrateHostedInvoicePhase1 creates the invoice_hosted_links table and its indexes.
//
// The table is created with IF NOT EXISTS so this function is safe to call on
// databases that already have the table. GORM AutoMigrate adds new columns
// automatically after this runs.
//
// Key index: uk_ihl_invoice_active is a PostgreSQL partial unique index that
// enforces "at most one active link per invoice" at the DB level. The service
// layer enforces this constraint as well (so SQLite tests work without partial
// index support).
func migrateHostedInvoicePhase1(db *gorm.DB) error {
	steps := []string{
		`
CREATE TABLE IF NOT EXISTS invoice_hosted_links (
    id          BIGSERIAL PRIMARY KEY,
    company_id  BIGINT    NOT NULL,
    invoice_id  BIGINT    NOT NULL,
    token_hash  TEXT      NOT NULL,
    status      TEXT      NOT NULL DEFAULT 'active',
    expires_at  TIMESTAMPTZ,
    revoked_at  TIMESTAMPTZ,
    created_by  BIGINT,
    last_viewed_at TIMESTAMPTZ,
    view_count  INT       NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uk_ihl_token    ON invoice_hosted_links (token_hash);`,
		`CREATE INDEX        IF NOT EXISTS idx_ihl_company ON invoice_hosted_links (company_id);`,
		`CREATE INDEX        IF NOT EXISTS idx_ihl_invoice ON invoice_hosted_links (invoice_id);`,
		// Partial unique index: enforces one active link per invoice in PostgreSQL.
		// SQLite (used in tests) ignores this silently; service layer is the
		// authoritative enforcement point for both environments.
		`CREATE UNIQUE INDEX IF NOT EXISTS uk_ihl_invoice_active
             ON invoice_hosted_links (invoice_id)
             WHERE status = 'active';`,
	}

	for _, sql := range steps {
		if err := db.Exec(sql).Error; err != nil {
			// Ignore "table already exists" (42P07) and "relation already exists" (42P07).
			// SQLite partial index syntax is unsupported: ignore those errors silently.
			if strings.Contains(err.Error(), "42P07") ||
				strings.Contains(err.Error(), "already exists") ||
				strings.Contains(err.Error(), "near \"WHERE\"") {
				continue
			}
			return fmt.Errorf("migrateHostedInvoicePhase1: %w", err)
		}
	}
	return nil
}
