// 遵循project_guide.md
package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// ── VendorReturn status ───────────────────────────────────────────────────────

// VendorReturnStatus tracks the lifecycle of a vendor return.
//
// draft      → created, not yet submitted to vendor
// submitted  → sent to vendor for acknowledgement
// approved   → vendor acknowledged the return
// processed  → physically returned and confirmed
// cancelled  → abandoned before processing
//
// VendorReturns do NOT create formal accounting entries.
// A posted VendorCreditNote is required for the accounting adjustment.
type VendorReturnStatus string

const (
	VendorReturnStatusDraft     VendorReturnStatus = "draft"
	VendorReturnStatusSubmitted VendorReturnStatus = "submitted"
	VendorReturnStatusApproved  VendorReturnStatus = "approved"
	VendorReturnStatusProcessed VendorReturnStatus = "processed"
	VendorReturnStatusCancelled VendorReturnStatus = "cancelled"
)

func AllVendorReturnStatuses() []VendorReturnStatus {
	return []VendorReturnStatus{
		VendorReturnStatusDraft,
		VendorReturnStatusSubmitted,
		VendorReturnStatusApproved,
		VendorReturnStatusProcessed,
		VendorReturnStatusCancelled,
	}
}

func VendorReturnStatusLabel(s VendorReturnStatus) string {
	switch s {
	case VendorReturnStatusDraft:
		return "Draft"
	case VendorReturnStatusSubmitted:
		return "Submitted"
	case VendorReturnStatusApproved:
		return "Approved"
	case VendorReturnStatusProcessed:
		return "Processed"
	case VendorReturnStatusCancelled:
		return "Cancelled"
	default:
		return string(s)
	}
}

// ── VendorReturn model ────────────────────────────────────────────────────────

// VendorReturn records the intent and status of returning goods or services
// to a vendor. It is a pure business object with no direct accounting impact.
// Accounting adjustment is handled by a linked VendorCreditNote.
type VendorReturn struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	ReturnNumber string             `gorm:"not null;default:'';index"`
	VendorID     uint               `gorm:"not null;index"`
	Vendor       Vendor             `gorm:"foreignKey:VendorID"`
	Status       VendorReturnStatus `gorm:"type:text;not null;default:'draft'"`

	ReturnDate time.Time `gorm:"not null"`

	// BillID links back to the original purchase bill (optional).
	BillID *uint `gorm:"index"`
	Bill   *Bill `gorm:"foreignKey:BillID"`

	CurrencyCode string          `gorm:"type:varchar(3);not null;default:''"`
	Amount       decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	Reason string `gorm:"type:text;not null;default:''"`
	Memo   string `gorm:"type:text;not null;default:''"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
