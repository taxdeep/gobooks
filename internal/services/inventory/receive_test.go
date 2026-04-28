// 遵循project_guide.md
package inventory

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"balanciz/internal/models"
)

// testDB spins up an in-memory SQLite for a single test. It migrates only
// the tables touched by this package (Company / ProductService / Warehouse
// / InventoryMovement / InventoryBalance). Tests are parallel-safe via the
// per-test DSN.
func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:invmod_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Warehouse{},
		&models.ProductService{},
		&models.InventoryMovement{},
		&models.InventoryBalance{},
		&models.InventoryCostLayer{},
		&models.InventoryLayerConsumption{},
		&models.InventoryLot{},
		&models.InventorySerialUnit{},
		&models.InventoryTrackingConsumption{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

// seedTestFixture creates one company, one inventory item, one warehouse
// and returns their IDs. Keeps each test focused on the behaviour under
// test rather than boilerplate.
func seedTestFixture(t *testing.T, db *gorm.DB) (companyID, itemID, warehouseID uint) {
	t.Helper()
	c := models.Company{Name: "TestCo", IsActive: true}
	if err := db.Create(&c).Error; err != nil {
		t.Fatalf("seed company: %v", err)
	}
	wh := models.Warehouse{CompanyID: c.ID, Name: "Main", IsActive: true}
	if err := db.Create(&wh).Error; err != nil {
		t.Fatalf("seed warehouse: %v", err)
	}
	item := models.ProductService{
		CompanyID:   c.ID,
		Name:        "Widget",
		Type:        models.ProductServiceTypeInventory,
		IsStockItem: true,
		IsActive:    true,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("seed item: %v", err)
	}
	return c.ID, item.ID, wh.ID
}

// Happy path: a single receipt creates one movement and a balance reflecting
// the received quantity and unit cost.
func TestReceiveStock_WritesMovementAndUpdatesBalance(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)

	result, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID:    companyID,
		ItemID:       itemID,
		WarehouseID:  warehouseID,
		Quantity:     decimal.NewFromInt(10),
		MovementDate: time.Now(),
		UnitCost:     decimal.NewFromInt(5),
		ExchangeRate: decimal.NewFromInt(1),
		SourceType:   "bill",
		SourceID:     42,
		Memo:         "Receipt",
	})
	if err != nil {
		t.Fatalf("ReceiveStock: %v", err)
	}
	if result.MovementID == 0 {
		t.Fatalf("expected non-zero MovementID")
	}
	if !result.UnitCostBase.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("UnitCostBase: got %s want 5", result.UnitCostBase)
	}
	if !result.InventoryValueBase.Equal(decimal.NewFromInt(50)) {
		t.Fatalf("InventoryValueBase: got %s want 50", result.InventoryValueBase)
	}

	// Invariant: sum(delta) == on_hand.
	var bal models.InventoryBalance
	if err := db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?",
		companyID, itemID, warehouseID).First(&bal).Error; err != nil {
		t.Fatalf("load balance: %v", err)
	}
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("QuantityOnHand: got %s want 10", bal.QuantityOnHand)
	}
	if !bal.AverageCost.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("AverageCost: got %s want 5", bal.AverageCost)
	}
}

// Weighted average is recomputed on each subsequent receipt.
// 10 × $5 then 10 × $7 → avg = $6 over 20 units.
func TestReceiveStock_WeightedAverageOnSecondReceive(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)

	in := func(unitCost int64) ReceiveStockInput {
		return ReceiveStockInput{
			CompanyID:    companyID,
			ItemID:       itemID,
			WarehouseID:  warehouseID,
			Quantity:     decimal.NewFromInt(10),
			MovementDate: time.Now(),
			UnitCost:     decimal.NewFromInt(unitCost),
			ExchangeRate: decimal.NewFromInt(1),
			SourceType:   "bill",
			SourceID:     uint(unitCost), // distinct per call
		}
	}
	if _, err := ReceiveStock(db, in(5)); err != nil {
		t.Fatalf("first receive: %v", err)
	}
	if _, err := ReceiveStock(db, in(7)); err != nil {
		t.Fatalf("second receive: %v", err)
	}

	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?",
		companyID, itemID, warehouseID).First(&bal)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("QuantityOnHand: got %s want 20", bal.QuantityOnHand)
	}
	if !bal.AverageCost.Equal(decimal.NewFromInt(6)) {
		t.Fatalf("AverageCost: got %s want 6", bal.AverageCost)
	}
}

// Landed cost is apportioned per unit and folded into UnitCostBase.
// 10 units × $5 + $20 landed → UnitCostBase = $7 ($5 + $2 landed/unit).
func TestReceiveStock_LandedCostApportioned(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)

	result, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID:            companyID,
		ItemID:               itemID,
		WarehouseID:          warehouseID,
		Quantity:             decimal.NewFromInt(10),
		MovementDate:         time.Now(),
		UnitCost:             decimal.NewFromInt(5),
		ExchangeRate:         decimal.NewFromInt(1),
		LandedCostAllocation: decimal.NewFromInt(20),
		SourceType:           "bill",
		SourceID:             1,
	})
	if err != nil {
		t.Fatalf("ReceiveStock: %v", err)
	}
	if !result.UnitCostBase.Equal(decimal.NewFromInt(7)) {
		t.Fatalf("UnitCostBase: got %s want 7", result.UnitCostBase)
	}
	if !result.InventoryValueBase.Equal(decimal.NewFromInt(70)) {
		t.Fatalf("InventoryValueBase: got %s want 70", result.InventoryValueBase)
	}

	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?",
		companyID, itemID, warehouseID).First(&bal)
	if !bal.AverageCost.Equal(decimal.NewFromInt(7)) {
		t.Fatalf("AverageCost: got %s want 7 (should include landed cost)", bal.AverageCost)
	}
}

