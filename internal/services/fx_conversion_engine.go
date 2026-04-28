// 遵循project_guide.md
package services

import (
	"fmt"

	"github.com/shopspring/decimal"
)

type FXLineAmounts struct {
	TxDebit  decimal.Decimal
	TxCredit decimal.Decimal
}

type FXLineConversion struct {
	TxDebit  decimal.Decimal
	TxCredit decimal.Decimal
	Debit    decimal.Decimal
	Credit   decimal.Decimal
}

type FXConversionTotals struct {
	TxDebitTotal    decimal.Decimal
	TxCreditTotal   decimal.Decimal
	BaseDebitTotal  decimal.Decimal
	BaseCreditTotal decimal.Decimal
}

type FXConversionResult struct {
	Lines  []FXLineConversion
	Totals FXConversionTotals
}

// RoundBankMoney applies banker's rounding to 2 decimal places.
// This is the standard rounding rule for all base-currency amounts in Balanciz.
func RoundBankMoney(amount decimal.Decimal) decimal.Decimal {
	return amount.RoundBank(2)
}

// ConvertJournalLineAmounts converts each line individually using the exchange rate,
// then applies the anchor pattern to absorb any base-currency rounding residual.
//
// Per-line rounding (banker's rounding to 2dp) can produce a 1-cent imbalance when
// multiple lines are converted independently — for example, 3 lines of USD 0.01 at
// rate 1.5 each round to CAD 0.02, giving total debits 0.04 vs total credits 0.03.
// The anchor pattern corrects this by adjusting the last debit or credit line by the
// residual, rather than blocking the save. The maximum adjustment is bounded by
// (n-1) × 0.005 where n is the number of lines — for typical entries (2–10 lines)
// this is under 5 cents.
//
// This is the same anchor approach used by applyFXScaling in fragment_builder.go
// and by computeAllocationAmounts in fx_settle.go.
//
// Precondition: TxDebitTotal must equal TxCreditTotal. If the entry does not balance
// in transaction currency, an error is returned before any conversion is attempted.
func ConvertJournalLineAmounts(lines []FXLineAmounts, exchangeRate decimal.Decimal) (FXConversionResult, error) {
	if !exchangeRate.GreaterThan(decimal.Zero) {
		return FXConversionResult{}, fmt.Errorf("exchange rate must be greater than 0")
	}

	result := FXConversionResult{
		Lines: make([]FXLineConversion, 0, len(lines)),
		Totals: FXConversionTotals{
			TxDebitTotal:    decimal.Zero,
			TxCreditTotal:   decimal.Zero,
			BaseDebitTotal:  decimal.Zero,
			BaseCreditTotal: decimal.Zero,
		},
	}

	// Track the index of the last debit and credit line for anchor absorption.
	lastDebitIdx  := -1
	lastCreditIdx := -1

	for i, line := range lines {
		if line.TxDebit.GreaterThan(decimal.Zero) && line.TxCredit.GreaterThan(decimal.Zero) {
			return FXConversionResult{}, fmt.Errorf("a line cannot have both debit and credit")
		}

		converted := FXLineConversion{
			TxDebit:  line.TxDebit,
			TxCredit: line.TxCredit,
			Debit:    RoundBankMoney(line.TxDebit.Mul(exchangeRate)),
			Credit:   RoundBankMoney(line.TxCredit.Mul(exchangeRate)),
		}
		result.Lines = append(result.Lines, converted)
		result.Totals.TxDebitTotal    = result.Totals.TxDebitTotal.Add(converted.TxDebit)
		result.Totals.TxCreditTotal   = result.Totals.TxCreditTotal.Add(converted.TxCredit)
		result.Totals.BaseDebitTotal  = result.Totals.BaseDebitTotal.Add(converted.Debit)
		result.Totals.BaseCreditTotal = result.Totals.BaseCreditTotal.Add(converted.Credit)

		if line.TxDebit.GreaterThan(decimal.Zero) {
			lastDebitIdx = i
		}
		if line.TxCredit.GreaterThan(decimal.Zero) {
			lastCreditIdx = i
		}
	}

	// Transaction-currency balance is a hard precondition.
	if !result.Totals.TxDebitTotal.Equal(result.Totals.TxCreditTotal) {
		return FXConversionResult{}, fmt.Errorf("total debits must equal total credits")
	}

	// Anchor pattern: absorb any base-currency rounding residual.
	// residual = BaseDebitTotal - BaseCreditTotal
	//   > 0: debits exceed credits → reduce the last debit (anchor) by residual.
	//   < 0: credits exceed debits → reduce the last credit (anchor) by abs(residual).
	residual := result.Totals.BaseDebitTotal.Sub(result.Totals.BaseCreditTotal)
	if !residual.IsZero() {
		if residual.GreaterThan(decimal.Zero) {
			if lastDebitIdx < 0 {
				return FXConversionResult{}, fmt.Errorf("cannot absorb base rounding residual: no debit line found")
			}
			result.Lines[lastDebitIdx].Debit = result.Lines[lastDebitIdx].Debit.Sub(residual)
			result.Totals.BaseDebitTotal = result.Totals.BaseDebitTotal.Sub(residual)
		} else {
			if lastCreditIdx < 0 {
				return FXConversionResult{}, fmt.Errorf("cannot absorb base rounding residual: no credit line found")
			}
			// residual is negative; adding it reduces the credit.
			result.Lines[lastCreditIdx].Credit = result.Lines[lastCreditIdx].Credit.Add(residual)
			result.Totals.BaseCreditTotal = result.Totals.BaseCreditTotal.Add(residual)
		}
	}

	return result, nil
}
