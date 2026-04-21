// 遵循project_guide.md
package models

// vendor_return_shipment.go — Phase I slice I.6b.1 outbound AP-return
// document (VendorReturnShipment + VendorReturnShipmentLine).
//
// Role in Phase I.6 (see INVENTORY_MODULE_API.md §7 Phase I.6 +
// PHASE_I6_CHARTER.md)
// -----------------------------------------------------------------
// VendorReturnShipment is the physical-truth document for our
// returns of stock items TO a vendor (we ship goods out; vendor
// takes them back). Buy-side mirror of Shipment (I.3) in document
// shape — single warehouse per document, lifecycle
// draft → posted → voided, one item/qty per line. Specialised for
// the return direction: goods LEAVE a warehouse (Dr AP / Cr
// Inventory at traced cost, wired in I.6b.2 via the dedicated
// narrow verb from I.6b.2a).
//
// Distinct from sibling documents by audience + table name:
//   - `vendor_return_shipments` (this file, Phase I.6b) — we return
//     stock to vendor.
//   - `shipments` (shipment.go, Phase I) — customer gets stock from
//     us (forward sell-side outbound).
//   - `vendor_returns` (ap_vendor_return.go, AP Phase A pre-IN.6a) —
//     a separate AP business-fact concept (no JE, no inventory
//     movement) that predates the Rule #4 work. A VendorCreditNote
//     may carry a VendorReturnID pointer to one of these; NOT the
//     same table as this file.
//   - `vendor_credit_notes` (ap_credit_note.go) — commercial
//     document that books the financial side (Dr AP / Cr Offset).
//
// The charter Q2 UI label is "Return to Vendor" (not "Vendor Return
// Shipment"). The internal model / table / source_type keep the
// full name to disambiguate from `models.VendorReturn`.
//
// Commercial-document linkage
// ---------------------------
// Each VendorReturnShipment ties to a VendorCreditNote header
// (VendorCreditNoteID) and each line to a specific
// VendorCreditNoteLine (VendorCreditNoteLineID). The per-line link
// carries the identity chain used at post time in I.6b.2:
//
//   BillLine → VendorCreditNoteLine → VendorReturnShipmentLine → inventory_movement
//
// The inventory outflow cost comes from the ORIGINAL Bill's
// inventory_movement — traced via
// VendorCreditNoteLine.OriginalBillLineID (the IN.6a field).
// VendorReturnShipment never declares cost; the I.6b.2a narrow
// verb reads `unit_cost_base` from the source movement internally
// and writes the outflow at that exact cost.
//
// Per charter Q7 hard rules, BOTH FKs (header VendorCreditNoteID +
// line VendorCreditNoteLineID) are NULLABLE at schema level.
// Legality is enforced at service layer — Q8's "no standalone
// Return Shipment" is a save-time check, Q6's "exact per-line
// coverage" is a VCN-post-time check (wired in I.6b.3). Schema
// nullability keeps orphan rows recoverable (Q7 mitigation #4) — if
// a VCN is voided after the physical movement posted, the
// VendorReturnShipment stays and its own void reverses its own
// movement independently per Q5 document-local rule.
//
// What this slice (I.6b.1) does NOT do
// ------------------------------------
// Post is a status flip only. No inventory movement, no JE, no VCN
// coupling. The narrow traced-cost outflow verb itself lands in
// I.6b.2a (`IssueVendorReturn` / `ReturnToVendorAtTracedCost`,
// final name pinned at that slice's start). Service wrapping
// (`CreateVendorReturnShipment` / `PostVendorReturnShipment` /
// `VoidVendorReturnShipment`) lands in I.6b.2. The VCN controlled-
// mode retrofit (`Rule4DocVendorCreditNote.IsMovementOwner` →
// surrenders to new `Rule4DocVendorReturnShipment` under
// `receipt_required=true`, + posted-void symmetry extension) lands
// in I.6b.3.
//
// Why there is no UnitCost on VendorReturnShipmentLine
// -----------------------------------------------------
// Same reason as ShipmentLine (I.2), ReceiptLine's outbound peer,
// and ARReturnReceiptLine (I.6a.1). Outbound cost is authoritative
// from the inventory module, never from the business-document
// layer. For AP return-at-traced-cost specifically, charter Q3
// locks a dedicated narrow inventory verb that takes lineage +
// intent only — the module reads `unit_cost_base` internally.
// Adding a UnitCost column here would create a second source of
// truth and reintroduce exactly the authority creep Q3 exists to
// prevent. Forbidden.

