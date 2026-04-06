// 遵循project_guide.md
package models

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// ── Item type ────────────────────────────────────────────────────────────────

// ProductServiceType classifies what the product/service represents.
type ProductServiceType string

const (
	ProductServiceTypeService      ProductServiceType = "service"
	ProductServiceTypeNonInventory ProductServiceType = "non_inventory"
	ProductServiceTypeInventory    ProductServiceType = "inventory"
)

// AllProductServiceTypes returns the currently supported types in display order.
func AllProductServiceTypes() []ProductServiceType {
	return []ProductServiceType{
		ProductServiceTypeService,
		ProductServiceTypeNonInventory,
		ProductServiceTypeInventory,
	}
}

// ProductServiceTypeLabel returns a human-readable label for a type.
func ProductServiceTypeLabel(t ProductServiceType) string {
	switch t {
	case ProductServiceTypeService:
		return "Service"
	case ProductServiceTypeNonInventory:
		return "Non-Inventory"
	case ProductServiceTypeInventory:
		return "Inventory"
	default:
		return string(t)
	}
}

// ParseProductServiceType parses a raw string into a ProductServiceType, returning an error
// if the value is not recognised.
func ParseProductServiceType(s string) (ProductServiceType, error) {
	switch ProductServiceType(s) {
	case ProductServiceTypeService, ProductServiceTypeNonInventory, ProductServiceTypeInventory:
		return ProductServiceType(s), nil
	default:
		return "", fmt.Errorf("unknown product/service type: %q", s)
	}
}

// ── Item structure type ──────────────────────────────────────────────────────

// ItemStructureType describes whether an item is a single product, a bundle of
// existing items sold as a package, or an assembly whose components are consumed
// during manufacturing/build.
type ItemStructureType string

const (
	// ItemStructureSingle is a standalone item with no component relationships.
	ItemStructureSingle ItemStructureType = "single"
	// ItemStructureBundle is a sellable package of existing items (no inventory
	// transformation; components remain in stock individually).
	ItemStructureBundle ItemStructureType = "bundle"
	// ItemStructureAssembly is a finished good built from component items via a
	// build/manufacturing process (components are consumed, finished good is produced).
	ItemStructureAssembly ItemStructureType = "assembly"
)

// ── Capability defaults ──────────────────────────────────────────────────────

// ApplyTypeDefaults sets capability flags based on the item type.
// Called on create; does not override explicit user choices on update.
func (ps *ProductService) ApplyTypeDefaults() {
	switch ps.Type {
	case ProductServiceTypeService:
		ps.CanBeSold = true
		ps.CanBePurchased = false
		ps.IsStockItem = false
	case ProductServiceTypeNonInventory:
		ps.CanBeSold = true
		ps.CanBePurchased = true
		ps.IsStockItem = false
	case ProductServiceTypeInventory:
		ps.CanBeSold = true
		ps.CanBePurchased = true
		ps.IsStockItem = true
	}
	if ps.ItemStructureType == "" {
		ps.ItemStructureType = ItemStructureSingle
	}
}

// ── ProductService model ─────────────────────────────────────────────────────

// ProductService is a company-scoped item that can appear on invoice and bill lines.
//
// Core identity: Name, Type, SKU.
// Accounting links: RevenueAccountID (required for invoice posting),
//   COGSAccountID and InventoryAccountID (reserved for inventory items).
// Capability flags: CanBeSold, CanBePurchased, IsStockItem — derived from Type
//   on creation but stored independently for future flexibility.
// ItemStructureType: single (default), bundle, or assembly — controls whether
//   the item has component relationships in the item_components table.
type ProductService struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	Name        string             `gorm:"not null"`
	SKU         string             `gorm:"type:text;not null;default:''"`
	Type        ProductServiceType `gorm:"type:text;not null"`
	Description string             `gorm:"not null;default:''"`

	DefaultPrice  decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`
	PurchasePrice decimal.Decimal `gorm:"type:numeric(18,4);not null;default:0"`

	// Capability flags — set from Type on create via ApplyTypeDefaults.
	CanBeSold     bool `gorm:"not null;default:true"`
	CanBePurchased bool `gorm:"not null;default:false"`
	IsStockItem   bool `gorm:"not null;default:false"`

	// Structure type: single | bundle | assembly. Default single.
	ItemStructureType ItemStructureType `gorm:"type:text;not null;default:'single'"`

	// Revenue account credited on invoice posting (required).
	RevenueAccountID uint    `gorm:"not null;index"`
	RevenueAccount   Account `gorm:"foreignKey:RevenueAccountID"`

	// COGS account debited on sale for inventory items (future; nullable).
	COGSAccountID *uint    `gorm:"index"`
	COGSAccount   *Account `gorm:"foreignKey:COGSAccountID"`

	// Inventory asset account for stock tracking (future; nullable).
	InventoryAccountID *uint    `gorm:"index"`
	InventoryAccount   *Account `gorm:"foreignKey:InventoryAccountID"`

	DefaultTaxCodeID *uint    `gorm:"index"`
	DefaultTaxCode   *TaxCode `gorm:"foreignKey:DefaultTaxCodeID"`

	IsActive bool `gorm:"not null;default:true"`

	// SystemCode is a stable identifier for system-reserved items (e.g. "TASK_LABOR",
	// "TASK_REIM"). NULL for all user-created items.
	// Uniqueness within a company is enforced by a partial DB index
	// (uq_product_services_company_system_code, added in migration 042).
	SystemCode *string `gorm:"type:text;index"`

	// IsSystem = true marks items that must not be deleted or have their Type changed.
	// The service layer checks this flag before allowing mutations.
	IsSystem bool `gorm:"not null;default:false"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
