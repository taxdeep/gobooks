// 遵循project_guide.md
package models

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// CurrencyMode controls how an account denominates amounts.
type CurrencyMode string

const (
	// CurrencyModeBaseOnly — all amounts are recorded in the company's base currency (default).
	CurrencyModeBaseOnly CurrencyMode = "base_only"
	// CurrencyModeFixedForeign — the account is denominated in a specific foreign currency.
	// Both foreign amounts and base-equivalent amounts must be recorded.
	CurrencyModeFixedForeign CurrencyMode = "fixed_foreign"
)

// Currency is the global ISO 4217 currency dictionary. Not per-company.
// Code (e.g. "CAD", "USD") is the natural primary key.
type Currency struct {
	Code          string `gorm:"primaryKey;size:3"`
	Name          string `gorm:"not null"`
	Symbol        string `gorm:"not null;default:''"`
	DecimalPlaces int    `gorm:"not null;default:2"`
	IsActive      bool   `gorm:"not null;default:true"`
}

// CompanyCurrency records which currencies a company actively uses in addition
// to its base currency. Uniqueness is enforced on (company_id, currency_code).
type CompanyCurrency struct {
	ID           uint   `gorm:"primaryKey"`
	CompanyID    uint   `gorm:"not null;uniqueIndex:uq_company_currency"`
	CurrencyCode string `gorm:"size:3;not null;uniqueIndex:uq_company_currency"`
	IsActive     bool   `gorm:"not null;default:true"`
}

// ValidateCurrencyMode checks that CurrencyMode and CurrencyCode are mutually consistent.
// Rules:
//   - base_only (or zero value) → CurrencyCode must be nil or empty.
//   - fixed_foreign              → CurrencyCode must be a 3-uppercase-letter ISO 4217 code.
func ValidateCurrencyMode(mode CurrencyMode, currencyCode *string) error {
	switch mode {
	case CurrencyModeBaseOnly, "":
		if currencyCode != nil && *currencyCode != "" {
			return fmt.Errorf("currency_code must be empty when currency_mode is base_only")
		}
	case CurrencyModeFixedForeign:
		if currencyCode == nil || *currencyCode == "" {
			return fmt.Errorf("currency_code is required when currency_mode is fixed_foreign")
		}
		code := *currencyCode
		if len(code) != 3 {
			return fmt.Errorf("currency_code must be exactly 3 characters (ISO 4217)")
		}
		for _, r := range code {
			if r < 'A' || r > 'Z' {
				return fmt.Errorf("currency_code must contain uppercase letters only (ISO 4217)")
			}
		}
	default:
		return fmt.Errorf("invalid currency_mode %q: must be %q or %q",
			mode, CurrencyModeBaseOnly, CurrencyModeFixedForeign)
	}
	return nil
}

// ExchangeRate stores a dated rate between two currencies.
//
// CompanyID is nullable:
//   - NULL      → system/shared rate available to all companies
//   - non-null  → company-specific override; takes precedence during lookup
//
// Lookup order (implemented in currency_service.GetExchangeRate):
//
//	company override exact → company override nearest prior
//	→ system exact → system nearest prior → ErrNoRate
type ExchangeRate struct {
	ID                 uint            `gorm:"primaryKey"`
	CompanyID          *uint           `gorm:"index"`
	BaseCurrencyCode   string          `gorm:"size:3;not null"`
	TargetCurrencyCode string          `gorm:"size:3;not null"`
	// Rate is stored as NUMERIC(20,8) — never float64.
	Rate          decimal.Decimal `gorm:"type:numeric(20,8);not null"`
	// RateType is one of: "spot", "average", "custom".
	RateType      string    `gorm:"not null;default:'spot'"`
	Source        string    `gorm:"not null;default:''"`
	EffectiveDate time.Time `gorm:"type:date;not null;index"`
	CreatedAt     time.Time
}
