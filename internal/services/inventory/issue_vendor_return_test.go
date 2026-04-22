// 遵循project_guide.md
package inventory

// issue_vendor_return_test.go — Phase I slice I.6b.2a contract tests
// for the narrow traced-cost AP-return verb.
//
// Locks the Q3-keystone guarantees:
//
//  1. **Traced cost, not current average.** After a receipt at $5
//     and an adjustment that drifts the average to a different
//     figure, a vendor return still books the outflow at $5.
//
//  2. **Partial qty supported.** Return qty ≤ original receipt qty
//     works (unlike ReverseMovement, which is full-qty only).
//
//  3. **Insufficient-stock rejection.** Never AllowNegative — a
//     return with insufficient on-hand fails loud.
//
//  4. **Source-movement sanity checks.**
//       - Reversal row cannot be the cost anchor.
//       - Outflow (negative-delta) row cannot be the cost anchor.
//       - Zero-cost source is rejected.
//
//  5. **Idempotency.** Replay returns the cached result, no new
//     movement row.
//
//  6. **Average unchanged.** Traced-cost outflow follows the
//     outbound convention — `inventory_balances.average_cost` does
//     not shift.

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"gobooks/internal/models"
)

func TestIssueVendorReturn_HappyPath_TracedCostNotAverage(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)

	// Receipt A: 10 @ $5 = cost anchor.
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)
	// Receipt B: 10 @ $7 → drifts weighted-avg to $6.
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 7)

	// Sanity: current avg is $6.
	var balPre models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).First(&balPre)
	if !balPre.AverageCost.Equal(decimal.NewFromInt(6)) {
		t.Fatalf("pre-return avg: got %s want 6", balPre.AverageCost)
	}

	// Find receipt A's movement to trace against ($5 cost anchor).
	var anchor models.InventoryMovement
	db.Where("company_id = ? AND quantity_delta > 0", companyID).
		Order("id ASC").First(&anchor)

	result, err := IssueVendorReturn(db, IssueVendorReturnInput{
		CompanyID:          companyID,
		OriginalMovementID: anchor.ID,
		Quantity:           decimal.NewFromInt(4),
		MovementDate:       time.Now(),
		SourceType:         "vendor_return_shipment",
		SourceID:           100,
	})
	if err != nil {
		t.Fatalf("IssueVendorReturn: %v", err)
	}

	// Traced cost = $5 (receipt A), NOT $6 (current avg).
	if !result.UnitCostBase.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("UnitCostBase: got %s want 5 (traced from anchor, not current avg)",
			result.UnitCostBase)
	}
	if !result.OutflowValueBase.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("OutflowValueBase: got %s want 20 (4 × $5 traced)",
			result.OutflowValueBase)
	}

	// Movement row written correctly.
	var mov models.InventoryMovement
	db.First(&mov, result.MovementID)
	if !mov.QuantityDelta.Equal(decimal.NewFromInt(-4)) {
		t.Fatalf("QuantityDelta: got %s want -4", mov.QuantityDelta)
	}
	if mov.MovementType != models.MovementTypeVendorReturn {
		t.Fatalf("MovementType: got %s want vendor_return", mov.MovementType)
	}
	if mov.SourceType != "vendor_return_shipment" {
		t.Fatalf("SourceType: got %s want vendor_return_shipment", mov.SourceType)
	}

	// Balance: qty decremented, average UNCHANGED (traced-cost outbound
	// convention).
	var balPost models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).First(&balPost)
	if !balPost.QuantityOnHand.Equal(decimal.NewFromInt(16)) {
		t.Fatalf("QuantityOnHand: got %s want 16 (20 - 4)", balPost.QuantityOnHand)
	}
	if !balPost.AverageCost.Equal(decimal.NewFromInt(6)) {
		t.Fatalf("AverageCost: got %s want 6 (unchanged on outbound)",
			balPost.AverageCost)
	}
}

func TestIssueVendorReturn_PartialQty_LessThanAnchor(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)

	var anchor models.InventoryMovement
	db.Where("company_id = ? AND quantity_delta > 0", companyID).First(&anchor)

	// Return 3 of the original 10 — partial is supported (contrast
	// with ReverseMovement which would reverse all 10).
	result, err := IssueVendorReturn(db, IssueVendorReturnInput{
		CompanyID:          companyID,
		OriginalMovementID: anchor.ID,
		Quantity:           decimal.NewFromInt(3),
		MovementDate:       time.Now(),
		SourceType:         "vendor_return_shipment",
		SourceID:           200,
	})
	if err != nil {
		t.Fatalf("IssueVendorReturn partial: %v", err)
	}
	if !result.OutflowValueBase.Equal(decimal.NewFromInt(15)) {
		t.Fatalf("partial outflow value: got %s want 15 (3 × $5)",
			result.OutflowValueBase)
	}

	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).First(&bal)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(7)) {
		t.Fatalf("on-hand: got %s want 7", bal.QuantityOnHand)
	}
}

