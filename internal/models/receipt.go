// 遵循project_guide.md
package models

// receipt.go — Phase H slice H.2 inbound Receipt document.
//
// Distinct from ar_receipt.go's CustomerReceipt
// ----------------------------------------------
// CustomerReceipt (AR side) records that the company *received cash
// from a customer*. This file's Receipt (Phase H) records that the
// company *received goods from a vendor into a warehouse*. Different
// domain, different table, different consumers. Name collision avoided
// by table names (`receipts` vs `customer_receipts`) and by audience —
// CustomerReceipt is payment, Receipt is inbound stock.
//
// Role in Phase H
// ---------------
// Receipt is the first-class document that, starting in H.3, produces
// inventory truth (Dr Inventory / Cr GR/IR) when posted. In H.2 it is
// a document-layer shell only: Create / Read / Update (draft) / Post
// (status flip) / Void (status flip) / Delete (draft) exist, but
// Post and Void have zero side effects on inventory or GL. That
// wiring lands in H.3.
//
// Relationship to the receipt_required rail
// -----------------------------------------
// H.2 does NOT consult companies.receipt_required. Receipt creation
// and posting are allowed regardless of the flag. The flag's consumer
// is Bill-side (H.4 — flag=true disables Bill-forms-inventory) and
// matching (H.5). Receipt itself is flag-agnostic.

import (
	"time"

	"github.com/shopspring/decimal"
)

// ReceiptStatus tracks the lifecycle of an inbound Receipt.
//
// Lifecycle (H.2 scope):
//
//	draft    → posted   (via PostReceipt; requires Status==draft)
//	posted   → voided   (via VoidReceipt; requires Status==posted)
//	draft    → deleted  (via DeleteReceipt; requires Status==draft)
//
// Terminal states: voided. Deleted rows leave no document trace by
// design — drafts that never posted carry no audit obligation.
//
// H.2 locks the state machine at these transitions. Later slices
// (H.3 post, H.5 matching) bolt behavior onto Post/Void; they do
// NOT add new states without a dedicated slice and doc update.
type ReceiptStatus string

const (
	ReceiptStatusDraft  ReceiptStatus = "draft"
	ReceiptStatusPosted ReceiptStatus = "posted"
	ReceiptStatusVoided ReceiptStatus = "voided"
)

// AllReceiptStatuses returns receipt statuses in logical order.
func AllReceiptStatuses() []ReceiptStatus {
	return []ReceiptStatus{
		ReceiptStatusDraft,
		ReceiptStatusPosted,
		ReceiptStatusVoided,
	}
}

