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
	// FX Phase: add tx_debit / tx_credit to journal_lines with safe backfill before
	// AutoMigrate enforces NOT NULL. Fresh installs skip (table doesn't exist yet).
	if err := migrateJournalLinesTxAmounts(db); err != nil {
		return err
	}
	// Ensure user_plans seed data exists and backfill users.plan_id = 0 → 1
	// before AutoMigrate adds the FK constraint. This handles cases where GORM
	// added the plan_id column on a previous deploy before the SQL migration ran,
	// leaving existing rows with plan_id = 0 which violates the FK.
	if err := migrateEnsureUserPlans(db); err != nil {
		return err
	}
	if err := db.AutoMigrate(
		// UserPlan must precede User because User.PlanID is a FK into user_plans.
		&models.UserPlan{},
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
		// Phase 6: versioned accounting standard profiles + per-company accounting books.
		// AccountingStandardProfile must precede AccountingBook (FK dependency).
		&models.AccountingStandardProfile{},
		&models.AccountingBook{},
		// Phase 7: immutable FX rate snapshots (linked from JournalEntry + SettlementAllocation).
		&models.FXSnapshot{},
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
		// Vendor credit balance (bill overpayment → vendor prepayment)
		&models.VendorCredit{},
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
		// SmartPicker: selection event log (ranking/popularity signal)
		&models.SmartPickerUsage{},
		// User preferences (number format, etc.) — one row per user
		&models.UserPreference{},
		// Phase 8: per-secondary-book accounted amounts for each journal line.
		// JournalLineBookAmount depends on JournalLine and AccountingBook (FK deps above).
		&models.JournalLineBookAmount{},
		// Phase 9: period-end IAS 21 translation runs + per-account lines.
		// TranslationRun depends on AccountingBook; TranslationLine depends on TranslationRun.
		&models.TranslationRun{},
		&models.TranslationLine{},
		// AR Phase 1: credit note header + lines + application allocations.
		// CreditNote depends on Customer and Invoice (nullable FK).
		&models.CreditNote{},
		&models.CreditNoteLine{},
		&models.CreditNoteApplication{},
		// Phase 10: fiscal period governance + accounting standard change audit trail.
		&models.FiscalPeriod{},
		&models.BookStandardChange{},
		// Phase 11: AR/AP control-account routing table + Customer/Vendor currency policy.
		&models.ARAPControlMapping{},
		// Phase 12: per-customer and per-vendor allowed-currency lists.
		&models.CustomerAllowedCurrency{},
		&models.VendorAllowedCurrency{},
		// AR Phase 13 (AR Module Phase 1): formal AR object skeletons.
		// Quote → SalesOrder: commercial pre-chain, no JE.
		// CustomerDeposit + CustomerDepositApplication: pre-invoice cash, JE = liability.
		// CustomerReceipt: formal AR receipt header, JE = Dr Cash Cr AR (Phase 4).
		// PaymentApplication: AR open-item matching record (Phase 4).
		// ARReturn: business-fact return object, no JE.
		// ARRefund: fund-outflow object, JE = Dr Liability Cr Cash (Phase 5).
		&models.Quote{},
		&models.QuoteLine{},
		&models.SalesOrder{},
		&models.SalesOrderLine{},
		&models.CustomerDeposit{},
		&models.CustomerDepositApplication{},
		&models.CustomerReceipt{},
		&models.PaymentApplication{},
		&models.ARReturn{},
		&models.ARRefund{},
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
	if err := migrateTaskServiceItem(db); err != nil {
		return err
	}
	// Phase 6: accounting_standard_profiles + accounting_books tables;
	// companies.primary_book_id; revaluation_runs.book_id backfill.
	// No behavioral changes — all existing posting logic continues unchanged.
	if err := migrateCurrencyPhase6(db); err != nil {
		return err
	}
	// Phase 7: fx_snapshots table; journal_entries.fx_snapshot_id;
	// settlement_allocations.fx_snapshot_id. Existing rows get NULL (valid).
	if err := migrateCurrencyPhase7(db); err != nil {
		return err
	}
	// Phase 8: journal_line_book_amounts table (per-secondary-book accounted amounts).
	// AutoMigrate above handles fresh installs; this guard adds the table on live DBs
	// that already have journal_lines and accounting_books but predate Phase 8.
	if err := migrateCurrencyPhase8(db); err != nil {
		return err
	}
	// Phase 9: translation_runs + translation_lines tables (IAS 21 period-end translation).
	if err := migrateCurrencyPhase9(db); err != nil {
		return err
	}
	// AR Phase 1: credit_notes + credit_note_lines + credit_note_applications tables.
	if err := migrateARPhase1(db); err != nil {
		return err
	}
	// Phase 10: fiscal_periods + book_standard_changes tables.
	if err := migratePhase10(db); err != nil {
		return err
	}
	// Phase 11: ar_ap_control_mappings table + currency_policy columns on customers/vendors.
	if err := migratePhase11(db); err != nil {
		return err
	}
	// Phase 12: customer_allowed_currencies + vendor_allowed_currencies tables.
	if err := migratePhase12(db); err != nil {
		return err
	}
	// AR Phase 13 (AR Module Phase 1): Quote, SalesOrder, CustomerDeposit,
	// CustomerReceipt, PaymentApplication, ARReturn, ARRefund tables.
	return migratePhase13(db)
}

