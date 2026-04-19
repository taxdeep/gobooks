// 遵循project_guide.md
package inventory

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"gobooks/internal/models"
)

// ── Lot issue + reverse ──────────────────────────────────────────────────────

// Lot explicit issue: caller names lots and quantities; each lot's
// remaining decrements accordingly and a consumption row is written.
func TestIssueStock_LotTracked_ExplicitSelectionHappyPath(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingLot)

	// Two lots on hand: LOT-A(10) + LOT-B(5).
	recv := func(qty int64, lotNum string, sid uint) {
		if _, err := ReceiveStock(db, ReceiveStockInput{
			CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
			Quantity: decimal.NewFromInt(qty), MovementDate: time.Now(),
			UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
			SourceType: "bill", SourceID: sid,
			LotNumber: lotNum,
		}); err != nil {
			t.Fatalf("seed %s: %v", lotNum, err)
		}
	}
	recv(10, "LOT-A", 1)
	recv(5, "LOT-B", 2)

	var lotA, lotB models.InventoryLot
	db.Where("lot_number = ?", "LOT-A").First(&lotA)
	db.Where("lot_number = ?", "LOT-B").First(&lotB)

	// Issue 7: 4 from A + 3 from B.
	issue, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(7), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 99,
		LotSelections: []LotSelection{
			{LotID: lotA.ID, Quantity: decimal.NewFromInt(4)},
			{LotID: lotB.ID, Quantity: decimal.NewFromInt(3)},
		},
	})
	if err != nil {
		t.Fatalf("IssueStock: %v", err)
	}

	// Verify lot remainings.
	db.First(&lotA, lotA.ID)
	db.First(&lotB, lotB.ID)
	if !lotA.RemainingQuantity.Equal(decimal.NewFromInt(6)) {
		t.Fatalf("A remaining: got %s want 6", lotA.RemainingQuantity)
	}
	if !lotB.RemainingQuantity.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("B remaining: got %s want 2", lotB.RemainingQuantity)
	}

	// Verify consumption rows.
	var rows []models.InventoryTrackingConsumption
	db.Where("issue_movement_id = ?", issue.MovementID).Find(&rows)
	if len(rows) != 2 {
		t.Fatalf("consumption rows: got %d want 2", len(rows))
	}
	for _, r := range rows {
		if r.SerialUnitID != nil {
			t.Fatalf("lot path wrote serial_unit_id: %+v", r)
		}
		if r.LotID == nil {
			t.Fatalf("lot path missing lot_id: %+v", r)
		}
		if r.ReversedByMovementID != nil {
			t.Fatalf("fresh consumption row should not be reversed yet")
		}
	}
}

// Lot selection sum mismatched with quantity is rejected.
func TestIssueStock_LotTracked_SelectionSumMismatchRejected(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingLot)

	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(10), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		LotNumber: "LOT-X",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var lot models.InventoryLot
	db.Where("lot_number = ?", "LOT-X").First(&lot)

	_, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(5), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
		LotSelections: []LotSelection{
			{LotID: lot.ID, Quantity: decimal.NewFromInt(3)}, // sums to 3, not 5
		},
	})
	if !errors.Is(err, ErrLotSelectionMissing) {
		t.Fatalf("got %v, want ErrLotSelectionMissing", err)
	}
}

// Lot selection exceeding remaining quantity is rejected.
func TestIssueStock_LotTracked_SelectionExceedsRemainingRejected(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingLot)

	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(3), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		LotNumber: "LOT-SMALL",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var lot models.InventoryLot
	db.Where("lot_number = ?", "LOT-SMALL").First(&lot)

	_, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(5), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
		AllowNegative: true, // bypass on-hand guard to reach the lot check
		LotSelections: []LotSelection{
			{LotID: lot.ID, Quantity: decimal.NewFromInt(5)},
		},
	})
	if !errors.Is(err, ErrLotSelectionExceedsRemaining) {
		t.Fatalf("got %v, want ErrLotSelectionExceedsRemaining", err)
	}
}

