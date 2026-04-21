// 遵循project_guide.md
package models

// ar_return_receipt.go — Phase I slice I.6a.1 inbound AR-return
// document (ARReturnReceipt + ARReturnReceiptLine).
//
// Role in Phase I.6 (see INVENTORY_MODULE_API.md §7 Phase I.6 +
// PHASE_I6_CHARTER.md)
// -----------------------------------------------------------------
// ARReturnReceipt is the physical-truth document for customer
// returns of stock items. Sell-side mirror of Receipt (H.3) in
// document shape — single warehouse per document, lifecycle
// draft → posted → voided, one item/qty per line. Specialised for
// the return direction: goods land BACK at a warehouse (Dr Inventory
// / Cr COGS at traced cost, wired in I.6a.2).
//
// Distinct from sibling documents by audience + table name:
//   - `ar_return_receipts` (this file, Phase I.6a) — customer
//     returns stock to us.
//   - `receipts` (receipt.go, Phase H) — vendor delivers stock to us.
//   - `customer_receipts` (ar_receipt.go, AR Phase 4) — customer
//     pays us cash.
//
// Commercial-document linkage
// ---------------------------
// Each ARReturnReceipt is tied to a CreditNote header (CreditNoteID)
// and each line to a specific CreditNoteLine (CreditNoteLineID). The
// per-line link carries the identity chain used at post time in
// I.6a.2:
//
//   InvoiceLine → CreditNoteLine → ARReturnReceiptLine → inventory_movement
//
// The inventory-return cost comes from the ORIGINAL Invoice's
// inventory_movement — traced via CreditNoteLine.OriginalInvoiceLineID
// (the IN.5 field). ARReturnReceipt never declares cost; it consumes
// the traced cost at post time.
//
// Per charter Q7 hard rules, BOTH FKs (header CreditNoteID + line
// CreditNoteLineID) are NULLABLE at schema level. Legality is
// enforced at service layer — Q8's "no standalone Return Receipt"
// is a save-time check, Q6's "exact per-line coverage" is a
// CreditNote-post-time check (wired in I.6a.3). Schema nullability
// keeps orphan rows recoverable (Q7 mitigation #4) — if a CreditNote
// is voided after the physical movement posted, the ARReturnReceipt
// stays and its own void reverses its own movement independently
// per Q5 document-local rule.
//
// What this slice (I.6a.1) does NOT do
// ------------------------------------
// Post is a status flip only. No inventory movement, no JE, no
// CreditNote coupling. That wiring lands in I.6a.2 (service layer:
// CreateARReturnReceipt / PostARReturnReceipt / VoidARReturnReceipt —
// uses inventory.ReceiveStock at traced cost). The CreditNote
// controlled-mode retrofit (Rule4DocCreditNote surrenders movement
// ownership to Rule4DocARReturnReceipt under shipment_required=true)
// lands in I.6a.3.
//
// Why there is no UnitCost on ARReturnReceiptLine
// ------------------------------------------------
// Per the authoritative-cost principle in INVENTORY_MODULE_API.md §2,
// outbound AND return cost is authoritative from the inventory
// module, not declared by the business-document layer. I.6a.2's
// PostARReturnReceipt will call inventory.ReceiveStock at the traced
// cost read from the source invoice-line's inventory_movement via
// CreditNoteLine.OriginalInvoiceLineID. Adding a UnitCost column
// here would create a second source of truth — the same silent
// authority conflict that keeps UnitCost off ShipmentLine. Forbidden.

import (
	"time"

	"github.com/shopspring/decimal"
)