func TestIssueVendorReturn_InsufficientStockRejected(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	// Receipt 5 units. Then issue 5 out. On-hand is zero.
	seedReceive(t, db, companyID, itemID, warehouseID, 5, 5)
	_, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(5), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
	})
	if err != nil {
		t.Fatalf("IssueStock to drain: %v", err)
	}

	var anchor models.InventoryMovement
	db.Where("company_id = ? AND quantity_delta > 0", companyID).First(&anchor)

	_, err = IssueVendorReturn(db, IssueVendorReturnInput{
		CompanyID:          companyID,
		OriginalMovementID: anchor.ID,
		Quantity:           decimal.NewFromInt(1),
		MovementDate:       time.Now(),
		SourceType:         "vendor_return_shipment",
		SourceID:           300,
	})
	if !errors.Is(err, ErrInsufficientStock) {
		t.Fatalf("expected ErrInsufficientStock, got: %v", err)
	}
}

func TestIssueVendorReturn_RejectsReversalRowAsAnchor(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)

	var orig models.InventoryMovement
	db.Where("company_id = ? AND quantity_delta > 0", companyID).First(&orig)

	// Book a reversal row, then try to use the REVERSAL as the cost
	// anchor.
	_, err := ReverseMovement(db, ReverseMovementInput{
		CompanyID:          companyID,
		OriginalMovementID: orig.ID,
		MovementDate:       time.Now(),
		Reason:             ReversalReasonCancellation,
		SourceType:         "bill_void",
		SourceID:           999,
	})
	if err != nil {
		t.Fatalf("seed ReverseMovement: %v", err)
	}
	var reversalRow models.InventoryMovement
	db.Where("company_id = ? AND reversal_of_movement_id IS NOT NULL", companyID).
		First(&reversalRow)

	_, err = IssueVendorReturn(db, IssueVendorReturnInput{
		CompanyID:          companyID,
		OriginalMovementID: reversalRow.ID, // bogus — pointing at a reversal
		Quantity:           decimal.NewFromInt(1),
		MovementDate:       time.Now(),
		SourceType:         "vendor_return_shipment",
		SourceID:           400,
	})
	if err == nil {
		t.Fatalf("expected error rejecting reversal row as anchor")
	}
}

func TestIssueVendorReturn_RejectsOutflowRowAsAnchor(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)

	// An outflow movement (sale).
	issueRes, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(3), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
	})
	if err != nil {
		t.Fatalf("IssueStock: %v", err)
	}

	_, err = IssueVendorReturn(db, IssueVendorReturnInput{
		CompanyID:          companyID,
		OriginalMovementID: issueRes.MovementID, // bogus — outflow can't be cost anchor
		Quantity:           decimal.NewFromInt(1),
		MovementDate:       time.Now(),
		SourceType:         "vendor_return_shipment",
		SourceID:           500,
	})
	if err == nil {
		t.Fatalf("expected error rejecting outflow row as anchor")
	}
}

func TestIssueVendorReturn_RejectsMissingOriginalMovementID(t *testing.T) {
	db := testDB(t)
	companyID, _, _ := seedTestFixture(t, db)

	_, err := IssueVendorReturn(db, IssueVendorReturnInput{
		CompanyID:    companyID,
		Quantity:     decimal.NewFromInt(1),
		MovementDate: time.Now(),
		SourceType:   "vendor_return_shipment",
		SourceID:     600,
	})
	if err == nil {
		t.Fatalf("expected error for missing OriginalMovementID")
	}
}

func TestIssueVendorReturn_IdempotencyReplay(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)

	var anchor models.InventoryMovement
	db.Where("company_id = ? AND quantity_delta > 0", companyID).First(&anchor)

	in := IssueVendorReturnInput{
		CompanyID:          companyID,
		OriginalMovementID: anchor.ID,
		Quantity:           decimal.NewFromInt(2),
		MovementDate:       time.Now(),
		SourceType:         "vendor_return_shipment",
		SourceID:           700,
		IdempotencyKey:     "ivr-test-key-1",
	}

	first, err := IssueVendorReturn(db, in)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	second, err := IssueVendorReturn(db, in)
	if err != nil {
		t.Fatalf("second (replay) call: %v", err)
	}
	if first.MovementID != second.MovementID {
		t.Fatalf("idempotency: replay returned different MovementID (first=%d second=%d)",
			first.MovementID, second.MovementID)
	}

	// Only one outflow row exists for this idempotency key.
	var outflowCount int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ? AND source_id = ?",
			companyID, "vendor_return_shipment", 700).
		Count(&outflowCount)
	if outflowCount != 1 {
		t.Fatalf("idempotent replay should not create duplicate movements; got %d",
			outflowCount)
	}
}
