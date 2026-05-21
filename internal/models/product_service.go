// 遵循project_guide.md
package models

import (
	"fmt"
	"strings"
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
	// ProductServiceTypeOtherCharge is a line-item charge (e.g. discount, surcharge) whose
	// account code is an Expense or Cost-of-Sales account rather than a Revenue account.
	// A negative unit price on an Other Charge line produces a DR Expense / CR AR reduction.
	ProductServiceTypeOtherCharge ProductServiceType = "other_charge"
)

// AllProductServiceTypes returns the currently supported types in display order.
func AllProductServiceTypes() []ProductServiceType {
	return []ProductServiceType{
		ProductServiceTypeService,
		ProductServiceTypeNonInventory,
		ProductServiceTypeInventory,
		ProductServiceTypeOtherCharge,
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
	case ProductServiceTypeOtherCharge:
		return "Other Charge"
	default:
		return string(t)
	}
}

// ParseProductServiceType parses a raw string into a ProductServiceType, returning an error
// if the value is not recognised.
func ParseProductServiceType(s string) (ProductServiceType, error) {
	switch ProductServiceType(s) {
	case ProductServiceTypeService, ProductServiceTypeNonInventory, ProductServiceTypeInventory,
		ProductServiceTypeOtherCharge:
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

// ── Tracking mode (Phase F1) ─────────────────────────────────────────────────

// TrackingMode values. Lot/serial/expiry capture is governed by this field
// on ProductService. Costing remains orthogonal (moving-avg / FIFO).
const (
	TrackingNone   = "none"
	TrackingLot    = "lot"
	TrackingSerial = "serial"
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
	case ProductServiceTypeOtherCharge:
		ps.CanBeSold = true
		ps.CanBePurchased = false
		ps.IsStockItem = false
	}
	if ps.ItemStructureType == "" {
		ps.ItemStructureType = ItemStructureSingle
	}
	// Phase F1 hard rule: non-stock items can never carry lot/serial
	// tracking. Force to TrackingNone on defaulting; ValidateTrackingMode
	// re-enforces the same rule on update paths.
	if ps.TrackingMode == "" || !ps.IsStockItem {
		ps.TrackingMode = TrackingNone
	}
	// UOM defaults (Phase U1) — every new item gets EA / EA / EA / 1 / 1
	// so the existing single-unit flow keeps working without operator
	// action.  Operators opt in to multi-UOM by editing the item.
	ps.ApplyUOMDefaults()
}

// ValidateTrackingMode ensures the mode is legal both in-value and
// in-context. Non-stock items and service items are permitted ONLY
// "none". Stock items may be any of the three. Returns a human-readable
// error suitable for surfacing in handlers.
func (ps *ProductService) ValidateTrackingMode() error {
	switch ps.TrackingMode {
	case TrackingNone:
		return nil
	case TrackingLot, TrackingSerial:
		if !ps.IsStockItem {
			return fmt.Errorf("tracking_mode %q is only valid for stock items; %q is not a stock item",
				ps.TrackingMode, ps.Name)
		}
		return nil
	default:
		return fmt.Errorf("tracking_mode must be one of none|lot|serial (got %q)", ps.TrackingMode)
	}
}

// ── UOM (Phase U1) ───────────────────────────────────────────────────────────

// NormalizeUOM uppercases + trims a UOM string, falling back to "EA"
// when empty.  Called by save handlers + the model's
// ApplyUOMDefaults so different casings of "ea" / "Ea" / "EA" don't
// fragment the picker / catalogs.
func NormalizeUOM(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "EA"
	}
	return strings.ToUpper(s)
}

// ApplyUOMDefaults fills any missing UOM fields with safe defaults
// (StockUOM = SellUOM = PurchaseUOM = "EA"; factors = 1).  Idempotent —
// re-running on a fully-populated row is a no-op.  Used by ApplyTypeDefaults
// + by the save handler before persisting.
func (ps *ProductService) ApplyUOMDefaults() {
	ps.StockUOM = NormalizeUOM(ps.StockUOM)
	ps.SellUOM = NormalizeUOM(ps.SellUOM)
	ps.PurchaseUOM = NormalizeUOM(ps.PurchaseUOM)
	if !ps.SellUOMFactor.IsPositive() {
		ps.SellUOMFactor = decimal.NewFromInt(1)
	}
	if !ps.PurchaseUOMFactor.IsPositive() {
		ps.PurchaseUOMFactor = decimal.NewFromInt(1)
	}
}