// ARReturnReceiptStatus tracks the lifecycle of an inbound
// ARReturnReceipt.
//
// Lifecycle (I.6a.1 scope):
//
//	draft    → posted   (via PostARReturnReceipt; requires Status==draft)
//	posted   → voided   (via VoidARReturnReceipt; requires Status==posted)
//	draft    → deleted  (via DeleteARReturnReceipt; requires Status==draft)
//
// Terminal states: voided. Deleted rows leave no document trace by
// design — drafts that never posted carry no audit obligation.
//
// I.6a.1 locks the state machine at these transitions. Later slices
// (I.6a.2 post wiring, I.6a.3 CreditNote retrofit) bolt behaviour
// onto Post/Void; they do NOT add new states without a dedicated
// slice and doc update.
type ARReturnReceiptStatus string

const (
	ARReturnReceiptStatusDraft  ARReturnReceiptStatus = "draft"
	ARReturnReceiptStatusPosted ARReturnReceiptStatus = "posted"
	ARReturnReceiptStatusVoided ARReturnReceiptStatus = "voided"
)

// AllARReturnReceiptStatuses returns ARReturnReceipt statuses in
// logical order.
func AllARReturnReceiptStatuses() []ARReturnReceiptStatus {
	return []ARReturnReceiptStatus{
		ARReturnReceiptStatusDraft,
		ARReturnReceiptStatusPosted,
		ARReturnReceiptStatusVoided,
	}
}

