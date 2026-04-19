// 遵循project_guide.md
package inventory

// tracking_integration_test.go — Phase F5 closeout tests. Verifies the
// cross-cutting guardrails we rely on but that aren't obvious from any
// single slice's tests.

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"gobooks/internal/models"
)

// A tracked TransferStock fails at the IssueStock leg because the
// transfer code does not populate tracking selections. This is the
// correct, guarded behaviour — tracked transfers must be driven at a
// higher layer that knows which lots/serials to move. First-class
// tracked transfer support is a future slice.
func TestTransferStock_OnLotTrackedItem_FailsWithoutSelections(t *testing.T) {
	db := testDB(t)
	companyID, itemID, srcID := seedTrackedItem(t, db, models.TrackingLot)

	dst := models.Warehouse{CompanyID: companyID, Name: "Dest", IsActive: true}
	db.Create(&dst)

	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: srcID,
		Quantity: decimal.NewFromInt(5), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		LotNumber: "LOT-MOVE",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	now := time.Now()
	_, err := TransferStock(db, TransferStockInput{
		CompanyID:       companyID,
		TransferID:      1,
		ItemID:          itemID,
		FromWarehouseID: srcID,
		ToWarehouseID:   dst.ID,
		Quantity:        decimal.NewFromInt(3),
		ShippedDate:     now,
		ReceivedDate:    &now,
	})
	if !errors.Is(err, ErrLotSelectionMissing) {
		t.Fatalf("tracked transfer should reject without selections: got %v", err)
	}
}

// Tracked components in a PostInventoryBuild call are the policy-locked
// equivalent of tracked transfers: the orchestrator calls IssueStock +
// ReceiveStock without tracking selections, which makes tracked items
// fail loud at the IN-verb layer. This is a deliberate guard, not a
// transient "TODO" — first-class tracked build is a future scheduled
// slice (see §7 "Phase F post-decision upgrade anchors"). Removing the
// guard would silently drop serial / lot identity on assembly lines.
func TestPostInventoryBuild_TrackedComponent_RejectsWithoutSelections(t *testing.T) {
	db := testDB(t)
	companyID, _, warehouseID := seedTestFixture(t, db)
	// item_components is not in the default test migration set.
	if err := db.AutoMigrate(&models.ItemComponent{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}

	parent := models.ProductService{
		CompanyID: companyID, Name: "ParentAssy",
		Type: models.ProductServiceTypeInventory, IsStockItem: true, IsActive: true,
	}
	db.Create(&parent)

	// Serial-tracked component.
	comp := models.ProductService{
		CompanyID: companyID, Name: "SerialPart",
		Type: models.ProductServiceTypeInventory, IsStockItem: true, IsActive: true,
		TrackingMode: models.TrackingSerial,
	}
	db.Create(&comp)
	db.Create(&models.ItemComponent{
		CompanyID: companyID, ParentItemID: parent.ID,
		ComponentItemID: comp.ID, Quantity: decimal.NewFromInt(1),
	})
	// Seed the component's serial stock.
	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: comp.ID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(1), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		SerialNumbers: []string{"SN-PART"},
	}); err != nil {
		t.Fatalf("seed serial part: %v", err)
	}

	_, err := PostInventoryBuild(db, PostInventoryBuildInput{
		CompanyID: companyID, ParentItemID: parent.ID,
		WarehouseID: warehouseID,
		Quantity:    decimal.NewFromInt(1),
		BuildDate:   time.Now(),
		BuildRef:    1,
	})
	// The IssueStock for the tracked component has no SerialSelections.
	// Expected error: ErrSerialSelectionMissing bubbling up from the
	// consume leg.
	if !errors.Is(err, ErrSerialSelectionMissing) {
		t.Fatalf("tracked build should reject without serial selection: got %v", err)
	}
}

// Sanity check: tracked stock issuance and costing are orthogonal.
// Issue a lot-tracked item on a FIFO company; verify the cost comes
// from the FIFO layer (not the lot's own cost) and the lot.remaining
// decrements independently. This guards against the "lot-as-cost-layer"
// temptation — tracking truth never flows into cost truth.
func TestTracking_CostingOrthogonality_FIFOCompanyLotTracked(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTrackedItem(t, db, models.TrackingLot)
	// Flip company to FIFO so the costing path exercises layers.
	db.Model(&models.Company{}).Where("id = ?", companyID).
		Update("inventory_costing_method", models.InventoryCostingFIFO)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Two receipts, same lot number, DIFFERENT unit cost for the FIFO
	// side. The layer table sees two rows (FIFO tracks cost per receipt),
	// while the lot table sees one lot with top-up.
	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(10), MovementDate: base,
		UnitCost: decimal.NewFromInt(3), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1,
		LotNumber: "LOT-Z",
	}); err != nil {
		t.Fatalf("receive 1: %v", err)
	}
	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(10), MovementDate: base.AddDate(0, 0, 1),
		UnitCost: decimal.NewFromInt(7), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 2,
		LotNumber: "LOT-Z",
	}); err != nil {
		t.Fatalf("receive 2: %v", err)
	}

	var lot models.InventoryLot
	db.Where("lot_number = ?", "LOT-Z").First(&lot)
	if !lot.RemainingQuantity.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("lot top-up: got %s want 20", lot.RemainingQuantity)
	}

	// FIFO issue 12 from lot Z. FIFO draws from layer 1 ($3 × 10) +
	// layer 2 ($7 × 2) = $30 + $14 = $44; blended unit = $3.6667.
	result, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(12), MovementDate: base.AddDate(0, 0, 2),
		SourceType: "invoice", SourceID: 10,
		CostingMethod: CostingMethodFIFO,
		LotSelections: []LotSelection{{LotID: lot.ID, Quantity: decimal.NewFromInt(12)}},
	})
	if err != nil {
		t.Fatalf("IssueStock: %v", err)
	}

	// Cost truth: from FIFO layers.
	if !result.CostOfIssueBase.Equal(decimal.NewFromInt(44)) {
		t.Fatalf("cost: got %s want 44 (FIFO-driven, NOT from lot)", result.CostOfIssueBase)
	}
	if len(result.CostLayers) != 2 {
		t.Fatalf("cost layers touched: got %d want 2", len(result.CostLayers))
	}

	// Tracking truth: lot.remaining decreased by 12.
	db.First(&lot, lot.ID)
	if !lot.RemainingQuantity.Equal(decimal.NewFromInt(8)) {
		t.Fatalf("lot remaining: got %s want 8", lot.RemainingQuantity)
	}

	// The two truths are INDEPENDENT — a tracking consumption row and a
	// FIFO layer consumption row coexist with different granularity.
	var trackingRows int64
	db.Model(&models.InventoryTrackingConsumption{}).
		Where("issue_movement_id = ?", result.MovementID).
		Count(&trackingRows)
	if trackingRows != 1 {
		t.Fatalf("tracking rows: got %d want 1 (one lot consumed)", trackingRows)
	}
	var layerRows int64
	db.Model(&models.InventoryLayerConsumption{}).
		Where("issue_movement_id = ?", result.MovementID).
		Count(&layerRows)
	if layerRows != 2 {
		t.Fatalf("layer consumption rows: got %d want 2 (two FIFO layers)", layerRows)
	}
}
