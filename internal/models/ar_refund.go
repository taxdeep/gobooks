// 遵循project_guide.md
package models

// ar_refund.go — ARRefund: AR 退款资金流出对象。
//
// ARRefund 记录公司向客户退款的资金流出事实。它是独立的会计对象，
// 不等于 CreditNote，不等于 Return。
//
// 资金来源可以是：
//   - 一笔 CustomerDeposit 的退还
//   - 一笔 CustomerReceipt 的超额退回（overpayment refund）
//   - 一张 CreditNote 的现金兑现
//   - 上述的组合（partial）
//
// 会计规则（Phase 5 实现正式 posting）：
//
//	退款时：
//	  Dr  AR / Customer Deposit / Customer Credit
//	  Cr  Cash / Bank / Clearing
//
// 状态机：
//
//	draft → posted → reversed
//	       ↘ voided (from draft)

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ARRefundStatus tracks the lifecycle of a customer refund.
type ARRefundStatus string

const (
	// ARRefundStatusDraft — refund prepared but not yet posted.
	ARRefundStatusDraft ARRefundStatus = "draft"

	// ARRefundStatusPosted — JE created; funds have been returned to customer.
	ARRefundStatusPosted ARRefundStatus = "posted"

	// ARRefundStatusReversed — refund was reversed (rare; requires audit trail).
	ARRefundStatusReversed ARRefundStatus = "reversed"

	// ARRefundStatusVoided — cancelled before posting.
	ARRefundStatusVoided ARRefundStatus = "voided"
)

// AllARRefundStatuses returns statuses in display order.
func AllARRefundStatuses() []ARRefundStatus {
	return []ARRefundStatus{
		ARRefundStatusDraft,
		ARRefundStatusPosted,
		ARRefundStatusReversed,
		ARRefundStatusVoided,
	}
}

// ARRefundStatusLabel returns a human-readable label.
func ARRefundStatusLabel(s ARRefundStatus) string {
	switch s {
	case ARRefundStatusDraft:
		return "Draft"
	case ARRefundStatusPosted:
		return "Posted"
	case ARRefundStatusReversed:
		return "Reversed"
	case ARRefundStatusVoided:
		return "Voided"
	default:
		return string(s)
	}
}

// ARRefundSourceType identifies where the refunded money originates.
type ARRefundSourceType string

const (
	// ARRefundSourceDeposit — refund of a CustomerDeposit (unused pre-paid amount).
	ARRefundSourceDeposit ARRefundSourceType = "customer_deposit"

	// ARRefundSourceOverpayment — refund of an overpaid CustomerReceipt.
	ARRefundSourceOverpayment ARRefundSourceType = "overpayment"

	// ARRefundSourceCreditNote — cash payout of a CreditNote balance.
	ARRefundSourceCreditNote ARRefundSourceType = "credit_note"

	// ARRefundSourceOther — manually specified refund source.
	ARRefundSourceOther ARRefundSourceType = "other"
)

// ARRefund records the formal AR-side refund to a customer.
//
// It is a fund-outflow accounting object. It is NOT automatically triggered
// by a Return or CreditNote — it must be explicitly created.
//
// Phase 1 establishes the model skeleton. Phase 5 implements posting.
type ARRefund struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	CustomerID uint     `gorm:"not null;index"`
	Customer   Customer `gorm:"foreignKey:CustomerID"`

	// JournalEntryID is set when the refund is posted.
	JournalEntryID *uint         `gorm:"uniqueIndex"`
	JournalEntry   *JournalEntry `gorm:"foreignKey:JournalEntryID"`

	// BankAccountID is the cash/bank account funds are paid from.
	BankAccountID *uint    `gorm:"index"`
	BankAccount   *Account `gorm:"foreignKey:BankAccountID"`

	// SourceType identifies where the refunded amount originates.
	SourceType ARRefundSourceType `gorm:"type:text;not null;default:'other'"`

	// Source linkage — set according to SourceType.
	CustomerDepositID *uint            `gorm:"index"`
	CustomerDeposit   *CustomerDeposit `gorm:"foreignKey:CustomerDepositID"`

	CustomerReceiptID *uint            `gorm:"index"`
	CustomerReceipt   *CustomerReceipt `gorm:"foreignKey:CustomerReceiptID"`

	CreditNoteID *uint       `gorm:"index"`
	CreditNote   *CreditNote `gorm:"foreignKey:CreditNoteID"`

	// ARReturnID links to the originating return request (optional).
	ARReturnID *uint     `gorm:"index"`
	ARReturn   *ARReturn `gorm:"foreignKey:ARReturnID"`

	RefundNumber string         `gorm:"type:varchar(50);not null;default:''"`
	Status       ARRefundStatus `gorm:"type:text;not null;default:'draft'"`
	RefundDate   time.Time      `gorm:"not null"`

	// CurrencyCode — currency of the refund.
	CurrencyCode string          `gorm:"type:varchar(3);not null;default:''"`
	ExchangeRate decimal.Decimal `gorm:"type:numeric(18,8);not null;default:1"`

	// Amount is the document-currency refund total.
	Amount decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	// AmountBase is the base-currency equivalent at time of posting.
	AmountBase decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	PaymentMethod PaymentMethod `gorm:"type:text;not null;default:'other'"`
	Reference     string        `gorm:"type:varchar(200);not null;default:''"`
	Memo          string        `gorm:"type:text;not null;default:''"`

	// PostedAt is set when the refund is posted.
	PostedAt *time.Time
	// PostedBy is the actor who posted.
	PostedBy string `gorm:"type:varchar(200);not null;default:''"`
	// PostedByUserID links to the user who posted (optional).
	PostedByUserID *uuid.UUID `gorm:"type:uuid"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
