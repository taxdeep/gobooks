package services

import (
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"

	"balanciz/internal/models"
)

// CompanyCurrencyContext is the reusable currency contract for transaction forms.
type CompanyCurrencyContext struct {
	CompanyID              uint
	BaseCurrencyCode       string
	MultiCurrencyEnabled   bool
	CompanyCurrencies      []models.CompanyCurrency
	AllowedCurrencyOptions []string
}

// LoadCompanyCurrencyContext returns the reusable company/base/allowed-currency contract.
func LoadCompanyCurrencyContext(db *gorm.DB, companyID uint) (CompanyCurrencyContext, error) {
	var company models.Company
	if err := db.Select("id", "base_currency_code", "multi_currency_enabled").
		First(&company, companyID).Error; err != nil {
		return CompanyCurrencyContext{}, err
	}

	rows, err := ListCompanyCurrencies(db, companyID)
	if err != nil {
		return CompanyCurrencyContext{}, err
	}

	options := []string{normalizeCurrencyCode(company.BaseCurrencyCode)}
	if company.MultiCurrencyEnabled {
		seen := map[string]struct{}{options[0]: {}}
		foreign := make([]string, 0, len(rows))
		for _, row := range rows {
			if !row.IsActive {
				continue
			}
			code := normalizeCurrencyCode(row.CurrencyCode)
			if code == "" {
				continue
			}
			if _, ok := seen[code]; ok {
				continue
			}
			seen[code] = struct{}{}
			foreign = append(foreign, code)
		}
		sort.Strings(foreign)
		options = append(options, foreign...)
	}

	return CompanyCurrencyContext{
		CompanyID:              companyID,
		BaseCurrencyCode:       normalizeCurrencyCode(company.BaseCurrencyCode),
		MultiCurrencyEnabled:   company.MultiCurrencyEnabled,
		CompanyCurrencies:      rows,
		AllowedCurrencyOptions: options,
	}, nil
}

func (c CompanyCurrencyContext) IsBaseCurrency(code string) bool {
	return normalizeCurrencyCode(code) == c.BaseCurrencyCode
}

func (c CompanyCurrencyContext) IsAllowedTransactionCurrency(code string) bool {
	code = normalizeCurrencyCode(code)
	for _, allowed := range c.AllowedCurrencyOptions {
		if allowed == code {
			return true
		}
	}
	return false
}

// NormalizeTransactionCurrencyCode ensures JE transaction currency is always explicit.
func NormalizeTransactionCurrencyCode(ctx CompanyCurrencyContext, raw string) (string, error) {
	code := normalizeCurrencyCode(raw)
	if code == "" {
		code = ctx.BaseCurrencyCode
	}
	if !ctx.MultiCurrencyEnabled && code != ctx.BaseCurrencyCode {
		return "", fmt.Errorf("multi-currency is not enabled for this company")
	}
	if !ctx.IsAllowedTransactionCurrency(code) {
		return "", fmt.Errorf("select a valid company currency")
	}
	return code, nil
}

func normalizeCurrencyCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}
