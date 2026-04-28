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

func testPhaseEDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:phe_%s?mode=memory&cache=shared", t.Name())
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

type phaseESetup struct {
	companyID   uint
	stockItemID uint
	whAID       uint
	whBID       uint
}

func setupPhaseE(t *testing.T, db *gorm.DB) phaseESetup {
	t.Helper()

	co := models.Company{Name: "PhaseE Co", IsActive: true}
	db.Create(&co)

	cogsAcct := models.Account{CompanyID: co.ID, Code: "5000", Name: "COGS",
		RootAccountType: models.RootCostOfSales, DetailAccountType: models.DetailCostOfGoodsSold, IsActive: true}
	db.Create(&cogsAcct)
	invAcct := models.Account{CompanyID: co.ID, Code: "1300", Name: "Inventory",
		RootAccountType: models.RootAsset, DetailAccountType: models.DetailInventory, IsActive: true}
	db.Create(&invAcct)

	item := models.ProductService{
		CompanyID: co.ID, Name: "Widget", Type: models.ProductServiceTypeInventory,
		RevenueAccountID: 1, COGSAccountID: &cogsAcct.ID, InventoryAccountID: &invAcct.ID,
		IsActive: true,
	}
	item.ApplyTypeDefaults()
	db.Create(&item)

	whA := models.Warehouse{CompanyID: co.ID, Code: "WH-A", Name: "Warehouse A", IsDefault: false, IsActive: true}
	db.Create(&whA)
	whB := models.Warehouse{CompanyID: co.ID, Code: "WH-B", Name: "Warehouse B", IsDefault: false, IsActive: true}
	db.Create(&whB)

	return phaseESetup{
		companyID:   co.ID,
		stockItemID: item.ID,
		whAID:       whA.ID,
		whBID:       whB.ID,
	}
}

// ── Opening balance ──────────────────────────────────────────────────────────

func TestOpeningBalance_RoutesToWarehouse(t *testing.T) {
	db := testPhaseEDB(t)
	s := setupPhaseE(t, db)

	_, err := CreateOpeningBalance(db, OpeningBalanceInput{
		CompanyID:   s.companyID,
		ItemID:      s.stockItemID,
		Quantity:    decimal.NewFromInt(30),
		UnitCost:    decimal.NewFromInt(10),
		AsOfDate:    time.Now(),
		WarehouseID: &s.whAID,
	})
	if err != nil {
		t.Fatalf("CreateOpeningBalance failed: %v", err)
	}

	// Balance should exist on WH-A.
	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?", s.companyID, s.stockItemID, s.whAID).First(&bal)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(30)) {
		t.Errorf("WH-A balance expected 30, got %s", bal.QuantityOnHand)
	}

	// WH-B should have no balance.
	var whBBal models.InventoryBalance
	res := db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?", s.companyID, s.stockItemID, s.whBID).First(&whBBal)
	if res.Error == nil {
		t.Errorf("WH-B should have no balance, got %s", whBBal.QuantityOnHand)
	}

	// Movement stamped with WH-A.
	var mov models.InventoryMovement
	db.Where("company_id = ? AND item_id = ? AND movement_type = ?", s.companyID, s.stockItemID, models.MovementTypeOpening).First(&mov)
	if mov.WarehouseID == nil || *mov.WarehouseID != s.whAID {
		t.Errorf("Movement warehouse expected WH-A (%d), got %v", s.whAID, mov.WarehouseID)
	}
}

func TestOpeningBalance_DuplicatePerWarehouseBlocked(t *testing.T) {
	db := testPhaseEDB(t)
	s := setupPhaseE(t, db)

	in := OpeningBalanceInput{
		CompanyID: s.companyID, ItemID: s.stockItemID,
		Quantity: decimal.NewFromInt(10), UnitCost: decimal.NewFromInt(5),
		AsOfDate: time.Now(), WarehouseID: &s.whAID,
	}
	if _, err := CreateOpeningBalance(db, in); err != nil {
		t.Fatalf("first CreateOpeningBalance failed: %v", err)
	}
	// Second opening in the same warehouse must fail.
	if _, err := CreateOpeningBalance(db, in); err == nil {
		t.Error("Expected ErrOpeningExists for duplicate warehouse opening")
	}
}

