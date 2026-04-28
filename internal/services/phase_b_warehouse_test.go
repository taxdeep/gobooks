// 遵循project_guide.md
package services

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ── Test DB setup ─────────────────────────────────────────────────────────────

func testWarehouseDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:wh_%s?mode=memory&cache=shared", t.Name())
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

func seedWarehouseCompany(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	c := models.Company{Name: "WH Test Co", IsActive: true}
	db.Create(&c)
	return c.ID
}

func seedWarehouseItem(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	cogsAcct := models.Account{CompanyID: companyID, Code: "5000", Name: "COGS",
		RootAccountType: models.RootCostOfSales, DetailAccountType: models.DetailCostOfGoodsSold, IsActive: true}
	db.Create(&cogsAcct)
	invAcct := models.Account{CompanyID: companyID, Code: "1300", Name: "Inventory",
		RootAccountType: models.RootAsset, DetailAccountType: models.DetailInventory, IsActive: true}
	db.Create(&invAcct)

	item := models.ProductService{
		CompanyID: companyID, Name: "Part", Type: models.ProductServiceTypeInventory,
		COGSAccountID: &cogsAcct.ID, InventoryAccountID: &invAcct.ID, IsActive: true,
	}
	item.ApplyTypeDefaults()
	db.Create(&item)
	return item.ID
}

// ── Warehouse CRUD ────────────────────────────────────────────────────────────

