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
	// ErrPeriodAlreadyClosed is returned when trying to close a period that is not open.
	ErrPeriodAlreadyClosed = errors.New("fiscal period is already closed or locked")

	// ErrPeriodOverlap is returned when the proposed dates overlap an existing period.
	ErrPeriodOverlap = errors.New("period dates overlap an existing period for this book")

	// ErrPeriodInvalidRange is returned when period_start >= period_end.
	ErrPeriodInvalidRange = errors.New("period start must be before period end")
)

// CreateFiscalPeriodInput holds parameters for creating a new open fiscal period.
type CreateFiscalPeriodInput struct {
	CompanyID   uint
	BookID      uint // 0 = company-wide
	Label       string
	PeriodStart time.Time
	PeriodEnd   time.Time
}

// CreateFiscalPeriod adds a new open fiscal period. Validates label, date range,
// and non-overlap with existing periods for the same company+book combination.
func CreateFiscalPeriod(db *gorm.DB, in CreateFiscalPeriodInput) (*models.FiscalPeriod, error) {
	in.Label = strings.TrimSpace(in.Label)
	if in.Label == "" {
		return nil, errors.New("period label is required")
	}
	if !in.PeriodStart.Before(in.PeriodEnd) {
		return nil, ErrPeriodInvalidRange
	}

	// Overlap check: any period for the same company+book whose [start,end) overlaps.
	var count int64
	if err := db.Model(&models.FiscalPeriod{}).
		Where("company_id = ? AND book_id = ? AND period_start < ? AND period_end > ?",
			in.CompanyID, in.BookID, in.PeriodEnd, in.PeriodStart).
		Count(&count).Error; err != nil {
		return nil, fmt.Errorf("overlap check: %w", err)
	}
	if count > 0 {
		return nil, ErrPeriodOverlap
	}

	fp := models.FiscalPeriod{
		CompanyID:   in.CompanyID,
		BookID:      in.BookID,
		Label:       in.Label,
		PeriodStart: in.PeriodStart,
		PeriodEnd:   in.PeriodEnd,
		Status:      models.FiscalPeriodStatusOpen,
	}
	if err := db.Create(&fp).Error; err != nil {
		return nil, fmt.Errorf("create fiscal period: %w", err)
	}
	return &fp, nil
}

// CloseFiscalPeriod transitions a period from open → closed and then calls
// RefreshBookStandardChangePolicy so the book's policy is updated immediately.
func CloseFiscalPeriod(db *gorm.DB, companyID, periodID uint, actor string) error {
	var fp models.FiscalPeriod
	if err := db.Where("id = ? AND company_id = ?", periodID, companyID).
		First(&fp).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("fiscal period not found")
		}
		return fmt.Errorf("load fiscal period: %w", err)
	}
	if fp.Status != models.FiscalPeriodStatusOpen {
		return ErrPeriodAlreadyClosed
	}

	now := time.Now()
	if err := db.Model(&fp).Updates(map[string]any{
		"status":    string(models.FiscalPeriodStatusClosed),
		"closed_at": &now,
		"closed_by": actor,
	}).Error; err != nil {
		return fmt.Errorf("close fiscal period: %w", err)
	}

	// Refresh the book's StandardChangePolicy to ForbidDirect.
	if err := RefreshBookStandardChangePolicy(db, companyID, fp.BookID); err != nil {
		return fmt.Errorf("refresh book policy after period close: %w", err)
	}
	return nil
}

// ListFiscalPeriods returns all fiscal periods for the company, ordered by
// period_start descending (most recent first).
func ListFiscalPeriods(db *gorm.DB, companyID uint) ([]models.FiscalPeriod, error) {
	var fps []models.FiscalPeriod
	if err := db.Where("company_id = ?", companyID).
		Order("period_start desc").
		Find(&fps).Error; err != nil {
		return nil, fmt.Errorf("list fiscal periods: %w", err)
	}
	return fps, nil
}

// ListFiscalPeriodsForBook returns periods that apply to a specific book:
// company-wide periods (book_id=0) and book-specific periods (book_id=bookID).
func ListFiscalPeriodsForBook(db *gorm.DB, companyID, bookID uint) ([]models.FiscalPeriod, error) {
	var fps []models.FiscalPeriod
	if err := db.Where("company_id = ? AND (book_id = 0 OR book_id = ?)", companyID, bookID).
		Order("period_start desc").
		Find(&fps).Error; err != nil {
		return nil, fmt.Errorf("list fiscal periods for book: %w", err)
	}
	return fps, nil
}

// HasClosedPeriods returns true if any closed or locked period exists that
// applies to the given book (company-wide or book-specific).
func HasClosedPeriods(db *gorm.DB, companyID, bookID uint) (bool, error) {
	var count int64
	err := db.Model(&models.FiscalPeriod{}).
		Where("company_id = ? AND (book_id = 0 OR book_id = ?) AND status IN ?",
			companyID, bookID, []string{
				string(models.FiscalPeriodStatusClosed),
				string(models.FiscalPeriodStatusLocked),
			}).
		Count(&count).Error
	if err != nil {
		if isNoSuchTableError(err) {
			return false, nil
		}
		return false, fmt.Errorf("has closed periods: %w", err)
	}
	return count > 0, nil
}

// RefreshBookStandardChangePolicy recomputes and persists the StandardChangePolicy
// for books belonging to the company. If bookID > 0, only that book is updated;
// bookID == 0 refreshes all books for the company.
func RefreshBookStandardChangePolicy(db *gorm.DB, companyID, bookID uint) error {
	q := db.Where("company_id = ?", companyID)
	if bookID > 0 {
		q = q.Where("id = ?", bookID)
	}
	var books []models.AccountingBook
	if err := q.Find(&books).Error; err != nil {
		return fmt.Errorf("load books for policy refresh: %w", err)
	}

	// Check whether the company has any posted journal entries.
	var jeCount int64
	db.Model(&models.JournalEntry{}).Where("company_id = ?", companyID).Count(&jeCount)
	hasPostedHistory := jeCount > 0

	for i := range books {
		b := &books[i]
		hasClosed, _ := HasClosedPeriods(db, companyID, b.ID)
		b.ResolveStandardChangePolicy(hasPostedHistory, hasClosed)
		if err := db.Model(b).
			Update("standard_change_policy", string(b.StandardChangePolicy)).Error; err != nil {
			return fmt.Errorf("update book %d standard_change_policy: %w", b.ID, err)
		}
	}
	return nil
}
