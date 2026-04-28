// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"

	"balanciz/internal/models"

	"gorm.io/gorm"
)

// EnsureJournalLineAccountsBelongToCompany checks that every line's account_id
// references an account row with the given company_id.
func EnsureJournalLineAccountsBelongToCompany(tx *gorm.DB, companyID uint, lines []models.JournalLine) error {
	for _, line := range lines {
		if line.AccountID == 0 {
			return fmt.Errorf("invalid account on a journal line")
		}
		var acc models.Account
		if err := tx.Select("id", "company_id").First(&acc, line.AccountID).Error; err != nil {
			return err
		}
		if acc.CompanyID != companyID {
			return fmt.Errorf("one or more accounts do not belong to this company")
		}
	}
	return nil
}

// EnsureJournalLineReferencesBelongToCompany validates accounts, parties, and fixed-foreign account compatibility.
func EnsureJournalLineReferencesBelongToCompany(tx *gorm.DB, companyID uint, transactionCurrencyCode string, lines []models.JournalLine) error {
	for _, line := range lines {
		if line.AccountID == 0 {
			return fmt.Errorf("invalid account on a journal line")
		}

		var acc models.Account
		if err := tx.Select("id", "company_id", "currency_mode", "currency_code").
			First(&acc, line.AccountID).Error; err != nil {
			return err
		}
		if acc.CompanyID != companyID {
			return fmt.Errorf("one or more accounts do not belong to this company")
		}
		if acc.CurrencyMode == models.CurrencyModeFixedForeign {
			if acc.CurrencyCode == nil || *acc.CurrencyCode == "" {
				return fmt.Errorf("fixed foreign account %d is missing its currency configuration", acc.ID)
			}
			if normalizeCurrencyCode(*acc.CurrencyCode) != normalizeCurrencyCode(transactionCurrencyCode) {
				return fmt.Errorf("account %d requires %s journal-entry currency", acc.ID, normalizeCurrencyCode(*acc.CurrencyCode))
			}
		}

		switch line.PartyType {
		case models.PartyTypeNone:
			if line.PartyID != 0 {
				return fmt.Errorf("invalid party selection")
			}
		case models.PartyTypeCustomer:
			if line.PartyID == 0 {
				return fmt.Errorf("invalid customer selection")
			}
			var customer models.Customer
			if err := tx.Select("id", "company_id").First(&customer, line.PartyID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return fmt.Errorf("one or more selected customers are invalid for this company")
				}
				return err
			}
			if customer.CompanyID != companyID {
				return fmt.Errorf("one or more customers do not belong to this company")
			}
		case models.PartyTypeVendor:
			if line.PartyID == 0 {
				return fmt.Errorf("invalid vendor selection")
			}
			var vendor models.Vendor
			if err := tx.Select("id", "company_id").First(&vendor, line.PartyID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return fmt.Errorf("one or more selected vendors are invalid for this company")
				}
				return err
			}
			if vendor.CompanyID != companyID {
				return fmt.Errorf("one or more vendors do not belong to this company")
			}
		default:
			return fmt.Errorf("invalid party selection")
		}
	}
	return nil
}