// Reversing a lot-tracked issue restores each lot's remaining exactly
// and stamps consumption rows reversed_by.
func TestReverseMovement_LotTracked_RestoresLotRemainings(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingLot)

	// Setup: LOT-A 10 units, issue 4.
	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(10), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		LotNumber: "LOT-A",
	}); err != nil {
		t.Fatalf("receive: %v", err)
	}
	var lot models.InventoryLot
	db.Where("lot_number = ?", "LOT-A").First(&lot)

	issue, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(4), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 42,
		LotSelections: []LotSelection{
			{LotID: lot.ID, Quantity: decimal.NewFromInt(4)},
		},
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
		SourceID:           42,
	}); err != nil {
		t.Fatalf("ReverseMovement: %v", err)
	}

	// Lot remaining back at 10.
	db.First(&lot, lot.ID)
	if !lot.RemainingQuantity.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("lot remaining after reversal: got %s want 10", lot.RemainingQuantity)
	}

	// Consumption row stamped reversed.
	var rows []models.InventoryTrackingConsumption
	db.Where("issue_movement_id = ?", issue.MovementID).Find(&rows)
	if len(rows) != 1 || rows[0].ReversedByMovementID == nil {
		t.Fatalf("reversal should stamp consumption: %+v", rows)
	}
}

// ── Serial issue + reverse ───────────────────────────────────────────────────

// Serial explicit issue: caller names serials, each flips to 'issued',
// one consumption row written per serial.
func TestIssueStock_SerialTracked_ExplicitSelectionHappyPath(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingSerial)

	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(3), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		SerialNumbers: []string{"SN-1", "SN-2", "SN-3"},
	}); err != nil {
		t.Fatalf("receive: %v", err)
	}

	issue, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(2), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 5,
		SerialSelections: []string{"SN-1", "SN-3"},
	})
	if err != nil {
		t.Fatalf("IssueStock: %v", err)
	}

	// SN-1 and SN-3 are issued; SN-2 still on_hand.
	var units []models.InventorySerialUnit
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).
		Order("serial_number asc").Find(&units)
	byName := map[string]models.SerialState{}
	for _, u := range units {
		byName[u.SerialNumber] = u.CurrentState
	}
	if byName["SN-1"] != models.SerialStateIssued {
		t.Fatalf("SN-1 state: got %s want issued", byName["SN-1"])
	}
	if byName["SN-3"] != models.SerialStateIssued {
		t.Fatalf("SN-3 state: got %s want issued", byName["SN-3"])
	}
	if byName["SN-2"] != models.SerialStateOnHand {
		t.Fatalf("SN-2 state: got %s want on_hand (was not issued)", byName["SN-2"])
	}

	// Two consumption rows.
	var rows []models.InventoryTrackingConsumption
	db.Where("issue_movement_id = ?", issue.MovementID).Find(&rows)
	if len(rows) != 2 {
		t.Fatalf("consumption rows: got %d want 2", len(rows))
	}
}

// Issuing a serial that is not on_hand is rejected.
func TestIssueStock_SerialTracked_NotOnHandRejected(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingSerial)

	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(1), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		SerialNumbers: []string{"SN-X"},
	}); err != nil {
		t.Fatalf("receive: %v", err)
	}

	// First issue: OK.
	if _, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(1), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
		SerialSelections: []string{"SN-X"},
	}); err != nil {
		t.Fatalf("first issue: %v", err)
	}

	// Second issue of same serial (now in state=issued) must fail.
	_, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(1), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 2,
		AllowNegative: true,
		SerialSelections: []string{"SN-X"},
	})
	if !errors.Is(err, ErrSerialNotOnHand) {
		t.Fatalf("got %v, want ErrSerialNotOnHand", err)
	}
}

// Issuing a non-existent serial is rejected.
func TestIssueStock_SerialTracked_NotFoundRejected(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingSerial)

	// No seed — no serials exist for this item.
	_, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(1), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
		AllowNegative: true,
		SerialSelections: []string{"SN-NEVER-SEEN"},
	})
	if !errors.Is(err, ErrSerialNotFound) {
		t.Fatalf("got %v, want ErrSerialNotFound", err)
	}
}

// Reversing a serial-tracked issue flips the serial back to on_hand and
// permits a later re-issue.
func TestReverseMovement_SerialTracked_RestoresToOnHand(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingSerial)

	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(1), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		SerialNumbers: []string{"SN-R"},
	}); err != nil {
		t.Fatalf("receive: %v", err)
	}
	issue, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(1), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
		SerialSelections: []string{"SN-R"},
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
		t.Fatalf("reverse: %v", err)
	}

	var unit models.InventorySerialUnit
	db.Where("serial_number = ?", "SN-R").First(&unit)
	if unit.CurrentState != models.SerialStateOnHand {
		t.Fatalf("serial after reverse: got %s want on_hand", unit.CurrentState)
	}

	// Re-issue should now work.
	if _, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(1), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 2,
		SerialSelections: []string{"SN-R"},
	}); err != nil {
		t.Fatalf("re-issue after reverse: %v", err)
	}
}

