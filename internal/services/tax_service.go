// 遵循project_guide.md
package services

import (
	"balanciz/internal/models"

	"github.com/shopspring/decimal"
)

// TaxResult holds the three components of a tax computation.
//
//   TaxAmount           = amount × rate  (banker's rounding, 2 dp)
//   RecoverableAmount   = portion the company can reclaim as an ITC
//   NonRecoverableAmount = tax_amount − recoverable_amount
//
// RecoveryRate is stored as a 0–100 percentage on TaxCode; we divide by 100 here.
type TaxResult struct {
	TaxAmount            decimal.Decimal
	RecoverableAmount    decimal.Decimal
	NonRecoverableAmount decimal.Decimal
}

// LineTaxAmounts is the line-level tax breakdown for posting (net + tax splits).
// Recoverability affects purchase/expense treatment; on sales, full tax_amount posts to payable.
type LineTaxAmounts struct {
	NetAmount               decimal.Decimal
	TaxAmount               decimal.Decimal
	RecoverableTaxAmount    decimal.Decimal
	NonRecoverableTaxAmount decimal.Decimal
}

// ComputeLineTax returns net and tax components for one line using the tax code rate and recovery rules.
// It does not change stored line data — compute-only.
func ComputeLineTax(netAmount decimal.Decimal, code models.TaxCode) LineTaxAmounts {
	r := ComputeTax(netAmount, code)
	return LineTaxAmounts{
		NetAmount:               netAmount,
		TaxAmount:               r.TaxAmount,
		RecoverableTaxAmount:    r.RecoverableAmount,
		NonRecoverableTaxAmount: r.NonRecoverableAmount,
	}
}

// AsTaxResult maps LineTaxAmounts to the legacy TaxResult shape for helpers like SalesTaxPostingLine.
func (l LineTaxAmounts) AsTaxResult() TaxResult {
	return TaxResult{
		TaxAmount:            l.TaxAmount,
		RecoverableAmount:    l.RecoverableTaxAmount,
		NonRecoverableAmount: l.NonRecoverableTaxAmount,
	}
}

// TaxPostingLine is a single GL debit or credit line derived from a tax computation.
// AccountID is 0 when the line should not be posted (e.g. non-recoverable with no
// expense account override — the caller adds the amount to the line's expense account).
type TaxPostingLine struct {
	AccountID uint
	Amount    decimal.Decimal
}

// ComputeTax calculates tax on amount using code's rate and recovery rules.
// Amount must be the pre-tax net (qty × unit price).
// Returns a zero TaxResult when amount or rate is zero.
func ComputeTax(amount decimal.Decimal, code models.TaxCode) TaxResult {
	if amount.IsZero() || code.Rate.IsZero() {
		return TaxResult{}
	}

	taxAmount := amount.Mul(code.Rate).RoundBank(2)

	var recoverable decimal.Decimal
	switch code.RecoveryMode {
	case models.TaxRecoveryFull:
		recoverable = taxAmount
	case models.TaxRecoveryPartial:
		// RecoveryRate is a 0–100 percentage; divide by 100 before multiplying.
		pct := code.RecoveryRate.Div(decimal.NewFromInt(100))
		recoverable = taxAmount.Mul(pct).RoundBank(2)
	default: // none or unrecognised
		recoverable = decimal.Zero
	}

	nonRecoverable := taxAmount.Sub(recoverable)

	return TaxResult{
		TaxAmount:            taxAmount,
		RecoverableAmount:    recoverable,
		NonRecoverableAmount: nonRecoverable,
	}
}

// SalesTaxPostingLine returns the single GL line to post when tax is collected on a sale.
//
// Sales tax is always a liability: credit SalesTaxAccountID for the full tax amount.
// The caller places this into a journal entry as a credit line.
func SalesTaxPostingLine(result TaxResult, code models.TaxCode) TaxPostingLine {
	return TaxPostingLine{
		AccountID: code.SalesTaxAccountID,
		Amount:    result.TaxAmount,
	}
}

// PurchaseTaxPostingLines returns the GL lines required when tax is paid on a purchase.
//
// Two lines may be returned:
//
//  1. Recoverable portion → debit PurchaseRecoverableAccountID (ITC Receivable).
//     Omitted (AccountID=0) when RecoverableAmount is zero or no recoverable account is set.
//
//  2. Non-recoverable portion → AccountID=0, Amount=NonRecoverableAmount.
//     The caller is responsible for routing this amount: it is typically added to the
//     expense or inventory account of the purchase line (not posted to a separate account
//     here, because the correct account depends on the line item context).
func PurchaseTaxPostingLines(result TaxResult, code models.TaxCode) (recoverable TaxPostingLine, nonRecoverable TaxPostingLine) {
	// Recoverable ITC line.
	if result.RecoverableAmount.IsPositive() && code.PurchaseRecoverableAccountID != nil {
		recoverable = TaxPostingLine{
			AccountID: *code.PurchaseRecoverableAccountID,
			Amount:    result.RecoverableAmount,
		}
	}

	// Non-recoverable line: AccountID=0 signals "add to expense account of the line".
	nonRecoverable = TaxPostingLine{
		AccountID: 0,
		Amount:    result.NonRecoverableAmount,
	}

	return recoverable, nonRecoverable
}
