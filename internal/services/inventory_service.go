// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/services/inventory"
)

// translateInventoryErr maps errors returned by the new inventory package
// back to this package's legacy sentinel errors so existing callers keep
// matching them via errors.Is. Dropped entirely in slice 8 once all callers
// consume the inventory package directly.
func translateInventoryErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, inventory.ErrItemNotTracked):
		return ErrNotInventoryItem
	case errors.Is(err, inventory.ErrInsufficientStock):
		return ErrInsufficientStock
	default:
		return err
	}
}

// ── Errors ───────────────────────────────────────────────────────────────────

var (
	ErrNotInventoryItem  = errors.New("only inventory-type items support stock operations")
	ErrOpeningExists     = errors.New("opening balance already exists for this item and location")
	ErrInsufficientStock = errors.New("adjustment would result in negative inventory — not allowed")
)

// ── Opening balance ──────────────────────────────────────────────────────────

// OpeningBalanceInput holds the parameters for recording an inventory opening.
type OpeningBalanceInput struct {
	CompanyID    uint
	ItemID       uint
	Quantity     decimal.Decimal
	UnitCost     decimal.Decimal
	AsOfDate     time.Time
	LocationType models.LocationType
	LocationRef  string
	// WarehouseID routes the opening to a specific warehouse (nil = legacy path).
	WarehouseID *uint
}

// CreateOpeningBalance records the initial stock level for an inventory item.
// Each (item × warehouse_id) or (item × location_type × location_ref) combination
// may have at most one opening movement.
//
// As of Phase D.0 slice 2 this function is a thin facade over
// inventory.ReceiveStock — the body below preserves the legacy pre-check
// (ErrOpeningExists), then delegates. Callers should eventually migrate to
// calling inventory.ReceiveStock directly with SourceType="opening" and an
// idempotency key of the form "opening:item:<id>:warehouse:<id>".
func CreateOpeningBalance(db *gorm.DB, in OpeningBalanceInput) (*models.InventoryMovement, error) {
	if in.WarehouseID == nil && in.LocationType == "" {
		in.LocationType = models.LocationTypeInternal
	}
	if in.Quantity.IsNegative() {
		return nil, fmt.Errorf("opening quantity cannot be negative")
	}
	if in.Quantity.IsPositive() && in.UnitCost.IsNegative() {
		return nil, fmt.Errorf("unit cost cannot be negative")
	}

	// Legacy invariant: one opening row per (item × warehouse). Retained here
	// because the new API expresses this via idempotency keys, which would
	// turn a "second opening attempt" into a silent replay — different UX.
	var existing int64
	q := db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND item_id = ? AND movement_type = ? AND source_type = ?",
			in.CompanyID, in.ItemID, models.MovementTypeOpening, "opening")
	if in.WarehouseID != nil {
		q = q.Where("warehouse_id = ?", *in.WarehouseID)
	} else {
		q = q.Where("warehouse_id IS NULL")
	}
	q.Count(&existing)
	if existing > 0 {
		return nil, ErrOpeningExists
	}

	warehouseID := uint(0)
	if in.WarehouseID != nil {
		warehouseID = *in.WarehouseID
	}

	var mov models.InventoryMovement
	err := db.Transaction(func(tx *gorm.DB) error {
		result, err := inventory.ReceiveStock(tx, inventory.ReceiveStockInput{
			CompanyID:    in.CompanyID,
			ItemID:       in.ItemID,
			WarehouseID:  warehouseID,
			Quantity:     in.Quantity,
			MovementDate: in.AsOfDate,
			UnitCost:     in.UnitCost,
			ExchangeRate: decimal.NewFromInt(1),
			SourceType:   "opening",
			IdempotencyKey: fmt.Sprintf("opening:item:%d:warehouse:%d:v1",
				in.ItemID, warehouseID),
			Memo: "Opening balance",
		})
		if err != nil {
			return translateInventoryErr(err)
		}
		return tx.First(&mov, result.MovementID).Error
	})
	if err != nil {
		return nil, err
	}
	return &mov, nil
}

// ── Inventory adjustment ─────────────────────────────────────────────────────