// Reversing a TRACKED issue whose consumption anchors were never
// written (simulates legacy / data anomaly) must fail with
// ErrTrackingAnchorMissing. This is the F3 correctness gate — same
// stance as E2.1 for FIFO cost layers.
func TestReverseMovement_LotTracked_MissingAnchorRejected(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingLot)

	// Seed a lot and record a movement manually (bypassing IssueStock)
	// so there's no consumption anchor.
	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(10), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		LotNumber: "LOT-LEGACY",
	}); err != nil {
		t.Fatalf("receive: %v", err)
	}
	// Manually craft an outbound movement without consumption rows.
	qty := decimal.NewFromInt(3)
	unit := decimal.NewFromInt(5)
	total := qty.Mul(unit)
	whVal := warehouseID
	legacy := models.InventoryMovement{
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
	db.Create(&legacy)
	// Drain the balance so reversal's on-hand math works cleanly.
	db.Model(&models.InventoryBalance{}).
		Where("company_id = ? AND item_id = ?", companyID, itemID).
		Update("quantity_on_hand", decimal.NewFromInt(7))

	_, err := ReverseMovement(db, ReverseMovementInput{
		CompanyID:          companyID,
		OriginalMovementID: legacy.ID,
		MovementDate:       time.Now(),
		Reason:             ReversalReasonCustomerReturn,
		SourceType:         "invoice_reversal",
		SourceID:           1,
	})
	if !errors.Is(err, ErrTrackingAnchorMissing) {
		t.Fatalf("got %v, want ErrTrackingAnchorMissing", err)
	}
}

// ── Backward compat ──────────────────────────────────────────────────────────

// Untracked item IssueStock still works with empty Lot/Serial selections.
func TestIssueStock_Untracked_WithoutSelections_StillWorks(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db) // tracking=none
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)

	if _, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(3), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
	}); err != nil {
		t.Fatalf("untracked plain issue: %v", err)
	}
}

// Untracked item IssueStock with selections is rejected (defense
// against misconfiguration that would drop provenance).
func TestIssueStock_Untracked_WithSelectionsRejected(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)

	_, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(1), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
		LotSelections: []LotSelection{{LotID: 1, Quantity: decimal.NewFromInt(1)}},
	})
	if !errors.Is(err, ErrTrackingDataOnUntrackedItem) {
		t.Fatalf("got %v, want ErrTrackingDataOnUntrackedItem", err)
	}
}

// Cross-company isolation for lots: company A cannot consume company B's
// lot even if the caller supplies B's lot_id.
func TestIssueStock_LotTracked_CrossCompanyLotRejected(t *testing.T) {
	db := testDB(t)

	// Company A — set up fully.
	companyA, itemA, whA := seedTrackedItem(t, db, models.TrackingLot)
	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyA, ItemID: itemA, WarehouseID: whA,
		Quantity: decimal.NewFromInt(5), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		LotNumber: "LOT-A",
	}); err != nil {
		t.Fatalf("seed A: %v", err)
	}

	// Company B — set up a lot-tracked item + lot of its own.
	c2 := models.Company{Name: "Co2", IsActive: true}
	db.Create(&c2)
	wh2 := models.Warehouse{CompanyID: c2.ID, Name: "Main", IsActive: true}
	db.Create(&wh2)
	item2 := models.ProductService{
		CompanyID: c2.ID, Name: "Widget2",
		Type: models.ProductServiceTypeInventory, IsStockItem: true, IsActive: true,
		TrackingMode: models.TrackingLot,
	}
	db.Create(&item2)
	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: c2.ID, ItemID: item2.ID, WarehouseID: wh2.ID,
		Quantity: decimal.NewFromInt(5), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 10,
		LotNumber: "LOT-B",
	}); err != nil {
		t.Fatalf("seed B: %v", err)
	}
	var lotB models.InventoryLot
	db.Where("company_id = ? AND lot_number = ?", c2.ID, "LOT-B").First(&lotB)

	// Company A attempts to consume company B's lot.
	_, err := IssueStock(db, IssueStockInput{
		CompanyID: companyA, ItemID: itemA, WarehouseID: whA,
		Quantity: decimal.NewFromInt(1), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
		LotSelections: []LotSelection{
			{LotID: lotB.ID, Quantity: decimal.NewFromInt(1)},
		},
	})
	if !errors.Is(err, ErrLotNotFound) {
		t.Fatalf("got %v, want ErrLotNotFound (cross-company isolation)", err)
	}
}
