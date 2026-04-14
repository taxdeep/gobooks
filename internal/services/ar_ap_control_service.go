// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"gobooks/internal/models"
)

var (
	// ErrControlMappingNotFound is returned when no control mapping matches and the
	// legacy fallback also finds no account.
	ErrControlMappingNotFound = errors.New("no AR/AP control account found for this document type and currency")

	// ErrDuplicateControlMapping is returned when a mapping for the same
	// (company, book, doc_type, currency_code) already exists.
	ErrDuplicateControlMapping = errors.New("a control account mapping for this document type and currency already exists")
)

// ResolveControlAccount resolves the AR or AP control account for a posting operation.
//
// Resolution order:
//  1. ARAPControlMapping exact:     (company, book_id>0, doc_type, currency_code)
//  2. ARAPControlMapping book-agnostic specific: (company, 0, doc_type, currency_code)
//  3. ARAPControlMapping default:   (company, 0, doc_type, currency_code='')
//  4. Legacy system_key:            "ar_{code}" / "ap_{code}"
//  5. First active account by detail_account_type
//
// bookID = 0 means primary book (pass the actual primary book ID if known).
// isForeignCurrency should be true when transactionCurrencyCode != company base currency.
func ResolveControlAccount(
	db *gorm.DB,
	companyID, bookID uint,
	docType models.ARAPControlDocType,
	transactionCurrencyCode string,
	isForeignCurrency bool,
	fallbackDetailType models.DetailAccountType,
	noAccountErr error,
) (models.Account, error) {
	// ── 1 & 2. Explicit mapping lookup ────────────────────────────────────────
	// Try book-specific first, then book-agnostic.
	bookIDs := []uint{bookID, 0}
	if bookID == 0 {
		bookIDs = []uint{0}
	}

	for _, bid := range bookIDs {
		// Try currency-specific mapping.
		var m models.ARAPControlMapping
		err := db.Preload("ControlAccount").
			Where("company_id = ? AND book_id = ? AND document_type = ? AND currency_code = ?",
				companyID, bid, string(docType), transactionCurrencyCode).
			First(&m).Error
		if err == nil {
			if m.ControlAccount.IsActive {
				return m.ControlAccount, nil
			}
		}

		// ── 3. Default mapping (currency_code = '') ────────────────────────────
		var def models.ARAPControlMapping
		err = db.Preload("ControlAccount").
			Where("company_id = ? AND book_id = ? AND document_type = ? AND currency_code = ''",
				companyID, bid, string(docType)).
			First(&def).Error
		if err == nil {
			if def.ControlAccount.IsActive {
				return def.ControlAccount, nil
			}
		}
	}

	// ── 4. Legacy system_key fallback ─────────────────────────────────────────
	// Keys are stored as "ar_USD" / "ap_EUR" (lowercase prefix, uppercase currency).
	if isForeignCurrency && transactionCurrencyCode != "" {
		var sidePrefix string
		if side, ok := models.ARAPDocTypeSide[docType]; ok {
			sidePrefix = strings.ToLower(side)
		}
		if sidePrefix != "" {
			sysKey := sidePrefix + "_" + strings.ToUpper(transactionCurrencyCode)
			var acc models.Account
			if err := db.Where("company_id = ? AND system_key = ? AND is_active = true",
				companyID, sysKey).First(&acc).Error; err == nil {
				return acc, nil
			}
		}
	}

	// ── 5. First active account by detail type ────────────────────────────────
	var acc models.Account
	if err := db.
		Where("company_id = ? AND detail_account_type = ? AND is_active = true",
			companyID, string(fallbackDetailType)).
		Order("code asc").
		First(&acc).Error; err != nil {
		if noAccountErr != nil {
			return models.Account{}, noAccountErr
		}
		return models.Account{}, ErrControlMappingNotFound
	}
	return acc, nil
}

// ── CRUD ──────────────────────────────────────────────────────────────────────

// CreateARAPControlMappingInput holds the parameters for adding a mapping.
type CreateARAPControlMappingInput struct {
	CompanyID        uint
	BookID           uint // 0 = all books
	DocumentType     models.ARAPControlDocType
	CurrencyCode     string // '' = default/fallback
	ControlAccountID uint
	Notes            string
}

