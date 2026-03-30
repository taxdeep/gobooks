// 遵循project_guide.md
package models

import (
	"fmt"
	"strings"
	"time"
)

// Account code length is configured per company (4–12). Default 4.
// Template COA rows use 4-digit base codes; longer lengths pad right with zeros.
const (
	AccountCodeLengthMin      = 4
	AccountCodeLengthMax      = 12
	TemplateAccountCodeDigits = 4
)

// ValidateAccountCodeStrict enforces company-configured length, numeric-only, no leading zero, > 0.
// Empty code returns nil (caller enforces "required").
func ValidateAccountCodeStrict(code string, companyLength int) error {
	if code == "" {
		return nil
	}
	if companyLength < AccountCodeLengthMin || companyLength > AccountCodeLengthMax {
		return fmt.Errorf("invalid account code length configuration.")
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return fmt.Errorf("%s", accountCodeFormatError(companyLength))
		}
	}
	if len(code) != companyLength {
		return fmt.Errorf("Account code must be exactly %d digits.", companyLength)
	}
	if code[0] == '0' {
		return fmt.Errorf("%s", accountCodeFormatError(companyLength))
	}
	return nil
}

// ValidateGifiCode checks optional CRA GIFI mapping (4 digits). Empty/whitespace is allowed.
func ValidateGifiCode(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if len(s) != 4 {
		return fmt.Errorf("GIFI code must be exactly 4 digits or left empty.")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return fmt.Errorf("GIFI code must contain digits only.")
		}
	}
	return nil
}

func accountCodeFormatError(companyLength int) string {
	return fmt.Sprintf(
		"Account code must be a %d-digit positive integer and cannot start with 0.",
		companyLength,
	)
}

// ExpandAccountCodeToLength pads a 4-digit template code (e.g. 1000) to the target length
// by appending zeros on the right: 1000→10000 (5), 1000→100000 (6).
func ExpandAccountCodeToLength(baseCode string, targetLen int) (string, error) {
	if targetLen < AccountCodeLengthMin || targetLen > AccountCodeLengthMax {
		return "", fmt.Errorf("invalid target length")
	}
	if len(baseCode) != TemplateAccountCodeDigits {
		return "", fmt.Errorf("template account code must be %d digits", TemplateAccountCodeDigits)
	}
	for _, r := range baseCode {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("template account code must be numeric")
		}
	}
	if baseCode[0] == '0' {
		return "", fmt.Errorf("template account code cannot start with 0")
	}
	if targetLen < len(baseCode) {
		return "", fmt.Errorf("account code length cannot be shorter than %d", len(baseCode))
	}
	extra := targetLen - len(baseCode)
	return baseCode + strings.Repeat("0", extra), nil
}

type AccountReportGroup string

const (
	AccountReportGroupAsset           AccountReportGroup = "Asset"
	AccountReportGroupLiability       AccountReportGroup = "Liability"
	AccountReportGroupEquity          AccountReportGroup = "Equity"
	AccountReportGroupIncome          AccountReportGroup = "Income"
	AccountReportGroupCostOfGoodsSold AccountReportGroup = "Cost of Goods Sold"
	AccountReportGroupExpense         AccountReportGroup = "Expense"
)

// Account is one row in the Chart of Accounts (scoped to one company).
// Uniqueness of Code is per-company (see migration uq_accounts_company_code).
type Account struct {
	ID        uint   `gorm:"primaryKey"`
	CompanyID uint   `gorm:"not null;index;uniqueIndex:uq_accounts_company_code"`
	Code      string `gorm:"not null;uniqueIndex:uq_accounts_company_code"`
	Name      string `gorm:"not null"`
	// RootAccountType and DetailAccountType replace the legacy single-type column.
	RootAccountType   RootAccountType   `gorm:"column:root_account_type;type:text;not null"`
	DetailAccountType DetailAccountType `gorm:"column:detail_account_type;type:text;not null"`
	// IsActive is false when the account is retired from the chart for new transactions;
	// historical journal lines remain tied to this account.
	IsActive bool `gorm:"not null;default:true"`
	// GifiCode optional 4-digit CRA GIFI mapping; not used as identity.
	GifiCode string `gorm:"size:4;default:''"`
	// FieldRecommendationSourcesJSON is optional client-reported analytics (see account_recommendation_sources.go).
	// Not used for validation. Null for legacy rows.
	FieldRecommendationSourcesJSON *string `gorm:"column:field_recommendation_sources;type:text"`
	// IsSystemDefault is true when this account was generated from the default COA template.
	// Useful for preventing accidental deletion of critical accounts and enabling future reset/re-sync.
	IsSystemDefault bool      `gorm:"not null;default:false"`
	CreatedAt       time.Time
}

// ReportGroup returns the financial reporting bucket for this account.
func (a *Account) ReportGroup() AccountReportGroup {
	return a.RootAccountType.ReportGroup()
}
