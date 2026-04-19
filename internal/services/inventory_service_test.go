// 遵循project_guide.md
package services

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gobooks/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testInventoryDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:inv_%s?mode=memory&cache=shared", t.Name())
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

func seedInventoryCompany(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	c := models.Company{Name: "Test Co", IsActive: true}
	db.Create(&c)
	return c.ID
}

func seedInventoryItem(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	revAcct := models.Account{CompanyID: companyID, Code: "4000", Name: "Revenue", RootAccountType: models.RootRevenue, DetailAccountType: "revenue", IsActive: true}
	db.Create(&revAcct)
	cogsAcct := models.Account{CompanyID: companyID, Code: "5000", Name: "COGS", RootAccountType: models.RootCostOfSales, DetailAccountType: models.DetailCostOfGoodsSold, IsActive: true}
	db.Create(&cogsAcct)
	invAcct := models.Account{CompanyID: companyID, Code: "1300", Name: "Inventory", RootAccountType: models.RootAsset, DetailAccountType: models.DetailInventory, IsActive: true}
	db.Create(&invAcct)

	item := models.ProductService{
		CompanyID: companyID, Name: "Widget", Type: models.ProductServiceTypeInventory,
		RevenueAccountID: revAcct.ID, COGSAccountID: &cogsAcct.ID, InventoryAccountID: &invAcct.ID,
		IsActive: true,
	}
	item.ApplyTypeDefaults()
	db.Create(&item)
	return item.ID
}

func seedServiceItem(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	revAcct := models.Account{CompanyID: companyID, Code: "4100", Name: "Service Rev", RootAccountType: models.RootRevenue, DetailAccountType: "service_revenue", IsActive: true}
	db.Create(&revAcct)
	item := models.ProductService{
		CompanyID: companyID, Name: "Consulting", Type: models.ProductServiceTypeService,
		RevenueAccountID: revAcct.ID, IsActive: true,
	}
	item.ApplyTypeDefaults()
	db.Create(&item)
	return item.ID
}

// ── Opening balance tests ────────────────────────────────────────────────────

func TestCreateOpeningBalance_Success(t *testing.T) {
	db := testInventoryDB(t)
	companyID := seedInventoryCompany(t, db)
	itemID := seedInventoryItem(t, db, companyID)

	mov, err := CreateOpeningBalance(db, OpeningBalanceInput{
		CompanyID: companyID, ItemID: itemID,
		Quantity: decimal.NewFromInt(100), UnitCost: decimal.NewFromFloat(5.50),
		AsOfDate: time.Now(),
	})
	if err != nil {
		t.Fatalf("CreateOpeningBalance failed: %v", err)
	}
	if mov.MovementType != models.MovementTypeOpening {
		t.Fatalf("Expected opening, got %s", mov.MovementType)
	}

	// Verify balance.
	bal, _ := GetBalance(db, companyID, itemID)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("Expected qty 100, got %s", bal.QuantityOnHand)
	}
	if !bal.AverageCost.Equal(decimal.NewFromFloat(5.50)) {
		t.Fatalf("Expected avg cost 5.50, got %s", bal.AverageCost)
	}
}

