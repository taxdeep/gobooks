// 遵循project_guide.md
package inventory

// receive.go — ReceiveStock (stock inflow) — first IN verb implemented in
// Phase D.0 slice 2.
//
// Flow:
//   1. Validate input
//   2. Check idempotency (replay-safe: return cached result on duplicate key)
//   3. Load + verify item tracks inventory
//   4. Compute base-currency figures (FX + landed cost apportionment)
//   5. Delegate to the active costing engine to update InventoryBalance
//   6. Persist an immutable InventoryMovement row with full context
//   7. Return base-currency values for the caller (GL) to post its entries

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ReceiveStock books a stock inflow against the ledger. See
// INVENTORY_MODULE_API.md §3.1 for the full contract.
func ReceiveStock(db *gorm.DB, in ReceiveStockInput) (*ReceiveStockResult, error) {
	if err := validateReceiveInput(in); err != nil {
		return nil, err
	}

	// Idempotency short-circuit. If a prior call with the same key already
	// landed, return the cached figures instead of re-writing.
	if in.IdempotencyKey != "" {
		if result, err := receiveByIdempotencyKey(db, in.CompanyID, in.IdempotencyKey); err != nil {
			return nil, err
		} else if result != nil {
			return result, nil
		}
	}

	// Item must exist, belong to this company, and be inventory-tracked.
	if err := verifyInventoryItem(db, in.CompanyID, in.ItemID); err != nil {
		return nil, err
	}

	// Phase F2: validate lot/serial tracking shape matches the item's
	// tracking_mode BEFORE any mutation. Guards against callers either
	// forgetting to supply tracking data or silently passing it for an
	// untracked item (where it would be dropped by the old code).
	trackingMode, err := loadTrackingMode(db, in.CompanyID, in.ItemID)
	if err != nil {
		return nil, err
	}
	if err := validateInboundTracking(trackingMode, in); err != nil {
		return nil, err
	}

	// Warehouse validation — soft check. When WarehouseID is zero we fall
	// back to the legacy LocationType path used by older code; Slice 2
	// callers (Bill / Opening) always pass a warehouse or zero with the
	// explicit intent to route legacy-style.
	warehouseID := ptrUintIfNonZero(in.WarehouseID)
	if warehouseID != nil {
		if err := verifyWarehouseBelongsToCompany(db, in.CompanyID, *warehouseID); err != nil {
			return nil, err
		}
	}

	// Base-currency conversion: default rate 1 when caller passes zero (legacy
	// base-currency-only paths). Landed cost is already in base currency and
	// is apportioned per-unit to blend into UnitCostBase so the costing
	// engine sees a single weighted-average input.
	exchangeRate := in.ExchangeRate
	if exchangeRate.IsZero() {
		exchangeRate = decimal.NewFromInt(1)
	}
	unitCostBase := in.UnitCost.Mul(exchangeRate)
	if in.LandedCostAllocation.IsPositive() && in.Quantity.IsPositive() {
		landedPerUnit := in.LandedCostAllocation.Div(in.Quantity)
		unitCostBase = unitCostBase.Add(landedPerUnit)
	}
	unitCostBase = unitCostBase.Round(4)
	totalCostBase := in.Quantity.Mul(unitCostBase).RoundBank(2)

	movementType := resolveReceiveMovementType(in.SourceType)

	// Weighted-average balance update. Pass the blended base-currency unit
	// cost so the running average is computed on final booked cost (including
	// landed cost apportionment), not the raw document figure.
	bal, err := readOrCreateBalance(db, in.CompanyID, in.ItemID, warehouseID, true)
	if err != nil {
		return nil, err
	}
	if err := applyInboundToBalance(db, bal, in.Quantity, unitCostBase); err != nil {
		return nil, err
	}

	// Persist the immutable movement row. Both document-currency and
	// base-currency figures are stored so historical statements can be
	// regenerated in either currency without recomputing.
	unitCostDoc := in.UnitCost
	totalCostDoc := in.Quantity.Mul(unitCostDoc).RoundBank(2)
	landed := in.LandedCostAllocation
	rate := exchangeRate

	mov := models.InventoryMovement{
		CompanyID:            in.CompanyID,
		ItemID:               in.ItemID,
		MovementType:         movementType,
		QuantityDelta:        in.Quantity, // always positive for a receipt
		UnitCost:             &unitCostDoc,
		TotalCost:            &totalCostDoc,
		CurrencyCode:         in.CurrencyCode,
		ExchangeRate:         &rate,
		UnitCostBase:         &unitCostBase,
		LandedCostAllocation: nilIfZero(landed),
		SourceType:           in.SourceType,
		SourceID:             ptrUintIfNonZero(in.SourceID),
		SourceLineID:         in.SourceLineID,
		ReferenceNote:        buildLegacyReferenceNote(in),
		Memo:                 in.Memo,
		IdempotencyKey:       in.IdempotencyKey,
		ActorUserID:          in.ActorUserID,
		WarehouseID:          warehouseID,
		MovementDate:         in.MovementDate,
	}
	if err := db.Create(&mov).Error; err != nil {
		return nil, fmt.Errorf("inventory.ReceiveStock: create movement: %w", err)
	}

	// Phase E2: every receipt produces a FIFO cost layer. Weighted-average
	// costing does not read from this table, so the work is cheap insurance
	// for companies that later switch to FIFO. See INVENTORY_MODULE_API.md
	// §3.1 / §7 "Phase E2".
	layer := models.InventoryCostLayer{
		CompanyID:         in.CompanyID,
		ItemID:            in.ItemID,
		WarehouseID:       warehouseID,
		SourceMovementID:  mov.ID,
		OriginalQuantity:  in.Quantity,
		RemainingQuantity: in.Quantity,
		UnitCostBase:      unitCostBase,
		ReceivedDate:      in.MovementDate,
	}
	if err := db.Create(&layer).Error; err != nil {
		return nil, fmt.Errorf("inventory.ReceiveStock: create cost layer: %w", err)
	}

	// Phase F2: persist tracking truth for lot / serial items. Costing
	// has already been computed (see above) and is NOT affected by the
	// tracking structures — lot/serial capture is orthogonal.
	switch trackingMode {
	case models.TrackingLot:
		if err := persistLotInbound(db, in); err != nil {
			return nil, fmt.Errorf("inventory.ReceiveStock: lot persist: %w", err)
		}
	case models.TrackingSerial:
		if err := persistSerialInbound(db, in); err != nil {
			return nil, fmt.Errorf("inventory.ReceiveStock: serial persist: %w", err)
		}
	}

	return &ReceiveStockResult{
		MovementID:         mov.ID,
		UnitCostBase:       unitCostBase,
		InventoryValueBase: totalCostBase,
	}, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

func validateReceiveInput(in ReceiveStockInput) error {
	if in.CompanyID == 0 {
		return fmt.Errorf("inventory.ReceiveStock: CompanyID required")
	}
	if in.ItemID == 0 {
		return fmt.Errorf("inventory.ReceiveStock: ItemID required")
	}
	if !in.Quantity.IsPositive() {
		return ErrNegativeQuantity
	}
	if in.SourceType == "" {
		return ErrInvalidSource
	}
	if in.MovementDate.IsZero() {
		return fmt.Errorf("inventory.ReceiveStock: MovementDate required")
	}
	if in.UnitCost.IsNegative() {
		return fmt.Errorf("inventory.ReceiveStock: UnitCost cannot be negative")
	}
	if in.LandedCostAllocation.IsNegative() {
		return fmt.Errorf("inventory.ReceiveStock: LandedCostAllocation cannot be negative")
	}
	// Foreign-currency movement without explicit rate is ambiguous; caller
	// must pass 1 for base currency.
	if in.CurrencyCode != "" && in.ExchangeRate.IsZero() {
		return ErrCurrencyRateRequired
	}
	return nil
}

// receiveByIdempotencyKey returns the cached result for a prior receive that
// used the same key, or (nil, nil) if no such movement exists.
func receiveByIdempotencyKey(db *gorm.DB, companyID uint, key string) (*ReceiveStockResult, error) {
	var mov models.InventoryMovement
	err := db.Where("company_id = ? AND idempotency_key = ?", companyID, key).First(&mov).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inventory.ReceiveStock: idempotency lookup: %w", err)
	}
	// Only treat receipt movements as valid replays; a key collision with a
	// different op-type is an error the caller must resolve.
	if !mov.QuantityDelta.IsPositive() {
		return nil, ErrDuplicateIdempotency
	}
	unitCostBase := decimal.Zero
	if mov.UnitCostBase != nil {
		unitCostBase = *mov.UnitCostBase
	}
	return &ReceiveStockResult{
		MovementID:         mov.ID,
		UnitCostBase:       unitCostBase,
		InventoryValueBase: mov.QuantityDelta.Mul(unitCostBase).RoundBank(2),
	}, nil
}

