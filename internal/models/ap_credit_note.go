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

	// Applications records each allocation of this credit note to a bill.
	Applications []APCreditApplication `gorm:"foreignKey:VendorCreditNoteID"`

	// Lines is the IN.6a line detail. Empty = legacy header-only
	// credit (Dr AP / Cr Offset posting). Non-empty = line-by-line
	// dispatch where stock-item lines are routed through the
	// Rule #4 inventory-reversal path.
	Lines []VendorCreditNoteLine `gorm:"foreignKey:VendorCreditNoteID"`

	PostedAt       *time.Time `gorm:"index"`
	PostedBy       string     `gorm:"type:text;not null;default:''"`
	PostedByUserID *uuid.UUID `gorm:"type:text"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ── VendorCreditNoteLine ──────────────────────────────────────────────────────

// VendorCreditNoteLine is IN.6a's line model. Each row represents
// one item being credited back (physical stock return OR a service
// adjustment).
//
// Dispatch at post time:
//   ProductService.IsStockItem=true  → stock return. Must carry
//       OriginalBillLineID so vendor_credit_note_posting.go can trace
//       the original Bill movement and book the return at the
//       authoritative snapshot cost (legacy mode). Under
//       receipt_required=true the post is rejected with
//       ErrVendorCreditNoteStockItemRequiresReturnReceipt.
//   ProductService.IsStockItem=false → service adjustment, no
//       inventory side-effect; goes through the existing Cr Offset
//       path unchanged.
type VendorCreditNoteLine struct {
	ID                 uint `gorm:"primaryKey"`
	CompanyID          uint `gorm:"not null;index"`
	VendorCreditNoteID uint `gorm:"not null;index"`

	SortOrder uint `gorm:"not null;default:1"`

	// ProductServiceID drives the stock/service dispatch.
	ProductServiceID *uint           `gorm:"index"`
	ProductService   *ProductService `gorm:"foreignKey:ProductServiceID"`

	// OriginalBillLineID — IN.6a trace back to the BillLine that
	// originally received this qty in. Required on stock-item lines
	// (IsStockItem=true); ignored otherwise. Drives the authoritative
	// cost lookup (original bill movement's unit_cost_base) for the
	// inventory-out movement.
	//
	// No DB-level FK constraint so mixed-company joins stay legal at
	// the schema layer. Cross-tenant + existence checks live in
	// vendor_credit_note_posting.go.
	OriginalBillLineID *uint     `gorm:"index"`
	OriginalBillLine   *BillLine `gorm:"foreignKey:OriginalBillLineID"`

	Description string          `gorm:"not null;default:''"`
	Qty         decimal.Decimal `gorm:"type:numeric(10,4);not null;default:1"`
	UnitPrice   decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`
	Amount      decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
