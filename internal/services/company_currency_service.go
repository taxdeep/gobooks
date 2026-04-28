// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"
	"strconv"

	"gorm.io/gorm"

	"balanciz/internal/models"
)

// EnableMultiCurrency sets multi_currency_enabled = true on the company.
// Safe to call multiple times (idempotent).
func EnableMultiCurrency(db *gorm.DB, companyID uint) error {
	return db.Model(&models.Company{}).
		Where("id = ?", companyID).
		Update("multi_currency_enabled", true).Error
}

// AddCompanyCurrency registers a foreign currency for a company and auto-creates
// dedicated Accounts Receivable and Accounts Payable accounts for that currency.
//
// Rules enforced:
//   - The currency must exist in the currencies table and be active.
//   - The currency must not equal the company's base currency.
//   - Calling with a currency that is already active is a no-op (idempotent).
//   - AR/AP accounts are only created if no account with the matching system_key
//     ("ar_{code}" / "ap_{code}") already exists for the company.
//
// No float64 is used; all amounts and rates stay in decimal.Decimal.
func AddCompanyCurrency(db *gorm.DB, companyID uint, code string) error {
	// Validate currency exists and is active.
	var cur models.Currency
	if err := db.Where("code = ? AND is_active = true", code).First(&cur).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("currency %q is not an active currency", code)
		}
		return err
	}

	// Load company for base currency + account code length.
	var company models.Company
	if err := db.First(&company, companyID).Error; err != nil {
		return fmt.Errorf("company not found")
	}
	if code == company.BaseCurrencyCode {
		return fmt.Errorf("cannot add base currency %q as a foreign currency", code)
	}

	// Idempotency check — if already active, nothing to do.
	var cc models.CompanyCurrency
	err := db.Where("company_id = ? AND currency_code = ?", companyID, code).First(&cc).Error
	if err == nil && cc.IsActive {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		// 1. Upsert company_currencies row.
		if err := EnsureCompanyCurrency(tx, companyID, code); err != nil {
			return err
		}

		codeLen := company.AccountCodeLength
		if codeLen < models.AccountCodeLengthMin || codeLen > models.AccountCodeLengthMax {
			codeLen = models.AccountCodeLengthMin // safe default
		}

		// 2. Auto-create AR account for this currency.
		if err := ensureSystemAccount(tx, companyID, code, codeLen,
			models.RootAsset, models.DetailAccountsReceivable,
			"Accounts Receivable - "+code,
			"ar_"+code,
		); err != nil {
			return fmt.Errorf("auto-create AR account for %s: %w", code, err)
		}

		// 3. Auto-create AP account for this currency.
		if err := ensureSystemAccount(tx, companyID, code, codeLen,
			models.RootLiability, models.DetailAccountsPayable,
			"Accounts Payable - "+code,
			"ap_"+code,
		); err != nil {
			return fmt.Errorf("auto-create AP account for %s: %w", code, err)
		}

		return nil
	})
}

// ensureSystemAccount creates a system-generated account if one with the given
// systemKey does not already exist for the company. If the account already exists,
// it is a no-op.
func ensureSystemAccount(
	db *gorm.DB,
	companyID uint,
	currencyCode string,
	codeLength int,
	root models.RootAccountType,
	detail models.DetailAccountType,
	name string,
	systemKey string,
) error {
	if err := models.ValidateCurrencyMode(models.CurrencyModeFixedForeign, &currencyCode); err != nil {
		return err
	}

	var count int64
	db.Model(&models.Account{}).
		Where("company_id = ? AND system_key = ?", companyID, systemKey).
		Count(&count)
	if count > 0 {
		return nil // already exists
	}

	accountCode, err := findNextAccountCode(db, companyID, codeLength, root, detail)
	if err != nil {
		return err
	}

	cur := currencyCode
	sk := systemKey
	acc := models.Account{
		CompanyID:         companyID,
		Code:              accountCode,
		Name:              name,
		RootAccountType:   root,
		DetailAccountType: detail,
		IsActive:          true,
		CurrencyMode:      models.CurrencyModeFixedForeign,
		CurrencyCode:      &cur,
		IsSystemGenerated: true,
		SystemKey:         &sk,
	}
	return db.Create(&acc).Error
}

// findNextAccountCode returns the next available account code for the given
// root+detail type within the company, respecting the company's code length.
//
// Algorithm:
//  1. Find all existing account codes for this company of the same root+detail type.
//  2. Take the maximum code number; if none exist start from the lower bound of
//     the prefix range (e.g., 1000 for 4-digit asset accounts).
//  3. Increment by 1 until a free code is found within the prefix's upper bound.
func findNextAccountCode(
	db *gorm.DB,
	companyID uint,
	codeLength int,
	root models.RootAccountType,
	detail models.DetailAccountType,
) (string, error) {
	prefixByte, err := models.RootRequiredPrefixDigit(root)
	if err != nil {
		return "", err
	}

	var codes []string
	db.Model(&models.Account{}).
		Where("company_id = ? AND root_account_type = ? AND detail_account_type = ?",
			companyID, string(root), string(detail)).
		Pluck("code", &codes)

	maxNum := 0
	for _, c := range codes {
		if n, e := strconv.Atoi(c); e == nil && n > maxNum {
			maxNum = n
		}
	}

	prefixDigit := int(prefixByte - '0')
	scale := intPow10(codeLength - 1)
	lowerBound := prefixDigit * scale       // e.g., 1*1000 = 1000 (4-digit asset)
	upperBound := (prefixDigit + 1) * scale // e.g., 2*1000 = 2000 (exclusive)

	start := maxNum + 1
	if start < lowerBound {
		start = lowerBound
	}
	for candidate := start; candidate < upperBound; candidate++ {
		code := fmt.Sprintf("%0*d", codeLength, candidate)
		var cnt int64
		db.Model(&models.Account{}).
			Where("company_id = ? AND code = ?", companyID, code).
			Count(&cnt)
		if cnt == 0 {
			return code, nil
		}
	}
	return "", fmt.Errorf("no available account code in range %d–%d for %d-digit codes; add accounts manually",
		lowerBound, upperBound-1, codeLength)
}

// intPow10 returns 10^n as an integer.
func intPow10(n int) int {
	result := 1
	for i := 0; i < n; i++ {
		result *= 10
	}
	return result
}
