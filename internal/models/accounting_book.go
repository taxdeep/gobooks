// 遵循project_guide.md
package models

import "time"

// AccountingStandardProfileCode identifies a versioned accounting standard ruleset.
// Each code represents a specific version of a standard as it was defined at a
// point in time. New codes are added when standards are revised; existing codes
// are never mutated.
type AccountingStandardProfileCode string

const (
	// ProfileASPE2024 is the Accounting Standards for Private Enterprises (Canada),
	// 2024 edition. Default for new Canadian private companies.
	ProfileASPE2024 AccountingStandardProfileCode = "ASPE_2024"

	// ProfileIFRSIAS212025 is IFRS with IAS 21 (The Effects of Changes in Foreign
	// Exchange Rates) as amended up to 2025.
	ProfileIFRSIAS212025 AccountingStandardProfileCode = "IFRS_IAS21_2025"

	// ProfileIFRSIAS212027 reflects the 2023 IASB amendments to IAS 21 that are
	// mandatorily effective for periods beginning on or after 2027-01-01.
	// Key change: enhanced disclosure requirements for rate types and exchange
	// differences on the cash flow statement.
	ProfileIFRSIAS212027 AccountingStandardProfileCode = "IFRS_IAS21_2027"
)

// AccountingStandardProfile is a system-managed, versioned set of accounting rules.
// Rows are seeded by migration and may never be mutated by user code.
// New standard versions are added as new rows; old rows are never changed.
//
// Fields are intentionally kept minimal for Phase 0. Additional policy columns
// (RemeasurementScope, FXGainLossTreatment, ForeignOpMethod, etc.) will be added
// in Phase 3 when secondary IFRS books are implemented.
type AccountingStandardProfile struct {
	ID   uint                          `gorm:"primaryKey"`
	Code AccountingStandardProfileCode `gorm:"type:text;not null;uniqueIndex"`

	// DisplayName is a human-readable label shown in the UI.
	DisplayName string `gorm:"type:text;not null"`

	// EffectiveFrom is the earliest date from which this profile's rules apply.
	EffectiveFrom time.Time `gorm:"type:date;not null"`

	// IsSystem marks rows seeded by migration. System profiles may not be
	// deleted or modified by users.
	IsSystem bool `gorm:"not null;default:true"`

	CreatedAt time.Time
}

// AccountingBookType classifies the role of a book within a company.
type AccountingBookType string

const (
	// AccountingBookTypePrimary is the main ledger book. Every company has exactly one.
	AccountingBookTypePrimary AccountingBookType = "primary"

	// AccountingBookTypeSecondary is an optional parallel book for a different standard
	// (e.g. an IFRS secondary book alongside an ASPE primary). Phase 2+.
	AccountingBookTypeSecondary AccountingBookType = "secondary"

	// AccountingBookTypeAdjustment is an adjustment-only book that holds entries which
	// exist only in that book's context (e.g. GAAP → IFRS transition adjustments).
	AccountingBookTypeAdjustment AccountingBookType = "adjustment"

	// AccountingBookTypeTax is a tax-basis book. Phase 3+.
	AccountingBookTypeTax AccountingBookType = "tax"
)

// StandardChangePolicy governs what is permitted when switching a book's
// accounting standard profile. It is a domain value: do not derive it in a
// handler; call AccountingBook.ResolveStandardChangePolicy() instead.
type StandardChangePolicy string

const (
	// StandardChangePolicyAllowDirect — the standard profile may be changed immediately.
	// Applies when the book has no posted history.
	StandardChangePolicyAllowDirect StandardChangePolicy = "allow_direct"

	// StandardChangePolicyRequireWizard — standard change must go through the migration
	// wizard, which records a cutover date and optionally creates a secondary book.
	// Applies when the book has posted history but no closed/filed periods.
	StandardChangePolicyRequireWizard StandardChangePolicy = "require_wizard"

	// StandardChangePolicyForbidDirect — in-place standard change is forbidden.
	// The only legal options are: add a secondary book, or begin a future-dated cutover
	// from a new fiscal year start.
	// Applies when the book has closed periods, filed reports, or filed taxes.
	StandardChangePolicyForbidDirect StandardChangePolicy = "forbid_direct"
)

// AccountingBook is a per-company ledger book with its own accounting standard
// and functional currency. Every company has exactly one primary book; additional
// secondary books (IFRS, tax, management) are optional.
//
// The FunctionalCurrencyCode on the book is the authoritative functional currency
// for that book. For the primary book this matches Company.BaseCurrencyCode; for
// secondary books it may differ (e.g. a USD-functional subsidiary book).
//
// Multiple books do not mean multiple sources of truth. The source transaction is
// always singular; each book has its own independent accounted amounts,
// remeasurement JEs, and adjustment trail.
type AccountingBook struct {
	ID        uint               `gorm:"primaryKey"`
	CompanyID uint               `gorm:"not null;index"`

	// BookType is "primary" for the main ledger; other values are reserved for Phase 2+.
	BookType AccountingBookType `gorm:"type:text;not null;default:'primary'"`

	// FunctionalCurrencyCode is the ISO 4217 code of this book's functional currency.
	FunctionalCurrencyCode string `gorm:"type:varchar(3);not null"`

	// StandardProfileID links to the versioned accounting rules this book applies.
	StandardProfileID uint                       `gorm:"not null"`
	StandardProfile   AccountingStandardProfile  `gorm:"foreignKey:StandardProfileID"`

	// StandardChangePolicy encodes the current governance state for standard changes.
	// Resolved by ResolveStandardChangePolicy(); stored so the value is auditable
	// and survives across deploys without recomputation.
	StandardChangePolicy StandardChangePolicy `gorm:"type:text;not null;default:'allow_direct'"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ResolveStandardChangePolicy derives the correct StandardChangePolicy from the
// book's current state and persists it. This must be called whenever the book's
// history state changes (first post, first period close, first filed report).
//
// This is a domain method. It must never be bypassed in a handler.
func (b *AccountingBook) ResolveStandardChangePolicy(hasPostedHistory, hasClosedPeriods bool) StandardChangePolicy {
	switch {
	case hasClosedPeriods:
		b.StandardChangePolicy = StandardChangePolicyForbidDirect
	case hasPostedHistory:
		b.StandardChangePolicy = StandardChangePolicyRequireWizard
	default:
		b.StandardChangePolicy = StandardChangePolicyAllowDirect
	}
	return b.StandardChangePolicy
}

// CanChangeStandardDirectly reports whether the standard profile may be changed
// without a wizard or cutover process. Returns (allowed, reason).
// reason is empty when allowed is true.
func (b *AccountingBook) CanChangeStandardDirectly() (bool, string) {
	switch b.StandardChangePolicy {
	case StandardChangePolicyAllowDirect:
		return true, ""
	case StandardChangePolicyRequireWizard:
		return false, "accounting standard changes for this book require the guided migration wizard"
	case StandardChangePolicyForbidDirect:
		return false, "accounting standard changes are not permitted; use a secondary book or a future-dated cutover"
	default:
		return false, "unknown standard change policy"
	}
}