// migrateEnsureUserPlans seeds the user_plans table with the three default tiers
// and backfills users.plan_id = 0 → 1 (Starter).
//
// This guard is needed because a previous GORM AutoMigrate may have added the
// plan_id column to users before the SQL migration 053 ran, leaving existing rows
// with plan_id = 0 which would violate the fk_users_plan FK constraint.
// Running this before AutoMigrate ensures all rows are valid before the FK is added.
func migrateEnsureUserPlans(db *gorm.DB) error {
	// 1. Create the table if it doesn't exist yet (safety net for fresh installs
	//    where ApplySQLMigrations hasn't run or the SQL migration file is missing).
	if err := db.Exec(`
		CREATE TABLE IF NOT EXISTS user_plans (
			id                      SERIAL PRIMARY KEY,
			name                    TEXT    NOT NULL,
			max_owned_companies     INTEGER NOT NULL DEFAULT 3,
			max_members_per_company INTEGER NOT NULL DEFAULT 5,
			is_active               BOOLEAN NOT NULL DEFAULT TRUE,
			sort_order              INTEGER NOT NULL DEFAULT 0,
			created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`).Error; err != nil {
		// SQLite (used in tests) uses a different dialect; ignore if already exists.
		if !strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("migrateEnsureUserPlans create table: %w", err)
		}
	}

	// 2. Seed the three default plans (idempotent).
	if err := db.Exec(`
		INSERT INTO user_plans (id, name, max_owned_companies, max_members_per_company, is_active, sort_order)
		VALUES
			(1, 'Starter',      3,  5,  TRUE, 10),
			(2, 'Professional', 5,  15, TRUE, 20),
			(3, 'Business',     -1, -1, TRUE, 30)
		ON CONFLICT (id) DO NOTHING
	`).Error; err != nil {
		// Ignore "no such table" on SQLite test DBs — they use AutoMigrate paths.
		if !strings.Contains(err.Error(), "no such table") {
			return fmt.Errorf("migrateEnsureUserPlans seed: %w", err)
		}
	}

	// 3. Backfill users that have plan_id = 0 (written by GORM before seed ran).
	if err := db.Exec(`
		UPDATE users SET plan_id = 1 WHERE plan_id = 0 OR plan_id IS NULL
	`).Error; err != nil {
		// Ignore if users table doesn't exist yet (fresh install before AutoMigrate).
		if !strings.Contains(err.Error(), "no such table") &&
			!strings.Contains(err.Error(), `relation "users" does not exist`) {
			return fmt.Errorf("migrateEnsureUserPlans backfill: %w", err)
		}
	}

	return nil
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
//  3. Create the exchange_rates table with partial lookup indexes.
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
		// ── Step 3: exchange_rates table + partial lookup indexes ──────────────
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
CREATE INDEX IF NOT EXISTS idx_exchange_rates_system_lookup
  ON exchange_rates (base_currency_code, target_currency_code, rate_type, effective_date DESC, id DESC)
  WHERE company_id IS NULL;
CREATE INDEX IF NOT EXISTS idx_exchange_rates_company_lookup
  ON exchange_rates (company_id, base_currency_code, target_currency_code, rate_type, effective_date DESC, id DESC)
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

// migrateJournalLinesTxAmounts adds tx_debit and tx_credit to journal_lines with a
// safe three-step approach for existing databases:
//  1. Add columns as nullable (no DEFAULT needed; we backfill immediately).
//  2. Backfill existing rows from debit / credit (base-currency truth → tx amounts).
//  3. Set NOT NULL now that every row has a value.
//
// Fresh installs skip this entirely (42P01 undefined table) — AutoMigrate creates the
// columns correctly from the model definition.
func migrateJournalLinesTxAmounts(db *gorm.DB) error {
	steps := []string{
		// Step 1 + 2: add nullable, backfill immediately in one transaction block.
		`
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = CURRENT_SCHEMA()
      AND table_name   = 'journal_lines'
      AND column_name  = 'tx_debit'
  ) THEN
    ALTER TABLE journal_lines
      ADD COLUMN tx_debit  NUMERIC(18,2),
      ADD COLUMN tx_credit NUMERIC(18,2);
    -- Backfill: for all existing base-currency lines, tx amounts equal base amounts.
    UPDATE journal_lines SET tx_debit = debit, tx_credit = credit;
  END IF;
END $$;
`,
		// Step 3: enforce NOT NULL now that every row is populated.
		`
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = CURRENT_SCHEMA()
      AND table_name   = 'journal_lines'
      AND column_name  = 'tx_debit'
      AND is_nullable  = 'YES'
  ) THEN
    ALTER TABLE journal_lines
      ALTER COLUMN tx_debit  SET NOT NULL,
      ALTER COLUMN tx_credit SET NOT NULL;
  END IF;
END $$;
`,
	}

	for i, stmt := range steps {
		if err := db.Exec(stmt).Error; err != nil {
			if strings.Contains(err.Error(), "42P01") {
				// Table doesn't exist yet — fresh install; AutoMigrate will handle it.
				return nil
			}
			return fmt.Errorf("migrateJournalLinesTxAmounts step %d: %w", i+1, err)
		}
	}
	return nil
}

