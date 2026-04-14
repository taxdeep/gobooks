// 遵循project_guide.md
package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ── VendorPrepayment status ───────────────────────────────────────────────────

// VendorPrepaymentStatus tracks the lifecycle of a vendor prepayment.
//
// draft    → created, not yet posted to accounting
// posted   → JE created: Dr VendorPrepaymentAsset / Cr Bank
// applied  → fully applied against one or more bills
// voided   → cancelled draft; no JE
//
// Posting creates formal accounting outcomes.
type VendorPrepaymentStatus string

const (
	VendorPrepaymentStatusDraft   VendorPrepaymentStatus = "draft"
	VendorPrepaymentStatusPosted  VendorPrepaymentStatus = "posted"
	VendorPrepaymentStatusApplied VendorPrepaymentStatus = "applied"
	VendorPrepaymentStatusVoided  VendorPrepaymentStatus = "voided"
)

func AllVendorPrepaymentStatuses() []VendorPrepaymentStatus {
	return []VendorPrepaymentStatus{
		VendorPrepaymentStatusDraft,
		VendorPrepaymentStatusPosted,
		VendorPrepaymentStatusApplied,
		VendorPrepaymentStatusVoided,
	}
}

func VendorPrepaymentStatusLabel(s VendorPrepaymentStatus) string {
	switch s {
	case VendorPrepaymentStatusDraft:
		return "Draft"
	case VendorPrepaymentStatusPosted:
		return "Posted"
	case VendorPrepaymentStatusApplied:
		return "Applied"
	case VendorPrepaymentStatusVoided:
		return "Voided"
	default:
		return string(s)
	}
}

// ── VendorPrepayment model ────────────────────────────────────────────────────

// VendorPrepayment records an advance payment made to a vendor before a bill
// is received. It is an asset (prepaid expense / vendor deposit) until applied.
//
// Posting JE:
//   Dr  PrepaymentAccountID   AmountBase   (vendor prepayment asset)
//   Cr  BankAccountID         AmountBase   (cash/bank outflow)
//
// Applied when: a Bill is paid and the prepayment balance is drawn down.
// Remaining balance tracked via RemainingAmount (updated on each application).
type VendorPrepayment struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	PrepaymentNumber string                 `gorm:"not null;default:'';index"`
	VendorID         uint                   `gorm:"not null;index"`
	Vendor           Vendor                 `gorm:"foreignKey:VendorID"`
	Status           VendorPrepaymentStatus `gorm:"type:text;not null;default:'draft'"`

	PrepaymentDate time.Time `gorm:"not null"`

	CurrencyCode string          `gorm:"type:varchar(3);not null;default:''"`
	ExchangeRate decimal.Decimal `gorm:"type:numeric(20,8);not null;default:1"`
	Amount       decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"` // document currency
	AmountBase   decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"` // base currency

	// Application tracking
	AppliedAmount   decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	RemainingAmount decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	// BankAccountID: cash/bank account credited on posting (where money went out).
	BankAccountID *uint    `gorm:"index"`
	BankAccount   *Account `gorm:"foreignKey:BankAccountID"`

	// PrepaymentAccountID: asset account debited on posting (Vendor Prepayments).
	PrepaymentAccountID *uint    `gorm:"index"`
	PrepaymentAccount   *Account `gorm:"foreignKey:PrepaymentAccountID"`

	PaymentMethod PaymentMethod `gorm:"type:text;not null;default:'other'"`
	Reference     string        `gorm:"type:text;not null;default:''"`
	Memo          string        `gorm:"type:text;not null;default:''"`

	// PurchaseOrderID optionally links this prepayment to a PO.
	PurchaseOrderID *uint          `gorm:"index"`
	PurchaseOrder   *PurchaseOrder `gorm:"foreignKey:PurchaseOrderID"`

	JournalEntryID *uint         `gorm:"index"`
	JournalEntry   *JournalEntry `gorm:"foreignKey:JournalEntryID"`

	PostedAt       *time.Time `gorm:"index"`
	PostedBy       string     `gorm:"type:text;not null;default:''"`
	PostedByUserID *uuid.UUID `gorm:"type:text"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
