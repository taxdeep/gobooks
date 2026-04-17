// 遵循project_guide.md
package inventory

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"gobooks/internal/models"
)

// Reversing a sale (negative-delta original) restores stock at the ORIGINAL
// cost, not the current weighted average. Setup: receive 10 @ $5, sell 4
// (COGS $5/unit), then receive more stock that changes avg to $7. When the
// sale is reversed the 4 units come back at $5 each — avg should re-settle
// below $7.
func TestReverseMovement_SaleReversalUsesSnapshotCost(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)

	// Receive 10 @ $5.
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)
	// Sell 4. Records COGS unit $5.
	issue, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(4), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 100,
	})
	if err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	// Receive 10 @ $10 — now balance: qty=16, avg=(6*5 + 10*10)/16 = 8.125.
	// (Not 7 as the spec sketch implied; the actual math is correct.)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 10)

	// Reverse the sale.
	result, err := ReverseMovement(db, ReverseMovementInput{
		CompanyID:          companyID,
		OriginalMovementID: issue.MovementID,
		MovementDate:       time.Now(),
		Reason:             ReversalReasonCustomerReturn,
		SourceType:         "invoice_reversal",
		SourceID:           100,
	})
	if err != nil {
		t.Fatalf("ReverseMovement: %v", err)
	}
	// Reversal uses the ORIGINAL sale's unit cost ($5), not current avg.
	if !result.UnitCostBase.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("UnitCostBase: got %s want 5 (snapshot cost)", result.UnitCostBase)
	}
	// Value = +4 × $5 = +$20 (restoring stock)
	if !result.ReversalValueBase.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("ReversalValueBase: got %s want 20", result.ReversalValueBase)
	}

	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).First(&bal)
	// After reversal: qty = 16 + 4 = 20.
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("QuantityOnHand: got %s want 20", bal.QuantityOnHand)
	}
	// Value = 16 × 8.125 + 4 × 5 = 130 + 20 = 150; new avg = 150 / 20 = 7.5.
	if !bal.AverageCost.Equal(decimal.NewFromFloat(7.5)) {
		t.Fatalf("AverageCost: got %s want 7.5", bal.AverageCost)
	}
}

// Reversing a purchase (positive-delta original) removes stock at the
// original cost. If the subsequent balance isn't enough to cover the
// reversal, it errors out with ErrInsufficientStock.
func TestReverseMovement_PurchaseReversalRemovesAtSnapshotCost(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)

	// Receive 10 @ $5 (original purchase we'll reverse).
	rs1, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(10), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
	})
	if err != nil {
		t.Fatalf("seed receive: %v", err)
	}
	// Receive 10 @ $7 (avg now 6).
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 7)

	// Reverse the original $5 purchase — removes 10 at $5.
	result, err := ReverseMovement(db, ReverseMovementInput{
		CompanyID:          companyID,
		OriginalMovementID: rs1.MovementID,
		MovementDate:       time.Now(),
		Reason:             ReversalReasonCancellation,
		SourceType:         "bill_reversal",
		SourceID:           1,
	})
	if err != nil {
		t.Fatalf("ReverseMovement: %v", err)
	}
	if !result.UnitCostBase.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("UnitCostBase: got %s want 5 (snapshot)", result.UnitCostBase)
	}
	// Value = -10 × 5 = -$50 (signed outflow).
	if !result.ReversalValueBase.Equal(decimal.NewFromInt(-50)) {
		t.Fatalf("ReversalValueBase: got %s want -50", result.ReversalValueBase)
	}

	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).First(&bal)
	// 20 units total, value = 20×6 = 120. Remove 10 at $5 = $50.
	// Remaining: qty=10, value=70, avg=7. This is the right answer — the $5
	// receipt is undone and only the $7 receipt's valuation survives.
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("QuantityOnHand: got %s want 10", bal.QuantityOnHand)
	}
	if !bal.AverageCost.Equal(decimal.NewFromInt(7)) {
		t.Fatalf("AverageCost: got %s want 7 (only the $7 receipt survives)", bal.AverageCost)
	}
}

// Reversing the same movement twice is blocked.
func TestReverseMovement_CannotReverseTwice(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)
	issue, _ := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(3), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
	})
	if _, err := ReverseMovement(db, ReverseMovementInput{
		CompanyID: companyID, OriginalMovementID: issue.MovementID,
		MovementDate: time.Now(), Reason: ReversalReasonCustomerReturn,
		SourceType: "invoice_reversal", SourceID: 1,
	}); err != nil {
		t.Fatalf("first reversal: %v", err)
	}
	_, err := ReverseMovement(db, ReverseMovementInput{
		CompanyID: companyID, OriginalMovementID: issue.MovementID,
		MovementDate: time.Now(), Reason: ReversalReasonCustomerReturn,
		SourceType: "invoice_reversal", SourceID: 1,
	})
	if !errors.Is(err, ErrReversalAlreadyApplied) {
		t.Fatalf("got %v, want ErrReversalAlreadyApplied", err)
	}
}