func TestCreateOpeningBalance_DuplicateRejected(t *testing.T) {
	db := testInventoryDB(t)
	companyID := seedInventoryCompany(t, db)
	itemID := seedInventoryItem(t, db, companyID)

	_, err := CreateOpeningBalance(db, OpeningBalanceInput{
		CompanyID: companyID, ItemID: itemID,
		Quantity: decimal.NewFromInt(10), UnitCost: decimal.NewFromInt(1),
		AsOfDate: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Second opening should fail.
	_, err = CreateOpeningBalance(db, OpeningBalanceInput{
		CompanyID: companyID, ItemID: itemID,
		Quantity: decimal.NewFromInt(20), UnitCost: decimal.NewFromInt(2),
		AsOfDate: time.Now(),
	})
	if err == nil {
		t.Fatal("Expected error for duplicate opening")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Expected 'already exists' error, got: %v", err)
	}
}

func TestCreateOpeningBalance_ServiceItemRejected(t *testing.T) {
	db := testInventoryDB(t)
	companyID := seedInventoryCompany(t, db)
	serviceID := seedServiceItem(t, db, companyID)

	_, err := CreateOpeningBalance(db, OpeningBalanceInput{
		CompanyID: companyID, ItemID: serviceID,
		Quantity: decimal.NewFromInt(10), UnitCost: decimal.NewFromInt(1),
		AsOfDate: time.Now(),
	})
	if err == nil {
		t.Fatal("Expected error for service item")
	}
	if !strings.Contains(err.Error(), "inventory") {
		t.Fatalf("Expected inventory-type error, got: %v", err)
	}
}

func TestCreateOpeningBalance_CrossCompanyRejected(t *testing.T) {
	db := testInventoryDB(t)
	company1 := seedInventoryCompany(t, db)
	company2 := seedInventoryCompany(t, db)
	itemID := seedInventoryItem(t, db, company1)

	_, err := CreateOpeningBalance(db, OpeningBalanceInput{
		CompanyID: company2, ItemID: itemID,
		Quantity: decimal.NewFromInt(10), UnitCost: decimal.NewFromInt(1),
		AsOfDate: time.Now(),
	})
	if err == nil {
		t.Fatal("Expected error for cross-company")
	}
}

func TestCreateOpeningBalance_NegativeQtyRejected(t *testing.T) {
	db := testInventoryDB(t)
	companyID := seedInventoryCompany(t, db)
	itemID := seedInventoryItem(t, db, companyID)

	_, err := CreateOpeningBalance(db, OpeningBalanceInput{
		CompanyID: companyID, ItemID: itemID,
		Quantity: decimal.NewFromInt(-5), UnitCost: decimal.NewFromInt(1),
		AsOfDate: time.Now(),
	})
	if err == nil {
		t.Fatal("Expected error for negative opening qty")
	}
}

// ── Adjustment tests ─────────────────────────────────────────────────────────

func TestCreateAdjustment_PositiveSuccess(t *testing.T) {
	db := testInventoryDB(t)
	companyID := seedInventoryCompany(t, db)
	itemID := seedInventoryItem(t, db, companyID)

	// First create opening.
	CreateOpeningBalance(db, OpeningBalanceInput{
		CompanyID: companyID, ItemID: itemID,
		Quantity: decimal.NewFromInt(50), UnitCost: decimal.NewFromFloat(10),
		AsOfDate: time.Now(),
	})

	// Positive adjustment.
	mov, err := CreateAdjustment(db, AdjustmentInput{
		CompanyID: companyID, ItemID: itemID,
		QuantityDelta: decimal.NewFromInt(25),
		MovementDate:  time.Now(), Note: "Found extra stock",
	})
	if err != nil {
		t.Fatalf("Adjustment failed: %v", err)
	}
	if mov.MovementType != models.MovementTypeAdjustment {
		t.Fatalf("Expected adjustment, got %s", mov.MovementType)
	}

	bal, _ := GetBalance(db, companyID, itemID)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(75)) {
		t.Fatalf("Expected qty 75, got %s", bal.QuantityOnHand)
	}
}

func TestCreateAdjustment_NegativeSuccess(t *testing.T) {
	db := testInventoryDB(t)
	companyID := seedInventoryCompany(t, db)
	itemID := seedInventoryItem(t, db, companyID)

	CreateOpeningBalance(db, OpeningBalanceInput{
		CompanyID: companyID, ItemID: itemID,
		Quantity: decimal.NewFromInt(50), UnitCost: decimal.NewFromFloat(10),
		AsOfDate: time.Now(),
	})

	// Negative adjustment (within stock).
	_, err := CreateAdjustment(db, AdjustmentInput{
		CompanyID: companyID, ItemID: itemID,
		QuantityDelta: decimal.NewFromInt(-20),
		MovementDate:  time.Now(), Note: "Damaged goods",
	})
	if err != nil {
		t.Fatalf("Adjustment failed: %v", err)
	}

	bal, _ := GetBalance(db, companyID, itemID)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(30)) {
		t.Fatalf("Expected qty 30, got %s", bal.QuantityOnHand)
	}
}

func TestCreateAdjustment_InsufficientStockRejected(t *testing.T) {
	db := testInventoryDB(t)
	companyID := seedInventoryCompany(t, db)
	itemID := seedInventoryItem(t, db, companyID)

	CreateOpeningBalance(db, OpeningBalanceInput{
		CompanyID: companyID, ItemID: itemID,
		Quantity: decimal.NewFromInt(10), UnitCost: decimal.NewFromFloat(5),
		AsOfDate: time.Now(),
	})

	_, err := CreateAdjustment(db, AdjustmentInput{
		CompanyID: companyID, ItemID: itemID,
		QuantityDelta: decimal.NewFromInt(-15),
		MovementDate:  time.Now(),
	})
	if err == nil {
		t.Fatal("Expected insufficient stock error")
	}
	if !strings.Contains(err.Error(), "negative") {
		t.Fatalf("Expected negative inventory error, got: %v", err)
	}

	// Balance should remain unchanged.
	bal, _ := GetBalance(db, companyID, itemID)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("Expected qty unchanged at 10, got %s", bal.QuantityOnHand)
	}
}

