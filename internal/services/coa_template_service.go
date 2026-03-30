// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"

	"gobooks/internal/models"

	"gorm.io/gorm"
)

// ErrAccountsInUse is returned by ResetCompanyCOA when the company has journal
// lines that reference existing accounts, making a hard reset unsafe.
var ErrAccountsInUse = errors.New("company accounts are referenced by journal entries; reset is not safe")

// defaultTemplateAccounts defines the comprehensive default Chart of Accounts
// used when creating a new company. All codes are 4-digit base codes
// (expanded to the company's configured account code length at import time).
//
// Prefix rules enforced by ValidateAccountCodePrefixForRoot:
//   1xxx = Asset, 2xxx = Liability, 3xxx = Equity,
//   4xxx = Revenue, 5xxx = Cost of Sales, 6xxx = Expense
var defaultTemplateAccounts = []models.COATemplateAccount{
	// ── Assets (1xxx) ──────────────────────────────────────────────────────
	{AccountCode: "1000", Name: "Cash", RootAccountType: models.RootAsset, DetailAccountType: models.DetailOtherCurrentAsset, NormalBalance: models.NormalBalanceDebit, SortOrder: 10},
	{AccountCode: "1010", Name: "Petty Cash", RootAccountType: models.RootAsset, DetailAccountType: models.DetailOtherCurrentAsset, NormalBalance: models.NormalBalanceDebit, SortOrder: 20},
	{AccountCode: "1100", Name: "Bank - Operating", RootAccountType: models.RootAsset, DetailAccountType: models.DetailBank, NormalBalance: models.NormalBalanceDebit, SortOrder: 30},
	{AccountCode: "1110", Name: "Bank - Savings", RootAccountType: models.RootAsset, DetailAccountType: models.DetailBank, NormalBalance: models.NormalBalanceDebit, SortOrder: 40},
	{AccountCode: "1200", Name: "Accounts Receivable", RootAccountType: models.RootAsset, DetailAccountType: models.DetailAccountsReceivable, NormalBalance: models.NormalBalanceDebit, SortOrder: 50},
	{AccountCode: "1210", Name: "Allowance for Doubtful Accounts", RootAccountType: models.RootAsset, DetailAccountType: models.DetailAccountsReceivable, NormalBalance: models.NormalBalanceCredit, SortOrder: 60},
	{AccountCode: "1300", Name: "Inventory", RootAccountType: models.RootAsset, DetailAccountType: models.DetailInventory, NormalBalance: models.NormalBalanceDebit, SortOrder: 70},
	{AccountCode: "1400", Name: "Prepaid Expenses", RootAccountType: models.RootAsset, DetailAccountType: models.DetailPrepaidExpense, NormalBalance: models.NormalBalanceDebit, SortOrder: 80},
	{AccountCode: "1500", Name: "Equipment", RootAccountType: models.RootAsset, DetailAccountType: models.DetailFixedAsset, NormalBalance: models.NormalBalanceDebit, SortOrder: 90},
	{AccountCode: "1510", Name: "Accumulated Amortization - Equipment", RootAccountType: models.RootAsset, DetailAccountType: models.DetailFixedAsset, NormalBalance: models.NormalBalanceCredit, SortOrder: 100},
	{AccountCode: "1520", Name: "Furniture & Fixtures", RootAccountType: models.RootAsset, DetailAccountType: models.DetailFixedAsset, NormalBalance: models.NormalBalanceDebit, SortOrder: 110},
	{AccountCode: "1530", Name: "Accumulated Amortization - Furniture", RootAccountType: models.RootAsset, DetailAccountType: models.DetailFixedAsset, NormalBalance: models.NormalBalanceCredit, SortOrder: 120},
	{AccountCode: "1600", Name: "Leasehold Improvements", RootAccountType: models.RootAsset, DetailAccountType: models.DetailFixedAsset, NormalBalance: models.NormalBalanceDebit, SortOrder: 130},
	{AccountCode: "1610", Name: "Accumulated Amortization - Leasehold", RootAccountType: models.RootAsset, DetailAccountType: models.DetailFixedAsset, NormalBalance: models.NormalBalanceCredit, SortOrder: 140},
	{AccountCode: "1900", Name: "Other Assets", RootAccountType: models.RootAsset, DetailAccountType: models.DetailOtherAsset, NormalBalance: models.NormalBalanceDebit, SortOrder: 150},

	// ── Liabilities (2xxx) ─────────────────────────────────────────────────
	{AccountCode: "2000", Name: "Accounts Payable", RootAccountType: models.RootLiability, DetailAccountType: models.DetailAccountsPayable, NormalBalance: models.NormalBalanceCredit, SortOrder: 210},
	{AccountCode: "2100", Name: "GST/HST Payable", RootAccountType: models.RootLiability, DetailAccountType: models.DetailSalesTaxPayable, NormalBalance: models.NormalBalanceCredit, SortOrder: 220},
	{AccountCode: "2200", Name: "Payroll Liabilities", RootAccountType: models.RootLiability, DetailAccountType: models.DetailPayrollLiability, NormalBalance: models.NormalBalanceCredit, SortOrder: 230},
	{AccountCode: "2210", Name: "CPP Payable", RootAccountType: models.RootLiability, DetailAccountType: models.DetailPayrollLiability, NormalBalance: models.NormalBalanceCredit, SortOrder: 240},
	{AccountCode: "2220", Name: "EI Payable", RootAccountType: models.RootLiability, DetailAccountType: models.DetailPayrollLiability, NormalBalance: models.NormalBalanceCredit, SortOrder: 250},
	{AccountCode: "2230", Name: "Income Tax Withheld Payable", RootAccountType: models.RootLiability, DetailAccountType: models.DetailPayrollLiability, NormalBalance: models.NormalBalanceCredit, SortOrder: 260},
	{AccountCode: "2300", Name: "Credit Card Payable", RootAccountType: models.RootLiability, DetailAccountType: models.DetailCreditCard, NormalBalance: models.NormalBalanceCredit, SortOrder: 270},
	{AccountCode: "2400", Name: "Accrued Liabilities", RootAccountType: models.RootLiability, DetailAccountType: models.DetailOtherCurrentLiability, NormalBalance: models.NormalBalanceCredit, SortOrder: 280},
	{AccountCode: "2500", Name: "Deferred Revenue", RootAccountType: models.RootLiability, DetailAccountType: models.DetailOtherCurrentLiability, NormalBalance: models.NormalBalanceCredit, SortOrder: 290},
	{AccountCode: "2700", Name: "Long-Term Loan Payable", RootAccountType: models.RootLiability, DetailAccountType: models.DetailLongTermLiability, NormalBalance: models.NormalBalanceCredit, SortOrder: 300},

	// ── Equity (3xxx) ──────────────────────────────────────────────────────
	{AccountCode: "3000", Name: "Share Capital", RootAccountType: models.RootEquity, DetailAccountType: models.DetailShareCapital, NormalBalance: models.NormalBalanceCredit, SortOrder: 310},
	{AccountCode: "3100", Name: "Retained Earnings", RootAccountType: models.RootEquity, DetailAccountType: models.DetailRetainedEarnings, NormalBalance: models.NormalBalanceCredit, SortOrder: 320},
	{AccountCode: "3200", Name: "Owner's Drawings", RootAccountType: models.RootEquity, DetailAccountType: models.DetailOwnerDrawings, NormalBalance: models.NormalBalanceDebit, SortOrder: 330},
	{AccountCode: "3300", Name: "Owner's Contributions", RootAccountType: models.RootEquity, DetailAccountType: models.DetailOwnerContribution, NormalBalance: models.NormalBalanceCredit, SortOrder: 340},

	// ── Revenue (4xxx) ─────────────────────────────────────────────────────
	{AccountCode: "4000", Name: "Sales Revenue", RootAccountType: models.RootRevenue, DetailAccountType: models.DetailSalesRevenue, NormalBalance: models.NormalBalanceCredit, SortOrder: 410},
	{AccountCode: "4100", Name: "Service Revenue", RootAccountType: models.RootRevenue, DetailAccountType: models.DetailServiceRevenue, NormalBalance: models.NormalBalanceCredit, SortOrder: 420},
	{AccountCode: "4200", Name: "Other Income", RootAccountType: models.RootRevenue, DetailAccountType: models.DetailOtherIncome, NormalBalance: models.NormalBalanceCredit, SortOrder: 430},

	// ── Cost of Sales (5xxx) ───────────────────────────────────────────────
	{AccountCode: "5000", Name: "Cost of Goods Sold", RootAccountType: models.RootCostOfSales, DetailAccountType: models.DetailCostOfGoodsSold, NormalBalance: models.NormalBalanceDebit, SortOrder: 510},
	{AccountCode: "5100", Name: "Direct Labour", RootAccountType: models.RootCostOfSales, DetailAccountType: models.DetailCostOfGoodsSold, NormalBalance: models.NormalBalanceDebit, SortOrder: 520},
	{AccountCode: "5200", Name: "Manufacturing Overhead", RootAccountType: models.RootCostOfSales, DetailAccountType: models.DetailCostOfGoodsSold, NormalBalance: models.NormalBalanceDebit, SortOrder: 530},

	// ── Expenses (6xxx) ────────────────────────────────────────────────────
	{AccountCode: "6000", Name: "Salaries & Wages Expense", RootAccountType: models.RootExpense, DetailAccountType: models.DetailPayrollExpense, NormalBalance: models.NormalBalanceDebit, SortOrder: 610},
	{AccountCode: "6010", Name: "Employee Benefits Expense", RootAccountType: models.RootExpense, DetailAccountType: models.DetailPayrollExpense, NormalBalance: models.NormalBalanceDebit, SortOrder: 620},
	{AccountCode: "6100", Name: "Rent Expense", RootAccountType: models.RootExpense, DetailAccountType: models.DetailRentExpense, NormalBalance: models.NormalBalanceDebit, SortOrder: 630},
	{AccountCode: "6200", Name: "Utilities Expense", RootAccountType: models.RootExpense, DetailAccountType: models.DetailUtilitiesExpense, NormalBalance: models.NormalBalanceDebit, SortOrder: 640},
	{AccountCode: "6210", Name: "Telephone & Internet Expense", RootAccountType: models.RootExpense, DetailAccountType: models.DetailUtilitiesExpense, NormalBalance: models.NormalBalanceDebit, SortOrder: 650},
	{AccountCode: "6300", Name: "Office Supplies Expense", RootAccountType: models.RootExpense, DetailAccountType: models.DetailOfficeExpense, NormalBalance: models.NormalBalanceDebit, SortOrder: 660},
	{AccountCode: "6310", Name: "Postage & Delivery Expense", RootAccountType: models.RootExpense, DetailAccountType: models.DetailOfficeExpense, NormalBalance: models.NormalBalanceDebit, SortOrder: 670},
	{AccountCode: "6400", Name: "Advertising & Marketing Expense", RootAccountType: models.RootExpense, DetailAccountType: models.DetailAdvertisingExpense, NormalBalance: models.NormalBalanceDebit, SortOrder: 680},
	{AccountCode: "6500", Name: "Professional Fees Expense", RootAccountType: models.RootExpense, DetailAccountType: models.DetailProfessionalFees, NormalBalance: models.NormalBalanceDebit, SortOrder: 690},
	{AccountCode: "6510", Name: "Accounting & Audit Fees", RootAccountType: models.RootExpense, DetailAccountType: models.DetailProfessionalFees, NormalBalance: models.NormalBalanceDebit, SortOrder: 700},
	{AccountCode: "6520", Name: "Legal Fees Expense", RootAccountType: models.RootExpense, DetailAccountType: models.DetailProfessionalFees, NormalBalance: models.NormalBalanceDebit, SortOrder: 710},
	{AccountCode: "6600", Name: "Bank Charges & Interest Expense", RootAccountType: models.RootExpense, DetailAccountType: models.DetailBankCharges, NormalBalance: models.NormalBalanceDebit, SortOrder: 720},
	{AccountCode: "6700", Name: "Insurance Expense", RootAccountType: models.RootExpense, DetailAccountType: models.DetailInsuranceExpense, NormalBalance: models.NormalBalanceDebit, SortOrder: 730},
	{AccountCode: "6800", Name: "Depreciation & Amortization Expense", RootAccountType: models.RootExpense, DetailAccountType: models.DetailOtherExpense, NormalBalance: models.NormalBalanceDebit, SortOrder: 740},
	{AccountCode: "6900", Name: "General & Administrative Expense", RootAccountType: models.RootExpense, DetailAccountType: models.DetailOperatingExpense, NormalBalance: models.NormalBalanceDebit, SortOrder: 750},
	{AccountCode: "6910", Name: "Travel & Entertainment Expense", RootAccountType: models.RootExpense, DetailAccountType: models.DetailOtherExpense, NormalBalance: models.NormalBalanceDebit, SortOrder: 760},
	{AccountCode: "6920", Name: "Vehicle Expense", RootAccountType: models.RootExpense, DetailAccountType: models.DetailOtherExpense, NormalBalance: models.NormalBalanceDebit, SortOrder: 770},
}

