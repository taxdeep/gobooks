// 遵循project_guide.md
package inventory

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"balanciz/internal/models"
)

// Happy path: 10 units at $5 in source, transfer 4 to destination same-day.
// Both legs book, source drops to 6, destination rises to 4, both movements
// share the same unit cost.
func TestTransferStock_HappyPathSameDay(t *testing.T) {
	db := testDB(t)
	companyID, itemID, srcID := seedTestFixture(t, db)
	dst := models.Warehouse{CompanyID: companyID, Name: "Dest", IsActive: true}
	db.Create(&dst)

	seedReceive(t, db, companyID, itemID, srcID, 10, 5)

	now := time.Now()
	result, err := TransferStock(db, TransferStockInput{
		CompanyID:       companyID,
		TransferID:      999,
		ItemID:          itemID,
		FromWarehouseID: srcID,
		ToWarehouseID:   dst.ID,
		Quantity:        decimal.NewFromInt(4),
		ShippedDate:     now,
		ReceivedDate:    &now,
	})
	if err != nil {
		t.Fatalf("TransferStock: %v", err)
	}
	if result.ReceiveMovementID == nil {
		t.Fatalf("same-day transfer should produce a receive movement")
	}
	if !result.UnitCostBase.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("UnitCostBase: got %s want 5", result.UnitCostBase)
	}
	if !result.TransitValueBase.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("TransitValueBase: got %s want 20", result.TransitValueBase)
	}

	var srcBal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?",
		companyID, itemID, srcID).First(&srcBal)
	if !srcBal.QuantityOnHand.Equal(decimal.NewFromInt(6)) {
		t.Fatalf("source qty: got %s want 6", srcBal.QuantityOnHand)
	}

	var dstBal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?",
		companyID, itemID, dst.ID).First(&dstBal)
	if !dstBal.QuantityOnHand.Equal(decimal.NewFromInt(4)) {
		t.Fatalf("destination qty: got %s want 4", dstBal.QuantityOnHand)
	}
	// Destination inherits the source's avg — cost-neutral transfer.
	if !dstBal.AverageCost.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("destination avg cost: got %s want 5 (should inherit source avg)", dstBal.AverageCost)
	}
}

// In-transit: no ReceivedDate means only the issue leg books. Source drops,
// destination stays at zero, ReceiveMovementID is nil.
func TestTransferStock_InTransitIssueOnly(t *testing.T) {
	db := testDB(t)
	companyID, itemID, srcID := seedTestFixture(t, db)
	dst := models.Warehouse{CompanyID: companyID, Name: "Dest", IsActive: true}
	db.Create(&dst)
	seedReceive(t, db, companyID, itemID, srcID, 10, 5)

	result, err := TransferStock(db, TransferStockInput{
		CompanyID:       companyID,
		TransferID:      1,
		ItemID:          itemID,
		FromWarehouseID: srcID,
		ToWarehouseID:   dst.ID,
		Quantity:        decimal.NewFromInt(3),
		ShippedDate:     time.Now(),
		// ReceivedDate nil → still in transit
	})
	if err != nil {
		t.Fatalf("TransferStock in-transit: %v", err)
	}
	if result.ReceiveMovementID != nil {
		t.Fatalf("in-transit transfer should not produce a receive movement")
	}

	var srcBal models.InventoryBalance
	db.Where("warehouse_id = ?", srcID).First(&srcBal)
	if !srcBal.QuantityOnHand.Equal(decimal.NewFromInt(7)) {
		t.Fatalf("source qty: got %s want 7", srcBal.QuantityOnHand)
	}
	var dstBal models.InventoryBalance
	err = db.Where("warehouse_id = ?", dst.ID).First(&dstBal).Error
	if err == nil && !dstBal.QuantityOnHand.IsZero() {
		t.Fatalf("destination should have zero stock pre-receive; got qty %s", dstBal.QuantityOnHand)
	}
}

