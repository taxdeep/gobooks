// 遵循project_guide.md
package inventory

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"balanciz/internal/models"
)

// Positive adjustment with an explicit unit cost: balance grows by the delta
// and the weighted average shifts accordingly. Mirrors a receipt, just with
// source_type="adjustment".
func TestAdjustStock_GainWithExplicitCost(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)

	unitCost := decimal.NewFromInt(7)
	result, err := AdjustStock(db, AdjustStockInput{
		CompanyID:     companyID,
		ItemID:        itemID,
		WarehouseID:   warehouseID,
		QuantityDelta: decimal.NewFromInt(10),
		MovementDate:  time.Now(),
		Reason:        AdjustmentReasonCountVariance,
		UnitCost:      &unitCost,
		Memo:          "Monthly cycle count",
	})
	if err != nil {
		t.Fatalf("AdjustStock gain: %v", err)
	}
	if !result.AdjustmentValueBase.Equal(decimal.NewFromInt(70)) {
		t.Fatalf("AdjustmentValueBase: got %s want 70", result.AdjustmentValueBase)
	}

	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).First(&bal)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("QuantityOnHand: got %s want 20", bal.QuantityOnHand)
	}
	// Avg = (10*5 + 10*7) / 20 = 6
	if !bal.AverageCost.Equal(decimal.NewFromInt(6)) {
		t.Fatalf("AverageCost: got %s want 6", bal.AverageCost)
	}
}

// Positive adjustment WITHOUT a unit cost falls back to current average so
// "found stock" events don't distort valuation. Balance grows; avg unchanged.
func TestAdjustStock_GainFallsBackToCurrentAvg(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)

	result, err := AdjustStock(db, AdjustStockInput{
		CompanyID:     companyID,
		ItemID:        itemID,
		WarehouseID:   warehouseID,
		QuantityDelta: decimal.NewFromInt(3),
		MovementDate:  time.Now(),
		Reason:        AdjustmentReasonCountVariance,
		// UnitCost deliberately nil
	})
	if err != nil {
		t.Fatalf("AdjustStock: %v", err)
	}
	if !result.UnitCostBase.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("UnitCostBase: got %s want 5 (current avg)", result.UnitCostBase)
	}

	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).First(&bal)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(13)) {
		t.Fatalf("QuantityOnHand: got %s want 13", bal.QuantityOnHand)
	}
	if !bal.AverageCost.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("AverageCost: got %s want 5 (should be unchanged)", bal.AverageCost)
	}
}

// Negative adjustment uses current avg as cost and returns a negative
// AdjustmentValueBase (signed loss).
func TestAdjustStock_LossUsesAverageAndReturnsNegativeValue(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)

	result, err := AdjustStock(db, AdjustStockInput{
		CompanyID:     companyID,
		ItemID:        itemID,
		WarehouseID:   warehouseID,
		QuantityDelta: decimal.NewFromInt(-4),
		MovementDate:  time.Now(),
		Reason:        AdjustmentReasonDamage,
		Memo:          "Broken in transit",
	})
	if err != nil {
		t.Fatalf("AdjustStock loss: %v", err)
	}
	if !result.UnitCostBase.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("UnitCostBase: got %s want 5", result.UnitCostBase)
	}
	// Value = -4 * 5 = -20 (signed loss)
	if !result.AdjustmentValueBase.Equal(decimal.NewFromInt(-20)) {
		t.Fatalf("AdjustmentValueBase: got %s want -20", result.AdjustmentValueBase)
	}

	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).First(&bal)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(6)) {
		t.Fatalf("QuantityOnHand: got %s want 6", bal.QuantityOnHand)
	}
}

