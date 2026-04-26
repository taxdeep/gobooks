// 遵循project_guide.md
package models

// ar_sales_order.go — SalesOrder: AR 商业承诺对象。
//
// SalesOrder 是从 Quote 转换或直接创建的商业承诺。
// 它确认了交付义务，但在 Invoice 过账前不产生任何会计事实。
//
// 会计规则：SalesOrder 不产生任何 JE。
// 它是商业层对象，驱动履约流程，不驱动会计真相。
//
// 状态机：
//
//	draft → confirmed → partially_invoiced → fully_invoiced
//	                  ↘ cancelled

import (
	"time"

	"github.com/shopspring/decimal"
)

// SalesOrderStatus tracks the lifecycle of a sales order.
type SalesOrderStatus string

const (
	// SalesOrderStatusDraft — created but not yet confirmed.
	SalesOrderStatusDraft SalesOrderStatus = "draft"

	// SalesOrderStatusConfirmed — confirmed; fulfillment can begin.
	SalesOrderStatusConfirmed SalesOrderStatus = "confirmed"

	// SalesOrderStatusPartiallyInvoiced — at least one invoice raised; order not complete.
	SalesOrderStatusPartiallyInvoiced SalesOrderStatus = "partially_invoiced"

	// SalesOrderStatusFullyInvoiced — all line amounts have been invoiced.
	SalesOrderStatusFullyInvoiced SalesOrderStatus = "fully_invoiced"

	// SalesOrderStatusCancelled — cancelled; no further invoicing allowed.
	SalesOrderStatusCancelled SalesOrderStatus = "cancelled"
)

// AllSalesOrderStatuses returns statuses in display order.
func AllSalesOrderStatuses() []SalesOrderStatus {
	return []SalesOrderStatus{
		SalesOrderStatusDraft,
		SalesOrderStatusConfirmed,
		SalesOrderStatusPartiallyInvoiced,
		SalesOrderStatusFullyInvoiced,
		SalesOrderStatusCancelled,
	}
}

// SalesOrderStatusLabel returns a human-readable label.
func SalesOrderStatusLabel(s SalesOrderStatus) string {
	switch s {
	case SalesOrderStatusDraft:
		return "Draft"
	case SalesOrderStatusConfirmed:
		return "Confirmed"
	case SalesOrderStatusPartiallyInvoiced:
		return "Partially Invoiced"
	case SalesOrderStatusFullyInvoiced:
		return "Fully Invoiced"
	case SalesOrderStatusCancelled:
		return "Cancelled"
	default:
		return string(s)
	}
}

// SalesOrder is a confirmed commercial commitment to a customer.
//
// No JE is generated for a SalesOrder. Accounting truth deferred until Invoice posting.
// A SalesOrder can be linked to an originating Quote via QuoteID.
// Invoices raised against this order link back via Invoice.SalesOrderID (set at Phase 2).
type SalesOrder struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	CustomerID uint     `gorm:"not null;index"`
	Customer   Customer `gorm:"foreignKey:CustomerID"`

	// QuoteID is set when this SalesOrder was converted from a Quote.
	QuoteID *uint  `gorm:"index"`
	Quote   *Quote `gorm:"foreignKey:QuoteID"`

	OrderNumber string           `gorm:"type:varchar(50);not null;default:''"`
	Status      SalesOrderStatus `gorm:"type:text;not null;default:'draft'"`
	OrderDate   time.Time        `gorm:"not null"`
	RequiredBy  *time.Time

	// Currency — inherited from Quote or set directly.
	CurrencyCode string `gorm:"type:varchar(3);not null;default:''"`

	Subtotal decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`
	TaxTotal decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`
	Total    decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`

	// InvoicedAmount tracks total amount raised on invoices so far.
	InvoicedAmount decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`

	Notes string `gorm:"type:text;not null;default:''"`
	Memo  string `gorm:"type:text;not null;default:''"`

	// CustomerPONumber — migration 088. The reference number the customer
	// quoted when they sent us this PO. Populated by the SO editor; prefills
	// downstream documents (Invoice, Shipment display) so the whole AR chain
	// shows the same reference.
	CustomerPONumber string `gorm:"type:varchar(64);not null;default:''"`

	// ConfirmedAt is set when the order is confirmed.
	ConfirmedAt *time.Time

	// Lines is the set of line items; populated via Preload.
	Lines []SalesOrderLine `gorm:"foreignKey:SalesOrderID"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// SalesOrderLine is a single line item on a SalesOrder.
type SalesOrderLine struct {
	ID           uint `gorm:"primaryKey"`
	SalesOrderID uint `gorm:"not null;index"`

	// ProductServiceID is optional.
	ProductServiceID *uint           `gorm:"index"`
	ProductService   *ProductService `gorm:"foreignKey:ProductServiceID"`

	// RevenueAccountID is the revenue account hint.
	RevenueAccountID *uint    `gorm:"index"`
	RevenueAccount   *Account `gorm:"foreignKey:RevenueAccountID"`

	// TaxCodeID is optional.
	TaxCodeID *uint    `gorm:"index"`
	TaxCode   *TaxCode `gorm:"foreignKey:TaxCodeID"`

	Description string          `gorm:"type:text;not null;default:''"`
	Quantity    decimal.Decimal `gorm:"type:numeric(18,4);not null;default:1"`

	// OriginalQuantity captures the contracted (initial-create) qty.
	// The S2 partially-invoiced Qty-edit path uses this as the stable
	// baseline for the over-shipment buffer cap (otherwise each edit
	// would shift the baseline and the buffer would compound).
	// Set on Create + Update (which is draft-only); never touched by
	// AdjustSalesOrderLineQty.
	OriginalQuantity decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`

	UnitPrice decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`
	LineNet   decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`
	TaxAmount decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`
	LineTotal decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`

	// UOM snapshot (Phase U2 — 2026-04-25). Matches the same shape on
	// InvoiceLine / BillLine. SO lines snapshot ProductService.SellUOM
	// at create time so a downstream Invoice picks up the same UOM by
	// default.  See UOM_DESIGN.md §3.2.
	LineUOM       string          `gorm:"type:varchar(16);not null;default:'EA'"`
	LineUOMFactor decimal.Decimal `gorm:"type:numeric(18,6);not null;default:1"`
	QtyInStockUOM decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`

	// InvoicedQty tracks how much of this line has been invoiced.
	InvoicedQty decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`

	SortOrder int `gorm:"not null;default:0"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