// migrateCurrencyPhase6 creates the accounting_standard_profiles and
// accounting_books tables, adds primary_book_id to companies, and backfills
// one primary AccountingBook per existing company (functional_currency =
// company.base_currency_code, standard_profile = ASPE_2024).
//
// Also adds book_id to revaluation_runs and backfills to the primary book.
//
// All steps are fully idempotent. On a fresh install the companies table does
// not yet exist when this runs (AutoMigrate hasn't fired), so backfill steps
// check for table existence before acting.
func migrateCurrencyPhase6(db *gorm.DB) error {
	steps := []string{
		// Step 1: accounting_standard_profiles table
		`CREATE TABLE IF NOT EXISTS accounting_standard_profiles (
    id             BIGSERIAL    PRIMARY KEY,
    code           TEXT         NOT NULL,
    display_name   TEXT         NOT NULL,
    effective_from DATE         NOT NULL,
    is_system      BOOLEAN      NOT NULL DEFAULT TRUE,
    created_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_asp_code UNIQUE (code)
);`,
		// Step 2: seed system profiles (idempotent via ON CONFLICT DO NOTHING)
		`INSERT INTO accounting_standard_profiles (code, display_name, effective_from, is_system, created_at)
VALUES
    ('ASPE_2024',       'ASPE 2024',          '2024-01-01', TRUE, NOW()),
    ('IFRS_IAS21_2025', 'IFRS IAS 21 (2025)', '2025-01-01', TRUE, NOW()),
    ('IFRS_IAS21_2027', 'IFRS IAS 21 (2027)', '2027-01-01', TRUE, NOW())
ON CONFLICT (code) DO NOTHING;`,
		// Step 3: accounting_books table (FK to accounting_standard_profiles)
		`CREATE TABLE IF NOT EXISTS accounting_books (
    id                     BIGSERIAL    PRIMARY KEY,
    company_id             BIGINT       NOT NULL,
    book_type              TEXT         NOT NULL DEFAULT 'primary',
    functional_currency    VARCHAR(3)   NOT NULL,
    standard_profile_id    BIGINT       NOT NULL REFERENCES accounting_standard_profiles(id),
    standard_change_policy TEXT         NOT NULL DEFAULT 'allow_direct',
    created_at             TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_accounting_books_company ON accounting_books (company_id);`,
		// Step 4: add primary_book_id to companies (nullable; backfilled in step 5)
		`DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = CURRENT_SCHEMA()
          AND table_name   = 'companies'
          AND column_name  = 'primary_book_id'
    ) THEN
        ALTER TABLE companies ADD COLUMN primary_book_id BIGINT;
    END IF;
END $$;`,
		// Step 5: backfill one primary book per company + wire primary_book_id
		`DO $$
DECLARE
    v_profile_id BIGINT;
BEGIN
    -- Skip entirely on fresh installs where the companies table does not exist yet.
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'companies'
    ) THEN
        RETURN;
    END IF;

    SELECT id INTO v_profile_id
    FROM accounting_standard_profiles WHERE code = 'ASPE_2024' LIMIT 1;

    IF v_profile_id IS NULL THEN
        RETURN; -- profile seed may not have committed; re-run will fix.
    END IF;

    -- Insert a primary book for each company that does not already have one.
    INSERT INTO accounting_books
        (company_id, book_type, functional_currency, standard_profile_id,
         standard_change_policy, created_at, updated_at)
    SELECT
        c.id,
        'primary',
        c.base_currency_code,
        v_profile_id,
        'allow_direct',
        NOW(),
        NOW()
    FROM companies c
    WHERE NOT EXISTS (
        SELECT 1 FROM accounting_books ab
        WHERE ab.company_id = c.id AND ab.book_type = 'primary'
    );

    -- Wire companies.primary_book_id to the newly created (or existing) primary book.
    UPDATE companies c
    SET    primary_book_id = ab.id
    FROM   accounting_books ab
    WHERE  ab.company_id  = c.id
      AND  ab.book_type   = 'primary'
      AND  c.primary_book_id IS NULL;
END $$;`,
		// Step 6: add book_id to revaluation_runs
		`DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = CURRENT_SCHEMA()
          AND table_name   = 'revaluation_runs'
          AND column_name  = 'book_id'
    ) THEN
        ALTER TABLE revaluation_runs ADD COLUMN book_id BIGINT;
    END IF;
END $$;`,
		// Step 7: backfill revaluation_runs.book_id to each company's primary book
		`DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'revaluation_runs'
    ) THEN
        RETURN;
    END IF;

    UPDATE revaluation_runs rr
    SET    book_id = ab.id
    FROM   companies c
    JOIN   accounting_books ab
           ON ab.company_id = c.id AND ab.book_type = 'primary'
    WHERE  rr.company_id = c.id
      AND  rr.book_id IS NULL;
END $$;`,
	}

	for i, stmt := range steps {
		if err := db.Exec(stmt).Error; err != nil {
			// 42P01 = undefined_table: safe to skip on fresh installs.
			if strings.Contains(err.Error(), "42P01") {
				continue
			}
			return fmt.Errorf("migrateCurrencyPhase6 step %d: %w", i+1, err)
		}
	}
	return nil
}

// migrateCurrencyPhase7 creates the fx_snapshots table and adds fx_snapshot_id
// (nullable) to journal_entries and settlement_allocations.
//
// Existing rows are left with fx_snapshot_id = NULL, which is valid; the column
// is nullable specifically to preserve backward compatibility.
// All steps are idempotent.
func migrateCurrencyPhase7(db *gorm.DB) error {
	steps := []string{
		// Step 1: fx_snapshots table
		`CREATE TABLE IF NOT EXISTS fx_snapshots (
    id             BIGSERIAL     PRIMARY KEY,
    company_id     BIGINT        NOT NULL,
    from_currency  VARCHAR(3)    NOT NULL,
    to_currency    VARCHAR(3)    NOT NULL,
    rate           NUMERIC(20,8) NOT NULL,
    effective_date DATE          NOT NULL,
    rate_type      TEXT          NOT NULL DEFAULT 'spot',
    quote_basis    TEXT          NOT NULL DEFAULT 'direct',
    posting_reason TEXT          NOT NULL,
    rate_category  TEXT          NOT NULL,
    source         TEXT          NOT NULL DEFAULT '',
    is_immutable   BOOLEAN       NOT NULL DEFAULT TRUE,
    created_at     TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_fx_snapshots_company
    ON fx_snapshots (company_id);
CREATE INDEX IF NOT EXISTS idx_fx_snapshots_currency_date
    ON fx_snapshots (from_currency, to_currency, effective_date DESC);`,
		// Step 2: add fx_snapshot_id to journal_entries
		`DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = CURRENT_SCHEMA()
          AND table_name   = 'journal_entries'
          AND column_name  = 'fx_snapshot_id'
    ) THEN
        ALTER TABLE journal_entries ADD COLUMN fx_snapshot_id BIGINT;
        CREATE INDEX IF NOT EXISTS idx_je_fx_snapshot
            ON journal_entries (fx_snapshot_id)
            WHERE fx_snapshot_id IS NOT NULL;
    END IF;
END $$;`,
		// Step 3: add fx_snapshot_id to settlement_allocations
		`DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = CURRENT_SCHEMA()
          AND table_name   = 'settlement_allocations'
          AND column_name  = 'fx_snapshot_id'
    ) THEN
        ALTER TABLE settlement_allocations ADD COLUMN fx_snapshot_id BIGINT;
        CREATE INDEX IF NOT EXISTS idx_sa_fx_snapshot
            ON settlement_allocations (fx_snapshot_id)
            WHERE fx_snapshot_id IS NOT NULL;
    END IF;
END $$;`,
	}

	for i, stmt := range steps {
		if err := db.Exec(stmt).Error; err != nil {
			if strings.Contains(err.Error(), "42P01") {
				continue
			}
			return fmt.Errorf("migrateCurrencyPhase7 step %d: %w", i+1, err)
		}
	}
	return nil
}