const defaultTemplateName = "Canadian Default"

// SeedDefaultCOATemplate ensures the default COA template exists in the database.
// Idempotent: if a template with is_default=true already exists, it is a no-op.
// Called once at startup from db.Migrate.
func SeedDefaultCOATemplate(db *gorm.DB) error {
	var count int64
	if err := db.Model(&models.COATemplate{}).Where("is_default = ?", true).Count(&count).Error; err != nil {
		return fmt.Errorf("coa template seed: count check: %w", err)
	}
	if count > 0 {
		return nil // already seeded
	}

	return db.Transaction(func(tx *gorm.DB) error {
		tmpl := models.COATemplate{
			Name:      defaultTemplateName,
			IsDefault: true,
		}
		if err := tx.Create(&tmpl).Error; err != nil {
			return fmt.Errorf("coa template seed: create template: %w", err)
		}

		rows := make([]models.COATemplateAccount, len(defaultTemplateAccounts))
		for i, r := range defaultTemplateAccounts {
			rows[i] = r
			rows[i].TemplateID = tmpl.ID
		}
		if err := tx.Create(&rows).Error; err != nil {
			return fmt.Errorf("coa template seed: create accounts: %w", err)
		}
		return nil
	})
}

// CreateDefaultAccountsForCompany loads the default COA template from the DB
// and inserts its accounts for the given company. Codes are expanded to codeLength.
//
// Idempotent: codes that already exist for the company are silently skipped,
// so calling this on a company that already has accounts is safe.
// IsSystemDefault is set to true on every inserted account.
func CreateDefaultAccountsForCompany(tx *gorm.DB, companyID uint, codeLength int) error {
	if codeLength < models.AccountCodeLengthMin || codeLength > models.AccountCodeLengthMax {
		return fmt.Errorf("invalid account code length: %d", codeLength)
	}

	// Load the default template.
	var tmpl models.COATemplate
	if err := tx.Where("is_default = ?", true).First(&tmpl).Error; err != nil {
		return fmt.Errorf("CreateDefaultAccountsForCompany: no default template: %w", err)
	}

	// Load template accounts ordered by sort_order.
	var templateAccounts []models.COATemplateAccount
	if err := tx.Where("template_id = ?", tmpl.ID).Order("sort_order asc").Find(&templateAccounts).Error; err != nil {
		return fmt.Errorf("CreateDefaultAccountsForCompany: load template accounts: %w", err)
	}

	// Build a set of codes already present for this company (idempotency).
	var existing []string
	if err := tx.Model(&models.Account{}).Where("company_id = ?", companyID).Pluck("code", &existing).Error; err != nil {
		return fmt.Errorf("CreateDefaultAccountsForCompany: load existing codes: %w", err)
	}
	existingSet := make(map[string]struct{}, len(existing))
	for _, c := range existing {
		existingSet[c] = struct{}{}
	}

	for _, t := range templateAccounts {
		code, err := models.ExpandAccountCodeToLength(t.AccountCode, codeLength)
		if err != nil {
			return fmt.Errorf("CreateDefaultAccountsForCompany: expand code %s: %w", t.AccountCode, err)
		}
		if err := models.ValidateAccountCodeAndClassification(code, codeLength, t.RootAccountType); err != nil {
			return fmt.Errorf("CreateDefaultAccountsForCompany: validate %s (%s): %w", t.AccountCode, t.Name, err)
		}
		if err := models.ValidateRootDetail(t.RootAccountType, t.DetailAccountType); err != nil {
			return fmt.Errorf("CreateDefaultAccountsForCompany: classify %s: %w", t.AccountCode, err)
		}
		if _, found := existingSet[code]; found {
			continue
		}

		acc := models.Account{
			CompanyID:         companyID,
			Code:              code,
			Name:              t.Name,
			RootAccountType:   t.RootAccountType,
			DetailAccountType: t.DetailAccountType,
			IsActive:          true,
			IsSystemDefault:   true,
		}
		if err := tx.Create(&acc).Error; err != nil {
			return fmt.Errorf("CreateDefaultAccountsForCompany: insert %s: %w", code, err)
		}
		existingSet[code] = struct{}{}
	}

	return nil
}

