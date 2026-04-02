// 遵循project_guide.md
package models

import (
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// PaymentTerm is a company-level master record defining how and when payment is due.
//
// Structure:
//
//	Code          — short identifier, unique within company (case-insensitive), e.g. "N30", "DOC", "N102%"
//	Description   — human-readable label, e.g. "Net 30"
//	DiscountDays  — days within which the early-payment discount applies (0 = no discount period)
//	DiscountPct   — early-payment discount percentage (0.00 = no discount)
//	NetDays       — days until the full amount is due (0 = due on receipt / cash)
//
// Business rules enforced by ValidatePaymentTerm:
//   - Code and Description must be non-empty after trimming
//   - DiscountDays >= 0, NetDays >= 0
//   - DiscountPct in [0, 100]
//   - If DiscountPct == 0 then DiscountDays must == 0
//   - If DiscountPct >  0 then DiscountDays must >  0
//   - If DiscountPct >  0 then DiscountDays must <= NetDays
//
// Exactly one PaymentTerm per company may have IsDefault == true.
// Inactive terms are excluded from new-document dropdowns but remain visible
// on historical documents via their stored snapshot.
type PaymentTerm struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	Code        string          `gorm:"type:text;not null"`
	Description string          `gorm:"type:text;not null;default:''"`
	DiscountDays int            `gorm:"not null;default:0"`
	DiscountPct  decimal.Decimal `gorm:"type:numeric(5,2);not null;default:0.00"`
	NetDays      int            `gorm:"not null;default:0"`

	IsDefault bool `gorm:"not null;default:false"`
	IsActive  bool `gorm:"not null;default:true"`
	SortOrder int  `gorm:"not null;default:0"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// DropdownLabel returns the text shown in editor dropdowns: "CODE — Description".
func (pt PaymentTerm) DropdownLabel() string {
	return pt.Code + " — " + pt.Description
}

// ValidatePaymentTerm validates the business rules for a PaymentTerm.
// Returns a non-nil error describing the first rule violation found.
func ValidatePaymentTerm(code, description string, discountDays int, discountPct decimal.Decimal, netDays int) error {
	if strings.TrimSpace(code) == "" {
		return fmt.Errorf("code is required")
	}
	if strings.TrimSpace(description) == "" {
		return fmt.Errorf("description is required")
	}
	if discountDays < 0 {
		return fmt.Errorf("discount days must be >= 0")
	}
	if netDays < 0 {
		return fmt.Errorf("net days must be >= 0")
	}
	if discountPct.LessThan(decimal.Zero) || discountPct.GreaterThan(decimal.NewFromInt(100)) {
		return fmt.Errorf("discount %% must be between 0 and 100")
	}
	if discountPct.IsZero() && discountDays != 0 {
		return fmt.Errorf("discount days must be 0 when discount %% is 0")
	}
	if !discountPct.IsZero() && discountDays == 0 {
		return fmt.Errorf("discount days must be > 0 when a discount %% is set")
	}
	if !discountPct.IsZero() && discountDays > netDays {
		return fmt.Errorf("discount days (%d) must be <= net days (%d)", discountDays, netDays)
	}
	return nil
}

// PaymentTermSnapshot holds an immutable copy of a PaymentTerm's key fields,
// embedded in Invoice and Bill to preserve historical accuracy.
// Stored as separate columns on the document; never updated after initial save.
type PaymentTermSnapshot struct {
	TermCode            string          `gorm:"column:term_code;type:text;not null;default:''"`
	TermDescription     string          `gorm:"column:term_description_snapshot;type:text;not null;default:''"`
	DiscountDaysSnapshot int            `gorm:"column:discount_days_snapshot;not null;default:0"`
	DiscountPctSnapshot  decimal.Decimal `gorm:"column:discount_pct_snapshot;type:numeric(5,2);not null;default:0.00"`
	NetDaysSnapshot      int            `gorm:"column:net_days_snapshot;not null;default:0"`
}

// BuildSnapshot constructs a PaymentTermSnapshot from a PaymentTerm master record.
func BuildSnapshot(pt PaymentTerm) PaymentTermSnapshot {
	return PaymentTermSnapshot{
		TermCode:             pt.Code,
		TermDescription:      pt.Description,
		DiscountDaysSnapshot: pt.DiscountDays,
		DiscountPctSnapshot:  pt.DiscountPct,
		NetDaysSnapshot:      pt.NetDays,
	}
}

// ComputeDueDate returns invoiceDate + netDays. Returns nil when netDays == 0
// (due on receipt / cash — no specific future due date required).
func ComputeDueDate(base time.Time, netDays int) *time.Time {
	if netDays <= 0 {
		return nil
	}
	d := base.AddDate(0, 0, netDays)
	return &d
}

// DefaultPaymentTermSeeds returns the five built-in payment terms seeded for
// every new company. The returned slice is ordered by SortOrder.
func DefaultPaymentTermSeeds() []PaymentTerm {
	return []PaymentTerm{
		{Code: "DOC", Description: "Delivery on Cash", DiscountDays: 0, DiscountPct: decimal.Zero, NetDays: 0, IsDefault: false, SortOrder: 1},
		{Code: "N15", Description: "Net 15", DiscountDays: 0, DiscountPct: decimal.Zero, NetDays: 15, IsDefault: false, SortOrder: 2},
		{Code: "N30", Description: "Net 30", DiscountDays: 0, DiscountPct: decimal.Zero, NetDays: 30, IsDefault: true, SortOrder: 3},
		{Code: "N60", Description: "Net 60", DiscountDays: 0, DiscountPct: decimal.Zero, NetDays: 60, IsDefault: false, SortOrder: 4},
		{Code: "N102%", Description: "2% discount if paid within 10 days; otherwise net 30", DiscountDays: 10, DiscountPct: decimal.NewFromFloat(2.00), NetDays: 30, IsDefault: false, SortOrder: 5},
	}
}
