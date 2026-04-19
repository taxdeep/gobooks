// 遵循project_guide.md
package models

import (
	"fmt"
	"time"
)

// EntityType is a strict enum for company type.
// Required by PROJECT_GUIDE: must not be a free-form string.
type EntityType string

const (
	EntityTypePersonal      EntityType = "Personal"
	EntityTypeIncorporated  EntityType = "Incorporated"
	EntityTypeLLP           EntityType = "LLP"
)

func (t EntityType) Valid() bool {
	switch t {
	case EntityTypePersonal, EntityTypeIncorporated, EntityTypeLLP:
		return true
	default:
		return false
	}
}

func ParseEntityType(s string) (EntityType, error) {
	t := EntityType(s)
	if !t.Valid() {
		return "", fmt.Errorf("invalid entity type: %q", s)
	}
	return t, nil
}

// BusinessType is a strict enum for high-level business type.
type BusinessType string

const (
	BusinessTypeRetail           BusinessType = "Retail"
	BusinessTypeProfessionalCorp BusinessType = "Professional Corp"
)

func (t BusinessType) Valid() bool {
	switch t {
	case BusinessTypeRetail, BusinessTypeProfessionalCorp:
		return true
	default:
		return false
	}
}

func ParseBusinessType(s string) (BusinessType, error) {
	t := BusinessType(s)
	if !t.Valid() {
		return "", fmt.Errorf("invalid business type: %q", s)
	}
	return t, nil
}

// Industry is a strict enum for a simple controlled industry list.
// This keeps the UI simple (dropdown) while keeping data clean.
type Industry string

const (
	IndustryRetail        Industry = "Retail"
	IndustryConsulting    Industry = "Consulting"
	IndustryServices      Industry = "Services"
	IndustryManufacturing Industry = "Manufacturing"
	IndustryConstruction  Industry = "Construction"
	IndustryOther         Industry = "Other"
)

func (i Industry) Valid() bool {
	switch i {
	case IndustryRetail,
		IndustryConsulting,
		IndustryServices,
		IndustryManufacturing,
		IndustryConstruction,
		IndustryOther:
		return true
	default:
		return false
	}
}

func ParseIndustry(s string) (Industry, error) {
	i := Industry(s)
	if !i.Valid() {
		return "", fmt.Errorf("invalid industry: %q", s)
	}
	return i, nil
}