import (
	"time"

	"github.com/shopspring/decimal"
)

// VendorReturnShipmentStatus tracks the lifecycle of an outbound
// VendorReturnShipment.
//
// Lifecycle (I.6b.1 scope):
//
//	draft    → posted   (via PostVendorReturnShipment; requires Status==draft)
//	posted   → voided   (via VoidVendorReturnShipment; requires Status==posted)
//	draft    → deleted  (via DeleteVendorReturnShipment; requires Status==draft)
//
// Terminal states: voided. Deleted rows leave no document trace by
// design — drafts that never posted carry no audit obligation.
//
// I.6b.1 locks the state machine at these transitions. Later slices
// (I.6b.2 post wiring, I.6b.3 VCN retrofit + VCN posted-void
// symmetry extension) bolt behavior onto Post/Void; they do NOT
// add new states without a dedicated slice and doc update.
type VendorReturnShipmentStatus string

const (
	VendorReturnShipmentStatusDraft  VendorReturnShipmentStatus = "draft"
	VendorReturnShipmentStatusPosted VendorReturnShipmentStatus = "posted"
	VendorReturnShipmentStatusVoided VendorReturnShipmentStatus = "voided"
)

// AllVendorReturnShipmentStatuses returns statuses in logical order.
func AllVendorReturnShipmentStatuses() []VendorReturnShipmentStatus {
	return []VendorReturnShipmentStatus{
		VendorReturnShipmentStatusDraft,
		VendorReturnShipmentStatusPosted,
		VendorReturnShipmentStatusVoided,
	}
}