func TestWarehouse_CreateAndGet(t *testing.T) {
	db := testWarehouseDB(t)
	cid := seedWarehouseCompany(t, db)

	w, err := CreateWarehouse(db, cid, WarehouseInput{
		Code: "WH1", Name: "Warehouse One", IsDefault: false, IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateWarehouse: %v", err)
	}
	if w.ID == 0 {
		t.Fatal("expected non-zero ID")
	}

	got, err := GetWarehouse(db, cid, w.ID)
	if err != nil {
		t.Fatalf("GetWarehouse: %v", err)
	}
	if got.Code != "WH1" {
		t.Errorf("Code = %q, want %q", got.Code, "WH1")
	}
}

func TestWarehouse_DuplicateCodeRejected(t *testing.T) {
	db := testWarehouseDB(t)
	cid := seedWarehouseCompany(t, db)

	CreateWarehouse(db, cid, WarehouseInput{Code: "WH1", Name: "First", IsActive: true})
	_, err := CreateWarehouse(db, cid, WarehouseInput{Code: "WH1", Name: "Second", IsActive: true})
	if err == nil {
		t.Fatal("expected error for duplicate code")
	}
}

func TestWarehouse_DuplicateCodeAllowedAcrossCompanies(t *testing.T) {
	db := testWarehouseDB(t)
	cid1 := seedWarehouseCompany(t, db)
	c2 := models.Company{Name: "Other Co", IsActive: true}
	db.Create(&c2)

	_, err := CreateWarehouse(db, cid1, WarehouseInput{Code: "MAIN", Name: "Main", IsDefault: true, IsActive: true})
	if err != nil {
		t.Fatalf("first company: %v", err)
	}
	_, err = CreateWarehouse(db, c2.ID, WarehouseInput{Code: "MAIN", Name: "Main", IsDefault: true, IsActive: true})
	if err != nil {
		t.Fatalf("second company should allow same code: %v", err)
	}
}

func TestWarehouse_DefaultFlagTransfer(t *testing.T) {
	db := testWarehouseDB(t)
	cid := seedWarehouseCompany(t, db)

	w1, _ := CreateWarehouse(db, cid, WarehouseInput{Code: "WH1", Name: "First", IsDefault: true, IsActive: true})
	w2, _ := CreateWarehouse(db, cid, WarehouseInput{Code: "WH2", Name: "Second", IsDefault: true, IsActive: true})

	// w1 should now have IsDefault=false.
	got1, _ := GetWarehouse(db, cid, w1.ID)
	if got1.IsDefault {
		t.Error("WH1 should no longer be default after WH2 took default flag")
	}
	got2, _ := GetWarehouse(db, cid, w2.ID)
	if !got2.IsDefault {
		t.Error("WH2 should be default")
	}
}

func TestWarehouse_EnsureDefaultIdempotent(t *testing.T) {
	db := testWarehouseDB(t)
	cid := seedWarehouseCompany(t, db)

	id1, err := EnsureDefaultWarehouse(db, cid)
	if err != nil || id1 == 0 {
		t.Fatalf("first call: %v", err)
	}
	id2, err := EnsureDefaultWarehouse(db, cid)
	if err != nil || id2 == 0 {
		t.Fatalf("second call: %v", err)
	}
	if id1 != id2 {
		t.Errorf("idempotency failed: id1=%d id2=%d", id1, id2)
	}

	// Only one warehouse should exist.
	ws, _ := ListWarehouses(db, cid)
	if len(ws) != 1 {
		t.Errorf("expected 1 warehouse, got %d", len(ws))
	}
}

func TestWarehouse_CannotDeactivateDefault(t *testing.T) {
	db := testWarehouseDB(t)
	cid := seedWarehouseCompany(t, db)

	w, _ := CreateWarehouse(db, cid, WarehouseInput{Code: "MAIN", Name: "Main", IsDefault: true, IsActive: true})
	_, err := UpdateWarehouse(db, cid, w.ID, WarehouseInput{
		Code: "MAIN", Name: "Main", IsDefault: true, IsActive: false,
	})
	if err == nil {
		t.Fatal("expected error deactivating default warehouse")
	}
}

// ── Multi-warehouse costing ───────────────────────────────────────────────────

func TestWarehouse_CostingEngineWarehouseRouting(t *testing.T) {
	db := testWarehouseDB(t)
	cid := seedWarehouseCompany(t, db)
	itemID := seedWarehouseItem(t, db, cid)

	w1, _ := CreateWarehouse(db, cid, WarehouseInput{Code: "WH1", Name: "Warehouse 1", IsActive: true})
	w2, _ := CreateWarehouse(db, cid, WarehouseInput{Code: "WH2", Name: "Warehouse 2", IsActive: true})

	engine := &MovingAverageCostingEngine{}

	// Receive 10 units @ $5 into WH1.
	_, err := engine.ApplyInbound(db, InboundRequest{
		CompanyID:    cid,
		ItemID:       itemID,
		Quantity:     decimal.NewFromInt(10),
		UnitCost:     decimal.NewFromInt(5),
		MovementType: models.MovementTypePurchase,
		WarehouseID:  &w1.ID,
	})
	if err != nil {
		t.Fatalf("inbound WH1: %v", err)
	}

	// Receive 20 units @ $8 into WH2.
	_, err = engine.ApplyInbound(db, InboundRequest{
		CompanyID:    cid,
		ItemID:       itemID,
		Quantity:     decimal.NewFromInt(20),
		UnitCost:     decimal.NewFromInt(8),
		MovementType: models.MovementTypePurchase,
		WarehouseID:  &w2.ID,
	})
	if err != nil {
		t.Fatalf("inbound WH2: %v", err)
	}

	// WH1 should have 10 units @ $5.
	result1, err := engine.PreviewOutbound(db, OutboundRequest{
		CompanyID:    cid,
		ItemID:       itemID,
		Quantity:     decimal.NewFromInt(10),
		MovementType: models.MovementTypeSale,
		WarehouseID:  &w1.ID,
	})
	if err != nil {
		t.Fatalf("preview WH1: %v", err)
	}
	if !result1.UnitCostUsed.Equal(decimal.NewFromInt(5)) {
		t.Errorf("WH1 unit cost = %s, want 5", result1.UnitCostUsed)
	}

	// WH2 should have 20 units @ $8.
	result2, err := engine.PreviewOutbound(db, OutboundRequest{
		CompanyID:    cid,
		ItemID:       itemID,
		Quantity:     decimal.NewFromInt(20),
		MovementType: models.MovementTypeSale,
		WarehouseID:  &w2.ID,
	})
	if err != nil {
		t.Fatalf("preview WH2: %v", err)
	}
	if !result2.UnitCostUsed.Equal(decimal.NewFromInt(8)) {
		t.Errorf("WH2 unit cost = %s, want 8", result2.UnitCostUsed)
	}
}

func TestWarehouse_InsufficientStockPerWarehouse(t *testing.T) {
	db := testWarehouseDB(t)
	cid := seedWarehouseCompany(t, db)
	itemID := seedWarehouseItem(t, db, cid)

	w1, _ := CreateWarehouse(db, cid, WarehouseInput{Code: "WH1", Name: "WH1", IsActive: true})
	w2, _ := CreateWarehouse(db, cid, WarehouseInput{Code: "WH2", Name: "WH2", IsActive: true})

	engine := &MovingAverageCostingEngine{}

	// 5 units into WH1 only.
	engine.ApplyInbound(db, InboundRequest{
		CompanyID: cid, ItemID: itemID,
		Quantity: decimal.NewFromInt(5), UnitCost: decimal.NewFromInt(10),
		MovementType: models.MovementTypePurchase,
		WarehouseID:  &w1.ID,
	})

	// Selling 6 from WH1 should fail.
	_, err := engine.PreviewOutbound(db, OutboundRequest{
		CompanyID: cid, ItemID: itemID,
		Quantity: decimal.NewFromInt(6), MovementType: models.MovementTypeSale,
		WarehouseID: &w1.ID,
	})
	if err == nil {
		t.Fatal("expected insufficient stock error for WH1")
	}

	// Selling any from WH2 (zero balance) should also fail.
	_, err = engine.PreviewOutbound(db, OutboundRequest{
		CompanyID: cid, ItemID: itemID,
		Quantity: decimal.NewFromInt(1), MovementType: models.MovementTypeSale,
		WarehouseID: &w2.ID,
	})
	if err == nil {
		t.Fatal("expected insufficient stock error for WH2")
	}
}

func TestWarehouse_LegacyLocationPathUnchanged(t *testing.T) {
	db := testWarehouseDB(t)
	cid := seedWarehouseCompany(t, db)
	itemID := seedWarehouseItem(t, db, cid)

	engine := &MovingAverageCostingEngine{}

	// Receive via legacy path (nil WarehouseID).
	_, err := engine.ApplyInbound(db, InboundRequest{
		CompanyID:    cid,
		ItemID:       itemID,
		Quantity:     decimal.NewFromInt(15),
		UnitCost:     decimal.NewFromInt(4),
		MovementType: models.MovementTypePurchase,
		LocationType: models.LocationTypeInternal,
	})
	if err != nil {
		t.Fatalf("legacy inbound: %v", err)
	}

	result, err := engine.PreviewOutbound(db, OutboundRequest{
		CompanyID:    cid,
		ItemID:       itemID,
		Quantity:     decimal.NewFromInt(15),
		MovementType: models.MovementTypeSale,
		LocationType: models.LocationTypeInternal,
	})
	if err != nil {
		t.Fatalf("legacy preview: %v", err)
	}
	if !result.UnitCostUsed.Equal(decimal.NewFromInt(4)) {
		t.Errorf("legacy unit cost = %s, want 4", result.UnitCostUsed)
	}
}