// migrateCurrencyPhase8 creates the journal_line_book_amounts table and its indexes.
// AutoMigrate handles fresh installs; this guard adds the table on live databases
// that predate Phase 8.
func migrateCurrencyPhase8(db *gorm.DB) error {
	steps := []string{
		// Step 1: journal_line_book_amounts table
		`CREATE TABLE IF NOT EXISTS journal_line_book_amounts (
    id               BIGSERIAL     PRIMARY KEY,
    journal_line_id  BIGINT        NOT NULL,
    book_id          BIGINT        NOT NULL,
    company_id       BIGINT        NOT NULL,
    accounted_debit  NUMERIC(18,2) NOT NULL,
    accounted_credit NUMERIC(18,2) NOT NULL,
    fx_snapshot_id   BIGINT,
    created_at       TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_jlba_line_book UNIQUE (journal_line_id, book_id)
);
CREATE INDEX IF NOT EXISTS idx_jlba_book_id
    ON journal_line_book_amounts (book_id);
CREATE INDEX IF NOT EXISTS idx_jlba_company_id
    ON journal_line_book_amounts (company_id);
CREATE INDEX IF NOT EXISTS idx_jlba_fx_snapshot_id
    ON journal_line_book_amounts (fx_snapshot_id)
    WHERE fx_snapshot_id IS NOT NULL;`,
	}

	for i, stmt := range steps {
		if err := db.Exec(stmt).Error; err != nil {
			if strings.Contains(err.Error(), "42P01") {
				continue
			}
			return fmt.Errorf("migrateCurrencyPhase8 step %d: %w", i+1, err)
		}
	}
	return nil
}

// migrateCurrencyPhase9 creates the translation_runs and translation_lines tables.
// AutoMigrate handles fresh installs; this guard adds the tables on live databases
// that predate Phase 9.
func migrateCurrencyPhase9(db *gorm.DB) error {
	steps := []string{
		// Step 1: translation_runs table
		`CREATE TABLE IF NOT EXISTS translation_runs (
    id                    BIGSERIAL     PRIMARY KEY,
    company_id            BIGINT        NOT NULL,
    book_id               BIGINT        NOT NULL,
    period_start          DATE          NOT NULL,
    period_end            DATE          NOT NULL,
    run_date              DATE          NOT NULL,
    functional_currency   VARCHAR(3)    NOT NULL,
    presentation_currency VARCHAR(3)    NOT NULL,
    closing_rate          NUMERIC(20,8) NOT NULL,
    average_rate          NUMERIC(20,8) NOT NULL,
    cta_amount            NUMERIC(18,2) NOT NULL,
    cta_account_id        BIGINT,
    status                TEXT          NOT NULL DEFAULT 'posted',
    created_at            TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_tr_company_id ON translation_runs (company_id);
CREATE INDEX IF NOT EXISTS idx_tr_book_id    ON translation_runs (book_id);`,
		// Step 2: translation_lines table
		`CREATE TABLE IF NOT EXISTS translation_lines (
    id                 BIGSERIAL     PRIMARY KEY,
    translation_run_id BIGINT        NOT NULL,
    company_id         BIGINT        NOT NULL,
    account_id         BIGINT        NOT NULL,
    functional_debit   NUMERIC(18,2) NOT NULL,
    functional_credit  NUMERIC(18,2) NOT NULL,
    rate_applied       NUMERIC(20,8) NOT NULL,
    rate_type          TEXT          NOT NULL,
    translated_debit   NUMERIC(18,2) NOT NULL,
    translated_credit  NUMERIC(18,2) NOT NULL,
    created_at         TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_tl_run_id     ON translation_lines (translation_run_id);
CREATE INDEX IF NOT EXISTS idx_tl_company_id ON translation_lines (company_id);
CREATE INDEX IF NOT EXISTS idx_tl_account_id ON translation_lines (account_id);`,
	}

	for i, stmt := range steps {
		if err := db.Exec(stmt).Error; err != nil {
			if strings.Contains(err.Error(), "42P01") {
				continue
			}
			return fmt.Errorf("migrateCurrencyPhase9 step %d: %w", i+1, err)
		}
	}
	return nil
}

