// 遵循project_guide.md
package services

// fx_settle.go — pure computation helpers and account provisioning for
// realized-FX settlement.
//
// Design principles:
//   - All arithmetic uses decimal.Decimal; no float64 anywhere.
//   - The anchor pattern (from Phase 3 applyFXScaling) is applied here too:
//     the final payment in a series releases exactly the remaining BalanceDueBase,
//     preventing cumulative rounding drift over multiple partial payments.
//   - EnsureFXGainLossAccount is idempotent: the first call creates the account,
//     subsequent calls return the existing ID.

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── Computation ───────────────────────────────────────────────────────────────

// fxSettleResult holds the computed base-currency amounts for one allocation.
type fxSettleResult struct {
	// amountApplied is the document-currency amount settled (caller's input).
	amountApplied decimal.Decimal
	// arapBaseReleased is the base-currency carrying value removed from AR or AP.
	// For base-currency documents: equal to amountApplied.
	arapBaseReleased decimal.Decimal
	// bankBaseAmount is the base-currency equivalent the bank receives/pays.
	// For base-currency documents: equal to amountApplied.
	bankBaseAmount decimal.Decimal
	// realizedFXGainLoss = bankBaseAmount − arapBaseReleased.
	//   > 0 → gain (received more base than the AR carrying value)
	//   < 0 → loss (paid more base than the AP carrying value)
	realizedFXGainLoss decimal.Decimal
	// settlementRate is documentCurrency→baseCurrency rate used. 1 for base docs.
	settlementRate decimal.Decimal
}

// computeAllocationAmounts calculates base-currency settlement amounts for one
// invoice or bill allocation.
//
// Parameters (all in their respective currencies):
//   - amountApplied:  document-currency payment amount for this allocation
//   - balanceDue:     current document-currency balance (before this payment)
//   - balanceDueBase: current base-currency carrying value (before this payment)
//   - settlementRate: documentCurrency → baseCurrency (1.0 for base docs)
//
// The "anchor" rule: when amountApplied == balanceDue (final payment), the entire
// remaining balanceDueBase is released, absorbing accumulated rounding so that the
// document's BalanceDueBase always reaches exactly zero on full settlement.
func computeAllocationAmounts(
	amountApplied, balanceDue, balanceDueBase, settlementRate decimal.Decimal,
) fxSettleResult {
	bankBaseAmount := amountApplied.Mul(settlementRate).Round(2)

	var arapBaseReleased decimal.Decimal
	if amountApplied.Equal(balanceDue) {
		// Final (or only) payment: release whatever remains to avoid accumulated rounding.
		arapBaseReleased = balanceDueBase
	} else {
		// Partial payment: pro-rate the carrying value.
		arapBaseReleased = balanceDueBase.Mul(amountApplied).Div(balanceDue).Round(2)
	}

	return fxSettleResult{
		amountApplied:      amountApplied,
		arapBaseReleased:   arapBaseReleased,
		bankBaseAmount:     bankBaseAmount,
		realizedFXGainLoss: bankBaseAmount.Sub(arapBaseReleased),
		settlementRate:     settlementRate,
	}
}

// effectiveBalances returns the operative balance and balance_due_base for a
// document, handling legacy rows where the values may be zero.
//
// For base-currency documents (CurrencyCode == "" or same as base):
//   - balanceDueBase mirrors balanceDue
//
// For foreign-currency documents with balanceDueBase == 0 (pre-Phase-4 data):
//   - falls back to amountBase (i.e. fully unpaid carrying value assumption)
func effectiveBalances(
	balanceDue, balanceDueBase, amount, amountBase decimal.Decimal,
	isForeign bool,
) (effBalanceDue, effBalanceDueBase decimal.Decimal) {
	// balanceDue: if zero or negative fall back to amount (pre-migration rows).
	effBalanceDue = balanceDue
	if effBalanceDue.LessThanOrEqual(decimal.Zero) {
		effBalanceDue = amount
	}

	if isForeign {
		effBalanceDueBase = balanceDueBase
		if effBalanceDueBase.LessThanOrEqual(decimal.Zero) {
			// Pre-Phase-4 foreign doc: assume fully unpaid.
			effBalanceDueBase = amountBase
		}
	} else {
		// Base-currency doc: carrying value mirrors document-currency balance.
		effBalanceDueBase = effBalanceDue
	}
	return
}

// ── FX Gain/Loss account provisioning ────────────────────────────────────────

// EnsureFXGainLossAccount returns the ID of the company's system FX
// realized-gain/loss account, creating it if it does not yet exist.
//
// The account is:
//   - RootRevenue / DetailOtherIncome
//   - IsSystemGenerated = true
//   - SystemKey = "fx_realized_gain_loss"
//   - Name = "Realized FX Gain/Loss"
//
// Debiting it records a foreign-exchange loss; crediting records a gain.
// This follows standard accounting practice (one contra-income account).
//
// Must be called inside a database transaction so the account creation is
// atomic with the payment journal entry.
func EnsureFXGainLossAccount(db *gorm.DB, companyID uint) (uint, error) {
	sysKey := "fx_realized_gain_loss"

	var acc models.Account
	err := db.Where("company_id = ? AND system_key = ?", companyID, sysKey).First(&acc).Error
	if err == nil {
		return acc.ID, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, fmt.Errorf("lookup FX gain/loss account: %w", err)
	}

	// Determine account code length from the company.
	var company models.Company
	if err := db.Select("id", "account_code_length").First(&company, companyID).Error; err != nil {
		return 0, fmt.Errorf("load company for FX account: %w", err)
	}
	codeLen := company.AccountCodeLength
	if codeLen < models.AccountCodeLengthMin || codeLen > models.AccountCodeLengthMax {
		codeLen = models.AccountCodeLengthMin
	}

	accountCode, err := findNextAccountCode(db, companyID, codeLen,
		models.RootRevenue, models.DetailOtherIncome)
	if err != nil {
		return 0, fmt.Errorf("find code for FX account: %w", err)
	}

	sk := sysKey
	acc = models.Account{
		CompanyID:         companyID,
		Code:              accountCode,
		Name:              "Realized FX Gain/Loss",
		RootAccountType:   models.RootRevenue,
		DetailAccountType: models.DetailOtherIncome,
		IsActive:          true,
		IsSystemGenerated: true,
		SystemKey:         &sk,
	}
	if err := db.Create(&acc).Error; err != nil {
		return 0, fmt.Errorf("create FX gain/loss account: %w", err)
	}
	return acc.ID, nil
}
