// 遵循project_guide.md
package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// ── Movement type ────────────────────────────────────────────────────────────

// InventoryMovementType identifies the reason for a stock change.
// New values can be added without migration; the column is TEXT.
type InventoryMovementType string

const (
	// Current phase
	MovementTypeOpening    InventoryMovementType = "opening"
	MovementTypeAdjustment InventoryMovementType = "adjustment"

	// Future — purchase / sales cycle
	MovementTypePurchase InventoryMovementType = "purchase"
	MovementTypeSale     InventoryMovementType = "sale"
	MovementTypeRefund   InventoryMovementType = "refund"

	// Future — external channel
	MovementTypeAmazonOrder  InventoryMovementType = "amazon_order"
	MovementTypeAmazonRefund InventoryMovementType = "amazon_refund"

	// Future — assembly / manufacturing
	MovementTypeAssemblyBuild   InventoryMovementType = "assembly_build"
	MovementTypeAssemblyUnbuild InventoryMovementType = "assembly_unbuild"
	MovementTypeMfgIssue        InventoryMovementType = "manufacturing_issue"
	MovementTypeMfgReceipt      InventoryMovementType = "manufacturing_receipt"
)

// ── Location type ────────────────────────────────────────────────────────────

// LocationType identifies where inventory is held.
type LocationType string

const (
	LocationTypeInternal   LocationType = "internal"
	LocationTypeAmazonFBA  LocationType = "amazon_fba"
	LocationTypeThirdParty LocationType = "third_party"
	LocationTypeAdjBucket  LocationType = "adjustment_bucket"
)

// ── Inventory movement ───────────────────────────────────────────────────────

// InventoryMovement records a single stock-level change for an item.
// quantity_delta is positive for inflows and negative for outflows.
//
// source_type + source_id trace the originating document (e.g. "bill", 42 or
// "adjustment", 7). This mirrors the JournalEntry source_type/source_id pattern.
//
// WarehouseID (nullable) links to a Warehouse row when multi-warehouse is active.
// Nil means the movement used the legacy LocationType/LocationRef path.
type InventoryMovement struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	ItemID        uint                  `gorm:"not null;index"`
	Item          ProductService        `gorm:"foreignKey:ItemID"`
	MovementType  InventoryMovementType `gorm:"type:text;not null"`
	QuantityDelta decimal.Decimal       `gorm:"type:numeric(18,4);not null"`

	// Document-currency unit cost (as captured at event time, raw input).
	UnitCost  *decimal.Decimal `gorm:"type:numeric(18,4)"`
	// Document-currency total cost = abs(QuantityDelta) × UnitCost.
	TotalCost *decimal.Decimal `gorm:"type:numeric(18,2)"`

	// ── Inventory API contract fields (Phase D.0, migration 056) ────────────
	// See INVENTORY_MODULE_API.md §6.

	// CurrencyCode is the ISO-4217 code of the document that drove this
	// movement. Empty when the movement was booked in company base currency
	// via a legacy path.
	CurrencyCode string `gorm:"type:varchar(3);not null;default:''"`

	// ExchangeRate converts UnitCost (document currency) into UnitCostBase
	// (company base). Base-currency movements use 1. Null on legacy rows.
	ExchangeRate *decimal.Decimal `gorm:"type:numeric(20,8)"`

	// UnitCostBase is the base-currency unit cost actually booked. Includes
	// apportioned landed cost for receipts. GL uses this × |QuantityDelta|
	// to post Dr Inventory / Cr COGS.
	UnitCostBase *decimal.Decimal `gorm:"type:numeric(18,4)"`

	// LandedCostAllocation is the per-line apportioned freight / duty / etc.
	// in base currency. Included in UnitCostBase on receive; kept separately
	// for reporting ("what part of cost is landed?").
	LandedCostAllocation *decimal.Decimal `gorm:"type:numeric(18,2)"`

	// ── Traceability ────────────────────────────────────────────────────────
	SourceType   string `gorm:"type:text;not null;default:''"`
	SourceID     *uint
	// SourceLineID narrows the reference to a specific document line (one
	// Bill has many lines, each producing its own movement). Nullable for
	// header-level sources like opening balances or stock counts.
	SourceLineID *uint `gorm:"index"`

	// (The old JournalEntryID reverse coupling was dropped in Phase D.0
	// slice 8 — migration 057. Readers that need the JE for a movement
	// resolve it via source_type + source_id -> business document ->
	// document.journal_entry_id.)

	ReferenceNote string `gorm:"type:text;not null;default:''"`
	// Memo is the human-readable context written by the IN event caller
	// ("Received under PO-2026-045"). Independent from ReferenceNote which
	// predates the API contract.
	Memo string `gorm:"type:text;not null;default:''"`

	// ── Audit + idempotency ────────────────────────────────────────────────
	// IdempotencyKey guards against replay. Format convention:
	// "<source_type>:<source_id>:line:<line_id>:v<version>". Unique per
	// company when non-null (partial index, migration 056).
	IdempotencyKey string `gorm:"type:text"`

	// ActorUserID: who triggered this movement; nullable for system events
	// (nightly FBA sync, scheduled revaluation).
	ActorUserID *uint `gorm:"index"`

	// ── Reversal linkage ───────────────────────────────────────────────────
	// Bidirectional: the original points to its reversal, the reversal
	// points to its original. Mutually exclusive on any given row.
	ReversedByMovementID *uint `gorm:"index"`
	ReversalOfMovementID *uint `gorm:"index"`

	// ── Location ───────────────────────────────────────────────────────────
	WarehouseID *uint      `gorm:"index"`
	Warehouse   *Warehouse `gorm:"foreignKey:WarehouseID"`

	MovementDate time.Time `gorm:"type:date;not null"`
	CreatedAt    time.Time
}

// ── Inventory balance ────────────────────────────────────────────────────────

// InventoryBalance is a materialized summary of stock on hand for an item at a
// specific location. Updated incrementally when movements are recorded.
//
// location_type + location_ref support external-channel scenarios:
//   - amazon_fba / "ATVPDKIKX0DER" → FBA US marketplace
//   - third_party / "3PL-XYZ"     → external 3PL warehouse
//
// For internal warehouses, WarehouseID (nullable FK → warehouses) is the
// preferred key. Legacy rows created before multi-warehouse support use
// LocationType="internal" / LocationRef="" with WarehouseID=nil.
type InventoryBalance struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	ItemID       uint           `gorm:"not null;index"`
	Item         ProductService `gorm:"foreignKey:ItemID"`
	LocationType LocationType   `gorm:"type:text;not null;default:'internal'"`
	LocationRef  string         `gorm:"type:text;not null;default:''"`

	// Multi-warehouse routing (nullable for backward compatibility)
	WarehouseID *uint      `gorm:"index"`
	Warehouse   *Warehouse `gorm:"foreignKey:WarehouseID"`

	QuantityOnHand decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`
	AverageCost    decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`

	UpdatedAt time.Time
}
