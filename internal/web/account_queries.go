// 遵循project_guide.md
package web

import (
	"balanciz/internal/models"

	"gorm.io/gorm"
)

// companyAccountCodeLength returns the configured chart code digit width (4–12), defaulting to 4 if unset.
func companyAccountCodeLength(db *gorm.DB, companyID uint) (int, error) {
	var co models.Company
	if err := db.Select("account_code_length").Where("id = ?", companyID).First(&co).Error; err != nil {
		return 0, err
	}
	n := co.AccountCodeLength
	if n < models.AccountCodeLengthMin || n > models.AccountCodeLengthMax {
		return models.AccountCodeLengthMin, nil
	}
	return n, nil
}

// activeAccountsForCompany returns accounts that may be selected on new transactions
// (posting, banking, journal lines). Inactive chart rows stay in the database for history
// but are omitted here.
func (s *Server) activeAccountsForCompany(companyID uint) ([]models.Account, error) {
	var accounts []models.Account
	err := s.DB.Where("company_id = ? AND is_active = ?", companyID, true).Order("code asc").Find(&accounts).Error
	return accounts, err
}
