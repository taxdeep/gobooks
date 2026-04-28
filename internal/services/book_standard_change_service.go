// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"balanciz/internal/models"
)

var (
	// ErrStandardChangeNotAllowed is returned when the book's policy is ForbidDirect.
	ErrStandardChangeNotAllowed = errors.New("accounting standard change not permitted: book has closed periods")

	// ErrSameStandardProfile is returned when the new profile matches the current one.
	ErrSameStandardProfile = errors.New("new standard profile is the same as the current profile")
)

// ChangeBookStandardInput holds the parameters for a standard-profile change.
type ChangeBookStandardInput struct {
	CompanyID      uint
	BookID         uint
	NewProfileCode models.AccountingStandardProfileCode

	// CutoverDate is required when the book's policy is RequireWizard.
	// It represents the first day from which the new standard applies.
	CutoverDate *time.Time

	// Notes is optional free-text rationale, captured by the wizard.
	Notes string

	// Actor is the email / user who performed the change.
	Actor string
}

// ChangeBookStandard applies a standard-profile change to an accounting book.
// The method (direct / wizard) is determined by the book's current
// StandardChangePolicy (refreshed immediately before the change is applied):
//
//   - AllowDirect → updates immediately; records a direct-method audit entry.
//   - RequireWizard → requires CutoverDate; records a wizard-method audit entry.
//   - ForbidDirect → returns ErrStandardChangeNotAllowed.
func ChangeBookStandard(db *gorm.DB, in ChangeBookStandardInput) error {
	// Reload and refresh policy first.
	if err := RefreshBookStandardChangePolicy(db, in.CompanyID, in.BookID); err != nil {
		return fmt.Errorf("refresh book policy: %w", err)
	}

	var book models.AccountingBook
	if err := db.Preload("StandardProfile").
		Where("id = ? AND company_id = ?", in.BookID, in.CompanyID).
		First(&book).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("accounting book not found")
		}
		return fmt.Errorf("load book: %w", err)
	}

	if book.StandardChangePolicy == models.StandardChangePolicyForbidDirect {
		return ErrStandardChangeNotAllowed
	}

	// Load new profile.
	var newProfile models.AccountingStandardProfile
	if err := db.Where("code = ?", string(in.NewProfileCode)).First(&newProfile).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("accounting standard profile %q not found", in.NewProfileCode)
		}
		return fmt.Errorf("load new profile: %w", err)
	}

	if newProfile.ID == book.StandardProfileID {
		return ErrSameStandardProfile
	}

	method := models.BookStandardChangeMethodDirect
	if book.StandardChangePolicy == models.StandardChangePolicyRequireWizard {
		method = models.BookStandardChangeMethodWizard
		if in.CutoverDate == nil {
			return errors.New("cutover date is required when changing standard via the migration wizard")
		}
	}

	return db.Transaction(func(tx *gorm.DB) error {
		change := models.BookStandardChange{
			CompanyID:    in.CompanyID,
			BookID:       in.BookID,
			OldProfileID: book.StandardProfileID,
			NewProfileID: newProfile.ID,
			Method:       method,
			CutoverDate:  in.CutoverDate,
			Notes:        strings.TrimSpace(in.Notes),
			ChangedBy:    in.Actor,
		}
		if err := tx.Create(&change).Error; err != nil {
			return fmt.Errorf("record standard change audit: %w", err)
		}

		if err := tx.Model(&book).
			Update("standard_profile_id", newProfile.ID).Error; err != nil {
			return fmt.Errorf("update book standard profile: %w", err)
		}
		return nil
	})
}

// ListBookStandardChanges returns the full audit history for a book, newest first.
func ListBookStandardChanges(db *gorm.DB, companyID, bookID uint) ([]models.BookStandardChange, error) {
	var changes []models.BookStandardChange
	if err := db.
		Preload("OldProfile").Preload("NewProfile").
		Where("company_id = ? AND book_id = ?", companyID, bookID).
		Order("created_at desc").
		Find(&changes).Error; err != nil {
		return nil, fmt.Errorf("list book standard changes: %w", err)
	}
	return changes, nil
}
