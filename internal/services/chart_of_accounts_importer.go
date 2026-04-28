// 遵循project_guide.md
package services

import (
	"fmt"

	"balanciz/internal/models"

	"gorm.io/gorm"
)

// AccountTemplate is a simple shape for default Chart of Accounts entries.
type AccountTemplate struct {
	Code   string
	Name   string
	Root   models.RootAccountType
	Detail models.DetailAccountType
}

// Default account templates keyed by (entity_type, business_type).
var defaultCOA = map[string][]AccountTemplate{
	key(models.EntityTypePersonal, models.BusinessTypeRetail): {
		{Code: "1000", Name: "Cash", Root: models.RootAsset, Detail: models.DetailOtherCurrentAsset},
		{Code: "1100", Name: "Bank", Root: models.RootAsset, Detail: models.DetailBank},
		{Code: "2000", Name: "Credit Card Payable", Root: models.RootLiability, Detail: models.DetailCreditCard},
		{Code: "3000", Name: "Owner's Equity", Root: models.RootEquity, Detail: models.DetailOtherEquity},
		{Code: "4000", Name: "Sales Revenue", Root: models.RootRevenue, Detail: models.DetailOperatingRevenue},
		{Code: "6000", Name: "General Expenses", Root: models.RootExpense, Detail: models.DetailOperatingExpense},
	},
	key(models.EntityTypeIncorporated, models.BusinessTypeRetail): {
		{Code: "1000", Name: "Cash", Root: models.RootAsset, Detail: models.DetailOtherCurrentAsset},
		{Code: "1100", Name: "Bank - Operating", Root: models.RootAsset, Detail: models.DetailBank},
		{Code: "1200", Name: "Inventory", Root: models.RootAsset, Detail: models.DetailInventory},
		{Code: "2100", Name: "Accounts Payable", Root: models.RootLiability, Detail: models.DetailAccountsPayable},
		{Code: "3000", Name: "Share Capital", Root: models.RootEquity, Detail: models.DetailShareCapital},
		{Code: "3100", Name: "Retained Earnings", Root: models.RootEquity, Detail: models.DetailRetainedEarnings},
		{Code: "4000", Name: "Sales Revenue", Root: models.RootRevenue, Detail: models.DetailSalesRevenue},
		{Code: "5000", Name: "Cost of Goods Sold", Root: models.RootCostOfSales, Detail: models.DetailCostOfGoodsSold},
		{Code: "6100", Name: "Rent Expense", Root: models.RootExpense, Detail: models.DetailRentExpense},
	},
	key(models.EntityTypeIncorporated, models.BusinessTypeProfessionalCorp): {
		{Code: "1000", Name: "Cash", Root: models.RootAsset, Detail: models.DetailOtherCurrentAsset},
		{Code: "1100", Name: "Bank - Operating", Root: models.RootAsset, Detail: models.DetailBank},
		{Code: "1200", Name: "Accounts Receivable", Root: models.RootAsset, Detail: models.DetailAccountsReceivable},
		{Code: "2100", Name: "Accounts Payable", Root: models.RootLiability, Detail: models.DetailAccountsPayable},
		{Code: "2200", Name: "Payroll Liabilities", Root: models.RootLiability, Detail: models.DetailPayrollLiability},
		{Code: "3000", Name: "Share Capital", Root: models.RootEquity, Detail: models.DetailShareCapital},
		{Code: "3100", Name: "Retained Earnings", Root: models.RootEquity, Detail: models.DetailRetainedEarnings},
		{Code: "4000", Name: "Professional Fees Revenue", Root: models.RootRevenue, Detail: models.DetailServiceRevenue},
		{Code: "6100", Name: "Salaries Expense", Root: models.RootExpense, Detail: models.DetailPayrollExpense},
		{Code: "6200", Name: "Office Rent Expense", Root: models.RootExpense, Detail: models.DetailRentExpense},
	},
	key(models.EntityTypeLLP, models.BusinessTypeProfessionalCorp): {
		{Code: "1000", Name: "Cash", Root: models.RootAsset, Detail: models.DetailOtherCurrentAsset},
		{Code: "1100", Name: "Bank - Operating", Root: models.RootAsset, Detail: models.DetailBank},
		{Code: "1200", Name: "Accounts Receivable", Root: models.RootAsset, Detail: models.DetailAccountsReceivable},
		{Code: "2100", Name: "Accounts Payable", Root: models.RootLiability, Detail: models.DetailAccountsPayable},
		{Code: "3000", Name: "Partners' Capital", Root: models.RootEquity, Detail: models.DetailShareCapital},
		{Code: "3100", Name: "Partners' Drawings", Root: models.RootEquity, Detail: models.DetailOwnerDrawings},
		{Code: "4000", Name: "Professional Fees Revenue", Root: models.RootRevenue, Detail: models.DetailServiceRevenue},
		{Code: "6100", Name: "Salaries Expense", Root: models.RootExpense, Detail: models.DetailPayrollExpense},
		{Code: "6200", Name: "Office Rent Expense", Root: models.RootExpense, Detail: models.DetailRentExpense},
	},
}

func key(entity models.EntityType, business models.BusinessType) string {
	return fmt.Sprintf("%s|%s", entity, business)
}

// ImportDefaultChartOfAccounts inserts default accounts for a company.
func ImportDefaultChartOfAccounts(tx *gorm.DB, companyID uint, entity models.EntityType, business models.BusinessType, codeLength int) error {
	if codeLength < models.AccountCodeLengthMin || codeLength > models.AccountCodeLengthMax {
		return fmt.Errorf("invalid account code length: %d", codeLength)
	}
	templates, ok := defaultCOA[key(entity, business)]
	if !ok {
		return nil
	}

	var existing []string
	if err := tx.Model(&models.Account{}).Where("company_id = ?", companyID).Pluck("code", &existing).Error; err != nil {
		return err
	}
	existingSet := make(map[string]struct{}, len(existing))
	for _, c := range existing {
		existingSet[c] = struct{}{}
	}

	for _, t := range templates {
		code, err := models.ExpandAccountCodeToLength(t.Code, codeLength)
		if err != nil {
			return err
		}
		if err := models.ValidateAccountCodeAndClassification(code, codeLength, t.Root); err != nil {
			return fmt.Errorf("default COA row %s (%s): %w", t.Code, t.Name, err)
		}
		if err := models.ValidateRootDetail(t.Root, t.Detail); err != nil {
			return fmt.Errorf("default COA row %s: %w", t.Code, err)
		}
		if _, found := existingSet[code]; found {
			continue
		}

		acc := models.Account{
			CompanyID:         companyID,
			Code:              code,
			Name:              t.Name,
			RootAccountType:   t.Root,
			DetailAccountType: t.Detail,
			IsActive:          true,
		}

		if err := tx.Create(&acc).Error; err != nil {
			return err
		}

		existingSet[code] = struct{}{}
	}

	return nil
}