// migrateARPhase1 creates the credit_notes, credit_note_lines, and
// credit_note_applications tables. AutoMigrate handles fresh installs;
// this guard adds the tables on live databases that predate AR Phase 1.
func migrateARPhase1(db *gorm.DB) error {
	steps := []string{
		// Step 1: credit_notes table
		`CREATE TABLE IF NOT EXISTS credit_notes (
    id                      BIGSERIAL     PRIMARY KEY,
    company_id              BIGINT        NOT NULL,
    credit_note_number      TEXT          NOT NULL,
    customer_id             BIGINT        NOT NULL,
    invoice_id              BIGINT,
    credit_note_date        DATE          NOT NULL,
    status                  TEXT          NOT NULL DEFAULT 'draft',
    reason                  TEXT          NOT NULL DEFAULT 'other',
    memo                    TEXT          NOT NULL DEFAULT '',
    subtotal                NUMERIC(18,2) NOT NULL DEFAULT 0,
    tax_total               NUMERIC(18,2) NOT NULL DEFAULT 0,
    amount                  NUMERIC(18,2) NOT NULL DEFAULT 0,
    balance_remaining       NUMERIC(18,2) NOT NULL DEFAULT 0,
    currency_code           VARCHAR(3)    NOT NULL DEFAULT '',
    exchange_rate           NUMERIC(20,8) NOT NULL DEFAULT 1,
    amount_base             NUMERIC(18,2) NOT NULL DEFAULT 0,
    journal_entry_id        BIGINT,
    issued_at               TIMESTAMPTZ,
    voided_at               TIMESTAMPTZ,
    customer_name_snapshot  TEXT          NOT NULL DEFAULT '',
    created_at              TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_cn_company_id    ON credit_notes (company_id);
CREATE INDEX IF NOT EXISTS idx_cn_customer_id   ON credit_notes (customer_id);
CREATE INDEX IF NOT EXISTS idx_cn_invoice_id    ON credit_notes (invoice_id)    WHERE invoice_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_cn_je_id         ON credit_notes (journal_entry_id) WHERE journal_entry_id IS NOT NULL;`,
		// Step 2: credit_note_lines table
		`CREATE TABLE IF NOT EXISTS credit_note_lines (
    id                  BIGSERIAL     PRIMARY KEY,
    company_id          BIGINT        NOT NULL,
    credit_note_id      BIGINT        NOT NULL,
    sort_order          INT           NOT NULL DEFAULT 1,
    product_service_id  BIGINT,
    revenue_account_id  BIGINT        NOT NULL,
    description         TEXT          NOT NULL,
    qty                 NUMERIC(10,4) NOT NULL DEFAULT 1,
    unit_price          NUMERIC(18,4) NOT NULL DEFAULT 0,
    tax_code_id         BIGINT,
    line_net            NUMERIC(18,2) NOT NULL DEFAULT 0,
    line_tax            NUMERIC(18,2) NOT NULL DEFAULT 0,
    line_total          NUMERIC(18,2) NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_cnl_credit_note_id ON credit_note_lines (credit_note_id);
CREATE INDEX IF NOT EXISTS idx_cnl_company_id     ON credit_note_lines (company_id);`,
		// Step 3: credit_note_applications table
		`CREATE TABLE IF NOT EXISTS credit_note_applications (
    id                   BIGSERIAL     PRIMARY KEY,
    company_id           BIGINT        NOT NULL,
    credit_note_id       BIGINT        NOT NULL,
    invoice_id           BIGINT        NOT NULL,
    amount_applied       NUMERIC(18,2) NOT NULL,
    amount_applied_base  NUMERIC(18,2) NOT NULL,
    applied_at           TIMESTAMPTZ   NOT NULL,
    created_at           TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_cna_credit_note_id ON credit_note_applications (credit_note_id);
CREATE INDEX IF NOT EXISTS idx_cna_invoice_id     ON credit_note_applications (invoice_id);
CREATE INDEX IF NOT EXISTS idx_cna_company_id     ON credit_note_applications (company_id);`,
	}

	for i, stmt := range steps {
		if err := db.Exec(stmt).Error; err != nil {
			if strings.Contains(err.Error(), "42P01") {
				continue
			}
			return fmt.Errorf("migrateARPhase1 step %d: %w", i+1, err)
		}
	}
	return nil
}

// migratePhase10 creates the fiscal_periods and book_standard_changes tables.
// AutoMigrate above handles fresh installs; this guard adds the tables on live
// databases that predate Phase 10.
func migratePhase10(db *gorm.DB) error {
	stmts := []string{
		// fiscal_periods: named reporting-period rows for a company.
		// book_id = 0 means company-wide.
		`CREATE TABLE IF NOT EXISTS fiscal_periods (
			id           BIGSERIAL PRIMARY KEY,
			company_id   BIGINT      NOT NULL,
			book_id      BIGINT      NOT NULL DEFAULT 0,
			label        TEXT        NOT NULL,
			period_start DATE        NOT NULL,
			period_end   DATE        NOT NULL,
			status       TEXT        NOT NULL DEFAULT 'open',
			closed_at    TIMESTAMPTZ,
			closed_by    TEXT        NOT NULL DEFAULT '',
			created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_fiscal_periods_company ON fiscal_periods(company_id)`,
		`CREATE INDEX IF NOT EXISTS idx_fiscal_periods_book   ON fiscal_periods(book_id)`,

		// book_standard_changes: immutable audit log of standard-profile changes.
		`CREATE TABLE IF NOT EXISTS book_standard_changes (
			id              BIGSERIAL PRIMARY KEY,
			company_id      BIGINT      NOT NULL,
			book_id         BIGINT      NOT NULL,
			old_profile_id  BIGINT      NOT NULL,
			new_profile_id  BIGINT      NOT NULL,
			method          TEXT        NOT NULL,
			cutover_date    DATE,
			notes           TEXT        NOT NULL DEFAULT '',
			changed_by      TEXT        NOT NULL DEFAULT '',
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_book_standard_changes_company ON book_standard_changes(company_id)`,
		`CREATE INDEX IF NOT EXISTS idx_book_standard_changes_book    ON book_standard_changes(book_id)`,
	}

	for i, stmt := range stmts {
		if err := db.Exec(stmt).Error; err != nil {
			// Postgres: 42P01 means relation already exists for IF NOT EXISTS guards.
			// Treat as a no-op on live databases.
			if strings.Contains(err.Error(), "42P01") ||
				strings.Contains(err.Error(), "already exists") {
				continue
			}
			return fmt.Errorf("migratePhase10 step %d: %w", i+1, err)
		}
	}
	return nil
}

