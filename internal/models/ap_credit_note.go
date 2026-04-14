// 遵循project_guide.md
package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ── VendorCreditNote status ───────────────────────────────────────────────────

// VendorCreditNoteStatus tracks the lifecycle of a vendor-issued credit note.
//
// draft             → created, not yet posted to accounting
// posted            → JE created: Dr AP / Cr Purchase Returns; full balance available
// partially_applied → part of the credit has been applied against bills
// fully_applied     → entire credit balance has been consumed
// voided            → cancelled; reversal JE created if was posted
//
// Posting creates formal accounting outcomes.
type VendorCreditNoteStatus string

const (
	VendorCreditNoteStatusDraft           VendorCreditNoteStatus = "draft"
	VendorCreditNoteStatusPosted          VendorCreditNoteStatus = "posted"
	VendorCreditNoteStatusPartiallyApplied VendorCreditNoteStatus = "partially_applied"
	VendorCreditNoteStatusFullyApplied    VendorCreditNoteStatus = "fully_applied"
	VendorCreditNoteStatusVoided          VendorCreditNoteStatus = "voided"
)

func AllVendorCreditNoteStatuses() []VendorCreditNoteStatus {
	return []VendorCreditNoteStatus{
		VendorCreditNoteStatusDraft,
		VendorCreditNoteStatusPosted,
		VendorCreditNoteStatusPartiallyApplied,
		VendorCreditNoteStatusFullyApplied,
		VendorCreditNoteStatusVoided,
	}
}

func VendorCreditNoteStatusLabel(s VendorCreditNoteStatus) string {
	switch s {
	case VendorCreditNoteStatusDraft:
		return "Draft"
	case VendorCreditNoteStatusPosted:
		return "Posted"
	case VendorCreditNoteStatusPartiallyApplied:
		return "Partially Applied"
	case VendorCreditNoteStatusFullyApplied:
		return "Fully Applied"
	case VendorCreditNoteStatusVoided:
		return "Voided"
	default:
		return string(s)
	}
}

// ── VendorCreditNote model ────────────────────────────────────────────────────

// VendorCreditNote records a credit issued by the vendor that reduces the
// company's AP balance. Distinct from VendorReturn (which is a physical
// return record) and VendorRefund (which is a cash receipt).
//
// Posting JE:
//   Dr  APAccountID       AmountBase   (reduces AP liability)
//   Cr  OffsetAccountID   AmountBase   (purchase returns / adjustments account)
//
// Applied against Bills by reducing BalanceDue.
// RemainingAmount = Amount - AppliedAmount.
type VendorCreditNote struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	CreditNoteNumber string                 `gorm:"not null;default:'';index"`
	VendorID         uint                   `gorm:"not null;index"`
	Vendor           Vendor                 `gorm:"foreignKey:VendorID"`
	Status           VendorCreditNoteStatus `gorm:"type:text;not null;default:'draft'"`

	CreditNoteDate time.Time `gorm:"not null"`

	// BillID optionally links back to the original bill.
	BillID *uint `gorm:"index"`
	Bill   *Bill `gorm:"foreignKey:BillID"`

	// VendorReturnID optionally links to the return that triggered this credit.
	VendorReturnID *uint         `gorm:"index"`
	VendorReturn   *VendorReturn `gorm:"foreignKey:VendorReturnID"`

	// APAccountID: AP liability account to debit (Dr) on posting.
	APAccountID *uint    `gorm:"index"`
	APAccount   *Account `gorm:"foreignKey:APAccountID"`

	// OffsetAccountID: purchase returns / adjustments account to credit (Cr) on posting.
	OffsetAccountID *uint    `gorm:"index"`
	OffsetAccount   *Account `gorm:"foreignKey:OffsetAccountID"`

	CurrencyCode string          `gorm:"type:varchar(3);not null;default:''"`
	ExchangeRate decimal.Decimal `gorm:"type:numeric(20,8);not null;default:1"`
	Amount       decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	AmountBase   decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	// Application tracking
	AppliedAmount   decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	RemainingAmount decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	Reason string `gorm:"type:text;not null;default:''"`
	Memo   string `gorm:"type:text;not null;default:''"`

	JournalEntryID *uint         `gorm:"index"`
	JournalEntry   *JournalEntry `gorm:"foreignKey:JournalEntryID"`

	PostedAt       *time.Time `gorm:"index"`
	PostedBy       string     `gorm:"type:text;not null;default:''"`
	PostedByUserID *uuid.UUID `gorm:"type:text"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
