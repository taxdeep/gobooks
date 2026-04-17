// 遵循project_guide.md
package inventory

// issue.go — IssueStock (stock outflow) — second IN verb implemented in
// Phase D.0 slice 3.
//
// The keystone of the bounded-context design: callers never pass a cost.
// They specify quantity and inventory returns the booked cost. That cost
// becomes the caller's Dr COGS / Cr Inventory figure at the GL layer.
//
// Flow mirrors ReceiveStock:
//   1. Validate input
//   2. Idempotency short-circuit (return cached result on replay)
//   3. Verify item tracks inventory + warehouse belongs to company
//   4. Read balance (row-locked)
//   5. Reject if insufficient stock (unless AllowNegative)
//   6. Apply weighted-average outbound (avg unchanged, qty decreased)
//   7. Persist movement row with negative QuantityDelta
//   8. Return MovementID + booked cost

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// IssueStock books a stock outflow and returns the cost the caller needs
// for its COGS posting. See INVENTORY_MODULE_API.md §3.2.
func IssueStock(db *gorm.DB, in IssueStockInput) (*IssueStockResult, error) {
	if err := validateIssueInput(in); err != nil {
		return nil, err
	}

	if in.IdempotencyKey != "" {
		if result, err := issueByIdempotencyKey(db, in.CompanyID, in.IdempotencyKey); err != nil {
			return nil, err
		} else if result != nil {
			return result, nil
		}
	}

	if err := verifyInventoryItem(db, in.CompanyID, in.ItemID); err != nil {
		return nil, err
	}

	warehouseID := ptrUintIfNonZero(in.WarehouseID)
	if warehouseID != nil {
		if err := verifyWarehouseBelongsToCompany(db, in.CompanyID, *warehouseID); err != nil {
			return nil, err
		}
	}

	// Read balance (row-locked). Insufficient-stock check runs before the
	// balance mutation so a rejected issue leaves the row untouched.
	bal, err := readOrCreateBalance(db, in.CompanyID, in.ItemID, warehouseID, true)
	if err != nil {
		return nil, err
	}
	if !in.AllowNegative && bal.QuantityOnHand.LessThan(in.Quantity) {
		return nil, fmt.Errorf("%w: need %s, have %s",
			ErrInsufficientStock, in.Quantity.String(), bal.QuantityOnHand.String())
	}

	// Weighted-average outflow: unit cost = current avg; avg unchanged.
	// FIFO / Specific methods land in Phase E with per-layer consumption.
	if in.CostingMethod != CostingMethodDefault &&
		in.CostingMethod != CostingMethodWeightedAvg {
		return nil, fmt.Errorf("inventory.IssueStock: costing method %q not yet implemented", in.CostingMethod)
	}
	unitCostBase, err := applyOutboundToBalance(db, bal, in.Quantity)
	if err != nil {
		return nil, err
	}
	costOfIssueBase := in.Quantity.Mul(unitCostBase).RoundBank(2)

	movementType := resolveIssueMovementType(in.SourceType)

	mov := models.InventoryMovement{
		CompanyID:      in.CompanyID,
		ItemID:         in.ItemID,
		MovementType:   movementType,
		QuantityDelta:  in.Quantity.Neg(), // negative for outflow
		UnitCost:       &unitCostBase,     // in this phase doc == base
		TotalCost:      &costOfIssueBase,
		UnitCostBase:   &unitCostBase,
		SourceType:     in.SourceType,
		SourceID:       ptrUintIfNonZero(in.SourceID),
		SourceLineID:   in.SourceLineID,
		ReferenceNote:  buildIssueReferenceNote(in),
		Memo:           in.Memo,
		IdempotencyKey: in.IdempotencyKey,
		ActorUserID:    in.ActorUserID,
		WarehouseID:    warehouseID,
		MovementDate:   in.MovementDate,
	}
	if err := db.Create(&mov).Error; err != nil {
		return nil, fmt.Errorf("inventory.IssueStock: create movement: %w", err)
	}

	return &IssueStockResult{
		MovementID:      mov.ID,
		UnitCostBase:    unitCostBase,
		CostOfIssueBase: costOfIssueBase,
		// CostLayers stays nil under weighted-average; populated only under
		// FIFO / Specific (Phase E).
	}, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

func validateIssueInput(in IssueStockInput) error {
	if in.CompanyID == 0 {
		return fmt.Errorf("inventory.IssueStock: CompanyID required")
	}
	if in.ItemID == 0 {
		return fmt.Errorf("inventory.IssueStock: ItemID required")
	}
	if !in.Quantity.IsPositive() {
		return ErrNegativeQuantity
	}
	if in.SourceType == "" {
		return ErrInvalidSource
	}
	if in.MovementDate.IsZero() {
		return fmt.Errorf("inventory.IssueStock: MovementDate required")
	}
	if in.CostingMethod == CostingMethodSpecific && in.SpecificLotID == nil {
		return fmt.Errorf("inventory.IssueStock: SpecificLotID required for CostingMethod=specific")
	}
	return nil
}

// issueByIdempotencyKey returns the cached result for a prior matching issue.
// Returns (nil, nil) if no prior movement exists.
func issueByIdempotencyKey(db *gorm.DB, companyID uint, key string) (*IssueStockResult, error) {
	var mov models.InventoryMovement
	err := db.Where("company_id = ? AND idempotency_key = ?", companyID, key).First(&mov).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inventory.IssueStock: idempotency lookup: %w", err)
	}
	// A cached key that maps to a receipt (positive delta) signals a
	// programming error — different event types must not share keys.
	if mov.QuantityDelta.IsPositive() {
		return nil, ErrDuplicateIdempotency
	}
	unitCostBase := decimal.Zero
	if mov.UnitCostBase != nil {
		unitCostBase = *mov.UnitCostBase
	}
	absQty := mov.QuantityDelta.Neg()
	return &IssueStockResult{
		MovementID:      mov.ID,
		UnitCostBase:    unitCostBase,
		CostOfIssueBase: absQty.Mul(unitCostBase).RoundBank(2),
	}, nil
}

// resolveIssueMovementType maps the API-level SourceType onto the legacy
// InventoryMovementType enum so existing reporting keeps working. Unknown
// source types default to Adjustment — the cautious choice for outflows.
func resolveIssueMovementType(sourceType string) models.InventoryMovementType {
	switch sourceType {
	case "invoice", "shipment":
		return models.MovementTypeSale
	case "build_consume":
		return models.MovementTypeAssemblyUnbuild
	case "amazon_order":
		return models.MovementTypeAmazonOrder
	case "manufacturing_issue":
		return models.MovementTypeMfgIssue
	case "scrap", "adjustment":
		return models.MovementTypeAdjustment
	default:
		// transfer_out and similar defer to Adjustment until slice 6 adds
		// first-class movement types for them.
		return models.MovementTypeAdjustment
	}
}

func buildIssueReferenceNote(in IssueStockInput) string {
	if in.Memo != "" {
		return in.Memo
	}
	switch in.SourceType {
	case "invoice", "shipment":
		return "Sale"
	case "build_consume":
		return "Assembly build — component consumption"
	case "scrap":
		return "Scrap / write-off"
	default:
		return ""
	}
}
