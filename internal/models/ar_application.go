// 遵循project_guide.md
package models

// ar_application.go — PaymentApplication: AR open-item 匹配记录。
//
// PaymentApplication 是把 CustomerReceipt（或 CustomerDeposit）金额
// 正式应用到 Invoice 的显式记录。它不是隐式操作，必须形成独立的
// 审计轨迹。
//
// 会计影响（Phase 4 实现）：
//   - apply：减少 Invoice.BalanceDue，更新 Invoice status
//   - unapply：恢复 Invoice.BalanceDue，恢复 Receipt.UnappliedAmount
//   - 不产生新 JE（AR 账户余额在 Receipt 过账时已经变动）
//   - Settlement FX 差额（外币）由 Phase 13 实现
//
// 状态机：
//
//	active → reversed (via unapply)

import (
	"time"

	"github.com/shopspring/decimal"
)

// PaymentApplicationStatus tracks the lifecycle of a payment application.
type PaymentApplicationStatus string

const (
	// PaymentApplicationStatusActive — the application is in effect; Invoice.BalanceDue is reduced.
	PaymentApplicationStatusActive PaymentApplicationStatus = "active"

	// PaymentApplicationStatusReversed — the application has been reversed (unapplied).
	// Invoice.BalanceDue is restored; Receipt.UnappliedAmount is restored.
	PaymentApplicationStatusReversed PaymentApplicationStatus = "reversed"
)

// AllPaymentApplicationStatuses returns statuses in display order.
func AllPaymentApplicationStatuses() []PaymentApplicationStatus {
	return []PaymentApplicationStatus{
		PaymentApplicationStatusActive,
		PaymentApplicationStatusReversed,
	}
}

// PaymentApplicationStatusLabel returns a human-readable label.
func PaymentApplicationStatusLabel(s PaymentApplicationStatus) string {
	switch s {
	case PaymentApplicationStatusActive:
		return "Active"
	case PaymentApplicationStatusReversed:
		return "Reversed"
	default:
		return string(s)
	}
}

// PaymentApplicationSourceType identifies the source of the payment being applied.
type PaymentApplicationSourceType string

const (
	// PaymentApplicationSourceReceipt — funds from a CustomerReceipt.
	PaymentApplicationSourceReceipt PaymentApplicationSourceType = "customer_receipt"

	// PaymentApplicationSourceDeposit — funds from a CustomerDeposit.
	PaymentApplicationSourceDeposit PaymentApplicationSourceType = "customer_deposit"
)

// PaymentApplication records the formal matching of a receipt/deposit amount to an Invoice.
//
// This is the AR open-item truth. It must be an explicit object — not embedded logic.
// Apply and Unapply operations both create / update PaymentApplication records,
// and are fully auditable.
//
// Phase 1 establishes the model skeleton. Phase 4 implements apply/unapply services.
type PaymentApplication struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	// SourceType identifies whether this application draws from a Receipt or Deposit.
	SourceType PaymentApplicationSourceType `gorm:"type:text;not null"`

	// CustomerReceiptID is set when SourceType = customer_receipt.
	CustomerReceiptID *uint            `gorm:"index"`
	CustomerReceipt   *CustomerReceipt `gorm:"foreignKey:CustomerReceiptID"`

	// CustomerDepositID is set when SourceType = customer_deposit.
	CustomerDepositID *uint            `gorm:"index"`
	CustomerDeposit   *CustomerDeposit `gorm:"foreignKey:CustomerDepositID"`

	// InvoiceID is the invoice being reduced.
	InvoiceID uint    `gorm:"not null;index"`
	Invoice   Invoice `gorm:"foreignKey:InvoiceID"`

	Status PaymentApplicationStatus `gorm:"type:text;not null;default:'active'"`

	// AmountApplied is the document-currency amount applied to the invoice.
	AmountApplied decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	// AmountAppliedBase is the base-currency equivalent.
	AmountAppliedBase decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	AppliedAt time.Time `gorm:"not null"`
	AppliedBy string    `gorm:"type:varchar(200);not null;default:''"`

	// ReversedAt is set when this application is reversed.
	ReversedAt *time.Time
	ReversedBy string `gorm:"type:varchar(200);not null;default:''"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
