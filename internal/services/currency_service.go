// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ErrNoRate is returned by GetExchangeRate when no applicable rate exists
// for the given currency pair, scope, and date.
var ErrNoRate = errors.New("no exchange rate found")

// GetExchangeRate returns the best available exchange rate for the currency pair
// on the given date, using the following priority order:
//
//  1. Company-specific override — exact date match
//  2. Company-specific override — nearest rate on or before date
//  3. System rate (company_id IS NULL) — exact date match
//  4. System rate (company_id IS NULL) — nearest rate on or before date
//
// companyID may be nil when only system rates should be considered.
// Returns ErrNoRate when no applicable rate is found.
// Rate values are stored as NUMERIC(20,8); decimal.Decimal is used throughout —
// no float64 conversion is performed.
func GetExchangeRate(db *gorm.DB, companyID *uint, base, target string, date time.Time) (decimal.Decimal, error) {
	// Normalise to start-of-day UTC so time-of-day does not affect date comparisons.
	day := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)

	// 1 & 2: company-specific override (skipped when companyID is nil).
	if companyID != nil {
		if r, err := lookupRate(db, companyID, base, target, day); err == nil {
			return r, nil
		}
	}

	// 3 & 4: system rate.
	if r, err := lookupRate(db, nil, base, target, day); err == nil {
		return r, nil
	}

	return decimal.Zero, ErrNoRate
}

// lookupRate queries for the most recent rate on or before day within the given
// company scope (nil = system rates only).
func lookupRate(db *gorm.DB, companyID *uint, base, target string, day time.Time) (decimal.Decimal, error) {
	q := db.Model(&models.ExchangeRate{}).
		Where("base_currency_code = ? AND target_currency_code = ?", base, target).
		Where("effective_date <= ?", day)

	if companyID == nil {
		q = q.Where("company_id IS NULL")
	} else {
		q = q.Where("company_id = ?", *companyID)
	}

	var er models.ExchangeRate
	if err := q.Order("effective_date DESC").First(&er).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return decimal.Zero, ErrNoRate
		}
		return decimal.Zero, err
	}
	return er.Rate, nil
}

// ListActiveCurrencies returns all active currencies from the global dictionary,
// ordered by code ascending.
func ListActiveCurrencies(db *gorm.DB) ([]models.Currency, error) {
	var currencies []models.Currency
	err := db.Where("is_active = true").Order("code asc").Find(&currencies).Error
	return currencies, err
}

// ListCompanyCurrencies returns all company_currencies rows for a company.
func ListCompanyCurrencies(db *gorm.DB, companyID uint) ([]models.CompanyCurrency, error) {
	var rows []models.CompanyCurrency
	err := db.Where("company_id = ?", companyID).Find(&rows).Error
	return rows, err
}

// ── Exchange rate CRUD ────────────────────────────────────────────────────────

// UpsertExchangeRateInput carries fields for creating or updating an exchange rate.
type UpsertExchangeRateInput struct {
	// CompanyID nil = system/shared rate; non-nil = company-specific override.
	CompanyID *uint
	Base      string
	Target    string
	// Rate must be positive and stored as NUMERIC(20,8) — never float64.
	Rate     decimal.Decimal
	RateType string // "spot" | "average" | "custom"; defaults to "spot"
	Source   string
	Date     time.Time
}

// UpsertExchangeRate creates a new exchange rate or updates the rate/source of an
// existing row that matches (companyID, base, target, rateType, effectiveDate).
// Rate must be strictly positive. Base and Target must differ.
func UpsertExchangeRate(db *gorm.DB, in UpsertExchangeRateInput) (models.ExchangeRate, error) {
	if in.Base == "" || in.Target == "" {
		return models.ExchangeRate{}, fmt.Errorf("base and target currencies are required")
	}
	if in.Base == in.Target {
		return models.ExchangeRate{}, fmt.Errorf("base and target currencies must differ")
	}
	if in.Rate.IsNegative() || in.Rate.IsZero() {
		return models.ExchangeRate{}, fmt.Errorf("exchange rate must be positive")
	}

	rateType := in.RateType
	if rateType == "" {
		rateType = "spot"
	}
	day := time.Date(in.Date.Year(), in.Date.Month(), in.Date.Day(), 0, 0, 0, 0, time.UTC)

	q := db.Model(&models.ExchangeRate{}).
		Where("base_currency_code = ? AND target_currency_code = ? AND rate_type = ? AND effective_date = ?",
			in.Base, in.Target, rateType, day)
	if in.CompanyID == nil {
		q = q.Where("company_id IS NULL")
	} else {
		q = q.Where("company_id = ?", *in.CompanyID)
	}

	var er models.ExchangeRate
	err := q.First(&er).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		er = models.ExchangeRate{
			CompanyID:          in.CompanyID,
			BaseCurrencyCode:   in.Base,
			TargetCurrencyCode: in.Target,
			Rate:               in.Rate,
			RateType:           rateType,
			Source:             in.Source,
			EffectiveDate:      day,
		}
		return er, db.Create(&er).Error
	}
	if err != nil {
		return er, err
	}
	// Update in-place.
	err = db.Model(&er).Updates(map[string]any{
		"rate":   in.Rate,
		"source": in.Source,
	}).Error
	return er, err
}

// ListExchangeRates returns all rates for a currency pair in the given company
// scope, ordered by effective_date DESC. Pass companyID=nil for system rates.
func ListExchangeRates(db *gorm.DB, companyID *uint, base, target string) ([]models.ExchangeRate, error) {
	q := db.Where("base_currency_code = ? AND target_currency_code = ?", base, target)
	if companyID == nil {
		q = q.Where("company_id IS NULL")
	} else {
		q = q.Where("company_id = ?", *companyID)
	}
	var rates []models.ExchangeRate
	err := q.Order("effective_date DESC").Find(&rates).Error
	return rates, err
}

// DeleteExchangeRate deletes one exchange rate row by ID, scoped to companyID
// (nil = system rates). Returns an error if the row is not found in that scope.
func DeleteExchangeRate(db *gorm.DB, companyID *uint, id uint) error {
	q := db.Where("id = ?", id)
	if companyID == nil {
		q = q.Where("company_id IS NULL")
	} else {
		q = q.Where("company_id = ?", *companyID)
	}
	result := q.Delete(&models.ExchangeRate{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("exchange rate not found")
	}
	return nil
}

// EnsureCompanyCurrency upserts a currency into the company_currencies table for
// a company, marking it active. Safe to call multiple times (idempotent).
func EnsureCompanyCurrency(db *gorm.DB, companyID uint, code string) error {
	var cc models.CompanyCurrency
	err := db.Where("company_id = ? AND currency_code = ?", companyID, code).First(&cc).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		cc = models.CompanyCurrency{
			CompanyID:    companyID,
			CurrencyCode: code,
			IsActive:     true,
		}
		return db.Create(&cc).Error
	}
	if err != nil {
		return err
	}
	if !cc.IsActive {
		return db.Model(&cc).Update("is_active", true).Error
	}
	return nil
}
