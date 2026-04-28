// 遵循project_guide.md
package services

import (
	"balanciz/internal/models"

	"github.com/shopspring/decimal"
)

// TaxLineResult holds the computed tax amount for one tax line on one invoice line.
// The posting service uses SalesTaxAccountID to credit the correct payable account.
type TaxLineResult struct {
	SalesTaxAccountID uint
	TaxAmount         decimal.Decimal
}

// CalculateTax computes the sales tax for a TaxCode applied to netAmount.
// Returns nil for zero amounts, zero rates, or purchase-only scoped codes.
// Delegates computation to ComputeTax.
func CalculateTax(netAmount decimal.Decimal, code models.TaxCode) []TaxLineResult {
	if netAmount.IsZero() || code.Rate.IsZero() {
		return nil
	}
	if code.Scope == models.TaxScopePurchase {
		return nil
	}

	result := ComputeTax(netAmount, code)
	if result.TaxAmount.IsZero() {
		return nil
	}

	return []TaxLineResult{{
		SalesTaxAccountID: code.SalesTaxAccountID,
		TaxAmount:         result.TaxAmount,
	}}
}

// SumTaxResults returns the total of all TaxLineResult amounts.
func SumTaxResults(results []TaxLineResult) decimal.Decimal {
	total := decimal.Zero
	for _, r := range results {
		total = total.Add(r.TaxAmount)
	}
	return total
}