func verifyWarehouseBelongsToCompany(db *gorm.DB, companyID, warehouseID uint) error {
	var count int64
	err := db.Model(&models.Warehouse{}).
		Where("id = ? AND company_id = ?", warehouseID, companyID).
		Count(&count).Error
	if err != nil {
		return fmt.Errorf("inventory.ReceiveStock: warehouse lookup: %w", err)
	}
	if count == 0 {
		return ErrInvalidWarehouse
	}
	return nil
}

// resolveReceiveMovementType maps the API-level SourceType onto the legacy
// InventoryMovementType enum so existing read paths keep working. When new
// source types are added (e.g. "transfer_in" in slice 6) this switch is the
// single place to extend.
func resolveReceiveMovementType(sourceType string) models.InventoryMovementType {
	switch sourceType {
	case "bill":
		return models.MovementTypePurchase
	case "opening":
		return models.MovementTypeOpening
	case "build_produce":
		return models.MovementTypeAssemblyBuild
	case "return_from_customer":
		return models.MovementTypeRefund
	case "amazon_order":
		return models.MovementTypeAmazonOrder
	case "manufacturing_receipt":
		return models.MovementTypeMfgReceipt
	case "adjustment":
		return models.MovementTypeAdjustment
	// transfer_in and other future sources default to Adjustment until we
	// introduce first-class movement types for them (Slice 6).
	default:
		return models.MovementTypeAdjustment
	}
}

// buildLegacyReferenceNote fills the pre-API-contract ReferenceNote column so
// old reports that read it still render sensibly. New code should prefer Memo.
func buildLegacyReferenceNote(in ReceiveStockInput) string {
	if in.Memo != "" {
		return in.Memo
	}
	switch in.SourceType {
	case "opening":
		return "Opening balance"
	case "bill":
		return "Purchase"
	case "build_produce":
		return "Assembly build — finished good"
	case "return_from_customer":
		return "Return from customer"
	default:
		return ""
	}
}

func verifyInventoryItem(db *gorm.DB, companyID, itemID uint) error {
	var item models.ProductService
	err := db.Where("id = ? AND company_id = ?", itemID, companyID).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrInvalidItem
	}
	if err != nil {
		return fmt.Errorf("inventory: item lookup: %w", err)
	}
	if item.Type != models.ProductServiceTypeInventory {
		return ErrItemNotTracked
	}
	return nil
}

func ptrUintIfNonZero(v uint) *uint {
	if v == 0 {
		return nil
	}
	return &v
}

func nilIfZero(d decimal.Decimal) *decimal.Decimal {
	if d.IsZero() {
		return nil
	}
	return &d
}