// Foreign-currency receipt: document-currency cost is multiplied by the rate.
// 10 units × 5 EUR × 1.1 rate = 55 base; UnitCostBase = 5.5.
func TestReceiveStock_ForeignCurrencyMultipliesByRate(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)

	result, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID:    companyID,
		ItemID:       itemID,
		WarehouseID:  warehouseID,
		Quantity:     decimal.NewFromInt(10),
		MovementDate: time.Now(),
		UnitCost:     decimal.NewFromInt(5),
		CurrencyCode: "EUR",
		ExchangeRate: decimal.NewFromFloat(1.1),
		SourceType:   "bill",
		SourceID:     1,
	})
	if err != nil {
		t.Fatalf("ReceiveStock: %v", err)
	}
	if !result.UnitCostBase.Equal(decimal.NewFromFloat(5.5)) {
		t.Fatalf("UnitCostBase: got %s want 5.5", result.UnitCostBase)
	}
	if !result.InventoryValueBase.Equal(decimal.NewFromInt(55)) {
		t.Fatalf("InventoryValueBase: got %s want 55", result.InventoryValueBase)
	}
}

// Idempotency: the same key replayed returns the cached result and does
// NOT create a second movement or update the balance a second time.
func TestReceiveStock_IdempotencyKey_ReplaySafe(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)

	in := ReceiveStockInput{
		CompanyID:      companyID,
		ItemID:         itemID,
		WarehouseID:    warehouseID,
		Quantity:       decimal.NewFromInt(10),
		MovementDate:   time.Now(),
		UnitCost:       decimal.NewFromInt(5),
		ExchangeRate:   decimal.NewFromInt(1),
		SourceType:     "bill",
		SourceID:       42,
		IdempotencyKey: "bill:42:line:1:v1",
	}

	first, err := ReceiveStock(db, in)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	second, err := ReceiveStock(db, in)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if first.MovementID != second.MovementID {
		t.Fatalf("replay returned different MovementID: %d vs %d", first.MovementID, second.MovementID)
	}

	// Exactly one movement, balance reflects one receipt.
	var movCount int64
	db.Model(&models.InventoryMovement{}).
		Where("idempotency_key = ?", "bill:42:line:1:v1").
		Count(&movCount)
	if movCount != 1 {
		t.Fatalf("movement count: got %d want 1", movCount)
	}
	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).First(&bal)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("balance after replay: got %s want 10", bal.QuantityOnHand)
	}
}

// Validation: negative / zero qty, missing source_type, and missing currency
// rate on foreign-currency receipts all fail early.
func TestReceiveStock_ValidationErrors(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)

	base := ReceiveStockInput{
		CompanyID:    companyID,
		ItemID:       itemID,
		WarehouseID:  warehouseID,
		Quantity:     decimal.NewFromInt(10),
		MovementDate: time.Now(),
		UnitCost:     decimal.NewFromInt(5),
		ExchangeRate: decimal.NewFromInt(1),
		SourceType:   "bill",
		SourceID:     1,
	}

	// Negative quantity
	neg := base
	neg.Quantity = decimal.NewFromInt(-1)
	if _, err := ReceiveStock(db, neg); err != ErrNegativeQuantity {
		t.Fatalf("negative qty: got %v want ErrNegativeQuantity", err)
	}

	// Empty source type
	empty := base
	empty.SourceType = ""
	if _, err := ReceiveStock(db, empty); err != ErrInvalidSource {
		t.Fatalf("empty source: got %v want ErrInvalidSource", err)
	}

	// Foreign currency with zero rate
	fx := base
	fx.CurrencyCode = "EUR"
	fx.ExchangeRate = decimal.Zero
	if _, err := ReceiveStock(db, fx); err != ErrCurrencyRateRequired {
		t.Fatalf("fx no rate: got %v want ErrCurrencyRateRequired", err)
	}
}

// Non-inventory item (service or bundle): receipts are rejected so we don't
// silently track stock on something the product config says we don't.
func TestReceiveStock_RejectsNonInventoryItem(t *testing.T) {
	db := testDB(t)
	c := models.Company{Name: "Co", IsActive: true}
	db.Create(&c)
	wh := models.Warehouse{CompanyID: c.ID, Name: "W", IsActive: true}
	db.Create(&wh)
	svcItem := models.ProductService{
		CompanyID: c.ID, Name: "Consulting", Type: models.ProductServiceTypeService, IsActive: true,
	}
	db.Create(&svcItem)

	_, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID:    c.ID,
		ItemID:       svcItem.ID,
		WarehouseID:  wh.ID,
		Quantity:     decimal.NewFromInt(1),
		MovementDate: time.Now(),
		UnitCost:     decimal.NewFromInt(100),
		ExchangeRate: decimal.NewFromInt(1),
		SourceType:   "bill",
		SourceID:     1,
	})
	if err != ErrItemNotTracked {
		t.Fatalf("got %v, want ErrItemNotTracked", err)
	}
}