// Insufficient source stock is rejected.
func TestTransferStock_RejectsInsufficient(t *testing.T) {
	db := testDB(t)
	companyID, itemID, srcID := seedTestFixture(t, db)
	dst := models.Warehouse{CompanyID: companyID, Name: "Dest", IsActive: true}
	db.Create(&dst)
	seedReceive(t, db, companyID, itemID, srcID, 2, 5)

	now := time.Now()
	_, err := TransferStock(db, TransferStockInput{
		CompanyID:       companyID,
		TransferID:      1,
		ItemID:          itemID,
		FromWarehouseID: srcID,
		ToWarehouseID:   dst.ID,
		Quantity:        decimal.NewFromInt(5),
		ShippedDate:     now,
		ReceivedDate:    &now,
	})
	if !errors.Is(err, ErrInsufficientStock) {
		t.Fatalf("got %v, want ErrInsufficientStock", err)
	}
}

// Source == destination is rejected — transfers must cross warehouses.
func TestTransferStock_RejectsSameWarehouse(t *testing.T) {
	db := testDB(t)
	companyID, itemID, srcID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, srcID, 10, 5)

	now := time.Now()
	_, err := TransferStock(db, TransferStockInput{
		CompanyID:       companyID,
		TransferID:      1,
		ItemID:          itemID,
		FromWarehouseID: srcID,
		ToWarehouseID:   srcID,
		Quantity:        decimal.NewFromInt(1),
		ShippedDate:     now,
		ReceivedDate:    &now,
	})
	if err == nil {
		t.Fatalf("expected error on same-warehouse transfer")
	}
}

// Idempotency: the two-phase completion pattern should re-use the same
// issue-leg movement on a retry. (An issue-only call followed by a both-
// dates call with the same key completes the receive leg without
// double-issuing.)
func TestTransferStock_TwoPhaseIdempotency(t *testing.T) {
	db := testDB(t)
	companyID, itemID, srcID := seedTestFixture(t, db)
	dst := models.Warehouse{CompanyID: companyID, Name: "Dest", IsActive: true}
	db.Create(&dst)
	seedReceive(t, db, companyID, itemID, srcID, 10, 5)

	now := time.Now()
	key := "warehouse_transfer:777:v1"

	// Phase 1: issue only.
	first, err := TransferStock(db, TransferStockInput{
		CompanyID:       companyID,
		TransferID:      777,
		ItemID:          itemID,
		FromWarehouseID: srcID,
		ToWarehouseID:   dst.ID,
		Quantity:        decimal.NewFromInt(3),
		ShippedDate:     now,
		IdempotencyKey:  key,
	})
	if err != nil {
		t.Fatalf("phase 1: %v", err)
	}
	if first.ReceiveMovementID != nil {
		t.Fatalf("phase 1 should not receive")
	}

	// Phase 2: both legs. The issue leg must reuse the earlier movement.
	second, err := TransferStock(db, TransferStockInput{
		CompanyID:       companyID,
		TransferID:      777,
		ItemID:          itemID,
		FromWarehouseID: srcID,
		ToWarehouseID:   dst.ID,
		Quantity:        decimal.NewFromInt(3),
		ShippedDate:     now,
		ReceivedDate:    &now,
		IdempotencyKey:  key,
	})
	if err != nil {
		t.Fatalf("phase 2: %v", err)
	}
	if second.IssueMovementID != first.IssueMovementID {
		t.Fatalf("issue leg should reuse earlier movement: got %d, want %d",
			second.IssueMovementID, first.IssueMovementID)
	}
	if second.ReceiveMovementID == nil {
		t.Fatalf("phase 2 should produce receive movement")
	}

	// Balances: exactly one issue and one receive applied.
	var srcBal models.InventoryBalance
	db.Where("warehouse_id = ?", srcID).First(&srcBal)
	if !srcBal.QuantityOnHand.Equal(decimal.NewFromInt(7)) {
		t.Fatalf("source qty: got %s want 7", srcBal.QuantityOnHand)
	}
	var dstBal models.InventoryBalance
	db.Where("warehouse_id = ?", dst.ID).First(&dstBal)
	if !dstBal.QuantityOnHand.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("destination qty: got %s want 3", dstBal.QuantityOnHand)
	}
}