// Company stores the company profile created during first-time setup.
// The setup wizard will create exactly one row for MVP.
type Company struct {
	ID uint `gorm:"primaryKey"`

	Name           string       `gorm:"not null"`
	EntityType     EntityType   `gorm:"type:text;not null"`
	BusinessType   BusinessType `gorm:"type:text;not null"`
	Industry       Industry     `gorm:"type:text;not null"`
	IncorporatedDate string     `gorm:"not null"`
	FiscalYearEnd  string       `gorm:"not null"` // keep as string for now; e.g. "12-31"
	BusinessNumber string       `gorm:"not null"`

	AddressLine string `gorm:"not null"`
	// City is required in app validation; DB default '' keeps AutoMigrate safe for existing rows.
	City       string `gorm:"type:text;not null;default:''"`
	Province   string `gorm:"not null"`
	PostalCode string `gorm:"not null"`
	Country     string `gorm:"not null"`

	// AccountCodeLength is the exact digit width for all chart of accounts codes (4–12). Default 4.
	AccountCodeLength int `gorm:"not null;default:4"`
	// AccountCodeLengthLocked is set true after initial COA import; length cannot be changed afterward.
	AccountCodeLengthLocked bool `gorm:"not null;default:false"`

	// LogoPath is the relative path to the uploaded company logo file.
	// Format: data/{companyID}/profile/logo.{ext}.
	// Empty string means no logo has been uploaded.
	LogoPath string `gorm:"type:text;not null;default:''"`

	// IsActive is set to false by SysAdmin to suspend a company without destroying data.
	// Existing members receive a 403 on login until reactivated.
	IsActive bool `gorm:"not null;default:true"`

	// Multi-currency support (Phase 1).
	// BaseCurrencyCode is the ISO 4217 code of the company's home currency (e.g. "CAD").
	// All reports and base-amount columns are denominated in this currency.
	BaseCurrencyCode string `gorm:"type:text;not null;default:'CAD'"`
	// MultiCurrencyEnabled gates all foreign-currency UI and posting logic.
	// false = base-currency-only mode (safe default for existing companies).
	MultiCurrencyEnabled bool `gorm:"not null;default:false"`

	// PrimaryBookID is the FK to this company's primary AccountingBook.
	// Nullable during Phase 0 migration; backfilled for all existing companies
	// by migrateCurrencyPhase6. New companies have this set at creation time.
	PrimaryBookID *uint `gorm:"index"`

	// Inventory costing method. Controls how unit costs are calculated on
	// inbound/outbound movements. Default: moving_average.
	// Once inventory movements exist for the company, this should not be changed.
	InventoryCostingMethod string `gorm:"type:text;not null;default:'moving_average'"`

	// TrackingEnabled is the company-level capability gate for
	// lot/serial/expiry tracking (Phase G slice G.1, migration 066).
	//
	// When FALSE (default), no item owned by this company may leave
	// tracking_mode='none'. ChangeTrackingMode rejects the transition
	// with ErrTrackingCapabilityNotEnabled, directing the caller to
	// the admin-level capability switch.
	//
	// When TRUE, per-item tracking_mode can be flipped subject to
	// the existing on-hand / layer-remaining guards.
	//
	// This field is the first concrete implementation of the
	// capability-gate pattern defined in INVENTORY_MODULE_API.md §F.7.
	// The remaining three gates (receipt_required, shipment_required,
	// manufacturing_enabled) land in their respective phases with the
	// same shape: conservative default, audited flip, no UI shortcut.
	TrackingEnabled bool `gorm:"not null;default:false"`

	// ReceiptRequired is the company-level capability rail for the
	// Phase H Receipt-first inbound model (slice H.1, migration 068).
	//
	// When FALSE (default), the company continues to run Phase G's
	// Bill-forms-inventory path unchanged — Bill post produces
	// inventory movements directly.
	//
	// When TRUE, the company has opted into the Phase H Receipt-first
	// model: Receipt posting produces inventory truth and accrues
	// Dr Inventory / Cr GR/IR; Bill posting handles AP only and
	// clears GR/IR against matching Receipts (with PPV on price
	// deltas).
	//
	// H.1 installs this field as a DORMANT RAIL. No handler reads it
	// yet; no Bill/Receipt behavior branches on it. Later slices
	// (H.3 Receipt post, H.4 Bill decoupling, H.5 matching + PPV)
	// become its consumers. Operational enablement is blocked until
	// H.5 closes — see INVENTORY_MODULE_API.md §Phase H Border 1.
	ReceiptRequired bool `gorm:"not null;default:false"`

	// GRIRClearingAccountID is the liability (clearing) account that
	// Phase H Receipt posting credits when it forms inventory truth
	// (migration 070, slice H.3). Bill posting under
	// receipt_required=true will later debit this same account in
	// H.5 when Bill ↔ Receipt matching clears the accrual.
	//
	// Nullable: only companies that opt into Receipt-first flow need
	// a value. When `receipt_required=true` is effective at PostReceipt
	// time, this must be set or PostReceipt fails with
	// ErrGRIRAccountNotConfigured. The configuration surface is
	// `services.ChangeCompanyGRIRClearingAccount`, audited in the
	// same family as the tracking-capability / receipt-required flips.
	GRIRClearingAccountID *uint `gorm:"column:gr_ir_clearing_account_id;index"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Inventory costing method enum values, kept alongside the column they
// represent so readers don't have to cross-reference services/inventory.
// New code should compare against these constants rather than string
// literals.
const (
	InventoryCostingMovingAverage = "moving_average"
	InventoryCostingFIFO          = "fifo"
)

