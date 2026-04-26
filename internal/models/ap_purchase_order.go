// 遵循project_guide.md
package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// ── PurchaseOrder status ──────────────────────────────────────────────────────

// POStatus tracks the lifecycle of a purchase order.
//
// draft           → internal working copy; not sent to vendor
// confirmed       → sent / accepted; commitment recorded
// partially_received → some lines received but not all (future: inventory receipt linkage)
// received        → all ordered quantities received
// closed          → fully billed and settled; no further action
// cancelled       → voided before fulfilment
//
// PurchaseOrders do NOT create formal accounting entries.
type POStatus string

const (
	POStatusDraft             POStatus = "draft"
	POStatusConfirmed         POStatus = "confirmed"
	POStatusPartiallyReceived POStatus = "partially_received"
	POStatusReceived          POStatus = "received"
	POStatusClosed            POStatus = "closed"
	POStatusCancelled         POStatus = "cancelled"
)

func AllPOStatuses() []POStatus {
	return []POStatus{
		POStatusDraft,
		POStatusConfirmed,
		POStatusPartiallyReceived,
		POStatusReceived,
		POStatusClosed,
		POStatusCancelled,
	}
}

func POStatusLabel(s POStatus) string {
	switch s {
	case POStatusDraft:
		return "Draft"
	case POStatusConfirmed:
		return "Confirmed"
	case POStatusPartiallyReceived:
		return "Partially Received"
	case POStatusReceived:
		return "Received"
	case POStatusClosed:
		return "Closed"
	case POStatusCancelled:
		return "Cancelled"
	default:
		return string(s)
	}
}

// ── PurchaseOrder model ───────────────────────────────────────────────────────

// PurchaseOrder is the AP commercial commitment document.
// It records what the company intends to buy from a vendor.
// No journal entry is created; accounting truth begins with the Bill.
type PurchaseOrder struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	PONumber string   `gorm:"not null;default:'';index"`
	VendorID uint     `gorm:"not null;index"`
	Vendor   Vendor   `gorm:"foreignKey:VendorID"`
	Status   POStatus `gorm:"type:text;not null;default:'draft'"`

	PODate       time.Time  `gorm:"not null"`
	ExpectedDate *time.Time // expected delivery / fulfilment date

	CurrencyCode string          `gorm:"type:varchar(3);not null;default:''"`
	ExchangeRate decimal.Decimal `gorm:"type:numeric(20,8);not null;default:1"`

	// Cached totals (recomputed by service on save)
	Subtotal decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	TaxTotal decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	Amount   decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"` // Subtotal + TaxTotal

	Notes string `gorm:"type:text;not null;default:''"`
	Memo  string `gorm:"type:text;not null;default:''"`

	Lines []PurchaseOrderLine `gorm:"foreignKey:PurchaseOrderID"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ── PurchaseOrderLine model ───────────────────────────────────────────────────

// PurchaseOrderLine is one line item on a PurchaseOrder.
type PurchaseOrderLine struct {
	ID              uint          `gorm:"primaryKey"`
	CompanyID       uint          `gorm:"not null;index"`
	PurchaseOrderID uint          `gorm:"not null;index"`
	PurchaseOrder   *PurchaseOrder `gorm:"foreignKey:PurchaseOrderID"`

	SortOrder uint `gorm:"not null;default:1"`

	ProductServiceID *uint           `gorm:"index"`
	ProductService   *ProductService `gorm:"foreignKey:ProductServiceID"`

	Description string `gorm:"not null;default:''"`

	Qty       decimal.Decimal `gorm:"type:numeric(10,4);not null;default:1"`
	UnitPrice decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`

	// UOM snapshot (Phase U2 — 2026-04-25). Defaults to
	// ProductService.PurchaseUOM at create time so a PO → Bill
	// conversion carries the same UOM forward.  See UOM_DESIGN.md §3.2.
	LineUOM       string          `gorm:"type:varchar(16);not null;default:'EA'"`
	LineUOMFactor decimal.Decimal `gorm:"type:numeric(18,6);not null;default:1"`
	QtyInStockUOM decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`

	TaxCodeID *uint    `gorm:"index"`
	TaxCode   *TaxCode `gorm:"foreignKey:TaxCodeID"`

	// ExpenseAccountID: GL account to debit on billing (mirrors BillLine).
	ExpenseAccountID *uint    `gorm:"index"`
	ExpenseAccount   *Account `gorm:"foreignKey:ExpenseAccountID"`

	// Cached computed values
	LineNet   decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	LineTax   decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	LineTotal decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