// Receipt is an inbound goods receipt header.
//
// Identity: (CompanyID, ReceiptNumber) is intended to be unique in
// practice; the uniqueness is not enforced by a DB constraint in H.2
// — the service layer assigns numbers and numbering logic (with
// company number sequences) lands in a later slice. Blank
// ReceiptNumber is allowed for draft receipts that have not yet been
// assigned one.
//
// VendorID is nullable because a receipt MAY represent an inbound
// event not yet attributed to a specific vendor (e.g. a warehouse-
// recorded delivery awaiting paperwork). H.3 will enforce vendor
// presence at post-time if accounting requires it; H.2 leaves it
// optional.
//
// WarehouseID is required — a receipt has to land somewhere.
//
// PurchaseOrderID is a nullable reservation field for Phase I's
// PO → Receipt → Bill identity chain. It is accepted on input and
// stored, but no consumer reads it in H.2.
type Receipt struct {
	ID uint `gorm:"primaryKey"`

	CompanyID uint `gorm:"not null;index"`

	// ReceiptNumber is the human-facing document identifier. Empty
	// string allowed for drafts. Numbering strategy (per-company
	// sequence, prefix) is owned by the service layer; the column
	// itself is just storage.
	ReceiptNumber string `gorm:"not null;default:''"`

	// VendorID is nullable — receipt may be pre-vendor-attribution.
	VendorID *uint   `gorm:"index"`
	Vendor   *Vendor `gorm:"foreignKey:VendorID"`

	// WarehouseID is required — the receipt lands here.
	WarehouseID uint       `gorm:"not null;index"`
	Warehouse   *Warehouse `gorm:"foreignKey:WarehouseID"`

	// ReceiptDate is the effective date of the inbound event (date
	// goods arrived / inspection completed). Kept separate from
	// CreatedAt so back-dated receipts are possible.
	ReceiptDate time.Time `gorm:"not null"`

	// Status carries the document lifecycle. Default 'draft' on create.
	Status ReceiptStatus `gorm:"type:text;not null;default:'draft'"`

	// Memo is free-form internal notes.
	Memo string `gorm:"not null;default:''"`

	// Reference is the external reference (packing slip / delivery
	// note / carrier tracking) provided by the vendor or carrier.
	// Kept separate from ReceiptNumber, which is the company-internal
	// document ID.
	Reference string `gorm:"not null;default:''"`

	// PurchaseOrderID is a Phase I source-identity reservation. H.2
	// stores but does not read. No FK constraint in H.2.
	PurchaseOrderID *uint `gorm:"index"`

	// Lifecycle timestamps. PostedAt is set once on the draft→posted
	// transition; VoidedAt is set once on the posted→voided transition.
	// Neither is cleared once set.
	PostedAt *time.Time
	VoidedAt *time.Time

	// JournalEntryID links this Receipt to the JE that booked its
	// inventory accrual (Dr Inventory / Cr GR/IR) at post time. Set
	// only when PostReceipt ran under `companies.receipt_required=true`
	// and the receipt had at least one stock-item line. Nil means
	// either (a) posted under flag=false (H.2 byte-identical status
	// flip) or (b) no stock lines on the receipt.
	//
	// VoidReceipt uses the presence of this link to decide whether
	// to reverse a JE + movements (non-nil) or to status-flip only
	// (nil). The column is deliberately not FK'd to journal_entries
	// at the schema layer, matching the existing convention used by
	// bills.journal_entry_id.
	JournalEntryID *uint         `gorm:"index"`
	JournalEntry   *JournalEntry `gorm:"foreignKey:JournalEntryID"`

	Lines []ReceiptLine `gorm:"foreignKey:ReceiptID"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName maps Receipt to the `receipts` table. Explicit to avoid
// any accidental GORM default-pluralization drift and to make the
// binding obvious to readers scanning the model.
func (Receipt) TableName() string {
	return "receipts"
}

// ReceiptLine is one item / qty / cost line on a Receipt.
//
// UnitCost is the per-unit cost of the inbound item at the time of
// receipt. H.2 stores this field but does not read it. H.3's
// ReceiveStockFromReceipt consumes UnitCost to form inventory cost
// layers (FIFO) or contribute to moving-average cost calculation.
//
// LotNumber / LotExpiryDate are the Phase H tracking-capture home
// for lot-tracked inbound. In H.2 they are accepted on input and
// persisted; H.3 will forward them into inventory.ReceiveStock per
// the F2 create-or-top-up rules. For non-lot-tracked items these
// stay empty / nil.
//
// PurchaseOrderLineID is the line-level Phase I source-identity
// reservation. H.2 stores but does not read.
type ReceiptLine struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`
	ReceiptID uint `gorm:"not null;index"`

	SortOrder int `gorm:"not null;default:0"`

	ProductServiceID uint            `gorm:"not null;index"`
	ProductService   *ProductService `gorm:"foreignKey:ProductServiceID"`

	Description string `gorm:"not null;default:''"`

	Qty      decimal.Decimal `gorm:"type:numeric(18,6);not null;default:0"`
	Unit     string          `gorm:"not null;default:''"`
	UnitCost decimal.Decimal `gorm:"type:numeric(18,6);not null;default:0"`

	LotNumber     string     `gorm:"not null;default:''"`
	LotExpiryDate *time.Time `gorm:"type:date"`

	PurchaseOrderLineID *uint `gorm:"index"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName maps ReceiptLine to the `receipt_lines` table.
func (ReceiptLine) TableName() string {
	return "receipt_lines"
}
