// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"

	"gobooks/internal/models"

	"gorm.io/gorm"
)

// EnsureCustomerDepositsAccount returns the ID of the company's system-
// generated Customer Deposits liability account, creating it on first use.
//
// The account is:
//   - root_account_type   = liability
//   - detail_account_type = other_current_liability
//   - system_key          = "customer_deposits"
//   - is_system_generated = true
//
// This is the AR-side counterpart to EnsureVendorPrepaymentAccount (on the
// AP side, Vendor Prepayments is an asset because we pre-paid the vendor;
// here, money held for a customer is a liability because we owe it back or
// owe a future invoice against it).
//
// Must be called inside a database transaction so account creation is
// atomic with the JE that uses it.
func EnsureCustomerDepositsAccount(db *gorm.DB, companyID uint) (uint, error) {
	const sysKey = "customer_deposits"

	var acc models.Account
	err := db.Where("company_id = ? AND system_key = ?", companyID, sysKey).First(&acc).Error
	if err == nil {
		return acc.ID, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, fmt.Errorf("lookup customer deposits account: %w", err)
	}

	// Determine account code length from the company.
	var company models.Company
	if err := db.Select("id", "account_code_length").First(&company, companyID).Error; err != nil {
		return 0, fmt.Errorf("load company for customer deposits account: %w", err)
	}
	codeLen := company.AccountCodeLength
	if codeLen < models.AccountCodeLengthMin || codeLen > models.AccountCodeLengthMax {
		codeLen = models.AccountCodeLengthMin
	}

	accountCode, err := findNextAccountCode(db, companyID, codeLen,
		models.RootLiability, models.DetailOtherCurrentLiability)
	if err != nil {
		return 0, fmt.Errorf("find code for customer deposits account: %w", err)
	}

	sk := sysKey
	acc = models.Account{
		CompanyID:         companyID,
		Code:              accountCode,
		Name:              "Customer Deposits",
		RootAccountType:   models.RootLiability,
		DetailAccountType: models.DetailOtherCurrentLiability,
		IsActive:          true,
		IsSystemGenerated: true,
		SystemKey:         &sk,
	}
	if err := db.Create(&acc).Error; err != nil {
		return 0, fmt.Errorf("create customer deposits account: %w", err)
	}
	return acc.ID, nil
}