// VendorReturnShipment is an outbound stock-return-to-vendor header.
//
// Identity: (CompanyID, VendorReturnShipmentNumber) is intended to
// be unique in practice; uniqueness is not enforced by a DB
// constraint in I.6b.1 — the service layer assigns numbers and
// numbering logic (with company number sequences) lands in a later
// slice. Blank number allowed for drafts.
//
// VendorID is nullable because a return MAY represent an outbound
// event not yet attributed to a specific vendor (e.g. a warehouse-
// recorded dispatch awaiting paperwork). I.6b.2 will enforce vendor
// presence at post-time if accounting requires it.
//
// WarehouseID is required — goods have to leave from somewhere
// (matching the charter non-scope "Multi-warehouse split returns" —
// one Return Shipment = one source warehouse).
//
// VendorCreditNoteID is nullable at schema per Q7 hard rule #1
// (orphan rows recoverable) but required at service save-time per
// Q8 (no standalone Return Shipment). The service enforces the
// draft-or-posted VCN link.
type VendorReturnShipment struct {
	ID uint `gorm:"primaryKey"`

	CompanyID uint `gorm:"not null;index"`

	// VendorReturnShipmentNumber is the human-facing document
	// identifier. Empty string allowed for drafts. Numbering
	// strategy is owned by the service layer; the column itself is
	// just storage.
	VendorReturnShipmentNumber string `gorm:"not null;default:''"`

	// VendorID is nullable — return may be pre-vendor-attribution.
	VendorID *uint   `gorm:"index"`
	Vendor   *Vendor `gorm:"foreignKey:VendorID"`

	// WarehouseID is required — the return leaves from here.
	WarehouseID uint       `gorm:"not null;index"`
	Warehouse   *Warehouse `gorm:"foreignKey:WarehouseID"`

	// ShipDate is the effective date of the outbound event (date
	// goods left our warehouse en route to the vendor). Kept
	// separate from CreatedAt so back-dated returns are possible.
	ShipDate time.Time `gorm:"not null"`

	// Status carries the document lifecycle. Default 'draft' on create.
	Status VendorReturnShipmentStatus `gorm:"type:text;not null;default:'draft'"`

	// Memo is free-form internal notes.
	Memo string `gorm:"not null;default:''"`

	// Reference is the external reference (carrier waybill / BOL /
	// vendor RMA number). Kept separate from the company-internal
	// VendorReturnShipmentNumber.
	Reference string `gorm:"not null;default:''"`

	// VendorCreditNoteID is the commercial-document link. Nullable
	// at schema per Q7 hard rule #1; required at service-layer
	// save-time per Q8. No DB-level FK constraint — cross-tenant +
	// existence checks live in the service layer (I.6b.2).
	VendorCreditNoteID *uint             `gorm:"index"`
	VendorCreditNote   *VendorCreditNote `gorm:"foreignKey:VendorCreditNoteID"`

	// Lifecycle timestamps. Neither is cleared once set.
	PostedAt *time.Time
	VoidedAt *time.Time

	// JournalEntryID links this VRS to the JE that booked its
	// inventory outflow + AP reduction (Dr AP / Cr Inventory at
	// traced cost) at post time. Set only when
	// PostVendorReturnShipment ran under
	// `companies.receipt_required=true` and the shipment had at
	// least one stock-item line. Nil means either (a) posted under
	// flag=false (status-flip only; IN.6a's VCN retains movement
	// ownership) or (b) no stock lines.
	//
	// VoidVendorReturnShipment uses the presence of this link to
	// decide whether to reverse a JE + movements (non-nil, Q5
	// document-local) or to status-flip only (nil). Not FK'd at the
	// schema layer — matches bills / receipts / shipments /
	// ar_return_receipts convention.
	JournalEntryID *uint         `gorm:"index"`
	JournalEntry   *JournalEntry `gorm:"foreignKey:JournalEntryID"`

	Lines []VendorReturnShipmentLine `gorm:"foreignKey:VendorReturnShipmentID"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName maps VendorReturnShipment to the `vendor_return_shipments`
// table. Explicit to avoid any accidental GORM default-pluralisation
// drift.
func (VendorReturnShipment) TableName() string {
	return "vendor_return_shipments"
}

// VendorReturnShipmentLine is one item / qty line on a
// VendorReturnShipment.
//
// Qty is the quantity leaving the warehouse on this line. I.6b.1
// stores it but does not consume it; I.6b.2's
// PostVendorReturnShipment will pass it as the outflow qty through
// the I.6b.2a narrow verb (at the traced cost read from the source
// bill-line's inventory_movement).
//
// There is deliberately NO UnitCost column — see file-level comment.
//
// Lot / serial selections are also NOT carried on the line in I.6b.1.
// I.6b.2 may introduce the tracking-selection payload shape when
// the PostVendorReturnShipment consumer is wired; pre-baking schema
// before the use site has informed it would commit to a shape
// prematurely.
//
// VendorCreditNoteLineID is the Q7-chosen junior-side FK. Nullable
// at schema (Q7 hard rule #1), required at post time (Q7 hard
// rule #2 + Q6 exact-coverage check wired in I.6b.3). No DB-level
// FK constraint — the service layer is the enforcement boundary.
type VendorReturnShipmentLine struct {
	ID                       uint `gorm:"primaryKey"`
	CompanyID                uint `gorm:"not null;index"`
	VendorReturnShipmentID   uint `gorm:"not null;index"`

	SortOrder int `gorm:"not null;default:0"`

	ProductServiceID uint            `gorm:"not null;index"`
	ProductService   *ProductService `gorm:"foreignKey:ProductServiceID"`

	Description string `gorm:"not null;default:''"`

	Qty  decimal.Decimal `gorm:"type:numeric(18,6);not null;default:0"`
	Unit string          `gorm:"not null;default:''"`

	// VendorCreditNoteLineID is the line-level commercial link.
	// Carries the identity chain (BillLine → VendorCreditNoteLine →
	// this line → inventory_movement) used by I.6b.2 to trace the
	// original movement's unit_cost_base.
	VendorCreditNoteLineID *uint                 `gorm:"index"`
	VendorCreditNoteLine   *VendorCreditNoteLine `gorm:"foreignKey:VendorCreditNoteLineID"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName maps VendorReturnShipmentLine to the
// `vendor_return_shipment_lines` table.
func (VendorReturnShipmentLine) TableName() string {
	return "vendor_return_shipment_lines"
}
