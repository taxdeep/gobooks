package models

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// PaymentMethod records the business-facing origin of a receipt.
// It does not affect JE posting logic or account selection.
type PaymentMethod string

const (
	PaymentMethodCheck   PaymentMethod = "check"
	PaymentMethodWire    PaymentMethod = "wire"
	PaymentMethodCash    PaymentMethod = "cash"
	PaymentMethodOther   PaymentMethod = "other"
	PaymentMethodGateway PaymentMethod = "gateway"
)

func AllPaymentMethods() []PaymentMethod {
	return []PaymentMethod{
		PaymentMethodCheck,
		PaymentMethodWire,
		PaymentMethodCash,
		PaymentMethodOther,
		PaymentMethodGateway,
	}
}

func ManualPaymentMethods() []PaymentMethod {
	return []PaymentMethod{
		PaymentMethodCheck,
		PaymentMethodWire,
		PaymentMethodCash,
		PaymentMethodOther,
	}
}

func PaymentMethodLabel(m PaymentMethod) string {
	switch m {
	case PaymentMethodCheck:
		return "Check"
	case PaymentMethodWire:
		return "Wire Transfer"
	case PaymentMethodCash:
		return "Cash"
	case PaymentMethodOther:
		return "Other"
	case PaymentMethodGateway:
		return "Payment Gateway"
	default:
		return string(m)
	}
}

func ParsePaymentMethod(raw string) (PaymentMethod, error) {
	switch PaymentMethod(raw) {
	case PaymentMethodCheck,
		PaymentMethodWire,
		PaymentMethodCash,
		PaymentMethodOther,
		PaymentMethodGateway:
		return PaymentMethod(raw), nil
	default:
		return "", fmt.Errorf("unknown payment method: %q", raw)
	}
}

func IsManualPaymentMethod(m PaymentMethod) bool {
	switch m {
	case PaymentMethodCheck, PaymentMethodWire, PaymentMethodCash, PaymentMethodOther:
		return true
	default:
		return false
	}
}

// PaymentReceipt is the business-layer header for a received customer payment.
// It complements the posted JE and stores user-facing receipt metadata.
type PaymentReceipt struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	CustomerID uint     `gorm:"not null;index"`
	Customer   Customer `gorm:"foreignKey:CustomerID"`

	InvoiceID *uint    `gorm:"index"`
	Invoice   *Invoice `gorm:"foreignKey:InvoiceID"`

	JournalEntryID uint         `gorm:"not null;uniqueIndex"`
	JournalEntry   JournalEntry `gorm:"foreignKey:JournalEntryID"`

	BankAccountID uint    `gorm:"not null;index"`
	BankAccount   Account `gorm:"foreignKey:BankAccountID"`

	PaymentMethod PaymentMethod   `gorm:"type:text;not null;default:'other'"`
	AmountBase    decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	Memo          string          `gorm:"type:text;not null;default:''"`
	EntryDate     time.Time       `gorm:"not null"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