// ARReturnReceipt is an inbound customer-return receipt header.
//
// Identity: (CompanyID, ReturnReceiptNumber) is intended to be
// unique in practice; uniqueness is not enforced by a DB constraint
// in I.6a.1 — the service layer assigns numbers and numbering logic
// (with company number sequences) lands in a later slice. Blank
// ReturnReceiptNumber is allowed for drafts that have not yet been
// assigned one.
//
// CustomerID is nullable because a return MAY represent an inbound
// event not yet attributed to a specific customer (e.g. a warehouse-
// recorded arrival awaiting paperwork). I.6a.2 will enforce customer
// presence at post-time if accounting requires it; I.6a.1 leaves it
// optional to mirror Receipt's conservative stance.
//
// WarehouseID is required — goods have to land somewhere (matching
// the charter non-scope "Multi-warehouse split returns" — one
// Return Receipt lands in exactly one warehouse).
//
// CreditNoteID is nullable at schema per Q7 hard rule #1 (orphan
// rows recoverable) but required at service save-time per Q8 (no
// standalone Return Receipt). The service enforces the draft-or-
// posted CreditNote link.
type ARReturnReceipt struct {
	ID uint `gorm:"primaryKey"`

	CompanyID uint `gorm:"not null;index"`

	// ReturnReceiptNumber is the human-facing document identifier.
	// Empty string allowed for drafts. Numbering strategy is owned
	// by the service layer; the column itself is just storage.
	ReturnReceiptNumber string `gorm:"not null;default:''"`

	// CustomerID is nullable — return may be pre-customer-attribution.
	CustomerID *uint     `gorm:"index"`
	Customer   *Customer `gorm:"foreignKey:CustomerID"`

	// WarehouseID is required — the return lands here.
	WarehouseID uint       `gorm:"not null;index"`
	Warehouse   *Warehouse `gorm:"foreignKey:WarehouseID"`

	// ReturnDate is the effective date of the inbound event (date
	// goods arrived back at the warehouse). Kept separate from
	// CreatedAt so back-dated returns are possible.
	ReturnDate time.Time `gorm:"not null"`

	// Status carries the document lifecycle. Default 'draft' on create.
	Status ARReturnReceiptStatus `gorm:"type:text;not null;default:'draft'"`

	// Memo is free-form internal notes.
	Memo string `gorm:"not null;default:''"`

	// Reference is the external reference (customer RMA number /
	// return-tracking code). Kept separate from ReturnReceiptNumber,
	// which is the company-internal document ID.
	Reference string `gorm:"not null;default:''"`

	// CreditNoteID is the commercial-document link. Nullable at
	// schema per Q7 hard rule #1; required at service-layer save-time
	// per Q8. No DB-level FK constraint — cross-tenant + existence
	// checks live in the service layer (I.6a.2).
	CreditNoteID *uint       `gorm:"index"`
	CreditNote   *CreditNote `gorm:"foreignKey:CreditNoteID"`

	// Lifecycle timestamps. PostedAt is set once on the draft→posted
	// transition; VoidedAt is set once on the posted→voided transition.
	// Neither is cleared once set.
	PostedAt *time.Time
	VoidedAt *time.Time

	// JournalEntryID links this ARReturnReceipt to the JE that booked
	// its inventory restoration (Dr Inventory / Cr COGS at traced cost)
	// at post time. Set only when PostARReturnReceipt ran under
	// `companies.shipment_required=true` and the receipt had at least
	// one stock-item line. Nil means either (a) posted under flag=false
	// (status-flip-only, IN.5's CreditNote retains movement ownership)
	// or (b) no stock lines on the return.
	//
	// VoidARReturnReceipt uses the presence of this link to decide
	// whether to reverse a JE + movements (non-nil, Q5 document-local)
	// or to status-flip only (nil). The column is deliberately not FK'd
	// to journal_entries at the schema layer, matching the convention
	// used by bills / receipts / shipments.
	JournalEntryID *uint         `gorm:"index"`
	JournalEntry   *JournalEntry `gorm:"foreignKey:JournalEntryID"`

	Lines []ARReturnReceiptLine `gorm:"foreignKey:ARReturnReceiptID"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName maps ARReturnReceipt to the `ar_return_receipts` table.
// Explicit to avoid any accidental GORM default-pluralisation drift.
func (ARReturnReceipt) TableName() string {
	return "ar_return_receipts"
}

// ARReturnReceiptLine is one item / qty line on an ARReturnReceipt.
//
// Qty is the quantity returned on this line. I.6a.1 stores it but
// does not consume it; I.6a.2's PostARReturnReceipt will pass it as
// the receive qty through inventory.ReceiveStock (at the traced cost
// read from the source invoice-line's inventory_movement).
//
// There is deliberately NO UnitCost column — see file-level comment.
//
// Lot / serial selections are also NOT carried on the line in I.6a.1.
// I.6a.2 will introduce the tracking-selection payload shape (likely
// mirroring ReceiptLine's lot_number / lot_expiry_date fields) when
// the PostARReturnReceipt consumer is actually wired; pre-baking the
// schema before the use site has informed it would commit to a shape
// prematurely.
//
// CreditNoteLineID is the Q7-chosen junior-side FK. Nullable at
// schema (Q7 hard rule #1), required at post time (Q7 hard rule #2
// + Q6 exact-coverage check wired in I.6a.3). No DB-level FK
// constraint — the service layer is the enforcement boundary.
type ARReturnReceiptLine struct {
	ID                uint `gorm:"primaryKey"`
	CompanyID         uint `gorm:"not null;index"`
	ARReturnReceiptID uint `gorm:"not null;index"`

	SortOrder int `gorm:"not null;default:0"`

	ProductServiceID uint            `gorm:"not null;index"`
	ProductService   *ProductService `gorm:"foreignKey:ProductServiceID"`

	Description string `gorm:"not null;default:''"`

	Qty  decimal.Decimal `gorm:"type:numeric(18,6);not null;default:0"`
	Unit string          `gorm:"not null;default:''"`

	// CreditNoteLineID is the line-level commercial link. Carries the
	// identity chain (InvoiceLine → CreditNoteLine → this line →
	// inventory_movement) used by I.6a.2 to trace the original
	// movement's unit_cost_base.
	CreditNoteLineID *uint           `gorm:"index"`
	CreditNoteLine   *CreditNoteLine `gorm:"foreignKey:CreditNoteLineID"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName maps ARReturnReceiptLine to the `ar_return_receipt_lines`
// table.
func (ARReturnReceiptLine) TableName() string {
	return "ar_return_receipt_lines"
}
