// 遵循project_guide.md
package models

// ar_receipt.go — CustomerReceipt: AR 侧正式收款对象。
//
// CustomerReceipt 是 AR 模块的正式收款凭证。它记录"收到了钱"这个事实，
// 并持有状态机，允许后续 apply / unapply / reverse / void 操作。
//
// 注意：现有 PaymentReceipt（legacy）保留不动。CustomerReceipt 是新的
// 正式 AR 对象，Phase 4 实现完整 posting + application 闭环。
//
// 会计规则（Phase 4 实现正式 posting）：
//
//	过账时：
//	  Dr  Cash / Bank / Clearing
//	  Cr  AR (Accounts Receivable)
//
// Payment Gateway 只是 payment method，不直接决定 CustomerReceipt 的会计真相。
//
// 状态机：
//
//	draft → confirmed → unapplied
//	                         ↓ partially_applied
//	                         ↓ fully_applied
//	                         ↓ reversed
//	       ↘ voided (from draft only)

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// CustomerReceiptStatus tracks the lifecycle of a customer receipt.
type CustomerReceiptStatus string

const (
	// CustomerReceiptStatusDraft — created, not yet confirmed (pre-posting).
	CustomerReceiptStatusDraft CustomerReceiptStatus = "draft"

	// CustomerReceiptStatusConfirmed — posted to accounting; JE created.
	// Funds are on the books; may be unapplied (not yet matched to an Invoice).
	CustomerReceiptStatusConfirmed CustomerReceiptStatus = "confirmed"

	// CustomerReceiptStatusPartiallyApplied — some amount applied to invoice(s); remainder open.
	CustomerReceiptStatusPartiallyApplied CustomerReceiptStatus = "partially_applied"

	// CustomerReceiptStatusFullyApplied — all receipt amount applied; no unapplied cash.
	CustomerReceiptStatusFullyApplied CustomerReceiptStatus = "fully_applied"

	// CustomerReceiptStatusReversed — a reversal JE has been posted; receipt is cancelled.
	// Differs from Voided: Reversed means it was once confirmed, then reversed.
	CustomerReceiptStatusReversed CustomerReceiptStatus = "reversed"

	// CustomerReceiptStatusVoided — cancelled before confirmation; no JE was ever created.
	CustomerReceiptStatusVoided CustomerReceiptStatus = "voided"
)

// AllCustomerReceiptStatuses returns statuses in display order.
func AllCustomerReceiptStatuses() []CustomerReceiptStatus {
	return []CustomerReceiptStatus{
		CustomerReceiptStatusDraft,
		CustomerReceiptStatusConfirmed,
		CustomerReceiptStatusPartiallyApplied,
		CustomerReceiptStatusFullyApplied,
		CustomerReceiptStatusReversed,
		CustomerReceiptStatusVoided,
	}
}

// CustomerReceiptStatusLabel returns a human-readable label.
func CustomerReceiptStatusLabel(s CustomerReceiptStatus) string {
	switch s {
	case CustomerReceiptStatusDraft:
		return "Draft"
	case CustomerReceiptStatusConfirmed:
		return "Confirmed (Unapplied)"
	case CustomerReceiptStatusPartiallyApplied:
		return "Partially Applied"
	case CustomerReceiptStatusFullyApplied:
		return "Fully Applied"
	case CustomerReceiptStatusReversed:
		return "Reversed"
	case CustomerReceiptStatusVoided:
		return "Voided"
	default:
		return string(s)
	}
}

// CustomerReceipt is the formal AR-side record of money received from a customer.
//
// It is the authoritative AR truth for a cash receipt. PaymentApplication records
// (separate object) track how the receipt amount is matched to Invoice(s).
//
// Phase 1 establishes the model skeleton. Phase 4 implements posting + application.
//
// GatewayTransactionID is informational only — it records which external payment
// gateway transaction (if any) triggered this receipt, but the gateway does NOT
// determine the accounting truth. Accounting truth is determined by posting.
type CustomerReceipt struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	CustomerID uint     `gorm:"not null;index"`
	Customer   Customer `gorm:"foreignKey:CustomerID"`

	// JournalEntryID is set when the receipt is confirmed (posted).
	JournalEntryID *uint         `gorm:"uniqueIndex"`
	JournalEntry   *JournalEntry `gorm:"foreignKey:JournalEntryID"`

	// BankAccountID is the cash/bank/clearing account that received the funds.
	BankAccountID *uint    `gorm:"index"`
	BankAccount   *Account `gorm:"foreignKey:BankAccountID"`

	ReceiptNumber string                `gorm:"type:varchar(50);not null;default:''"`
	Status        CustomerReceiptStatus `gorm:"type:text;not null;default:'draft'"`
	ReceiptDate   time.Time             `gorm:"not null"`

	// CurrencyCode — the currency of the payment received.
	CurrencyCode string          `gorm:"type:varchar(3);not null;default:''"`
	ExchangeRate decimal.Decimal `gorm:"type:numeric(18,8);not null;default:1"`

	// Amount is the document-currency total received.
	Amount decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	// AmountBase is the base-currency equivalent at time of posting.
	AmountBase decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	// UnappliedAmount is the document-currency amount not yet matched to invoices.
	UnappliedAmount decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	PaymentMethod PaymentMethod `gorm:"type:text;not null;default:'other'"`
	Reference     string        `gorm:"type:varchar(200);not null;default:''"`
	Memo          string        `gorm:"type:text;not null;default:''"`

	// GatewayTransactionID links to an external gateway transaction (informational only).
	// The gateway does NOT determine AR accounting truth; this field is audit-trail only.
	GatewayTransactionID *uint `gorm:"index"`

	// ConfirmedAt is set when the receipt is posted.
	ConfirmedAt *time.Time
	// ConfirmedBy is the actor who confirmed.
	ConfirmedBy string `gorm:"type:varchar(200);not null;default:''"`
	// ConfirmedByUserID links to the user who confirmed (optional).
	ConfirmedByUserID *uuid.UUID `gorm:"type:uuid"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
