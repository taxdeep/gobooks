// 遵循project_guide.md
package inventory

// balance.go — internal helpers for reading and updating the
// InventoryBalance cache inside IN verb implementations.
//
// Kept local to this package to avoid importing internal/services (which
// imports us back via the old costing engine callers — import cycle).
// The legacy services.MovingAverageCostingEngine still serves its existing
// callers; as they migrate to this package (Slices 3-6), the engine will
// be inlined/retired. The cost-flow math here is weighted-average only,
// matching the only algorithm the live system currently supports.

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"gobooks/internal/models"
)

// readOrCreateBalance locates the InventoryBalance row for the given
// (company, item, warehouse) triple, creating a zero-valued row if none
// exists. When forUpdate is true the row is locked with SELECT FOR UPDATE
// on engines that support it (skipped on sqlite for tests).
func readOrCreateBalance(db *gorm.DB, companyID, itemID uint, warehouseID *uint, forUpdate bool) (*models.InventoryBalance, error) {
	var bal models.InventoryBalance
	var q *gorm.DB
	if warehouseID != nil {
		q = db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?",
			companyID, itemID, *warehouseID)
	} else {
		// Legacy path: LocationType defaults to internal for receipts that
		// haven't been routed to a specific warehouse yet.
		q = db.Where("company_id = ? AND item_id = ? AND location_type = ? AND location_ref = ? AND warehouse_id IS NULL",
			companyID, itemID, models.LocationTypeInternal, "")
	}
	if forUpdate && db.Dialector.Name() != "sqlite" {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}

	err := q.First(&bal).Error
	if err == nil {
		return &bal, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("inventory: load balance: %w", err)
	}

	// First movement at this location — seed a zero row.
	bal = models.InventoryBalance{
		CompanyID:      companyID,
		ItemID:         itemID,
		WarehouseID:    warehouseID,
		QuantityOnHand: decimal.Zero,
		AverageCost:    decimal.Zero,
	}
	if warehouseID == nil {
		bal.LocationType = models.LocationTypeInternal
	}
	if err := db.Create(&bal).Error; err != nil {
		return nil, fmt.Errorf("inventory: create balance: %w", err)
	}
	return &bal, nil
}

// applyInboundToBalance updates the balance for a receipt using weighted
// average cost:
//
//	new_avg = (old_qty × old_avg + in_qty × in_cost) / (old_qty + in_qty)
//
// The balance row is saved in-place and returned. Quantity must be positive;
// unitCost is the per-unit base-currency cost (already inclusive of any
// apportioned landed cost).
func applyInboundToBalance(db *gorm.DB, bal *models.InventoryBalance, quantity, unitCostBase decimal.Decimal) error {
	oldValue := bal.QuantityOnHand.Mul(bal.AverageCost)
	inboundValue := quantity.Mul(unitCostBase)
	newQty := bal.QuantityOnHand.Add(quantity)

	var newAvg decimal.Decimal
	switch {
	case newQty.IsPositive():
		newAvg = oldValue.Add(inboundValue).Div(newQty).Round(4)
	default:
		// Edge case: previous balance was negative enough that this receipt
		// doesn't bring it back to positive. Fall back to the inbound cost
		// so we don't divide by a non-positive quantity. Shouldn't arise in
		// normal operation.
		newAvg = unitCostBase
	}

	bal.QuantityOnHand = newQty
	bal.AverageCost = newAvg
	bal.UpdatedAt = time.Now()
	if err := db.Save(bal).Error; err != nil {
		return fmt.Errorf("inventory: save balance: %w", err)
	}
	return nil
}

// applyOutboundToBalance updates the balance for an issue/outflow using the
// weighted-average rule:
//
//	unit_cost used = current average_cost
//	new_qty        = old_qty - out_qty
//	new_avg        = old_avg   (unchanged on outbound)
//
// Returns the unit cost that applies to the outflow so the caller can
// record it on the movement row and hand it to GL for the COGS posting.
// Quantity must be positive.
func applyOutboundToBalance(db *gorm.DB, bal *models.InventoryBalance, quantity decimal.Decimal) (decimal.Decimal, error) {
	unitCost := bal.AverageCost
	bal.QuantityOnHand = bal.QuantityOnHand.Sub(quantity)
	bal.UpdatedAt = time.Now()
	if err := db.Save(bal).Error; err != nil {
		return decimal.Zero, fmt.Errorf("inventory: save balance: %w", err)
	}
	return unitCost, nil
}