// ValidateUOMs checks the UOM tuple is internally consistent:
//   - All three UOM strings are non-empty (caller should normalise first
//     to fold case; this validator just rejects truly empty strings).
//   - Factors are strictly positive.
//   - When SellUOM == StockUOM, SellUOMFactor must be 1 (and same for
//     PurchaseUOM). Mismatched factor on the same unit is nonsense.
//   - Non-stock items must use defaults (StockUOM="EA", factors=1) —
//     UOM only makes sense for stock-tracked items.  Time-bill /
//     non-inventory don't have a stock unit to convert from.
//
// Returns the first failure, suitable for surfacing inline next to the
// offending field.
func (ps *ProductService) ValidateUOMs() error {
	if ps.StockUOM == "" || ps.SellUOM == "" || ps.PurchaseUOM == "" {
		return fmt.Errorf("UOM strings cannot be empty")
	}
	if !ps.SellUOMFactor.IsPositive() {
		return fmt.Errorf("sell UOM factor must be > 0 (got %s)", ps.SellUOMFactor.String())
	}
	if !ps.PurchaseUOMFactor.IsPositive() {
		return fmt.Errorf("purchase UOM factor must be > 0 (got %s)", ps.PurchaseUOMFactor.String())
	}
	one := decimal.NewFromInt(1)
	if ps.SellUOM == ps.StockUOM && !ps.SellUOMFactor.Equal(one) {
		return fmt.Errorf("sell UOM equals stock UOM (%s) so factor must be 1 (got %s)",
			ps.StockUOM, ps.SellUOMFactor.String())
	}
	if ps.PurchaseUOM == ps.StockUOM && !ps.PurchaseUOMFactor.Equal(one) {
		return fmt.Errorf("purchase UOM equals stock UOM (%s) so factor must be 1 (got %s)",
			ps.StockUOM, ps.PurchaseUOMFactor.String())
	}
	if !ps.IsStockItem {
		// Non-stock items don't have a real stock unit, but their sales and
		// purchase lines still need a display unit such as HOUR, DAY, or UNIT.
		// Keep all non-stock factors pinned to 1 so the unit is descriptive and
		// never drives inventory conversion math.
		if ps.StockUOM != "EA" || !ps.SellUOMFactor.Equal(one) || !ps.PurchaseUOMFactor.Equal(one) {
			return fmt.Errorf("non-stock items must keep StockUOM=EA and UOM factors=1 — got %q",
				ps.Name)
		}
	}
	return nil
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
	CanBeSold      bool `gorm:"not null;default:true"`
	CanBePurchased bool `gorm:"not null;default:false"`
	IsStockItem    bool `gorm:"not null;default:false"`

	// Structure type: single | bundle | assembly. Default single.
	ItemStructureType ItemStructureType `gorm:"type:text;not null;default:'single'"`

	// TrackingMode governs lot/serial/expiry capture for stock items.
	// Phase F1. Legal values: "none" | "lot" | "serial" (see
	// TrackingNone/TrackingLot/TrackingSerial constants). Default "none".
	//
	// Hard rule (enforced by ValidateTrackingModeForItem): non-stock
	// items MUST stay on "none". Only is_stock_item=TRUE items may be
	// set to lot or serial.
	//
	// Changing this field while the item has on-hand > 0 is rejected by
	// ChangeTrackingMode (see internal/services/product_service_tracking.go).
	// Phase F1 does not ship a conversion tool.
	TrackingMode string `gorm:"type:text;not null;default:'none'"`

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

	// ── Unit of Measure (Phase U1 — 2026-04-25) ──────────────────────────
	//
	// StockUOM is the unit the inventory module counts in (on-hand,
	// FIFO layers, transfers, costs all denominated here). Immutable
	// while inventory on-hand > 0 — enforced by
	// services.ChangeStockUOM (parallel to the TrackingMode rule).
	// Default "EA" so every existing item keeps working without
	// operator action.
	StockUOM string `gorm:"type:varchar(16);not null;default:'EA'"`

	// SellUOM is the AR-side default unit on Invoice / Quote / SO / CN
	// lines. SellUOMFactor is the conversion: how many StockUOMs equal
	// one SellUOM. When SellUOM == StockUOM the factor must be 1.
	SellUOM       string          `gorm:"type:varchar(16);not null;default:'EA'"`
	SellUOMFactor decimal.Decimal `gorm:"type:numeric(18,6);not null;default:1"`

	// PurchaseUOM is the AP-side default unit on Bill / PO / VCN
	// lines. Same semantics as SellUOM but for the buy side.
	PurchaseUOM       string          `gorm:"type:varchar(16);not null;default:'EA'"`
	PurchaseUOMFactor decimal.Decimal `gorm:"type:numeric(18,6);not null;default:1"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
