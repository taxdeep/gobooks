// 遵循project_guide.md
package models

// ar_quote.go — Quote: AR 前置商业报价对象。
//
// Quote 是 AR 主链的起点：Customer → Quote → SalesOrder → Invoice。
//
// 会计规则：Quote 不产生任何 JE。它是商业报价，不是法律承诺，
// 也不是会计事实。任何会计真相都必须等到 Invoice 过账时才形成。
//
// 状态机：
//
//	draft → sent → accepted → converted (→ SalesOrder)
//	             ↘ rejected
//	      ↘ cancelled (从 draft 或 sent)

import (
	"time"

	"github.com/shopspring/decimal"
)

// QuoteStatus tracks the lifecycle of a customer quote.
type QuoteStatus string

const (
	// QuoteStatusDraft — created, not yet sent to customer.
	QuoteStatusDraft QuoteStatus = "draft"

	// QuoteStatusSent — sent to customer; awaiting response.
	QuoteStatusSent QuoteStatus = "sent"

	// QuoteStatusAccepted — customer accepted; can be converted to SalesOrder.
	QuoteStatusAccepted QuoteStatus = "accepted"

	// QuoteStatusRejected — customer declined.
	QuoteStatusRejected QuoteStatus = "rejected"

	// QuoteStatusConverted — converted to a SalesOrder; source link set.
	QuoteStatusConverted QuoteStatus = "converted"

	// QuoteStatusCancelled — cancelled before acceptance.
	QuoteStatusCancelled QuoteStatus = "cancelled"
)

// AllQuoteStatuses returns statuses in display order.
func AllQuoteStatuses() []QuoteStatus {
	return []QuoteStatus{
		QuoteStatusDraft,
		QuoteStatusSent,
		QuoteStatusAccepted,
		QuoteStatusRejected,
		QuoteStatusConverted,
		QuoteStatusCancelled,
	}
}

// QuoteStatusLabel returns a human-readable label.
func QuoteStatusLabel(s QuoteStatus) string {
	switch s {
	case QuoteStatusDraft:
		return "Draft"
	case QuoteStatusSent:
		return "Sent"
	case QuoteStatusAccepted:
		return "Accepted"
	case QuoteStatusRejected:
		return "Rejected"
	case QuoteStatusConverted:
		return "Converted"
	case QuoteStatusCancelled:
		return "Cancelled"
	default:
		return string(s)
	}
}

// Quote is a commercial offer to a customer.
//
// No JE is generated for a Quote. Accounting truth is deferred until Invoice posting.
// A Quote can be converted to a SalesOrder; that link is recorded in SalesOrderID.
type Quote struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	CustomerID uint     `gorm:"not null;index"`
	Customer   Customer `gorm:"foreignKey:CustomerID"`

	// SalesOrderID is set when this Quote has been converted.
	SalesOrderID *uint `gorm:"index"`

	QuoteNumber string      `gorm:"type:varchar(50);not null;default:''"`
	Status      QuoteStatus `gorm:"type:text;not null;default:'draft'"`
	QuoteDate   time.Time   `gorm:"not null"`
	ExpiryDate  *time.Time

	// Currency — defaults to customer default currency or company base currency.
	CurrencyCode string `gorm:"type:varchar(3);not null;default:''"`

	Subtotal decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`
	TaxTotal decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`
	Total    decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`

	Notes string `gorm:"type:text;not null;default:''"`
	Memo  string `gorm:"type:text;not null;default:''"`

	// SentAt is set when the quote is marked as sent.
	SentAt *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// QuoteLine is a single line item on a Quote.
type QuoteLine struct {
	ID      uint `gorm:"primaryKey"`
	QuoteID uint `gorm:"not null;index"`

	// ProductServiceID is optional; free-text description is allowed.
	ProductServiceID *uint          `gorm:"index"`
	ProductService   *ProductService `gorm:"foreignKey:ProductServiceID"`

	// RevenueAccountID is the AR revenue account hint (informational only at quote stage).
	RevenueAccountID *uint    `gorm:"index"`
	RevenueAccount   *Account `gorm:"foreignKey:RevenueAccountID"`

	// TaxCodeID is optional; applied if the product/service is taxable.
	TaxCodeID *uint    `gorm:"index"`
	TaxCode   *TaxCode `gorm:"foreignKey:TaxCodeID"`

	Description string          `gorm:"type:text;not null;default:''"`
	Quantity    decimal.Decimal `gorm:"type:numeric(18,4);not null;default:1"`
	UnitPrice   decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`
	LineNet     decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`
	TaxAmount   decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`
	LineTotal   decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`

	SortOrder int `gorm:"not null;default:0"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
