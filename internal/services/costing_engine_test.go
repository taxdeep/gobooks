// 遵循project_guide.md
package services

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gobooks/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testCostingDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:costing_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	db.AutoMigrate(
		&models.Company{},
		&models.Account{},
		&models.ProductService{},
		&models.InventoryMovement{},
		&models.InventoryBalance{},
		&models.InventoryCostLayer{},
		&models.InventoryLayerConsumption{},
	)
	return db
}

func seedCostingItem(t *testing.T, db *gorm.DB) (companyID, itemID uint) {
	t.Helper()
	co := models.Company{Name: "Test", IsActive: true, InventoryCostingMethod: "moving_average"}
	db.Create(&co)
	acct := models.Account{CompanyID: co.ID, Code: "4000", Name: "Rev", RootAccountType: models.RootRevenue, DetailAccountType: "revenue", IsActive: true}
	db.Create(&acct)
	item := models.ProductService{CompanyID: co.ID, Name: "Widget", Type: models.ProductServiceTypeInventory, RevenueAccountID: acct.ID, IsActive: true}
	item.ApplyTypeDefaults()
	db.Create(&item)
	return co.ID, item.ID
}

// ── MovingAverage inbound tests ──────────────────────────────────────────────

func TestMA_InboundFromZero(t *testing.T) {
	db := testCostingDB(t)
	companyID, itemID := seedCostingItem(t, db)
	engine := &MovingAverageCostingEngine{}

	result, err := engine.ApplyInbound(db, InboundRequest{
		CompanyID: companyID, ItemID: itemID,
		Quantity: decimal.NewFromInt(100), UnitCost: decimal.NewFromFloat(10.50),
		MovementType: models.MovementTypeOpening, LocationType: models.LocationTypeInternal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.NewQuantityOnHand.Equal(decimal.NewFromInt(100)) {
		t.Errorf("qty expected 100, got %s", result.NewQuantityOnHand)
	}
	if !result.NewAverageCost.Equal(decimal.NewFromFloat(10.50)) {
		t.Errorf("avg expected 10.50, got %s", result.NewAverageCost)
	}
}

func TestMA_InboundUpdatesAvg(t *testing.T) {
	db := testCostingDB(t)
	companyID, itemID := seedCostingItem(t, db)
	engine := &MovingAverageCostingEngine{}

	// First inbound: 10 @ $20
	engine.ApplyInbound(db, InboundRequest{
		CompanyID: companyID, ItemID: itemID,
		Quantity: decimal.NewFromInt(10), UnitCost: decimal.NewFromInt(20),
		MovementType: models.MovementTypeOpening, LocationType: models.LocationTypeInternal,
	})

	// Second inbound: 20 @ $35
	result, err := engine.ApplyInbound(db, InboundRequest{
		CompanyID: companyID, ItemID: itemID,
		Quantity: decimal.NewFromInt(20), UnitCost: decimal.NewFromInt(35),
		MovementType: models.MovementTypePurchase, LocationType: models.LocationTypeInternal,
	})
	if err != nil {
		t.Fatal(err)
	}

	// (10×20 + 20×35) / 30 = 900/30 = 30
	expectedAvg := decimal.NewFromInt(30)
	if !result.NewAverageCost.Equal(expectedAvg) {
		t.Errorf("avg expected %s, got %s", expectedAvg, result.NewAverageCost)
	}
	if !result.NewQuantityOnHand.Equal(decimal.NewFromInt(30)) {
		t.Errorf("qty expected 30, got %s", result.NewQuantityOnHand)
	}
}

// ── MovingAverage outbound tests ─────────────────────────────────────────────

func TestMA_OutboundUsesCurrentAvg(t *testing.T) {
	db := testCostingDB(t)
	companyID, itemID := seedCostingItem(t, db)
	engine := &MovingAverageCostingEngine{}

	engine.ApplyInbound(db, InboundRequest{
		CompanyID: companyID, ItemID: itemID,
		Quantity: decimal.NewFromInt(50), UnitCost: decimal.NewFromFloat(12.50),
		MovementType: models.MovementTypeOpening, LocationType: models.LocationTypeInternal,
	})

	result, err := engine.ApplyOutbound(db, OutboundRequest{
		CompanyID: companyID, ItemID: itemID,
		Quantity: decimal.NewFromInt(15), MovementType: models.MovementTypeSale,
		LocationType: models.LocationTypeInternal,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !result.UnitCostUsed.Equal(decimal.NewFromFloat(12.50)) {
		t.Errorf("unit cost expected 12.50, got %s", result.UnitCostUsed)
	}
	// 15 × 12.50 = 187.50
	if !result.TotalCost.Equal(decimal.NewFromFloat(187.50)) {
		t.Errorf("total cost expected 187.50, got %s", result.TotalCost)
	}
	if !result.NewQuantityOnHand.Equal(decimal.NewFromInt(35)) {
		t.Errorf("qty expected 35, got %s", result.NewQuantityOnHand)
	}
	// avg unchanged on outbound
	if !result.NewAverageCost.Equal(decimal.NewFromFloat(12.50)) {
		t.Errorf("avg expected 12.50, got %s", result.NewAverageCost)
	}
}

func TestMA_OutboundInsufficientStock(t *testing.T) {
	db := testCostingDB(t)
	companyID, itemID := seedCostingItem(t, db)
	engine := &MovingAverageCostingEngine{}

	engine.ApplyInbound(db, InboundRequest{
		CompanyID: companyID, ItemID: itemID,
		Quantity: decimal.NewFromInt(5), UnitCost: decimal.NewFromInt(10),
		MovementType: models.MovementTypeOpening, LocationType: models.LocationTypeInternal,
	})

	_, err := engine.ApplyOutbound(db, OutboundRequest{
		CompanyID: companyID, ItemID: itemID,
		Quantity: decimal.NewFromInt(10), MovementType: models.MovementTypeSale,
		LocationType: models.LocationTypeInternal,
	})
	if err == nil {
		t.Fatal("Expected insufficient stock error")
	}
}

// ── Preview tests ────────────────────────────────────────────────────────────

func TestMA_PreviewOutbound(t *testing.T) {
	db := testCostingDB(t)
	companyID, itemID := seedCostingItem(t, db)
	engine := &MovingAverageCostingEngine{}

	engine.ApplyInbound(db, InboundRequest{
		CompanyID: companyID, ItemID: itemID,
		Quantity: decimal.NewFromInt(40), UnitCost: decimal.NewFromFloat(7.25),
		MovementType: models.MovementTypeOpening, LocationType: models.LocationTypeInternal,
	})

	result, err := engine.PreviewOutbound(db, OutboundRequest{
		CompanyID: companyID, ItemID: itemID,
		Quantity: decimal.NewFromInt(10), MovementType: models.MovementTypeSale,
		LocationType: models.LocationTypeInternal,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !result.UnitCostUsed.Equal(decimal.NewFromFloat(7.25)) {
		t.Errorf("preview unit cost expected 7.25, got %s", result.UnitCostUsed)
	}

	// Preview should NOT change the balance.
	bal, _ := GetBalance(db, companyID, itemID)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(40)) {
		t.Errorf("balance should be unchanged at 40, got %s", bal.QuantityOnHand)
	}
}

// ── Resolver tests ───────────────────────────────────────────────────────────

func TestGetCostingEngine_MovingAverage(t *testing.T) {
	engine, err := GetCostingEngine(CostingMethodMovingAverage)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := engine.(*MovingAverageCostingEngine); !ok {
		t.Fatal("Expected MovingAverageCostingEngine")
	}
}

func TestGetCostingEngine_UnknownReturnsError(t *testing.T) {
	_, err := GetCostingEngine("unknown_method")
	if err == nil {
		t.Fatal("Expected error for unknown costing method")
	}
}

func TestGetCostingEngine_EmptyDefaultsToMovingAverage(t *testing.T) {
	engine, err := GetCostingEngine("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := engine.(*MovingAverageCostingEngine); !ok {
		t.Fatal("Expected MovingAverageCostingEngine")
	}
}

func TestResolveCostingEngineForCompany(t *testing.T) {
	db := testCostingDB(t)
	co := models.Company{Name: "Test", IsActive: true, InventoryCostingMethod: "moving_average"}
	db.Create(&co)

	engine, err := ResolveCostingEngineForCompany(db, co.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := engine.(*MovingAverageCostingEngine); !ok {
		t.Fatal("Expected MovingAverageCostingEngine")
	}
}

func TestResolveCostingEngineForCompany_EmptyMethodFallback(t *testing.T) {
	db := testCostingDB(t)
	co := models.Company{Name: "Test", IsActive: true}
	db.Create(&co)

	engine, err := ResolveCostingEngineForCompany(db, co.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := engine.(*MovingAverageCostingEngine); !ok {
		t.Fatal("Expected fallback to MovingAverageCostingEngine")
	}
}

func TestResolveCostingEngineForCompany_UnknownMethodReturnsError(t *testing.T) {
	db := testCostingDB(t)
	co := models.Company{Name: "Test", IsActive: true, InventoryCostingMethod: "fifo_not_implemented"}
	db.Create(&co)

	_, err := ResolveCostingEngineForCompany(db, co.ID)
	if err == nil {
		t.Fatal("Expected error for unknown costing method")
	}
}

// ── Costing method change guard tests ────────────────────────────────────────

func TestValidateCostingMethodChange_NoMovements_Allowed(t *testing.T) {
	db := testCostingDB(t)
	co := models.Company{Name: "Test", IsActive: true}
	db.Create(&co)

	err := ValidateCostingMethodChange(db, co.ID)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
}

func TestValidateCostingMethodChange_WithMovements_Blocked(t *testing.T) {
	db := testCostingDB(t)
	companyID, itemID := seedCostingItem(t, db)

	// Create a movement
	engine := &MovingAverageCostingEngine{}
	engine.ApplyInbound(db, InboundRequest{
		CompanyID: companyID, ItemID: itemID,
		Quantity: decimal.NewFromInt(10), UnitCost: decimal.NewFromInt(5),
		MovementType: models.MovementTypeOpening, LocationType: models.LocationTypeInternal,
	})
	db.Create(&models.InventoryMovement{
		CompanyID: companyID, ItemID: itemID,
		MovementType: models.MovementTypeOpening, QuantityDelta: decimal.NewFromInt(10),
		SourceType: "opening", MovementDate: time.Now(),
	})

	err := ValidateCostingMethodChange(db, companyID)
	if err == nil {
		t.Fatal("Expected error when movements exist")
	}
}
