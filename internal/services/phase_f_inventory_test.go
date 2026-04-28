// 遵循project_guide.md
package services

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testPhaseFDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:phf_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	db.AutoMigrate(
		&models.Company{},
		&models.Account{},
		&models.ProductService{},
		&models.Warehouse{},
		&models.InventoryMovement{},
		&models.InventoryBalance{},
		&models.InventoryCostLayer{},
		&models.InventoryLayerConsumption{},
	)
	return db
}

type phaseFSetup struct {
	companyID   uint
	stockItemID uint
	whAID       uint
	whBID       uint
}

func setupPhaseF(t *testing.T, db *gorm.DB) phaseFSetup {
	t.Helper()

	co := models.Company{Name: "PhaseF Co", IsActive: true}
	db.Create(&co)

	cogsAcct := models.Account{CompanyID: co.ID, Code: "5000", Name: "COGS",
		RootAccountType: models.RootCostOfSales, DetailAccountType: models.DetailCostOfGoodsSold, IsActive: true}
	db.Create(&cogsAcct)
	invAcct := models.Account{CompanyID: co.ID, Code: "1300", Name: "Inventory",
		RootAccountType: models.RootAsset, DetailAccountType: models.DetailInventory, IsActive: true}
	db.Create(&invAcct)

	item := models.ProductService{
		CompanyID: co.ID, Name: "Gadget", Type: models.ProductServiceTypeInventory,
		RevenueAccountID: 1, COGSAccountID: &cogsAcct.ID, InventoryAccountID: &invAcct.ID,
		IsActive: true,
	}
	item.ApplyTypeDefaults()
	db.Create(&item)

	whA := models.Warehouse{CompanyID: co.ID, Code: "WH-A", Name: "Warehouse A", IsDefault: true, IsActive: true}
	db.Create(&whA)
	whB := models.Warehouse{CompanyID: co.ID, Code: "WH-B", Name: "Warehouse B", IsDefault: false, IsActive: true}
	db.Create(&whB)

	return phaseFSetup{
		companyID:   co.ID,
		stockItemID: item.ID,
		whAID:       whA.ID,
		whBID:       whB.ID,
	}
}

// TestMovementRow_IncludesWarehouseCode verifies that ListMovements populates
// WarehouseCode and WarehouseName for warehouse-routed movements.
func TestMovementRow_IncludesWarehouseCode(t *testing.T) {
	db := testPhaseFDB(t)
	s := setupPhaseF(t, db)

	qty5 := decimal.NewFromInt(5)

	// Insert a warehouse-routed movement directly.
	whaMov := models.InventoryMovement{
		CompanyID:     s.companyID,
		ItemID:        s.stockItemID,
		MovementType:  models.MovementTypePurchase,
		QuantityDelta: decimal.NewFromInt(10),
		UnitCost:      &qty5,
		WarehouseID:   &s.whAID,
		SourceType:    "bill",
		MovementDate:  time.Now(),
	}
	if err := db.Create(&whaMov).Error; err != nil {
		t.Fatalf("Create WH-A movement: %v", err)
	}

	// Insert a legacy movement (no warehouse).
	legacyMov := models.InventoryMovement{
		CompanyID:     s.companyID,
		ItemID:        s.stockItemID,
		MovementType:  models.MovementTypePurchase,
		QuantityDelta: decimal.NewFromInt(20),
		UnitCost:      &qty5,
		WarehouseID:   nil,
		SourceType:    "bill",
		MovementDate:  time.Now(),
	}
	if err := db.Create(&legacyMov).Error; err != nil {
		t.Fatalf("Create legacy movement: %v", err)
	}

	rows, total, err := ListMovements(db, s.companyID, s.stockItemID, 50, 0)
	if err != nil {
		t.Fatalf("ListMovements failed: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected 2 movements, got %d", total)
	}

	// Find the warehouse-routed movement.
	var whRow, legacyRow *MovementRow
	for i := range rows {
		if rows[i].WarehouseCode != "" {
			whRow = &rows[i]
		} else {
			legacyRow = &rows[i]
		}
	}

	if whRow == nil {
		t.Fatal("No movement row with WarehouseCode set")
	}
	if whRow.WarehouseCode != "WH-A" {
		t.Errorf("Expected WarehouseCode WH-A, got %q", whRow.WarehouseCode)
	}
	if whRow.WarehouseName != "Warehouse A" {
		t.Errorf("Expected WarehouseName 'Warehouse A', got %q", whRow.WarehouseName)
	}
	if legacyRow == nil {
		t.Fatal("No movement row with empty WarehouseCode (legacy)")
	}
	if legacyRow.WarehouseCode != "" {
		t.Errorf("Legacy movement should have empty WarehouseCode, got %q", legacyRow.WarehouseCode)
	}
}