// Loss exceeding on-hand is rejected via the underlying IssueStock guard.
func TestAdjustStock_LossRejectsInsufficient(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 3, 10)

	_, err := AdjustStock(db, AdjustStockInput{
		CompanyID:     companyID,
		ItemID:        itemID,
		WarehouseID:   warehouseID,
		QuantityDelta: decimal.NewFromInt(-5),
		MovementDate:  time.Now(),
		Reason:        AdjustmentReasonTheft,
	})
	if !errors.Is(err, ErrInsufficientStock) {
		t.Fatalf("got %v, want ErrInsufficientStock", err)
	}
}

// Zero-delta writes a marker movement for audit, doesn't touch the balance.
// Use case: user ran a cycle count that came out exactly right — still
// worth recording that the count happened.
func TestAdjustStock_ZeroDeltaWritesMarker(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)

	var beforeCount int64
	db.Model(&models.InventoryMovement{}).Count(&beforeCount)

	result, err := AdjustStock(db, AdjustStockInput{
		CompanyID:     companyID,
		ItemID:        itemID,
		WarehouseID:   warehouseID,
		QuantityDelta: decimal.Zero,
		MovementDate:  time.Now(),
		Reason:        AdjustmentReasonCountVariance,
		Memo:          "Zero-variance cycle count",
	})
	if err != nil {
		t.Fatalf("zero-delta AdjustStock: %v", err)
	}
	if !result.AdjustmentValueBase.IsZero() {
		t.Fatalf("AdjustmentValueBase: got %s want 0", result.AdjustmentValueBase)
	}

	// A fresh movement row should exist.
	var afterCount int64
	db.Model(&models.InventoryMovement{}).Count(&afterCount)
	if afterCount != beforeCount+1 {
		t.Fatalf("movement count: got %d want %d", afterCount, beforeCount+1)
	}

	// Balance unchanged.
	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).First(&bal)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("QuantityOnHand: got %s want 10 (balance should be untouched)", bal.QuantityOnHand)
	}
}

// The AdjustmentReason tag is prepended to the reference note so audit
// reports can scan for it without a dedicated column.
func TestAdjustStock_ReasonEmbeddedInMemo(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)

	result, err := AdjustStock(db, AdjustStockInput{
		CompanyID:     companyID,
		ItemID:        itemID,
		WarehouseID:   warehouseID,
		QuantityDelta: decimal.NewFromInt(-1),
		MovementDate:  time.Now(),
		Reason:        AdjustmentReasonDamage,
		Memo:          "Dropped during unpacking",
	})
	if err != nil {
		t.Fatalf("AdjustStock: %v", err)
	}

	var mov models.InventoryMovement
	db.First(&mov, result.MovementID)
	// The reason tag should appear in ReferenceNote built by the facade.
	if !strings.Contains(mov.ReferenceNote, "damage") {
		t.Fatalf("ReferenceNote: expected to contain 'damage', got %q", mov.ReferenceNote)
	}
	if !strings.Contains(mov.ReferenceNote, "Dropped during unpacking") {
		t.Fatalf("ReferenceNote: expected to contain user memo, got %q", mov.ReferenceNote)
	}
}

// Idempotency: replaying the same positive-delta adjustment returns the
// cached movement ID without creating a duplicate row.
func TestAdjustStock_IdempotencyReplay(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)

	unitCost := decimal.NewFromInt(7)
	in := AdjustStockInput{
		CompanyID:      companyID,
		ItemID:         itemID,
		WarehouseID:    warehouseID,
		QuantityDelta:  decimal.NewFromInt(3),
		MovementDate:   time.Now(),
		Reason:         AdjustmentReasonCountVariance,
		UnitCost:       &unitCost,
		IdempotencyKey: "adj:cycle-count:2026-04-17:v1",
	}
	first, err := AdjustStock(db, in)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	second, err := AdjustStock(db, in)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if first.MovementID != second.MovementID {
		t.Fatalf("replay returned different MovementID")
	}

	// Balance reflects a single application.
	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).First(&bal)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(13)) {
		t.Fatalf("QuantityOnHand: got %s want 13", bal.QuantityOnHand)
	}
}