// SyncDefaultAccountsForCompany reconciles a company's Chart of Accounts against
// the default template. It is safe to call on companies that already have
// transactions — it never deletes accounts.
//
// For each template account (identified by its 4-digit base code expanded to
// codeLength):
//   - Missing code → INSERT as is_system_default = true.
//   - Existing code + is_system_default = true → UPDATE Name, RootAccountType,
//     DetailAccountType to match the template (name/type are display-only, no FK
//     dependencies).
//   - Existing code + is_system_default = false (user-created) → skip entirely.
//
// Returns (added, updated, error).
func SyncDefaultAccountsForCompany(db *gorm.DB, companyID uint, codeLength int) (added int, updated int, err error) {
	if codeLength < models.AccountCodeLengthMin || codeLength > models.AccountCodeLengthMax {
		return 0, 0, fmt.Errorf("invalid account code length: %d", codeLength)
	}

	var tmpl models.COATemplate
	if err := db.Where("is_default = ?", true).First(&tmpl).Error; err != nil {
		return 0, 0, fmt.Errorf("SyncDefaultAccountsForCompany: no default template: %w", err)
	}

	var templateAccounts []models.COATemplateAccount
	if err := db.Where("template_id = ?", tmpl.ID).Order("sort_order asc").Find(&templateAccounts).Error; err != nil {
		return 0, 0, fmt.Errorf("SyncDefaultAccountsForCompany: load template accounts: %w", err)
	}

	// Load all existing accounts for this company as a map[code]Account.
	var existing []models.Account
	if err := db.Where("company_id = ?", companyID).Find(&existing).Error; err != nil {
		return 0, 0, fmt.Errorf("SyncDefaultAccountsForCompany: load existing accounts: %w", err)
	}
	existingByCode := make(map[string]*models.Account, len(existing))
	for i := range existing {
		existingByCode[existing[i].Code] = &existing[i]
	}

	for _, t := range templateAccounts {
		code, err := models.ExpandAccountCodeToLength(t.AccountCode, codeLength)
		if err != nil {
			return added, updated, fmt.Errorf("SyncDefaultAccountsForCompany: expand %s: %w", t.AccountCode, err)
		}
		if err := models.ValidateAccountCodeAndClassification(code, codeLength, t.RootAccountType); err != nil {
			return added, updated, fmt.Errorf("SyncDefaultAccountsForCompany: validate %s: %w", t.AccountCode, err)
		}
		if err := models.ValidateRootDetail(t.RootAccountType, t.DetailAccountType); err != nil {
			return added, updated, fmt.Errorf("SyncDefaultAccountsForCompany: classify %s: %w", t.AccountCode, err)
		}

		if acc, found := existingByCode[code]; found {
			// Only update accounts we originally generated from a template.
			if !acc.IsSystemDefault {
				continue
			}
			// Update only if something actually changed.
			if acc.Name == t.Name && acc.RootAccountType == t.RootAccountType && acc.DetailAccountType == t.DetailAccountType {
				continue
			}
			if err := db.Model(acc).Updates(map[string]any{
				"name":                t.Name,
				"root_account_type":   t.RootAccountType,
				"detail_account_type": t.DetailAccountType,
			}).Error; err != nil {
				return added, updated, fmt.Errorf("SyncDefaultAccountsForCompany: update %s: %w", code, err)
			}
			updated++
		} else {
			// Insert missing account.
			newAcc := models.Account{
				CompanyID:         companyID,
				Code:              code,
				Name:              t.Name,
				RootAccountType:   t.RootAccountType,
				DetailAccountType: t.DetailAccountType,
				IsActive:          true,
				IsSystemDefault:   true,
			}
			if err := db.Create(&newAcc).Error; err != nil {
				return added, updated, fmt.Errorf("SyncDefaultAccountsForCompany: insert %s: %w", code, err)
			}
			existingByCode[code] = &newAcc
			added++
		}
	}

	return added, updated, nil
}