// CreateARAPControlMapping adds a new control-account mapping. Validates that
// the control account is active and belongs to the company, and that no
// duplicate mapping exists for the same (company, book, doc_type, currency).
func CreateARAPControlMapping(db *gorm.DB, in CreateARAPControlMappingInput) (*models.ARAPControlMapping, error) {
	in.CurrencyCode = strings.ToUpper(strings.TrimSpace(in.CurrencyCode))

	// Validate doc type.
	found := false
	for _, dt := range models.AllARAPDocTypes() {
		if dt == in.DocumentType {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("unknown document type %q", in.DocumentType)
	}

	// Validate control account.
	var acc models.Account
	if err := db.Where("id = ? AND company_id = ? AND is_active = true", in.ControlAccountID, in.CompanyID).
		First(&acc).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("control account not found or not active")
		}
		return nil, fmt.Errorf("load control account: %w", err)
	}

	// Duplicate guard.
	var count int64
	db.Model(&models.ARAPControlMapping{}).
		Where("company_id = ? AND book_id = ? AND document_type = ? AND currency_code = ?",
			in.CompanyID, in.BookID, string(in.DocumentType), in.CurrencyCode).
		Count(&count)
	if count > 0 {
		return nil, ErrDuplicateControlMapping
	}

	m := models.ARAPControlMapping{
		CompanyID:        in.CompanyID,
		BookID:           in.BookID,
		DocumentType:     in.DocumentType,
		CurrencyCode:     in.CurrencyCode,
		ControlAccountID: in.ControlAccountID,
		Notes:            strings.TrimSpace(in.Notes),
	}
	if err := db.Create(&m).Error; err != nil {
		return nil, fmt.Errorf("create AR/AP control mapping: %w", err)
	}
	m.ControlAccount = acc
	return &m, nil
}

// DeleteARAPControlMapping removes a mapping by ID, scoped to the company.
func DeleteARAPControlMapping(db *gorm.DB, companyID, mappingID uint) error {
	result := db.Where("id = ? AND company_id = ?", mappingID, companyID).
		Delete(&models.ARAPControlMapping{})
	if result.Error != nil {
		return fmt.Errorf("delete AR/AP control mapping: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return errors.New("mapping not found")
	}
	return nil
}

// ListARAPControlMappings returns all mappings for the company, ordered by
// document_type, book_id, currency_code.
func ListARAPControlMappings(db *gorm.DB, companyID uint) ([]models.ARAPControlMapping, error) {
	var mappings []models.ARAPControlMapping
	if err := db.Preload("ControlAccount").
		Where("company_id = ?", companyID).
		Order("document_type asc, book_id asc, currency_code asc").
		Find(&mappings).Error; err != nil {
		return nil, fmt.Errorf("list AR/AP control mappings: %w", err)
	}
	return mappings, nil
}

// SeedDefaultARAPMappings creates the "default" (currency_code='') mappings for all
// standard document types using the company's current default AR and AP accounts.
// Idempotent — skips doc types that already have a default mapping.
func SeedDefaultARAPMappings(db *gorm.DB, companyID uint) error {
	// Find default AR account (first active accounts_receivable).
	var arAcc models.Account
	hasAR := db.Where("company_id = ? AND detail_account_type = ? AND is_active = true",
		companyID, string(models.DetailAccountsReceivable)).
		Order("code asc").First(&arAcc).Error == nil

	// Find default AP account.
	var apAcc models.Account
	hasAP := db.Where("company_id = ? AND detail_account_type = ? AND is_active = true",
		companyID, string(models.DetailAccountsPayable)).
		Order("code asc").First(&apAcc).Error == nil

	for _, dt := range models.AllARAPDocTypes() {
		side := models.ARAPDocTypeSide[dt]

		var accountID uint
		switch side {
		case "AR":
			if !hasAR {
				continue
			}
			accountID = arAcc.ID
		case "AP":
			if !hasAP {
				continue
			}
			accountID = apAcc.ID
		default:
			continue
		}

		// Skip if default already exists.
		var count int64
		db.Model(&models.ARAPControlMapping{}).
			Where("company_id = ? AND book_id = 0 AND document_type = ? AND currency_code = ''",
				companyID, string(dt)).Count(&count)
		if count > 0 {
			continue
		}

		m := models.ARAPControlMapping{
			CompanyID:        companyID,
			BookID:           0,
			DocumentType:     dt,
			CurrencyCode:     "",
			ControlAccountID: accountID,
			Notes:            "system default",
		}
		db.Create(&m) // best-effort; errors are non-fatal
	}
	return nil
}