// TestSnapshotWarehouseBreakdown verifies that GetInventorySnapshot returns
// per-warehouse balances in WarehouseBreakdown.
func TestSnapshotWarehouseBreakdown(t *testing.T) {
	db := testPhaseFDB(t)
	s := setupPhaseF(t, db)

	engine, _ := ResolveCostingEngineForCompany(db, s.companyID)

	// Inbound 10 units @ $5 to WH-A, 30 units @ $3 to WH-B.
	engine.ApplyInbound(db, InboundRequest{
		CompanyID:    s.companyID,
		ItemID:       s.stockItemID,
		Quantity:     decimal.NewFromInt(10),
		UnitCost:     decimal.NewFromInt(5),
		MovementType: models.MovementTypePurchase,
		WarehouseID:  &s.whAID,
		Date:         time.Now(),
	})
	engine.ApplyInbound(db, InboundRequest{
		CompanyID:    s.companyID,
		ItemID:       s.stockItemID,
		Quantity:     decimal.NewFromInt(30),
		UnitCost:     decimal.NewFromInt(3),
		MovementType: models.MovementTypePurchase,
		WarehouseID:  &s.whBID,
		Date:         time.Now(),
	})

	snap, err := GetInventorySnapshot(db, s.companyID, s.stockItemID)
	if err != nil {
		t.Fatalf("GetInventorySnapshot failed: %v", err)
	}

	if len(snap.WarehouseBreakdown) != 2 {
		t.Fatalf("Expected 2 warehouse breakdown rows, got %d", len(snap.WarehouseBreakdown))
	}

	byCode := make(map[string]WarehouseBalance)
	for _, wb := range snap.WarehouseBreakdown {
		byCode[wb.WarehouseCode] = wb
	}

	whA, ok := byCode["WH-A"]
	if !ok {
		t.Fatal("WH-A not in breakdown")
	}
	if !whA.QtyOnHand.Equal(decimal.NewFromInt(10)) {
		t.Errorf("WH-A qty expected 10, got %s", whA.QtyOnHand)
	}
	if !whA.Value.Equal(decimal.NewFromInt(50)) {
		t.Errorf("WH-A value expected 50, got %s", whA.Value)
	}

	whB, ok := byCode["WH-B"]
	if !ok {
		t.Fatal("WH-B not in breakdown")
	}
	if !whB.QtyOnHand.Equal(decimal.NewFromInt(30)) {
		t.Errorf("WH-B qty expected 30, got %s", whB.QtyOnHand)
	}
	if !whB.Value.Equal(decimal.NewFromInt(90)) {
		t.Errorf("WH-B value expected 90, got %s", whB.Value)
	}
}

// TestSnapshotWarehouseBreakdown_EmptyWhenNoWarehouses verifies that
// WarehouseBreakdown is empty for legacy (non-warehouse-routed) stock.
func TestSnapshotWarehouseBreakdown_EmptyWhenNoWarehouses(t *testing.T) {
	db := testPhaseFDB(t)
	s := setupPhaseF(t, db)

	engine, _ := ResolveCostingEngineForCompany(db, s.companyID)

	// Legacy inbound with no warehouse.
	engine.ApplyInbound(db, InboundRequest{
		CompanyID:    s.companyID,
		ItemID:       s.stockItemID,
		Quantity:     decimal.NewFromInt(15),
		UnitCost:     decimal.NewFromInt(8),
		MovementType: models.MovementTypePurchase,
		WarehouseID:  nil,
		Date:         time.Now(),
	})

	snap, err := GetInventorySnapshot(db, s.companyID, s.stockItemID)
	if err != nil {
		t.Fatalf("GetInventorySnapshot failed: %v", err)
	}

	if len(snap.WarehouseBreakdown) != 0 {
		t.Errorf("Expected empty WarehouseBreakdown for legacy stock, got %d rows", len(snap.WarehouseBreakdown))
	}
}
