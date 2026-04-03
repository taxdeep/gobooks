// 遵循project_guide.md
package models

import (
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/datatypes"
)

// ── Provider type ────────────────────────────────────────────────────────────

type PaymentProviderType string

const (
	ProviderStripe  PaymentProviderType = "stripe"
	ProviderPayPal  PaymentProviderType = "paypal"
	ProviderManual  PaymentProviderType = "manual"
	ProviderOther   PaymentProviderType = "other"
)

func AllPaymentProviderTypes() []PaymentProviderType {
	return []PaymentProviderType{ProviderStripe, ProviderPayPal, ProviderManual, ProviderOther}
}

func PaymentProviderLabel(t PaymentProviderType) string {
	switch t {
	case ProviderStripe:
		return "Stripe"
	case ProviderPayPal:
		return "PayPal"
	case ProviderManual:
		return "Manual"
	case ProviderOther:
		return "Other"
	default:
		return string(t)
	}
}

// ── Payment request status ───────────────────────────────────────────────────

type PaymentRequestStatus string

const (
	PaymentRequestDraft              PaymentRequestStatus = "draft"
	// PaymentRequestCreated is kept for backward compatibility with older rows
	// created before initial request status was unified to pending.
	PaymentRequestCreated            PaymentRequestStatus = "created"
	PaymentRequestPending            PaymentRequestStatus = "pending"
	PaymentRequestPaid               PaymentRequestStatus = "paid"
	PaymentRequestFailed             PaymentRequestStatus = "failed"
	PaymentRequestCancelled          PaymentRequestStatus = "cancelled"
	PaymentRequestRefunded           PaymentRequestStatus = "refunded"
	PaymentRequestPartiallyRefunded  PaymentRequestStatus = "partially_refunded"
)

func AllPaymentRequestStatuses() []PaymentRequestStatus {
	return []PaymentRequestStatus{
		PaymentRequestDraft, PaymentRequestCreated, PaymentRequestPending,
		PaymentRequestPaid, PaymentRequestFailed, PaymentRequestCancelled,
		PaymentRequestRefunded, PaymentRequestPartiallyRefunded,
	}
}

// ── Transaction type ─────────────────────────────────────────────────────────

type PaymentTransactionType string

const (
	TxnTypeCharge  PaymentTransactionType = "charge"
	TxnTypeCapture PaymentTransactionType = "capture"
	TxnTypeRefund  PaymentTransactionType = "refund"
	TxnTypeFee     PaymentTransactionType = "fee"
	TxnTypePayout  PaymentTransactionType = "payout"
	TxnTypeDispute PaymentTransactionType = "dispute"
)

func AllPaymentTransactionTypes() []PaymentTransactionType {
	return []PaymentTransactionType{
		TxnTypeCharge, TxnTypeCapture, TxnTypeRefund,
		TxnTypeFee, TxnTypePayout, TxnTypeDispute,
	}
}

func PaymentTransactionTypeLabel(t PaymentTransactionType) string {
	switch t {
	case TxnTypeCharge:
		return "Charge"
	case TxnTypeCapture:
		return "Capture"
	case TxnTypeRefund:
		return "Refund"
	case TxnTypeFee:
		return "Fee"
	case TxnTypePayout:
		return "Payout"
	case TxnTypeDispute:
		return "Dispute"
	default:
		return string(t)
	}
}

// ── Payment gateway account ──────────────────────────────────────────────────