// A reversal row itself cannot be reversed — the chain is explicitly linear.
// Forward corrections should be new movements, not reversal-of-reversals.
func TestReverseMovement_CannotReverseAReversalRow(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)
	issue, _ := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(3), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
	})
	rev, err := ReverseMovement(db, ReverseMovementInput{
		CompanyID: companyID, OriginalMovementID: issue.MovementID,
		MovementDate: time.Now(), Reason: ReversalReasonCustomerReturn,
		SourceType: "invoice_reversal", SourceID: 1,
	})
	if err != nil {
		t.Fatalf("first reversal: %v", err)
	}
	_, err = ReverseMovement(db, ReverseMovementInput{
		CompanyID: companyID, OriginalMovementID: rev.ReversalMovementID,
		MovementDate: time.Now(), Reason: ReversalReasonErrorCorrection,
		SourceType: "correction", SourceID: 1,
	})
	if err == nil {
		t.Fatalf("expected error reversing a reversal row, got nil")
	}
}

// Reversal leaves the original movement intact but links it back via
// reversed_by_movement_id, and the reversal row carries reversal_of_movement_id.
// Immutability invariant — original never mutated beyond that FK.
func TestReverseMovement_ChainLinkedBidirectionally(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)
	issue, _ := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(3), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
	})
	rev, err := ReverseMovement(db, ReverseMovementInput{
		CompanyID: companyID, OriginalMovementID: issue.MovementID,
		MovementDate: time.Now(), Reason: ReversalReasonCustomerReturn,
		SourceType: "invoice_reversal", SourceID: 1,
	})
	if err != nil {
		t.Fatalf("reversal: %v", err)
	}
	var orig, reversal models.InventoryMovement
	db.First(&orig, issue.MovementID)
	db.First(&reversal, rev.ReversalMovementID)

	if orig.ReversedByMovementID == nil || *orig.ReversedByMovementID != reversal.ID {
		t.Fatalf("original.ReversedByMovementID: got %v want %d",
			orig.ReversedByMovementID, reversal.ID)
	}
	if reversal.ReversalOfMovementID == nil || *reversal.ReversalOfMovementID != orig.ID {
		t.Fatalf("reversal.ReversalOfMovementID: got %v want %d",
			reversal.ReversalOfMovementID, orig.ID)
	}
	// Original QuantityDelta should still be the original's value.
	if !orig.QuantityDelta.Equal(decimal.NewFromInt(-3)) {
		t.Fatalf("original qty mutated: got %s", orig.QuantityDelta)
	}
}

// A reversal whose undo would require stock the system doesn't currently
// have is blocked with ErrInsufficientStock.
func TestReverseMovement_PurchaseReversalBlocksWhenStockConsumed(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	rs, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(10), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
	})
	if err != nil {
		t.Fatalf("seed receive: %v", err)
	}
	// Sell 7 — only 3 remain.
	if _, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(7), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 9,
	}); err != nil {
		t.Fatalf("seed issue: %v", err)
	}

	// Reverse the 10-unit purchase — needs to remove 10, only have 3.
	_, err = ReverseMovement(db, ReverseMovementInput{
		CompanyID:          companyID,
		OriginalMovementID: rs.MovementID,
		MovementDate:       time.Now(),
		Reason:             ReversalReasonCancellation,
		SourceType:         "bill_reversal",
		SourceID:           1,
	})
	if !errors.Is(err, ErrInsufficientStock) {
		t.Fatalf("got %v, want ErrInsufficientStock", err)
	}
}

// Idempotency: same key replayed returns the cached reversal.
func TestReverseMovement_IdempotencyReplay(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)
	issue, _ := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(3), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 7,
	})

	in := ReverseMovementInput{
		CompanyID: companyID, OriginalMovementID: issue.MovementID,
		MovementDate: time.Now(), Reason: ReversalReasonCustomerReturn,
		SourceType: "invoice_reversal", SourceID: 7,
		IdempotencyKey: "invoice:7:reverse:line:1:v1",
	}
	first, err := ReverseMovement(db, in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := ReverseMovement(db, in)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if first.ReversalMovementID != second.ReversalMovementID {
		t.Fatalf("replay returned different ID")
	}
}