// migratePhase11 creates the ar_ap_control_mappings table and adds
// currency_policy columns to customers and vendors. AutoMigrate handles fresh
// installs; this guard adds the table and columns on live databases.
func migratePhase11(db *gorm.DB) error {
	stmts := []string{
		// ar_ap_control_mappings: explicit AR/AP control-account routing.
		`CREATE TABLE IF NOT EXISTS ar_ap_control_mappings (
			id                 BIGSERIAL PRIMARY KEY,
			company_id         BIGINT      NOT NULL,
			book_id            BIGINT      NOT NULL DEFAULT 0,
			document_type      TEXT        NOT NULL,
			currency_code      VARCHAR(3)  NOT NULL DEFAULT '',
			control_account_id BIGINT      NOT NULL,
			notes              TEXT        NOT NULL DEFAULT '',
			created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ar_ap_ctrl_company ON ar_ap_control_mappings(company_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_ar_ap_ctrl_uniq
			ON ar_ap_control_mappings(company_id, book_id, document_type, currency_code)`,

		// currency_policy on customers (safe ADD COLUMN IF NOT EXISTS).
		`ALTER TABLE customers ADD COLUMN IF NOT EXISTS
			currency_policy TEXT NOT NULL DEFAULT 'single'`,

		// currency_policy on vendors.
		`ALTER TABLE vendors ADD COLUMN IF NOT EXISTS
			currency_policy TEXT NOT NULL DEFAULT 'single'`,
	}

	for i, stmt := range stmts {
		if err := db.Exec(stmt).Error; err != nil {
			if strings.Contains(err.Error(), "42P01") ||
				strings.Contains(err.Error(), "already exists") ||
				strings.Contains(err.Error(), "duplicate column") {
				continue
			}
			return fmt.Errorf("migratePhase11 step %d: %w", i+1, err)
		}
	}
	return nil
}