func TestOpeningBalance_DifferentWarehousesAllowed(t *testing.T) {
	db := testPhaseEDB(t)
	s := setupPhaseE(t, db)

	// WH-A
	_, err := CreateOpeningBalance(db, OpeningBalanceInput{
		CompanyID: s.companyID, ItemID: s.stockItemID,
		Quantity: decimal.NewFromInt(10), UnitCost: decimal.NewFromInt(5),
		AsOfDate: time.Now(), WarehouseID: &s.whAID,
	})
	if err != nil {
		t.Fatalf("WH-A opening failed: %v", err)
	}
	// WH-B — different warehouse, should succeed.
	_, err = CreateOpeningBalance(db, OpeningBalanceInput{
		CompanyID: s.companyID, ItemID: s.stockItemID,
		Quantity: decimal.NewFromInt(20), UnitCost: decimal.NewFromInt(5),
		AsOfDate: time.Now(), WarehouseID: &s.whBID,
	})
	if err != nil {
		t.Fatalf("WH-B opening failed (should be allowed): %v", err)
	}

	var countBal int64
	db.Model(&models.InventoryBalance{}).
		Where("company_id = ? AND item_id = ?", s.companyID, s.stockItemID).
		Count(&countBal)
	if countBal != 2 {
		t.Errorf("Expected 2 balance rows (one per warehouse), got %d", countBal)
	}
}

// ── Adjustment ───────────────────────────────────────────────────────────────

func TestAdjustment_RoutesToWarehouse(t *testing.T) {
	db := testPhaseEDB(t)
	s := setupPhaseE(t, db)

	// Seed WH-A with 50 units.
	engine, _ := ResolveCostingEngineForCompany(db, s.companyID)
	engine.ApplyInbound(db, InboundRequest{
		CompanyID:    s.companyID,
		ItemID:       s.stockItemID,
		Quantity:     decimal.NewFromInt(50),
		UnitCost:     decimal.NewFromInt(10),
		MovementType: models.MovementTypePurchase,
		WarehouseID:  &s.whAID,
		Date:         time.Now(),
	})

	// Positive adjustment +10 into WH-B.
	_, err := CreateAdjustment(db, AdjustmentInput{
		CompanyID:     s.companyID,
		ItemID:        s.stockItemID,
		QuantityDelta: decimal.NewFromInt(10),
		MovementDate:  time.Now(),
		WarehouseID:   &s.whBID,
	})
	if err != nil {
		t.Fatalf("CreateAdjustment (positive, WH-B) failed: %v", err)
	}

	var whBBal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?", s.companyID, s.stockItemID, s.whBID).First(&whBBal)
	if !whBBal.QuantityOnHand.Equal(decimal.NewFromInt(10)) {
		t.Errorf("WH-B balance expected 10, got %s", whBBal.QuantityOnHand)
	}

	// Negative adjustment -5 from WH-A.
	_, err = CreateAdjustment(db, AdjustmentInput{
		CompanyID:     s.companyID,
		ItemID:        s.stockItemID,
		QuantityDelta: decimal.NewFromInt(-5),
		MovementDate:  time.Now(),
		WarehouseID:   &s.whAID,
	})
	if err != nil {
		t.Fatalf("CreateAdjustment (negative, WH-A) failed: %v", err)
	}

	var whABal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?", s.companyID, s.stockItemID, s.whAID).First(&whABal)
	if !whABal.QuantityOnHand.Equal(decimal.NewFromInt(45)) {
		t.Errorf("WH-A balance expected 45, got %s", whABal.QuantityOnHand)
	}

	// WH-A should be unaffected by the WH-B positive adjustment.
	if !whABal.QuantityOnHand.Equal(decimal.NewFromInt(45)) {
		t.Errorf("WH-A should not be affected by WH-B adjustment")
	}
}

func TestAdjustment_NegativeBlockedWhenInsufficientInWarehouse(t *testing.T) {
	db := testPhaseEDB(t)
	s := setupPhaseE(t, db)

	// Seed only WH-B with 5 units; WH-A is empty.
	engine, _ := ResolveCostingEngineForCompany(db, s.companyID)
	engine.ApplyInbound(db, InboundRequest{
		CompanyID:    s.companyID,
		ItemID:       s.stockItemID,
		Quantity:     decimal.NewFromInt(5),
		UnitCost:     decimal.NewFromInt(10),
		MovementType: models.MovementTypePurchase,
		WarehouseID:  &s.whBID,
		Date:         time.Now(),
	})

	// Try to adjust -10 from WH-A (empty) — must fail.
	_, err := CreateAdjustment(db, AdjustmentInput{
		CompanyID:     s.companyID,
		ItemID:        s.stockItemID,
		QuantityDelta: decimal.NewFromInt(-10),
		MovementDate:  time.Now(),
		WarehouseID:   &s.whAID,
	})
	if err == nil {
		t.Error("Expected insufficient stock error for WH-A")
	}
}