// PaymentGatewayAccount represents a company's account with a payment processor
// (e.g. a Stripe account, PayPal merchant account). No credentials are stored
// here; actual API keys/tokens are deferred to a future secure vault layer.
type PaymentGatewayAccount struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	ProviderType       PaymentProviderType `gorm:"type:text;not null"`
	DisplayName        string              `gorm:"type:text;not null;default:''"`
	ExternalAccountRef string              `gorm:"type:text;not null;default:''"`
	AuthStatus         string              `gorm:"type:text;not null;default:'pending'"`
	WebhookStatus      string              `gorm:"type:text;not null;default:'not_configured'"`
	IsActive           bool                `gorm:"not null;default:true"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ── Payment accounting mapping ───────────────────────────────────────────────

// PaymentAccountingMapping defines which GL accounts to use when posting
// gateway-originated transactions (charges, fees, refunds, payouts).
// One mapping per gateway account. All account FKs must be company-scoped.
//
// Typical gateway flow:
//   customer pays  → gateway clearing increases (Dr GW Clearing, Cr Revenue/AR)
//   gateway fee    → gateway clearing decreases (Dr Fee Expense, Cr GW Clearing)
//   payout to bank → gateway clearing decreases (Dr Bank, Cr GW Clearing)
type PaymentAccountingMapping struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	GatewayAccountID    uint                   `gorm:"not null;uniqueIndex:uq_payment_acct_mapping"`
	GatewayAccount      PaymentGatewayAccount  `gorm:"foreignKey:GatewayAccountID"`

	ClearingAccountID   *uint    `gorm:"index"`
	ClearingAccount     *Account `gorm:"foreignKey:ClearingAccountID"`
	FeeExpenseAccountID *uint
	FeeExpenseAccount   *Account `gorm:"foreignKey:FeeExpenseAccountID"`
	RefundAccountID     *uint
	RefundAccount       *Account `gorm:"foreignKey:RefundAccountID"`
	PayoutBankAccountID *uint
	PayoutBankAccount   *Account `gorm:"foreignKey:PayoutBankAccountID"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ── Payment request ──────────────────────────────────────────────────────────

// PaymentRequest represents a request for payment from a customer, linked to
// a gateway account and optionally to an invoice. In the future, a provider
// adapter creates a real checkout session from this request.
type PaymentRequest struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	GatewayAccountID uint                  `gorm:"not null;index"`
	GatewayAccount   PaymentGatewayAccount `gorm:"foreignKey:GatewayAccountID"`

	InvoiceID  *uint `gorm:"index"`
	CustomerID *uint `gorm:"index"`

	Amount       decimal.Decimal      `gorm:"type:numeric(18,2);not null;default:0"`
	CurrencyCode string               `gorm:"type:text;not null;default:''"`
	Status       PaymentRequestStatus `gorm:"type:text;not null;default:'pending'"`
	Description  string               `gorm:"type:text;not null;default:''"`
	ExternalRef  string               `gorm:"type:text;not null;default:''"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ── Payment transaction ──────────────────────────────────────────────────────

// PaymentTransaction records a single event from the payment gateway (charge,
// refund, fee, payout, dispute). In the current phase these are entered manually;
// future provider webhooks will write directly to this table.
type PaymentTransaction struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	GatewayAccountID   uint                  `gorm:"not null;index"`
	GatewayAccount     PaymentGatewayAccount `gorm:"foreignKey:GatewayAccountID"`
	PaymentRequestID   *uint                 `gorm:"index"`

	TransactionType    PaymentTransactionType `gorm:"type:text;not null"`
	Amount             decimal.Decimal        `gorm:"type:numeric(18,2);not null;default:0"`
	CurrencyCode       string                 `gorm:"type:text;not null;default:''"`
	Status             string                 `gorm:"type:text;not null;default:'completed'"`
	ExternalTxnRef     string                 `gorm:"type:text;not null;default:''"`
	RawPayload         datatypes.JSON         `gorm:"not null"`

	// Posting state: non-nil means the transaction has been posted to a JE.
	PostedJournalEntryID *uint      `gorm:"index"`
	PostedAt             *time.Time

	// Application state: non-nil means the charge/capture has been applied to an invoice.
	// Only charge/capture transactions can be applied. Fee/refund/payout cannot.
	AppliedInvoiceID *uint      `gorm:"index"`
	AppliedAt        *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}