// migratePhase12 creates the customer_allowed_currencies and
// vendor_allowed_currencies tables. AutoMigrate handles fresh installs.
func migratePhase12(db *gorm.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS customer_allowed_currencies (
			id            BIGSERIAL PRIMARY KEY,
			company_id    BIGINT      NOT NULL,
			customer_id   BIGINT      NOT NULL,
			currency_code VARCHAR(3)  NOT NULL,
			created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX  IF NOT EXISTS idx_cust_allowed_curr_company  ON customer_allowed_currencies(company_id)`,
		`CREATE INDEX  IF NOT EXISTS idx_cust_allowed_curr_customer ON customer_allowed_currencies(customer_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_cust_allowed_curr_uniq
			ON customer_allowed_currencies(company_id, customer_id, currency_code)`,

		`CREATE TABLE IF NOT EXISTS vendor_allowed_currencies (
			id            BIGSERIAL PRIMARY KEY,
			company_id    BIGINT      NOT NULL,
			vendor_id     BIGINT      NOT NULL,
			currency_code VARCHAR(3)  NOT NULL,
			created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX  IF NOT EXISTS idx_vendor_allowed_curr_company ON vendor_allowed_currencies(company_id)`,
		`CREATE INDEX  IF NOT EXISTS idx_vendor_allowed_curr_vendor  ON vendor_allowed_currencies(vendor_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_vendor_allowed_curr_uniq
			ON vendor_allowed_currencies(company_id, vendor_id, currency_code)`,
	}
	for i, stmt := range stmts {
		if err := db.Exec(stmt).Error; err != nil {
			if strings.Contains(err.Error(), "42P01") ||
				strings.Contains(err.Error(), "already exists") {
				continue
			}
			return fmt.Errorf("migratePhase12 step %d: %w", i+1, err)
		}
	}
	return nil
}

// migratePhase13 creates the AR module object tables introduced in AR Phase 1.
// AutoMigrate handles fresh installs; this guard adds the tables on live databases.
//
// Tables: quotes, quote_lines, sales_orders, sales_order_lines,
//         customer_deposits, customer_deposit_applications,
//         customer_receipts, payment_applications,
//         ar_returns, ar_refunds.
func migratePhase13(db *gorm.DB) error {
	stmts := []string{
		// quotes
		`CREATE TABLE IF NOT EXISTS quotes (
			id              BIGSERIAL PRIMARY KEY,
			company_id      BIGINT       NOT NULL,
			customer_id     BIGINT       NOT NULL,
			sales_order_id  BIGINT,
			quote_number    VARCHAR(50)  NOT NULL DEFAULT '',
			status          TEXT         NOT NULL DEFAULT 'draft',
			quote_date      TIMESTAMPTZ  NOT NULL,
			expiry_date     TIMESTAMPTZ,
			currency_code   VARCHAR(3)   NOT NULL DEFAULT '',
			subtotal        NUMERIC(18,4) NOT NULL DEFAULT 0,
			tax_total       NUMERIC(18,4) NOT NULL DEFAULT 0,
			total           NUMERIC(18,4) NOT NULL DEFAULT 0,
			notes           TEXT         NOT NULL DEFAULT '',
			memo            TEXT         NOT NULL DEFAULT '',
			sent_at         TIMESTAMPTZ,
			created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
			updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_quotes_company    ON quotes(company_id)`,
		`CREATE INDEX IF NOT EXISTS idx_quotes_customer   ON quotes(customer_id)`,

		// quote_lines
		`CREATE TABLE IF NOT EXISTS quote_lines (
			id                   BIGSERIAL PRIMARY KEY,
			quote_id             BIGINT        NOT NULL,
			product_service_id   BIGINT,
			revenue_account_id   BIGINT,
			tax_code_id          BIGINT,
			description          TEXT          NOT NULL DEFAULT '',
			quantity             NUMERIC(18,4) NOT NULL DEFAULT 1,
			unit_price           NUMERIC(18,4) NOT NULL DEFAULT 0,
			line_net             NUMERIC(18,4) NOT NULL DEFAULT 0,
			tax_amount           NUMERIC(18,4) NOT NULL DEFAULT 0,
			line_total           NUMERIC(18,4) NOT NULL DEFAULT 0,
			sort_order           INT           NOT NULL DEFAULT 0,
			created_at           TIMESTAMPTZ   NOT NULL DEFAULT now(),
			updated_at           TIMESTAMPTZ   NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_quote_lines_quote ON quote_lines(quote_id)`,

		// sales_orders
		`CREATE TABLE IF NOT EXISTS sales_orders (
			id               BIGSERIAL PRIMARY KEY,
			company_id       BIGINT       NOT NULL,
			customer_id      BIGINT       NOT NULL,
			quote_id         BIGINT,
			order_number     VARCHAR(50)  NOT NULL DEFAULT '',
			status           TEXT         NOT NULL DEFAULT 'draft',
			order_date       TIMESTAMPTZ  NOT NULL,
			required_by      TIMESTAMPTZ,
			currency_code    VARCHAR(3)   NOT NULL DEFAULT '',
			subtotal         NUMERIC(18,4) NOT NULL DEFAULT 0,
			tax_total        NUMERIC(18,4) NOT NULL DEFAULT 0,
			total            NUMERIC(18,4) NOT NULL DEFAULT 0,
			invoiced_amount  NUMERIC(18,4) NOT NULL DEFAULT 0,
			notes            TEXT         NOT NULL DEFAULT '',
			memo             TEXT         NOT NULL DEFAULT '',
			confirmed_at     TIMESTAMPTZ,
			created_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
			updated_at       TIMESTAMPTZ  NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sales_orders_company  ON sales_orders(company_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sales_orders_customer ON sales_orders(customer_id)`,

		// sales_order_lines
		`CREATE TABLE IF NOT EXISTS sales_order_lines (
			id                   BIGSERIAL PRIMARY KEY,
			sales_order_id       BIGINT        NOT NULL,
			product_service_id   BIGINT,
			revenue_account_id   BIGINT,
			tax_code_id          BIGINT,
			description          TEXT          NOT NULL DEFAULT '',
			quantity             NUMERIC(18,4) NOT NULL DEFAULT 1,
			unit_price           NUMERIC(18,4) NOT NULL DEFAULT 0,
			line_net             NUMERIC(18,4) NOT NULL DEFAULT 0,
			tax_amount           NUMERIC(18,4) NOT NULL DEFAULT 0,
			line_total           NUMERIC(18,4) NOT NULL DEFAULT 0,
			invoiced_qty         NUMERIC(18,4) NOT NULL DEFAULT 0,
			sort_order           INT           NOT NULL DEFAULT 0,
			created_at           TIMESTAMPTZ   NOT NULL DEFAULT now(),
			updated_at           TIMESTAMPTZ   NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_so_lines_order ON sales_order_lines(sales_order_id)`,

		// customer_deposits
		`CREATE TABLE IF NOT EXISTS customer_deposits (
			id                           BIGSERIAL PRIMARY KEY,
			company_id                   BIGINT       NOT NULL,
			customer_id                  BIGINT       NOT NULL,
			sales_order_id               BIGINT,
			journal_entry_id             BIGINT       UNIQUE,
			bank_account_id              BIGINT,
			deposit_liability_account_id BIGINT,
			deposit_number               VARCHAR(50)  NOT NULL DEFAULT '',
			status                       TEXT         NOT NULL DEFAULT 'draft',
			deposit_date                 TIMESTAMPTZ  NOT NULL,
			currency_code                VARCHAR(3)   NOT NULL DEFAULT '',
			exchange_rate                NUMERIC(18,8) NOT NULL DEFAULT 1,
			amount                       NUMERIC(18,2) NOT NULL DEFAULT 0,
			amount_base                  NUMERIC(18,2) NOT NULL DEFAULT 0,
			balance_remaining            NUMERIC(18,2) NOT NULL DEFAULT 0,
			payment_method               TEXT         NOT NULL DEFAULT 'other',
			reference                    VARCHAR(200) NOT NULL DEFAULT '',
			memo                         TEXT         NOT NULL DEFAULT '',
			posted_at                    TIMESTAMPTZ,
			posted_by                    VARCHAR(200) NOT NULL DEFAULT '',
			posted_by_user_id            UUID,
			created_at                   TIMESTAMPTZ  NOT NULL DEFAULT now(),
			updated_at                   TIMESTAMPTZ  NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_deposits_company  ON customer_deposits(company_id)`,
		`CREATE INDEX IF NOT EXISTS idx_deposits_customer ON customer_deposits(customer_id)`,

		// customer_deposit_applications
		`CREATE TABLE IF NOT EXISTS customer_deposit_applications (
			id                   BIGSERIAL PRIMARY KEY,
			company_id           BIGINT        NOT NULL,
			customer_deposit_id  BIGINT        NOT NULL,
			invoice_id           BIGINT        NOT NULL,
			amount_applied       NUMERIC(18,2) NOT NULL DEFAULT 0,
			amount_applied_base  NUMERIC(18,2) NOT NULL DEFAULT 0,
			applied_at           TIMESTAMPTZ   NOT NULL,
			applied_by           VARCHAR(200)  NOT NULL DEFAULT '',
			created_at           TIMESTAMPTZ   NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_dep_app_company  ON customer_deposit_applications(company_id)`,
		`CREATE INDEX IF NOT EXISTS idx_dep_app_deposit  ON customer_deposit_applications(customer_deposit_id)`,
		`CREATE INDEX IF NOT EXISTS idx_dep_app_invoice  ON customer_deposit_applications(invoice_id)`,

		// customer_receipts
		`CREATE TABLE IF NOT EXISTS customer_receipts (
			id                    BIGSERIAL PRIMARY KEY,
			company_id            BIGINT       NOT NULL,
			customer_id           BIGINT       NOT NULL,
			journal_entry_id      BIGINT       UNIQUE,
			bank_account_id       BIGINT,
			receipt_number        VARCHAR(50)  NOT NULL DEFAULT '',
			status                TEXT         NOT NULL DEFAULT 'draft',
			receipt_date          TIMESTAMPTZ  NOT NULL,
			currency_code         VARCHAR(3)   NOT NULL DEFAULT '',
			exchange_rate         NUMERIC(18,8) NOT NULL DEFAULT 1,
			amount                NUMERIC(18,2) NOT NULL DEFAULT 0,
			amount_base           NUMERIC(18,2) NOT NULL DEFAULT 0,
			unapplied_amount      NUMERIC(18,2) NOT NULL DEFAULT 0,
			payment_method        TEXT         NOT NULL DEFAULT 'other',
			reference             VARCHAR(200) NOT NULL DEFAULT '',
			memo                  TEXT         NOT NULL DEFAULT '',
			gateway_transaction_id BIGINT,
			confirmed_at          TIMESTAMPTZ,
			confirmed_by          VARCHAR(200) NOT NULL DEFAULT '',
			confirmed_by_user_id  UUID,
			created_at            TIMESTAMPTZ  NOT NULL DEFAULT now(),
			updated_at            TIMESTAMPTZ  NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_receipts_company  ON customer_receipts(company_id)`,
		`CREATE INDEX IF NOT EXISTS idx_receipts_customer ON customer_receipts(customer_id)`,

		// payment_applications (AR open-item matching records)
		`CREATE TABLE IF NOT EXISTS payment_applications (
			id                    BIGSERIAL PRIMARY KEY,
			company_id            BIGINT        NOT NULL,
			source_type           TEXT          NOT NULL,
			customer_receipt_id   BIGINT,
			customer_deposit_id   BIGINT,
			invoice_id            BIGINT        NOT NULL,
			status                TEXT          NOT NULL DEFAULT 'active',
			amount_applied        NUMERIC(18,2) NOT NULL DEFAULT 0,
			amount_applied_base   NUMERIC(18,2) NOT NULL DEFAULT 0,
			applied_at            TIMESTAMPTZ   NOT NULL,
			applied_by            VARCHAR(200)  NOT NULL DEFAULT '',
			reversed_at           TIMESTAMPTZ,
			reversed_by           VARCHAR(200)  NOT NULL DEFAULT '',
			created_at            TIMESTAMPTZ   NOT NULL DEFAULT now(),
			updated_at            TIMESTAMPTZ   NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_pay_app_company   ON payment_applications(company_id)`,
		`CREATE INDEX IF NOT EXISTS idx_pay_app_receipt   ON payment_applications(customer_receipt_id)`,
		`CREATE INDEX IF NOT EXISTS idx_pay_app_deposit   ON payment_applications(customer_deposit_id)`,
		`CREATE INDEX IF NOT EXISTS idx_pay_app_invoice   ON payment_applications(invoice_id)`,

		// ar_returns
		`CREATE TABLE IF NOT EXISTS ar_returns (
			id               BIGSERIAL PRIMARY KEY,
			company_id       BIGINT       NOT NULL,
			customer_id      BIGINT       NOT NULL,
			invoice_id       BIGINT       NOT NULL,
			credit_note_id   BIGINT,
			return_number    VARCHAR(50)  NOT NULL DEFAULT '',
			status           TEXT         NOT NULL DEFAULT 'draft',
			return_date      TIMESTAMPTZ  NOT NULL,
			reason           TEXT         NOT NULL DEFAULT 'other',
			description      TEXT         NOT NULL DEFAULT '',
			currency_code    VARCHAR(3)   NOT NULL DEFAULT '',
			return_amount    NUMERIC(18,2) NOT NULL DEFAULT 0,
			approved_at      TIMESTAMPTZ,
			approved_by      VARCHAR(200) NOT NULL DEFAULT '',
			processed_at     TIMESTAMPTZ,
			created_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
			updated_at       TIMESTAMPTZ  NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ar_returns_company  ON ar_returns(company_id)`,
		`CREATE INDEX IF NOT EXISTS idx_ar_returns_customer ON ar_returns(customer_id)`,
		`CREATE INDEX IF NOT EXISTS idx_ar_returns_invoice  ON ar_returns(invoice_id)`,

		// ar_refunds
		`CREATE TABLE IF NOT EXISTS ar_refunds (
			id                    BIGSERIAL PRIMARY KEY,
			company_id            BIGINT       NOT NULL,
			customer_id           BIGINT       NOT NULL,
			journal_entry_id      BIGINT       UNIQUE,
			bank_account_id       BIGINT,
			source_type           TEXT         NOT NULL DEFAULT 'other',
			customer_deposit_id   BIGINT,
			customer_receipt_id   BIGINT,
			credit_note_id        BIGINT,
			ar_return_id          BIGINT,
			refund_number         VARCHAR(50)  NOT NULL DEFAULT '',
			status                TEXT         NOT NULL DEFAULT 'draft',
			refund_date           TIMESTAMPTZ  NOT NULL,
			currency_code         VARCHAR(3)   NOT NULL DEFAULT '',
			exchange_rate         NUMERIC(18,8) NOT NULL DEFAULT 1,
			amount                NUMERIC(18,2) NOT NULL DEFAULT 0,
			amount_base           NUMERIC(18,2) NOT NULL DEFAULT 0,
			payment_method        TEXT         NOT NULL DEFAULT 'other',
			reference             VARCHAR(200) NOT NULL DEFAULT '',
			memo                  TEXT         NOT NULL DEFAULT '',
			posted_at             TIMESTAMPTZ,
			posted_by             VARCHAR(200) NOT NULL DEFAULT '',
			posted_by_user_id     UUID,
			created_at            TIMESTAMPTZ  NOT NULL DEFAULT now(),
			updated_at            TIMESTAMPTZ  NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ar_refunds_company  ON ar_refunds(company_id)`,
		`CREATE INDEX IF NOT EXISTS idx_ar_refunds_customer ON ar_refunds(customer_id)`,
	}
	for i, stmt := range stmts {
		if err := db.Exec(stmt).Error; err != nil {
			if strings.Contains(err.Error(), "42P01") ||
				strings.Contains(err.Error(), "already exists") {
				continue
			}
			return fmt.Errorf("migratePhase13 step %d: %w", i+1, err)
		}
	}
	return nil
}
