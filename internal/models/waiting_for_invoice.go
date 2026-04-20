// 遵循project_guide.md
package models

// waiting_for_invoice.go — Phase I slice I.3 operational queue.
//
// Role in Phase I
// ---------------
// Phase I (scope I.B) decouples cost from revenue on the sell side:
// Shipment books COGS at ship time; Invoice books Revenue later. The
// gap between the two is an operational fact, not an accounting
// artefact, and WaitingForInvoiceItem makes that fact queryable.
//
// Deliberately NOT a JournalEntry or LedgerEntry
// ----------------------------------------------
// This table is outside the ledger. Rows here do not book to any
// account. Finance still sees the shipped-but-unbilled gap via the
// GL (COGS debit without a yet-matching Revenue credit) — the
// operational queue is the workflow view of that same gap, not a
// separate double-entry.
//
// Lifecycle summary (enforced by services.WaitingForInvoice* surface)
// -------------------------------------------------------------------
//
//	open    → closed   via Invoice line matching in I.5
//	open    → voided   via Shipment void in I.3
//	closed  → open     via Invoice void in I.5 (reopen, not re-insert)
//
// Terminal states: closed (if the matching Invoice is not later
// voided) and voided (if the Shipment is voided before any Invoice
// matches). "Closed" is not terminal until the matching Invoice's own
// lifecycle ends.
//
// 1:1 with ShipmentLine
// ---------------------
// Each stock-item ShipmentLine with positive qty produces exactly one
// WaitingForInvoiceItem. I.3/I.5 do not support partial closure: the
// Invoice line either fully matches the shipment line qty or the
// Invoice post fails. Partial-match / split-invoice behavior is out
// of Phase I scope and requires a dedicated slice.

import (
	"time"

	"github.com/shopspring/decimal"
)

// WaitingForInvoiceStatus tracks the queue item's lifecycle.
type WaitingForInvoiceStatus string

const (
	WaitingForInvoiceStatusOpen   WaitingForInvoiceStatus = "open"
	WaitingForInvoiceStatusClosed WaitingForInvoiceStatus = "closed"
	WaitingForInvoiceStatusVoided WaitingForInvoiceStatus = "voided"
)

// WaitingForInvoiceItem is one row in the Shipment→Invoice operational
// queue. See file-level comment for the full context.
type WaitingForInvoiceItem struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	// Source identity: never nil — a WFI row without a source
	// shipment/line/item is nonsensical.
	ShipmentID       uint `gorm:"not null;index"`
	ShipmentLineID   uint `gorm:"not null;index"`
	ProductServiceID uint `gorm:"not null;index"`
	WarehouseID      uint `gorm:"not null"`

	// Denormalised context for dashboards. Customer may be absent
	// (Shipment allows nullable customer in I.2), so nullable here.
	CustomerID       *uint `gorm:"index"`
	SalesOrderID     *uint
	SalesOrderLineID *uint

	// QtyPending is the qty from the shipment line. In I.3/I.5 this
	// is set once and not mutated; row closure is atomic.
	QtyPending   decimal.Decimal `gorm:"type:numeric(18,6);not null;default:0"`
	UnitCostBase decimal.Decimal `gorm:"type:numeric(18,6);not null;default:0"`

	ShipDate time.Time `gorm:"type:date;not null"`

	Status WaitingForInvoiceStatus `gorm:"type:text;not null;default:'open';index"`

	// Resolution identity — set when Invoice match closes the row (I.5).
	ResolvedInvoiceID     *uint
	ResolvedInvoiceLineID *uint
	ResolvedAt            *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName binds WaitingForInvoiceItem to the
// `waiting_for_invoice_items` table explicitly. GORM's default
// pluralisation would produce `waiting_for_invoice_items` too, but
// the name is important enough to pin.
func (WaitingForInvoiceItem) TableName() string {
	return "waiting_for_invoice_items"
}
