// 遵循project_guide.md
package models

// ar_deposit.go — CustomerDeposit: 客户预收款 / 定金对象。
//
// CustomerDeposit 是客户在正式 Invoice 之前支付的预付款或定金。
// 它不等于 Revenue，也不等于普通 CustomerReceipt。
//
// 会计规则（Phase 3 实现正式 posting）：
//
//	过账时：
//	  Dr  Cash / Bank / Clearing
//	  Cr  Customer Deposit Liability / Deferred Revenue
//
//	应用到 Invoice 时（Phase 3/4 实现）：
//	  Dr  Customer Deposit Liability
//	  Cr  AR（减少 Invoice BalanceDue）
//
// 状态机：
//
//	draft → posted → unapplied
//	                       ↓ partially_applied
//	                       ↓ fully_applied
//	                       ↓ refunded
//	       ↘ voided (from draft or posted/unapplied)

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// CustomerDepositStatus tracks the lifecycle of a customer deposit.
type CustomerDepositStatus string

const (
	// CustomerDepositStatusDraft — created but not yet posted to accounting.
	CustomerDepositStatusDraft CustomerDepositStatus = "draft"

	// CustomerDepositStatusPosted — JE created; deposit is on the books as a liability.
	// Equivalent to "unapplied" — the full amount is available for application.
	CustomerDepositStatusPosted CustomerDepositStatus = "posted"

	// CustomerDepositStatusPartiallyApplied — some amount applied to invoice(s); remainder open.
	CustomerDepositStatusPartiallyApplied CustomerDepositStatus = "partially_applied"

	// CustomerDepositStatusFullyApplied — all deposit amount applied; no remaining balance.
	CustomerDepositStatusFullyApplied CustomerDepositStatus = "fully_applied"

	// CustomerDepositStatusRefunded — deposit refunded to customer; refund JE created.
	CustomerDepositStatusRefunded CustomerDepositStatus = "refunded"

	// CustomerDepositStatusVoided — cancelled before or after posting; reversal JE created.
	CustomerDepositStatusVoided CustomerDepositStatus = "voided"
)

// CustomerDepositSource describes the business event that created the deposit.
// Added 2026-04-24 so the Receive Payment overpayment path can tell itself
// apart from the manual prepayment path at apply / reporting time.
type CustomerDepositSource string

const (
	// DepositSourceManual — bookkeeper recorded a prepayment directly
	// (no invoice yet). Default for backward compatibility.
	DepositSourceManual CustomerDepositSource = "manual"

	// DepositSourceOverpayment — auto-created by Receive Payment when the
	// operator applied more than the invoice(s) balance.
	DepositSourceOverpayment CustomerDepositSource = "overpayment"

	// DepositSourceSalesOrder — tied to a SalesOrder at creation time
	// (existing Phase-3 flow; see SalesOrderID link below).
	DepositSourceSalesOrder CustomerDepositSource = "sales_order"
)

// AllCustomerDepositStatuses returns statuses in display order.
func AllCustomerDepositStatuses() []CustomerDepositStatus {
	return []CustomerDepositStatus{
		CustomerDepositStatusDraft,
		CustomerDepositStatusPosted,
		CustomerDepositStatusPartiallyApplied,
		CustomerDepositStatusFullyApplied,
		CustomerDepositStatusRefunded,
		CustomerDepositStatusVoided,
	}
}

// CustomerDepositStatusLabel returns a human-readable label.
func CustomerDepositStatusLabel(s CustomerDepositStatus) string {
	switch s {
	case CustomerDepositStatusDraft:
		return "Draft"
	case CustomerDepositStatusPosted:
		return "Unapplied"
	case CustomerDepositStatusPartiallyApplied:
		return "Partially Applied"
	case CustomerDepositStatusFullyApplied:
		return "Fully Applied"
	case CustomerDepositStatusRefunded:
		return "Refunded"
	case CustomerDepositStatusVoided:
		return "Voided"
	default:
		return string(s)
	}
}

