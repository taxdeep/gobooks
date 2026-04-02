package web

import (
	"strings"

	"github.com/shopspring/decimal"

	"gobooks/internal/models"
)

type documentCurrencySelection struct {
	CurrencyCode string
	ExchangeRate decimal.Decimal
}

func normalizeDocumentCurrencySelection(
	multiCurrencyEnabled bool,
	baseCurrencyCode string,
	companyCurrencies []models.CompanyCurrency,
	rawCurrencyCode string,
	rawExchangeRate string,
) (documentCurrencySelection, string, string) {
	selection := documentCurrencySelection{
		ExchangeRate: decimal.NewFromInt(1),
	}

	currencyCode := strings.ToUpper(strings.TrimSpace(rawCurrencyCode))
	exchangeRateRaw := strings.TrimSpace(rawExchangeRate)
	baseCurrencyCode = strings.ToUpper(strings.TrimSpace(baseCurrencyCode))

	if !multiCurrencyEnabled {
		if currencyCode != "" || exchangeRateRaw != "" {
			return selection, "Multi-currency is not enabled for this company.", ""
		}
		return selection, "", ""
	}

	if currencyCode == "" || currencyCode == baseCurrencyCode {
		return selection, "", ""
	}

	allowed := false
	for _, cc := range companyCurrencies {
		if cc.IsActive && strings.EqualFold(strings.TrimSpace(cc.CurrencyCode), currencyCode) {
			allowed = true
			break
		}
	}
	if !allowed {
		return selection, "Select a valid company currency.", ""
	}

	selection.CurrencyCode = currencyCode
	if exchangeRateRaw == "" {
		selection.ExchangeRate = decimal.Zero
		return selection, "", ""
	}

	exchangeRate, err := decimal.NewFromString(exchangeRateRaw)
	if err != nil || !exchangeRate.GreaterThan(decimal.Zero) {
		return selection, "", "Enter a valid exchange rate greater than 0."
	}
	selection.ExchangeRate = exchangeRate.RoundBank(8)
	return selection, "", ""
}

func displayDocumentExchangeRate(currencyCode string, exchangeRate decimal.Decimal) string {
	if strings.TrimSpace(currencyCode) == "" || !exchangeRate.GreaterThan(decimal.Zero) {
		return ""
	}
	if exchangeRate.Equal(decimal.NewFromInt(1)) {
		return ""
	}
	return exchangeRate.String()
}