// AdjustmentInput holds the parameters for an inventory adjustment.
type AdjustmentInput struct {
	CompanyID     uint
	ItemID        uint
	QuantityDelta decimal.Decimal // positive = add, negative = remove
	UnitCost      *decimal.Decimal
	MovementDate  time.Time
	Note          string
	LocationType  models.LocationType
	LocationRef   string
	// WarehouseID routes the adjustment to a specific warehouse (nil = legacy path).
	WarehouseID *uint
}

// CreateAdjustment records a manual inventory adjustment.
//
// Phase D.0 slice 4: now a thin facade over inventory.AdjustStock.
// Behaviour preserved — positive delta = inbound, negative = outbound,
// zero delta writes an audit-only marker. Callers should eventually
// migrate to calling inventory.AdjustStock directly with an explicit
// AdjustmentReason and IdempotencyKey.
func CreateAdjustment(db *gorm.DB, in AdjustmentInput) (*models.InventoryMovement, error) {
	if in.WarehouseID == nil && in.LocationType == "" {
		in.LocationType = models.LocationTypeInternal
	}

	if _, err := loadInventoryItem(db, in.CompanyID, in.ItemID); err != nil {
		return nil, err
	}

	if in.UnitCost != nil && in.UnitCost.IsNegative() {
		return nil, fmt.Errorf("unit cost cannot be negative")
	}

	warehouseValue := uint(0)
	if in.WarehouseID != nil {
		warehouseValue = *in.WarehouseID
	}

	var mov models.InventoryMovement
	err := db.Transaction(func(tx *gorm.DB) error {
		result, err := inventory.AdjustStock(tx, inventory.AdjustStockInput{
			CompanyID:     in.CompanyID,
			ItemID:        in.ItemID,
			WarehouseID:   warehouseValue,
			QuantityDelta: in.QuantityDelta,
			MovementDate:  in.MovementDate,
			UnitCost:      in.UnitCost,
			SourceType:    "adjustment",
			Memo:          in.Note,
		})
		if err != nil {
			return translateInventoryErr(err)
		}
		return tx.First(&mov, result.MovementID).Error
	})
	if err != nil {
		return nil, err
	}

	return &mov, nil
}

// ── Balance query ────────────────────────────────────────────────────────────

// GetBalance returns the current inventory balance for an item at the default
// internal location. Returns zero-value balance if no record exists.
func GetBalance(db *gorm.DB, companyID, itemID uint) (*models.InventoryBalance, error) {
	var bal models.InventoryBalance
	err := db.Where("company_id = ? AND item_id = ? AND location_type = ? AND location_ref = ?",
		companyID, itemID, models.LocationTypeInternal, "").
		First(&bal).Error
	if err == gorm.ErrRecordNotFound {
		return &models.InventoryBalance{
			CompanyID:      companyID,
			ItemID:         itemID,
			LocationType:   models.LocationTypeInternal,
			QuantityOnHand: decimal.Zero,
			AverageCost:    decimal.Zero,
		}, nil
	}
	if err != nil {
		return nil, err
	}
	return &bal, nil
}

// HasOpening returns true if an opening movement exists for the item.
func HasOpening(db *gorm.DB, companyID, itemID uint) bool {
	var count int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND item_id = ? AND movement_type = ?",
			companyID, itemID, models.MovementTypeOpening).
		Count(&count)
	return count > 0
}

// RecentMovements returns the most recent N movements for an item.
func RecentMovements(db *gorm.DB, companyID, itemID uint, limit int) ([]models.InventoryMovement, error) {
	var movs []models.InventoryMovement
	err := db.Where("company_id = ? AND item_id = ?", companyID, itemID).
		Order("movement_date DESC, id DESC").
		Limit(limit).
		Find(&movs).Error
	return movs, err
}

// ── Internal helpers ─────────────────────────────────────────────────────────

func loadInventoryItem(db *gorm.DB, companyID, itemID uint) (*models.ProductService, error) {
	var item models.ProductService
	if err := db.Where("id = ? AND company_id = ?", itemID, companyID).First(&item).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("item %d not found in company %d", itemID, companyID)
		}
		return nil, fmt.Errorf("item lookup failed: %w", err)
	}
	if item.Type != models.ProductServiceTypeInventory {
		return nil, ErrNotInventoryItem
	}
	return &item, nil
}