// CustomerDeposit records a pre-invoice cash receipt from a customer.
//
// A CustomerDeposit is NOT revenue. It creates a liability (deferred revenue /
// customer deposit account) until applied to an Invoice.
//
// Posting is handled by Phase 3. Phase 1 establishes the model skeleton only.
type CustomerDeposit struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	CustomerID uint     `gorm:"not null;index"`
	Customer   Customer `gorm:"foreignKey:CustomerID"`

	// SalesOrderID links the deposit to an originating SalesOrder (optional).
	SalesOrderID *uint        `gorm:"index"`
	SalesOrder   *SalesOrder  `gorm:"foreignKey:SalesOrderID"`

	// JournalEntryID is set when the deposit is posted.
	JournalEntryID *uint         `gorm:"uniqueIndex"`
	JournalEntry   *JournalEntry `gorm:"foreignKey:JournalEntryID"`

	// BankAccountID is the cash/bank account that received the funds.
	BankAccountID *uint    `gorm:"index"`
	BankAccount   *Account `gorm:"foreignKey:BankAccountID"`

	// DepositLiabilityAccountID is the Cr side (Customer Deposit / Deferred Revenue).
	// Required before posting. Routing governed by backend rules, not UI.
	DepositLiabilityAccountID *uint    `gorm:"index"`
	DepositLiabilityAccount   *Account `gorm:"foreignKey:DepositLiabilityAccountID"`

	DepositNumber string                `gorm:"type:varchar(50);not null;default:''"`
	Status        CustomerDepositStatus `gorm:"type:text;not null;default:'draft'"`
	// Source tags the origin of the deposit — manual prepayment,
	// overpayment from Receive Payment, or SalesOrder-linked. Drives
	// reporting filters and (later) deposit-level analytics.
	Source      CustomerDepositSource `gorm:"type:text;not null;default:'manual'"`
	DepositDate time.Time             `gorm:"not null"`

	// CurrencyCode — defaults to customer default currency or company base currency.
	CurrencyCode string          `gorm:"type:varchar(3);not null;default:''"`
	ExchangeRate decimal.Decimal `gorm:"type:numeric(18,8);not null;default:1"`

	// Amount is the document-currency total.
	Amount decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	// AmountBase is the base-currency equivalent at the time of posting.
	AmountBase decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	// BalanceRemaining tracks how much of the deposit is still unapplied (doc currency).
	BalanceRemaining decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	PaymentMethod PaymentMethod `gorm:"type:text;not null;default:'other'"`
	Reference     string        `gorm:"type:varchar(200);not null;default:''"`
	Memo          string        `gorm:"type:text;not null;default:''"`

	// PostedAt is set when the deposit is posted.
	PostedAt *time.Time
	// PostedBy is the actor who posted.
	PostedBy string `gorm:"type:varchar(200);not null;default:''"`
	// PostedByUserID links to the user who posted (optional).
	PostedByUserID *uuid.UUID `gorm:"type:uuid"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// CustomerDepositApplication records the application of a deposit amount to an Invoice.
//
// This is the join record between CustomerDeposit and Invoice.
// Phase 3 implements the posting logic for applications.
type CustomerDepositApplication struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	CustomerDepositID uint            `gorm:"not null;index"`
	CustomerDeposit   CustomerDeposit `gorm:"foreignKey:CustomerDepositID"`

	InvoiceID uint    `gorm:"not null;index"`
	Invoice   Invoice `gorm:"foreignKey:InvoiceID"`

	// AmountApplied is the document-currency amount applied.
	AmountApplied decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	// AmountAppliedBase is the base-currency equivalent.
	AmountAppliedBase decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	// JournalEntryID points at the JE that posted the deposit-release
	// line (added 2026-04-24 — Receive Payment path now co-creates apps
	// and the parent JE in the same transaction). Nil for legacy Phase-3
	// applications that predate the unified JE recipe.
	JournalEntryID *uint `gorm:"index"`

	AppliedAt time.Time `gorm:"not null"`
	AppliedBy string    `gorm:"type:varchar(200);not null;default:''"`

	CreatedAt time.Time
}