// consumeFIFOLayers draws the requested quantity from the oldest cost
// layers for (company, item, warehouse). Returns the blended unit cost
// (total cost of consumed units / quantity) plus a per-layer breakdown
// so IssueStock can surface CostLayers in its result and the GL layer can
// produce multi-line COGS postings if it wants to.
//
// Preconditions: caller has verified enough stock exists (via
// applyOutboundToBalance's own bounds check). FIFO happens alongside the
// balance update, NOT instead of it — the balance still tracks
// on-hand / average. The average becomes less meaningful under FIFO but
// is kept for reporting parity.
//
// Returns ErrCostingLayerExhausted when the summed RemainingQuantity of
// available layers is less than `quantity`. This is the FIFO-equivalent of
// ErrInsufficientStock and can happen when weighted-avg writes have drifted
// the balance ahead of the layer sum (a known cross-method inconsistency
// pending a reconcile job in a future slice).
func consumeFIFOLayers(db *gorm.DB, companyID, itemID uint, warehouseID *uint, quantity decimal.Decimal) (decimal.Decimal, []CostLayerConsumed, error) {
	q := db.Model(&models.InventoryCostLayer{}).
		Where("company_id = ? AND item_id = ? AND remaining_quantity > 0",
			companyID, itemID)
	if warehouseID != nil {
		q = q.Where("warehouse_id = ?", *warehouseID)
	} else {
		q = q.Where("warehouse_id IS NULL")
	}

	var layers []models.InventoryCostLayer
	if err := q.Order("received_date asc, id asc").Find(&layers).Error; err != nil {
		return decimal.Zero, nil, fmt.Errorf("inventory: load cost layers: %w", err)
	}

	remaining := quantity
	totalCost := decimal.Zero
	consumed := make([]CostLayerConsumed, 0, 2)

	for i := range layers {
		if !remaining.IsPositive() {
			break
		}
		layer := &layers[i]
		draw := layer.RemainingQuantity
		if draw.GreaterThan(remaining) {
			draw = remaining
		}
		lineCost := draw.Mul(layer.UnitCostBase)

		consumed = append(consumed, CostLayerConsumed{
			LayerID:          layer.ID,
			SourceMovementID: layer.SourceMovementID,
			Quantity:         draw,
			UnitCostBase:     layer.UnitCostBase,
			TotalCostBase:    lineCost.RoundBank(2),
		})
		totalCost = totalCost.Add(lineCost)
		remaining = remaining.Sub(draw)

		layer.RemainingQuantity = layer.RemainingQuantity.Sub(draw)
		if err := db.Save(layer).Error; err != nil {
			return decimal.Zero, nil, fmt.Errorf("inventory: update cost layer: %w", err)
		}
	}

	if remaining.IsPositive() {
		return decimal.Zero, nil, fmt.Errorf("%w: need %s, layer coverage short by %s",
			ErrCostingLayerExhausted, quantity.String(), remaining.String())
	}
	blended := totalCost.Div(quantity).Round(4)
	return blended, consumed, nil
}

// applyReversalAtSnapshotCost updates the balance for a reversal, using the
// ORIGINAL movement's snapshot cost rather than the current average. This
// is the subtle but critical correctness property of ReverseMovement: if
// an old purchase at $5 is voided after avg has drifted to $6 (because of
// later receipts), the value removed must be the original $5 × qty, not
// the current $6. Otherwise the remaining inventory retains cost traces
// of a receipt that (for ledger purposes) never happened.
//
// Quantity is signed: positive restores stock (reversal of an issue),
// negative removes stock (reversal of a receipt).
//
// Math:
//
//	new_qty   = old_qty + delta
//	new_value = old_qty × old_avg + delta × snapshot_cost
//	new_avg   = new_value / new_qty    (or snapshot_cost when new_qty ≤ 0)
func applyReversalAtSnapshotCost(db *gorm.DB, bal *models.InventoryBalance, quantityDelta, snapshotUnitCost decimal.Decimal) error {
	oldValue := bal.QuantityOnHand.Mul(bal.AverageCost)
	deltaValue := quantityDelta.Mul(snapshotUnitCost)
	newQty := bal.QuantityOnHand.Add(quantityDelta)
	newValue := oldValue.Add(deltaValue)

	var newAvg decimal.Decimal
	if newQty.IsPositive() {
		newAvg = newValue.Div(newQty).Round(4)
	} else {
		// Edge case: reversal leaves the balance at zero or negative.
		// Fall back to the snapshot cost so the avg remains sensible if
		// another receipt later re-populates the balance.
		newAvg = snapshotUnitCost
	}

	bal.QuantityOnHand = newQty
	bal.AverageCost = newAvg
	bal.UpdatedAt = time.Now()
	if err := db.Save(bal).Error; err != nil {
		return fmt.Errorf("inventory: save balance: %w", err)
	}
	return nil
}
