// 遵循project_guide.md
package services

// moving_average_costing.go — Moving (weighted) average cost implementation.
//
// Inbound: new_avg = (old_qty × old_avg + inbound_qty × inbound_cost) / (old_qty + inbound_qty)
// Outbound: uses current average_cost as unit_cost; average_cost unchanged.
// Opening: treated as inbound from zero balance.
// Negative stock: rejected (ErrInsufficientStock).
//
// Future FIFO would maintain per-receipt cost layers instead of a single average;
// the CostingEngine interface is designed so FIFO can implement the same methods
// with layer-based logic, without changing callers.

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/gorm"
)

// MovingAverageCostingEngine implements CostingEngine using the weighted
// average cost method. It reads and writes inventory_balances rows directly.
type MovingAverageCostingEngine struct{}

var _ CostingEngine = (*MovingAverageCostingEngine)(nil)

// PreviewOutbound returns the cost that would apply for a sale/outbound without
// modifying state. Used by invoice posting pre-flight validation.
func (e *MovingAverageCostingEngine) PreviewOutbound(db *gorm.DB, req OutboundRequest) (*OutboundResult, error) {
	if req.LocationType == "" && req.WarehouseID == nil {
		req.LocationType = models.LocationTypeInternal
	}

	bal, err := readBalance(db, req.CompanyID, req.ItemID, req.LocationType, req.LocationRef, req.WarehouseID, false)
	if err != nil {
		return nil, err
	}

	if bal.QuantityOnHand.LessThan(req.Quantity) {
		return nil, fmt.Errorf("%w: need %s, have %s",
			ErrInsufficientStock, req.Quantity.String(), bal.QuantityOnHand.String())
	}

	totalCost := req.Quantity.Mul(bal.AverageCost).RoundBank(2)
	newQty := bal.QuantityOnHand.Sub(req.Quantity)

	return &OutboundResult{
		UnitCostUsed:      bal.AverageCost,
		TotalCost:         totalCost,
		NewQuantityOnHand: newQty,
		NewAverageCost:    bal.AverageCost, // unchanged on outbound
	}, nil
}

// ApplyInbound processes a receipt (purchase, opening, positive adjustment).
// Updates the balance with weighted average cost. Must run inside a transaction.
func (e *MovingAverageCostingEngine) ApplyInbound(tx *gorm.DB, req InboundRequest) (*InboundResult, error) {
	if req.LocationType == "" && req.WarehouseID == nil {
		req.LocationType = models.LocationTypeInternal
	}

	bal, err := readBalance(tx, req.CompanyID, req.ItemID, req.LocationType, req.LocationRef, req.WarehouseID, true)
	if err != nil {
		return nil, err
	}

	// Weighted average: (old_qty × old_avg + new_qty × new_cost) / total_qty
	oldValue := bal.QuantityOnHand.Mul(bal.AverageCost)
	newValue := req.Quantity.Mul(req.UnitCost)
	newQty := bal.QuantityOnHand.Add(req.Quantity)

	var newAvg decimal.Decimal
	if newQty.IsPositive() {
		newAvg = oldValue.Add(newValue).Div(newQty).RoundBank(4)
	} else {
		newAvg = req.UnitCost // edge case: qty was negative before (shouldn't happen)
	}

	totalCost := req.Quantity.Mul(req.UnitCost).RoundBank(2)

	// Persist balance.
	bal.QuantityOnHand = newQty
	bal.AverageCost = newAvg
	bal.UpdatedAt = time.Now()
	if err := tx.Save(bal).Error; err != nil {
		return nil, fmt.Errorf("save balance after inbound: %w", err)
	}

	return &InboundResult{
		UnitCostUsed:      req.UnitCost,
		TotalCost:         totalCost,
		NewQuantityOnHand: newQty,
		NewAverageCost:    newAvg,
	}, nil
}

// ApplyOutbound processes a sale or negative adjustment.
// Uses the current average cost as the unit cost for COGS.
// Rejects if insufficient stock. Must run inside a transaction.
func (e *MovingAverageCostingEngine) ApplyOutbound(tx *gorm.DB, req OutboundRequest) (*OutboundResult, error) {
	if req.LocationType == "" && req.WarehouseID == nil {
		req.LocationType = models.LocationTypeInternal
	}

	bal, err := readBalance(tx, req.CompanyID, req.ItemID, req.LocationType, req.LocationRef, req.WarehouseID, true)
	if err != nil {
		return nil, err
	}

	if bal.QuantityOnHand.LessThan(req.Quantity) {
		return nil, fmt.Errorf("%w: need %s, have %s",
			ErrInsufficientStock, req.Quantity.String(), bal.QuantityOnHand.String())
	}

	unitCost := bal.AverageCost
	totalCost := req.Quantity.Mul(unitCost).RoundBank(2)
	newQty := bal.QuantityOnHand.Sub(req.Quantity)

	// Average cost unchanged on outbound (moving average rule).
	bal.QuantityOnHand = newQty
	bal.UpdatedAt = time.Now()
	if err := tx.Save(bal).Error; err != nil {
		return nil, fmt.Errorf("save balance after outbound: %w", err)
	}

	return &OutboundResult{
		UnitCostUsed:      unitCost,
		TotalCost:         totalCost,
		NewQuantityOnHand: newQty,
		NewAverageCost:    unitCost,
	}, nil
}

// ── Internal helper ──────────────────────────────────────────────────────────

// readBalance loads or creates the inventory balance row for the given item+location.
// If forUpdate is true, applies SELECT FOR UPDATE (row lock).
//
// Routing logic:
//   - warehouseID != nil  → query by (company_id, item_id, warehouse_id)
//   - warehouseID == nil  → legacy path: query by (company_id, item_id, location_type, location_ref)
func readBalance(db *gorm.DB, companyID, itemID uint, locType models.LocationType, locRef string, warehouseID *uint, forUpdate bool) (*models.InventoryBalance, error) {
	var bal models.InventoryBalance
	var q *gorm.DB
	if warehouseID != nil {
		q = db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?",
			companyID, itemID, *warehouseID)
	} else {
		q = db.Where("company_id = ? AND item_id = ? AND location_type = ? AND location_ref = ?",
			companyID, itemID, locType, locRef)
	}
	if forUpdate {
		q = applyLockForUpdate(q)
	}
	err := q.First(&bal).Error
	if err == gorm.ErrRecordNotFound {
		bal = models.InventoryBalance{
			CompanyID:      companyID,
			ItemID:         itemID,
			LocationType:   locType,
			LocationRef:    locRef,
			WarehouseID:    warehouseID,
			QuantityOnHand: decimal.Zero,
			AverageCost:    decimal.Zero,
		}
		if err := db.Create(&bal).Error; err != nil {
			return nil, fmt.Errorf("create inventory balance: %w", err)
		}
		return &bal, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load inventory balance: %w", err)
	}
	return &bal, nil
}
