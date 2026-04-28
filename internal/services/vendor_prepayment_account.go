// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"

	"balanciz/internal/models"

	"gorm.io/gorm"
)

// EnsureVendorPrepaymentAccount returns the ID of the company's system-generated
// Vendor Prepayments asset account, creating it on first use if absent.
//
// The account is:
//   - root_account_type  = asset
//   - detail_account_type = other_current_asset
//   - system_key          = "vendor_prepayments"
//   - is_system_generated = true
//
// Must be called inside a database transaction so account creation is
// atomic with the payment journal entry.
func EnsureVendorPrepaymentAccount(db *gorm.DB, companyID uint) (uint, error) {
	const sysKey = "vendor_prepayments"

	var acc models.Account
	err := db.Where("company_id = ? AND system_key = ?", companyID, sysKey).First(&acc).Error
	if err == nil {
		return acc.ID, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, fmt.Errorf("lookup vendor prepayments account: %w", err)
	}

	// Determine account code length from the company.
	var company models.Company
	if err := db.Select("id", "account_code_length").First(&company, companyID).Error; err != nil {
		return 0, fmt.Errorf("load company for vendor prepayment account: %w", err)
	}
	codeLen := company.AccountCodeLength
	if codeLen < models.AccountCodeLengthMin || codeLen > models.AccountCodeLengthMax {
		codeLen = models.AccountCodeLengthMin
	}

	accountCode, err := findNextAccountCode(db, companyID, codeLen,
		models.RootAsset, models.DetailOtherCurrentAsset)
	if err != nil {
		return 0, fmt.Errorf("find code for vendor prepayment account: %w", err)
	}

	sk := sysKey
	acc = models.Account{
		CompanyID:         companyID,
		Code:              accountCode,
		Name:              "Vendor Prepayments",
		RootAccountType:   models.RootAsset,
		DetailAccountType: models.DetailOtherCurrentAsset,
		IsActive:          true,
		IsSystemGenerated: true,
		SystemKey:         &sk,
	}
	if err := db.Create(&acc).Error; err != nil {
		return 0, fmt.Errorf("create vendor prepayment account: %w", err)
	}
	return acc.ID, nil
}
