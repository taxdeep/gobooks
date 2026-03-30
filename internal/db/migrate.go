// 遵循project_guide.md
package db

import (
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
	// Historical databases may contain stale optional foreign keys that predate
	// current constraints. Null them before AutoMigrate adds FK constraints.
	if err := clearOptionalForeignKeyOrphans(db); err != nil {
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
	); err != nil {
		return err
	}
	if err := ensureCompanyAccountCodeDefaults(db); err != nil {
		return err
	}
	return ensureDocumentNumberIndexes(db)
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
