// 遵循project_guide.md
package services

import (
	"fmt"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

type cashPostingCurrency struct {
	TransactionCurrencyCode string
	ExchangeRate            decimal.Decimal
	BankIsForeign           bool
}

func resolveCashPostingCurrency(tx *gorm.DB, companyID, bankAccountID uint, documentCurrencyCode string, exchangeRate decimal.Decimal) (cashPostingCurrency, error) {
	var company models.Company
	if err := tx.Select("id", "base_currency_code", "multi_currency_enabled").
		First(&company, companyID).Error; err != nil {
		return cashPostingCurrency{}, fmt.Errorf("load company: %w", err)
	}
	base := normalizeCurrencyCode(company.BaseCurrencyCode)
	docCurrency := normalizeCurrencyCode(documentCurrencyCode)
	if docCurrency == "" {
		docCurrency = base
	}

	var bank models.Account
	if err := tx.Select("id", "company_id", "root_account_type", "detail_account_type", "currency_mode", "currency_code").
		Where("id = ? AND company_id = ?", bankAccountID, companyID).
		First(&bank).Error; err != nil {
		return cashPostingCurrency{}, fmt.Errorf("bank account not found")
	}
	if bank.ReportGroup() != models.AccountReportGroupAsset {
		return cashPostingCurrency{}, fmt.Errorf("bank account must be an asset")
	}

	rate := exchangeRate
	if rate.IsZero() || rate.IsNegative() {
		rate = decimal.NewFromInt(1)
	}

	out := cashPostingCurrency{
		TransactionCurrencyCode: base,
		ExchangeRate:            decimal.NewFromInt(1),
	}

	if bank.CurrencyMode != models.CurrencyModeFixedForeign {
		return out, nil
	}
	ctx, err := LoadCompanyCurrencyContext(tx, companyID)
	if err != nil {
		return cashPostingCurrency{}, fmt.Errorf("load company currency context: %w", err)
	}
	if bank.CurrencyCode == nil || normalizeCurrencyCode(*bank.CurrencyCode) == "" {
		return cashPostingCurrency{}, fmt.Errorf("fixed foreign bank account %d is missing its currency configuration", bank.ID)
	}
	if !company.MultiCurrencyEnabled {
		return cashPostingCurrency{}, fmt.Errorf("multi-currency is not enabled for this company")
	}

	bankCurrency := normalizeCurrencyCode(*bank.CurrencyCode)
	if !ctx.IsAllowedTransactionCurrency(bankCurrency) || bankCurrency == base {
		return cashPostingCurrency{}, fmt.Errorf("bank account currency %s is not an active foreign currency for this company", bankCurrency)
	}
	if docCurrency != bankCurrency {
		return cashPostingCurrency{}, fmt.Errorf("bank account %d is denominated in %s but this cash transaction is %s", bank.ID, bankCurrency, docCurrency)
	}
	if !rate.GreaterThan(decimal.Zero) {
		return cashPostingCurrency{}, fmt.Errorf("exchange rate is required for %s cash transactions", bankCurrency)
	}

	out.TransactionCurrencyCode = bankCurrency
	out.ExchangeRate = rate.RoundBank(8)
	out.BankIsForeign = true
	return out, nil
}

func cashExchangeRateSource(cash cashPostingCurrency) string {
	if cash.BankIsForeign {
		return JournalEntryExchangeRateSourceManual
	}
	return JournalEntryExchangeRateSourceIdentity
}