// ResetCompanyCOA performs a hard reset of a company's Chart of Accounts:
// it deletes ALL existing accounts and re-imports from the default template.
//
// Safety check: returns ErrAccountsInUse if any journal line references an
// account belonging to this company. In that case use SyncDefaultAccountsForCompany
// instead, which never deletes data.
//
// Must be called inside a transaction for atomicity.
func ResetCompanyCOA(tx *gorm.DB, companyID uint, codeLength int) error {
	if codeLength < models.AccountCodeLengthMin || codeLength > models.AccountCodeLengthMax {
		return fmt.Errorf("invalid account code length: %d", codeLength)
	}

	// Safety: reject if any journal line is tied to an account in this company.
	var refCount int64
	if err := tx.Table("journal_lines").
		Joins("JOIN accounts ON accounts.id = journal_lines.account_id").
		Where("accounts.company_id = ?", companyID).
		Count(&refCount).Error; err != nil {
		return fmt.Errorf("ResetCompanyCOA: check references: %w", err)
	}
	if refCount > 0 {
		return ErrAccountsInUse
	}

	// Delete all existing accounts for this company.
	if err := tx.Where("company_id = ?", companyID).Delete(&models.Account{}).Error; err != nil {
		return fmt.Errorf("ResetCompanyCOA: delete accounts: %w", err)
	}

	// Re-import from the default template.
	return CreateDefaultAccountsForCompany(tx, companyID, codeLength)
}
