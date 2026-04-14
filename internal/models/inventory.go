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
	UnitCost      *decimal.Decimal      `gorm:"type:numeric(18,4)"`
	TotalCost     *decimal.Decimal      `gorm:"type:numeric(18,2)"`

	SourceType     string `gorm:"type:text;not null;default:''"`
	SourceID       *uint
	JournalEntryID *uint `gorm:"index"` // links to the JE created in the same transaction
	ReferenceNote  string `gorm:"type:text;not null;default:''"`

	// Multi-warehouse routing (nullable for backward compatibility)
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
