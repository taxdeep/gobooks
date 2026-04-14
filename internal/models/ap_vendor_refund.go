// 遵循project_guide.md
package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ── VendorRefund status ───────────────────────────────────────────────────────

// VendorRefundStatus tracks the lifecycle of a vendor refund.
//
// draft    → created, not yet posted to accounting
// posted   → JE created: Dr Bank / Cr PrepaymentAsset or AP
// voided   → cancelled draft; no JE
// reversed → posted refund reversed; reversal JE created
//
// Posting creates formal accounting outcomes.
type VendorRefundStatus string

const (
	VendorRefundStatusDraft    VendorRefundStatus = "draft"
	VendorRefundStatusPosted   VendorRefundStatus = "posted"
	VendorRefundStatusVoided   VendorRefundStatus = "voided"
	VendorRefundStatusReversed VendorRefundStatus = "reversed"
)

func AllVendorRefundStatuses() []VendorRefundStatus {
	return []VendorRefundStatus{
		VendorRefundStatusDraft,
		VendorRefundStatusPosted,
		VendorRefundStatusVoided,
		VendorRefundStatusReversed,
	}
}

func VendorRefundStatusLabel(s VendorRefundStatus) string {
	switch s {
	case VendorRefundStatusDraft:
		return "Draft"
	case VendorRefundStatusPosted:
		return "Posted"
	case VendorRefundStatusVoided:
		return "Voided"
	case VendorRefundStatusReversed:
		return "Reversed"
	default:
		return string(s)
	}
}

// ── VendorRefundSourceType ────────────────────────────────────────────────────

// VendorRefundSourceType identifies what the vendor is refunding.
type VendorRefundSourceType string

const (
	VendorRefundSourcePrepayment VendorRefundSourceType = "prepayment"  // returning unused prepayment
	VendorRefundSourceCreditNote VendorRefundSourceType = "credit_note" // converting credit note to cash
	VendorRefundSourceOther      VendorRefundSourceType = "other"
)

// ── VendorRefund model ────────────────────────────────────────────────────────

// VendorRefund records cash received back from a vendor.
// Distinct from VendorCreditNote (which is an AP balance reduction).
// Distinct from VendorReturn (which is a physical return).
//
// Posting JE:
//   Dr  BankAccountID    AmountBase   (cash received)
//   Cr  CreditAccountID  AmountBase   (prepayment asset or AP account)
type VendorRefund struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	RefundNumber string             `gorm:"not null;default:'';index"`
	VendorID     uint               `gorm:"not null;index"`
	Vendor       Vendor             `gorm:"foreignKey:VendorID"`
	Status       VendorRefundStatus `gorm:"type:text;not null;default:'draft'"`

	SourceType VendorRefundSourceType `gorm:"type:text;not null;default:'other'"`

	// Optional links to source documents
	VendorPrepaymentID *uint             `gorm:"index"`
	VendorPrepayment   *VendorPrepayment `gorm:"foreignKey:VendorPrepaymentID"`

	VendorCreditNoteID *uint             `gorm:"index"`
	VendorCreditNote   *VendorCreditNote `gorm:"foreignKey:VendorCreditNoteID"`

	RefundDate time.Time `gorm:"not null"`

	CurrencyCode string          `gorm:"type:varchar(3);not null;default:''"`
	ExchangeRate decimal.Decimal `gorm:"type:numeric(20,8);not null;default:1"`
	Amount       decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	AmountBase   decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	// BankAccountID: cash/bank account debited on posting (where money came in).
	BankAccountID *uint    `gorm:"index"`
	BankAccount   *Account `gorm:"foreignKey:BankAccountID"`

	// CreditAccountID: account credited on posting.
	// For prepayment refund: the prepayment asset account.
	// For credit note refund: the AP account.
	CreditAccountID *uint    `gorm:"index"`
	CreditAccount   *Account `gorm:"foreignKey:CreditAccountID"`

	PaymentMethod PaymentMethod `gorm:"type:text;not null;default:'other'"`
	Reference     string        `gorm:"type:text;not null;default:''"`
	Memo          string        `gorm:"type:text;not null;default:''"`

	JournalEntryID *uint         `gorm:"index"`
	JournalEntry   *JournalEntry `gorm:"foreignKey:JournalEntryID"`

	PostedAt       *time.Time `gorm:"index"`
	PostedBy       string     `gorm:"type:text;not null;default:''"`
	PostedByUserID *uuid.UUID `gorm:"type:text"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
