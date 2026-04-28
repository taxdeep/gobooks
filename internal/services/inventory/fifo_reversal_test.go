// 遵循project_guide.md
package inventory

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"balanciz/internal/models"
)

// Every FIFO-costed IssueStock writes one consumption row per touched
// layer. The row captures the draw amount and the layer's unit cost so
// ReverseMovement can undo it later.
func TestIssueStock_FIFO_WritesConsumptionRows(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedLayeredReceipts(t, db, companyID, itemID, warehouseID, [][2]int64{
		{10, 4}, {10, 6},
	})

	issue, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(14), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
		CostingMethod: CostingMethodFIFO,
	})
	if err != nil {
		t.Fatalf("IssueStock FIFO: %v", err)
	}

	var rows []models.InventoryLayerConsumption
	db.Where("company_id = ? AND issue_movement_id = ?",
		companyID, issue.MovementID).Order("id asc").Find(&rows)
	if len(rows) != 2 {
		t.Fatalf("consumption rows: got %d want 2", len(rows))
	}

	// Row 1: first layer drained — 10 units.
	if !rows[0].QuantityDrawn.Equal(decimal.NewFromInt(10)) ||
		!rows[0].UnitCostBase.Equal(decimal.NewFromInt(4)) {
		t.Fatalf("row 0: got qty=%s cost=%s want 10/4",
			rows[0].QuantityDrawn, rows[0].UnitCostBase)
	}
	// Row 2: second layer partial — 4 units.
	if !rows[1].QuantityDrawn.Equal(decimal.NewFromInt(4)) ||
		!rows[1].UnitCostBase.Equal(decimal.NewFromInt(6)) {
		t.Fatalf("row 1: got qty=%s cost=%s want 4/6",
			rows[1].QuantityDrawn, rows[1].UnitCostBase)
	}
	// Neither row is marked reversed yet.
	for i, r := range rows {
		if r.ReversedByMovementID != nil {
			t.Fatalf("row %d: reserved_by should be nil on fresh draw", i)
		}
	}
}

// Reverting a FIFO issue restores every consumed layer's
// RemainingQuantity. After reversal, SUM(remaining) == on_hand again —
// the FIFO correctness gate.
func TestReverseMovement_FIFO_RestoresConsumedLayers(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedLayeredReceipts(t, db, companyID, itemID, warehouseID, [][2]int64{
		{10, 4}, {10, 6},
	})

	// Issue 14 → drains layer 1 fully, layer 2 by 4.
	issue, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(14), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
		CostingMethod: CostingMethodFIFO,
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	// Reverse.
	if _, err := ReverseMovement(db, ReverseMovementInput{
		CompanyID:          companyID,
		OriginalMovementID: issue.MovementID,
		MovementDate:       time.Now(),
		Reason:             ReversalReasonCustomerReturn,
		SourceType:         "invoice_reversal",
		SourceID:           1,
	}); err != nil {
		t.Fatalf("ReverseMovement: %v", err)
	}

	// Layers must be back to their original quantities.
	var layers []models.InventoryCostLayer
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).
		Order("received_date asc, id asc").Find(&layers)
	if !layers[0].RemainingQuantity.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("layer 0 remaining after reversal: got %s want 10",
			layers[0].RemainingQuantity)
	}
	if !layers[1].RemainingQuantity.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("layer 1 remaining after reversal: got %s want 10",
			layers[1].RemainingQuantity)
	}

	// Invariant: SUM(remaining) == on_hand.
	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?",
		companyID, itemID, warehouseID).First(&bal)
	var sumRemaining decimal.Decimal
	for _, l := range layers {
		sumRemaining = sumRemaining.Add(l.RemainingQuantity)
	}
	if !sumRemaining.Equal(bal.QuantityOnHand) {
		t.Fatalf("SUM(remaining)=%s != on_hand=%s", sumRemaining, bal.QuantityOnHand)
	}

	// Consumption rows are stamped as reversed (so a second reversal can't
	// double-restore).
	var rows []models.InventoryLayerConsumption
	db.Where("issue_movement_id = ?", issue.MovementID).Find(&rows)
	for i, r := range rows {
		if r.ReversedByMovementID == nil {
			t.Fatalf("consumption row %d: ReversedByMovementID should be set after reversal", i)
		}
	}
}

// A weighted-average issue produces no consumption rows, and its reversal
// still works using the snapshot-cost path (unchanged behaviour).
// Specifically, reversal must NOT error out on missing consumption log.
func TestReverseMovement_WeightedAvg_UnaffectedByConsumptionLog(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)

	issue, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(4), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
		// CostingMethod omitted → weighted-avg
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	// No consumption rows under weighted-avg.
	var count int64
	db.Model(&models.InventoryLayerConsumption{}).
		Where("issue_movement_id = ?", issue.MovementID).Count(&count)
	if count != 0 {
		t.Fatalf("weighted-avg issue should NOT write consumption rows: got %d", count)
	}

	// Reversal still succeeds.
	if _, err := ReverseMovement(db, ReverseMovementInput{
		CompanyID:          companyID,
		OriginalMovementID: issue.MovementID,
		MovementDate:       time.Now(),
		Reason:             ReversalReasonCustomerReturn,
		SourceType:         "invoice_reversal",
		SourceID:           1,
	}); err != nil {
		t.Fatalf("ReverseMovement on weighted-avg issue: %v", err)
	}

	// On-hand restored to 10.
	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?",
		companyID, itemID, warehouseID).First(&bal)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("on-hand after weighted-avg reversal: got %s want 10", bal.QuantityOnHand)
	}
}

