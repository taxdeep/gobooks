// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ErrBookAlreadyExists is returned when a secondary book with the same
// type, functional currency, and standard profile already exists.
var ErrBookAlreadyExists = errors.New("a book with this type, currency, and standard already exists for this company")

// CreateAccountingBookInput holds required fields for creating a new accounting book.
type CreateAccountingBookInput struct {
	CompanyID              uint
	BookType               models.AccountingBookType
	FunctionalCurrencyCode string
	StandardProfileCode    models.AccountingStandardProfileCode
}

// CreateAccountingBook creates a new non-primary accounting book for a company.
//
// Validates:
//   - BookType may not be "primary" (primary books are created by migration).
//   - FunctionalCurrencyCode must be non-empty.
//   - StandardProfileCode must match a seeded profile.
//   - No duplicate (company, bookType, currency, profile) exists.
func CreateAccountingBook(db *gorm.DB, in CreateAccountingBookInput) (*models.AccountingBook, error) {
	if in.BookType == models.AccountingBookTypePrimary {
		return nil, errors.New("primary books are created automatically — use type 'secondary', 'adjustment', or 'tax'")
	}
	if in.FunctionalCurrencyCode == "" {
		return nil, errors.New("functional currency code is required")
	}

	// Load the standard profile.
	var profile models.AccountingStandardProfile
	if err := db.Where("code = ?", string(in.StandardProfileCode)).First(&profile).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("accounting standard profile %q not found", in.StandardProfileCode)
		}
		return nil, fmt.Errorf("load standard profile: %w", err)
	}

	// Duplicate guard.
	var count int64
	db.Model(&models.AccountingBook{}).
		Where("company_id = ? AND book_type = ? AND functional_currency_code = ? AND standard_profile_id = ?",
			in.CompanyID, string(in.BookType), in.FunctionalCurrencyCode, profile.ID).
		Count(&count)
	if count > 0 {
		return nil, ErrBookAlreadyExists
	}

	book := models.AccountingBook{
		CompanyID:              in.CompanyID,
		BookType:               in.BookType,
		FunctionalCurrencyCode: in.FunctionalCurrencyCode,
		StandardProfileID:      profile.ID,
		StandardProfile:        profile,
		StandardChangePolicy:   models.StandardChangePolicyAllowDirect,
	}
	if err := db.Create(&book).Error; err != nil {
		return nil, fmt.Errorf("create accounting book: %w", err)
	}
	return &book, nil
}

// ListAccountingBooks returns all accounting books for the company,
// ordered primary-first then by ID. Preloads StandardProfile for display.
func ListAccountingBooks(db *gorm.DB, companyID uint) ([]models.AccountingBook, error) {
	var books []models.AccountingBook
	if err := db.
		Preload("StandardProfile").
		Where("company_id = ?", companyID).
		Order("CASE book_type WHEN 'primary' THEN 0 ELSE 1 END, id ASC").
		Find(&books).Error; err != nil {
		return nil, fmt.Errorf("list accounting books: %w", err)
	}
	return books, nil
}

// ListStandardProfiles returns all seeded accounting standard profiles,
// ordered by effective_from ascending.
func ListStandardProfiles(db *gorm.DB) ([]models.AccountingStandardProfile, error) {
	var profiles []models.AccountingStandardProfile
	if err := db.Order("effective_from asc").Find(&profiles).Error; err != nil {
		return nil, fmt.Errorf("list standard profiles: %w", err)
	}
	return profiles, nil
}

// EnsureCTAAccount returns the ID of the Cumulative Translation Adjustment equity
// account for the company, creating it (system_key = "fx_cta") if absent.
//
// The CTA account is classified as RootEquity / DetailOtherEquity and is always
// system-generated. It absorbs the OCI translation residual from RunTranslation.
func EnsureCTAAccount(db *gorm.DB, companyID uint) (uint, error) {
	const sysKey = "fx_cta"

	var acc models.Account
	err := db.Where("company_id = ? AND system_key = ?", companyID, sysKey).First(&acc).Error
	if err == nil {
		return acc.ID, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, fmt.Errorf("lookup CTA account: %w", err)
	}

	// Load company for account code length.
	var company models.Company
	if err := db.Select("id", "account_code_length").First(&company, companyID).Error; err != nil {
		return 0, fmt.Errorf("load company for CTA account: %w", err)
	}
	codeLen := company.AccountCodeLength
	if codeLen < models.AccountCodeLengthMin || codeLen > models.AccountCodeLengthMax {
		codeLen = models.AccountCodeLengthMin
	}

	accountCode, err := findNextAccountCode(db, companyID, codeLen,
		models.RootEquity, models.DetailOtherEquity)
	if err != nil {
		return 0, fmt.Errorf("find code for CTA account: %w", err)
	}

	sk := sysKey
	acc = models.Account{
		CompanyID:         companyID,
		Code:              accountCode,
		Name:              "Cumulative Translation Adjustment",
		RootAccountType:   models.RootEquity,
		DetailAccountType: models.DetailOtherEquity,
		IsActive:          true,
		IsSystemGenerated: true,
		SystemKey:         &sk,
	}
	if err := db.Create(&acc).Error; err != nil {
		return 0, fmt.Errorf("create CTA account: %w", err)
	}
	return acc.ID, nil
}