func TestCreateAdjustment_ServiceItemRejected(t *testing.T) {
	db := testInventoryDB(t)
	companyID := seedInventoryCompany(t, db)
	serviceID := seedServiceItem(t, db, companyID)

	_, err := CreateAdjustment(db, AdjustmentInput{
		CompanyID: companyID, ItemID: serviceID,
		QuantityDelta: decimal.NewFromInt(10),
		MovementDate:  time.Now(),
	})
	if err == nil {
		t.Fatal("Expected error for service item")
	}
}

func TestCreateAdjustment_CrossCompanyRejected(t *testing.T) {
	db := testInventoryDB(t)
	company1 := seedInventoryCompany(t, db)
	company2 := seedInventoryCompany(t, db)
	itemID := seedInventoryItem(t, db, company1)

	_, err := CreateAdjustment(db, AdjustmentInput{
		CompanyID: company2, ItemID: itemID,
		QuantityDelta: decimal.NewFromInt(10),
		MovementDate:  time.Now(),
	})
	if err == nil {
		t.Fatal("Expected cross-company error")
	}
}

// ── Balance query tests ──────────────────────────────────────────────────────

func TestGetBalance_NoRecord_ReturnsZero(t *testing.T) {
	db := testInventoryDB(t)
	companyID := seedInventoryCompany(t, db)
	itemID := seedInventoryItem(t, db, companyID)

	bal, err := GetBalance(db, companyID, itemID)
	if err != nil {
		t.Fatal(err)
	}
	if !bal.QuantityOnHand.IsZero() {
		t.Fatalf("Expected zero qty, got %s", bal.QuantityOnHand)
	}
}

func TestHasOpening_TrueAfterCreate(t *testing.T) {
	db := testInventoryDB(t)
	companyID := seedInventoryCompany(t, db)
	itemID := seedInventoryItem(t, db, companyID)

	if HasOpening(db, companyID, itemID) {
		t.Fatal("Should not have opening before creation")
	}

	CreateOpeningBalance(db, OpeningBalanceInput{
		CompanyID: companyID, ItemID: itemID,
		Quantity: decimal.NewFromInt(1), UnitCost: decimal.NewFromInt(1),
		AsOfDate: time.Now(),
	})

	if !HasOpening(db, companyID, itemID) {
		t.Fatal("Should have opening after creation")
	}
}

// ── ApplyTypeDefaults tests ──────────────────────────────────────────────────

func TestApplyTypeDefaults_Service(t *testing.T) {
	ps := models.ProductService{Type: models.ProductServiceTypeService}
	ps.ApplyTypeDefaults()
	if !ps.CanBeSold || ps.CanBePurchased || ps.IsStockItem {
		t.Fatalf("Service defaults wrong: sold=%v, purchased=%v, stock=%v", ps.CanBeSold, ps.CanBePurchased, ps.IsStockItem)
	}
}

func TestApplyTypeDefaults_NonInventory(t *testing.T) {
	ps := models.ProductService{Type: models.ProductServiceTypeNonInventory}
	ps.ApplyTypeDefaults()
	if !ps.CanBeSold || !ps.CanBePurchased || ps.IsStockItem {
		t.Fatalf("NonInventory defaults wrong: sold=%v, purchased=%v, stock=%v", ps.CanBeSold, ps.CanBePurchased, ps.IsStockItem)
	}
}

func TestApplyTypeDefaults_Inventory(t *testing.T) {
	ps := models.ProductService{Type: models.ProductServiceTypeInventory}
	ps.ApplyTypeDefaults()
	if !ps.CanBeSold || !ps.CanBePurchased || !ps.IsStockItem {
		t.Fatalf("Inventory defaults wrong: sold=%v, purchased=%v, stock=%v", ps.CanBeSold, ps.CanBePurchased, ps.IsStockItem)
	}
	if ps.ItemStructureType != models.ItemStructureSingle {
		t.Fatalf("Expected single, got %s", ps.ItemStructureType)
	}
}