// Double-reversal attempts: the second ReverseMovement call on the same
// original must fail with ErrReversalAlreadyApplied, preventing a
// double-restore of layers.
func TestReverseMovement_FIFO_DoubleReversalRejected(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedLayeredReceipts(t, db, companyID, itemID, warehouseID, [][2]int64{{10, 5}})

	issue, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(3), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
		CostingMethod: CostingMethodFIFO,
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	if _, err := ReverseMovement(db, ReverseMovementInput{
		CompanyID:          companyID,
		OriginalMovementID: issue.MovementID,
		MovementDate:       time.Now(),
		Reason:             ReversalReasonCustomerReturn,
		SourceType:         "invoice_reversal",
		SourceID:           1,
	}); err != nil {
		t.Fatalf("first reversal: %v", err)
	}

	_, err = ReverseMovement(db, ReverseMovementInput{
		CompanyID:          companyID,
		OriginalMovementID: issue.MovementID,
		MovementDate:       time.Now(),
		Reason:             ReversalReasonCustomerReturn,
		SourceType:         "invoice_reversal",
		SourceID:           1,
	})
	if err != ErrReversalAlreadyApplied {
		t.Fatalf("second reversal: got %v want ErrReversalAlreadyApplied", err)
	}

	// Layer should still have 10 remaining (restored exactly once).
	var layer models.InventoryCostLayer
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).First(&layer)
	if !layer.RemainingQuantity.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("layer remaining: got %s want 10 (must not double-restore)", layer.RemainingQuantity)
	}
}

// Legacy pre-E2.1 FIFO issue: no consumption log exists. Reversal still
// succeeds via snapshot-cost on-hand restoration, but layers stay stale.
// We verify the contract — no error; layer counter does NOT update — so
// callers/reconcile jobs have a defined point to target.
func TestReverseMovement_FIFO_LegacyIssueWithoutLog_LeavesLayersStale(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedLayeredReceipts(t, db, companyID, itemID, warehouseID, [][2]int64{{10, 5}})

	// Manually create an "issue" movement WITHOUT a consumption log row —
	// simulating a pre-E2.1 FIFO issue that landed before the log was
	// introduced, OR a weighted-avg issue misclassified.
	qty := decimal.NewFromInt(3)
	unit := decimal.NewFromInt(5)
	total := qty.Mul(unit)
	whVal := warehouseID
	mov := models.InventoryMovement{
		CompanyID:     companyID,
		ItemID:        itemID,
		MovementType:  models.MovementTypeSale,
		QuantityDelta: qty.Neg(),
		UnitCost:      &unit,
		UnitCostBase:  &unit,
		TotalCost:     &total,
		SourceType:    "invoice",
		MovementDate:  time.Now(),
		WarehouseID:   &whVal,
	}
	if err := db.Create(&mov).Error; err != nil {
		t.Fatalf("seed legacy issue: %v", err)
	}
	// Drain the balance and the layer directly to match what the legacy
	// issue would have done — then we verify reversal restores on-hand
	// but NOT the layer.
	db.Model(&models.InventoryBalance{}).
		Where("company_id = ? AND item_id = ?", companyID, itemID).
		Update("quantity_on_hand", decimal.NewFromInt(7))
	db.Model(&models.InventoryCostLayer{}).
		Where("company_id = ? AND item_id = ?", companyID, itemID).
		Update("remaining_quantity", decimal.NewFromInt(7))

	if _, err := ReverseMovement(db, ReverseMovementInput{
		CompanyID:          companyID,
		OriginalMovementID: mov.ID,
		MovementDate:       time.Now(),
		Reason:             ReversalReasonCustomerReturn,
		SourceType:         "invoice_reversal",
		SourceID:           99,
	}); err != nil {
		t.Fatalf("legacy reversal should succeed: %v", err)
	}

	// On-hand: restored to 10 via snapshot-cost reversal (always correct).
	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?",
		companyID, itemID, warehouseID).First(&bal)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("on-hand: got %s want 10", bal.QuantityOnHand)
	}
	// Layer: stays at 7 — this is the documented gap. Future reconcile
	// job should detect SUM(remaining) != on_hand and repair.
	var layer models.InventoryCostLayer
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).First(&layer)
	if !layer.RemainingQuantity.Equal(decimal.NewFromInt(7)) {
		t.Fatalf("legacy layer should stay stale: got %s want 7", layer.RemainingQuantity)
	}
}
