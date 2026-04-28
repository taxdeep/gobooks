// 遵循project_guide.md
package inventory

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// seedAssemblyFixture creates a parent assembly item plus two component items
// with item_components rows wiring them together. Returns IDs needed by the
// build tests and the warehouse the components are stocked in.
//
// BOM:
//   - 1 widget (parent) consumes 2 boards + 4 screws
func seedAssemblyFixture(t *testing.T, db *gorm.DB) (companyID, parentID, boardID, screwID, warehouseID uint) {
	t.Helper()
	// item_components is not in the default test fixture migration set.
	if err := db.AutoMigrate(&models.ItemComponent{}); err != nil {
		t.Fatalf("automigrate item_components: %v", err)
	}

	// Reuse the standard fixture (company / warehouse / one item) and treat
	// that item as the parent assembly.
	companyID, parentID, warehouseID = seedTestFixture(t, db)

	// Make the parent an assembly so semantically the structure is correct.
	if err := db.Model(&models.ProductService{}).Where("id = ?", parentID).
		Update("item_structure_type", models.ItemStructureAssembly).Error; err != nil {
		t.Fatalf("set parent structure: %v", err)
	}

	board := models.ProductService{
		CompanyID: companyID, Name: "Board",
		Type: models.ProductServiceTypeInventory, IsStockItem: true, IsActive: true,
	}
	if err := db.Create(&board).Error; err != nil {
		t.Fatalf("seed board: %v", err)
	}
	screw := models.ProductService{
		CompanyID: companyID, Name: "Screw",
		Type: models.ProductServiceTypeInventory, IsStockItem: true, IsActive: true,
	}
	if err := db.Create(&screw).Error; err != nil {
		t.Fatalf("seed screw: %v", err)
	}

	// 1 widget = 2 boards + 4 screws.
	if err := db.Create(&models.ItemComponent{
		CompanyID: companyID, ParentItemID: parentID,
		ComponentItemID: board.ID, Quantity: decimal.NewFromInt(2),
	}).Error; err != nil {
		t.Fatalf("seed board BOM row: %v", err)
	}
	if err := db.Create(&models.ItemComponent{
		CompanyID: companyID, ParentItemID: parentID,
		ComponentItemID: screw.ID, Quantity: decimal.NewFromInt(4),
	}).Error; err != nil {
		t.Fatalf("seed screw BOM row: %v", err)
	}
	return companyID, parentID, board.ID, screw.ID, warehouseID
}

// Happy path: build 5 widgets given enough boards and screws. Components are
// consumed at their current avg cost; finished good is produced at the
// blended unit cost (sum of component cost / produced qty).
//
// Setup: board avg = $3, screw avg = $1. One widget consumes 2 boards + 4
// screws → cost = 2×3 + 4×1 = $10/unit. Build 5 → consume 10 boards
// ($30) + 20 screws ($20), produce 5 widgets at $10/unit = $50.
func TestPostInventoryBuild_HappyPath(t *testing.T) {
	db := testDB(t)
	companyID, parentID, boardID, screwID, warehouseID := seedAssemblyFixture(t, db)
	seedReceive(t, db, companyID, boardID, warehouseID, 100, 3)
	seedReceive(t, db, companyID, screwID, warehouseID, 200, 1)

	result, err := PostInventoryBuild(db, PostInventoryBuildInput{
		CompanyID:    companyID,
		ParentItemID: parentID,
		WarehouseID:  warehouseID,
		Quantity:     decimal.NewFromInt(5),
		BuildDate:    time.Now(),
		BuildRef:     1001,
	})
	if err != nil {
		t.Fatalf("PostInventoryBuild: %v", err)
	}

	if !result.UnitCostBase.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("UnitCostBase: got %s want 10", result.UnitCostBase)
	}
	if !result.FinishedValueBase.Equal(decimal.NewFromInt(50)) {
		t.Fatalf("FinishedValueBase: got %s want 50", result.FinishedValueBase)
	}
	if !result.ComponentCostBase.Equal(decimal.NewFromInt(50)) {
		t.Fatalf("ComponentCostBase: got %s want 50", result.ComponentCostBase)
	}
	if len(result.Components) != 2 {
		t.Fatalf("Components len: got %d want 2", len(result.Components))
	}

	// Balances: parent gained 5; board dropped 10; screw dropped 20.
	assertOnHand(t, db, companyID, parentID, warehouseID, decimal.NewFromInt(5))
	assertOnHand(t, db, companyID, boardID, warehouseID, decimal.NewFromInt(90))
	assertOnHand(t, db, companyID, screwID, warehouseID, decimal.NewFromInt(180))

	// Parent's avg should equal the build's blended unit cost.
	var parentBal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?",
		companyID, parentID, warehouseID).First(&parentBal)
	if !parentBal.AverageCost.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("parent avg cost: got %s want 10", parentBal.AverageCost)
	}
}

// Labor + overhead get blended into the finished-good unit cost without
// touching component issue costs.
//
// Setup: 5 widgets × $10 raw + $20 labor + $5 overhead = $75 total, $15/unit.
func TestPostInventoryBuild_LaborAndOverheadAddedToCost(t *testing.T) {
	db := testDB(t)
	companyID, parentID, boardID, screwID, warehouseID := seedAssemblyFixture(t, db)
	seedReceive(t, db, companyID, boardID, warehouseID, 100, 3)
	seedReceive(t, db, companyID, screwID, warehouseID, 200, 1)

	result, err := PostInventoryBuild(db, PostInventoryBuildInput{
		CompanyID:        companyID,
		ParentItemID:     parentID,
		WarehouseID:      warehouseID,
		Quantity:         decimal.NewFromInt(5),
		BuildDate:        time.Now(),
		BuildRef:         1002,
		LaborCostBase:    decimal.NewFromInt(20),
		OverheadCostBase: decimal.NewFromInt(5),
	})
	if err != nil {
		t.Fatalf("PostInventoryBuild: %v", err)
	}

	if !result.UnitCostBase.Equal(decimal.NewFromInt(15)) {
		t.Fatalf("UnitCostBase: got %s want 15", result.UnitCostBase)
	}
	if !result.FinishedValueBase.Equal(decimal.NewFromInt(75)) {
		t.Fatalf("FinishedValueBase: got %s want 75", result.FinishedValueBase)
	}
	if !result.ComponentCostBase.Equal(decimal.NewFromInt(50)) {
		t.Fatalf("ComponentCostBase should be raw-only: got %s want 50", result.ComponentCostBase)
	}
	if !result.LaborCostBase.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("LaborCostBase echo: got %s want 20", result.LaborCostBase)
	}
	if !result.OverheadCostBase.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("OverheadCostBase echo: got %s want 5", result.OverheadCostBase)
	}
}

// Insufficient stock on any component aborts the build with
// ErrInsufficientStock. The earlier components that did issue successfully
// remain consumed (caller is expected to wrap the call in a DB transaction
// for atomic rollback — same contract as TransferStock).
func TestPostInventoryBuild_RejectsInsufficientComponentStock(t *testing.T) {
	db := testDB(t)
	companyID, parentID, boardID, screwID, warehouseID := seedAssemblyFixture(t, db)
	seedReceive(t, db, companyID, boardID, warehouseID, 100, 3)
	// Only 5 screws on hand; building 5 widgets needs 20 → bust.
	seedReceive(t, db, companyID, screwID, warehouseID, 5, 1)

	_, err := PostInventoryBuild(db, PostInventoryBuildInput{
		CompanyID:    companyID,
		ParentItemID: parentID,
		WarehouseID:  warehouseID,
		Quantity:     decimal.NewFromInt(5),
		BuildDate:    time.Now(),
		BuildRef:     1003,
	})
	if !errors.Is(err, ErrInsufficientStock) {
		t.Fatalf("got %v, want ErrInsufficientStock", err)
	}
}

// Parent without any BOM rows and no override is rejected — there is no way
// to compute a cost for the produced unit, so the build is meaningless.
func TestPostInventoryBuild_RejectsParentWithoutComponents(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&models.ItemComponent{}); err != nil {
		t.Fatalf("automigrate item_components: %v", err)
	}
	companyID, parentID, warehouseID := seedTestFixture(t, db) // no BOM seeded

	_, err := PostInventoryBuild(db, PostInventoryBuildInput{
		CompanyID:    companyID,
		ParentItemID: parentID,
		WarehouseID:  warehouseID,
		Quantity:     decimal.NewFromInt(1),
		BuildDate:    time.Now(),
		BuildRef:     1004,
	})
	if err == nil || !strings.Contains(err.Error(), "no components") {
		t.Fatalf("expected 'no components' error, got %v", err)
	}
}

// Idempotent replay: a second call with the same key returns the cached
// result and does NOT issue components or produce the parent again.
func TestPostInventoryBuild_IdempotencyReplaySafe(t *testing.T) {
	db := testDB(t)
	companyID, parentID, boardID, screwID, warehouseID := seedAssemblyFixture(t, db)
	seedReceive(t, db, companyID, boardID, warehouseID, 100, 3)
	seedReceive(t, db, companyID, screwID, warehouseID, 200, 1)

	in := PostInventoryBuildInput{
		CompanyID:      companyID,
		ParentItemID:   parentID,
		WarehouseID:    warehouseID,
		Quantity:       decimal.NewFromInt(2),
		BuildDate:      time.Now(),
		BuildRef:       1005,
		IdempotencyKey: "build:1005:v1",
	}
	first, err := PostInventoryBuild(db, in)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}

	second, err := PostInventoryBuild(db, in)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if first.ProduceMovementID != second.ProduceMovementID {
		t.Fatalf("replay mutated produce movement: %d vs %d",
			first.ProduceMovementID, second.ProduceMovementID)
	}
	if !first.UnitCostBase.Equal(second.UnitCostBase) {
		t.Fatalf("replay unit cost: %s vs %s", first.UnitCostBase, second.UnitCostBase)
	}

	// Exactly one produce movement and exactly two consume movements (one
	// per component) for this build.
	var produceCount, consumeCount int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ? AND source_id = ?",
			companyID, "build_produce", 1005).Count(&produceCount)
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ? AND source_id = ?",
			companyID, "build_consume", 1005).Count(&consumeCount)
	if produceCount != 1 {
		t.Fatalf("produce movement count: got %d want 1", produceCount)
	}
	if consumeCount != 2 {
		t.Fatalf("consume movement count: got %d want 2", consumeCount)
	}

	// Balances reflect a single build: parent +2, board -4, screw -8.
	assertOnHand(t, db, companyID, parentID, warehouseID, decimal.NewFromInt(2))
	assertOnHand(t, db, companyID, boardID, warehouseID, decimal.NewFromInt(96))
	assertOnHand(t, db, companyID, screwID, warehouseID, decimal.NewFromInt(192))
}

// Component overrides bypass the BOM. Useful for rework / substitution
// without mutating the parent's master BOM.
//
// Setup: parent BOM still says boards+screws, but we override to consume
// only screws (10 per unit × 1 = 10 screws). Builds 1 unit; cost = 10 × $1.
func TestPostInventoryBuild_ComponentOverridesReplaceBOM(t *testing.T) {
	db := testDB(t)
	companyID, parentID, boardID, screwID, warehouseID := seedAssemblyFixture(t, db)
	seedReceive(t, db, companyID, boardID, warehouseID, 100, 3)
	seedReceive(t, db, companyID, screwID, warehouseID, 200, 1)

	result, err := PostInventoryBuild(db, PostInventoryBuildInput{
		CompanyID:    companyID,
		ParentItemID: parentID,
		WarehouseID:  warehouseID,
		Quantity:     decimal.NewFromInt(1),
		BuildDate:    time.Now(),
		BuildRef:     1006,
		ComponentOverrides: []BuildComponentInput{
			{ItemID: screwID, PerUnitQuantity: decimal.NewFromInt(10)},
		},
	})
	if err != nil {
		t.Fatalf("PostInventoryBuild: %v", err)
	}
	if len(result.Components) != 1 {
		t.Fatalf("override: should consume only 1 distinct component, got %d", len(result.Components))
	}
	if !result.UnitCostBase.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("UnitCostBase: got %s want 10", result.UnitCostBase)
	}

	// Boards untouched (override skipped them).
	assertOnHand(t, db, companyID, boardID, warehouseID, decimal.NewFromInt(100))
	assertOnHand(t, db, companyID, screwID, warehouseID, decimal.NewFromInt(190))
}

// Validation: parent must be inventory-tracked; non-inventory rejection.
func TestPostInventoryBuild_RejectsNonInventoryParent(t *testing.T) {
	db := testDB(t)
	c := models.Company{Name: "Co", IsActive: true}
	db.Create(&c)
	wh := models.Warehouse{CompanyID: c.ID, Name: "W", IsActive: true}
	db.Create(&wh)
	svc := models.ProductService{
		CompanyID: c.ID, Name: "Consulting",
		Type: models.ProductServiceTypeService, IsActive: true,
	}
	db.Create(&svc)

	_, err := PostInventoryBuild(db, PostInventoryBuildInput{
		CompanyID:    c.ID,
		ParentItemID: svc.ID,
		WarehouseID:  wh.ID,
		Quantity:     decimal.NewFromInt(1),
		BuildDate:    time.Now(),
		BuildRef:     1,
		ComponentOverrides: []BuildComponentInput{
			{ItemID: svc.ID, PerUnitQuantity: decimal.NewFromInt(1)},
		},
	})
	if err != ErrItemNotTracked {
		t.Fatalf("got %v, want ErrItemNotTracked", err)
	}
}

// Validation: zero quantity, zero parent, negative labor — all caught.
func TestPostInventoryBuild_ValidationErrors(t *testing.T) {
	db := testDB(t)
	companyID, parentID, _, _, warehouseID := seedAssemblyFixture(t, db)

	base := PostInventoryBuildInput{
		CompanyID:    companyID,
		ParentItemID: parentID,
		WarehouseID:  warehouseID,
		Quantity:     decimal.NewFromInt(1),
		BuildDate:    time.Now(),
		BuildRef:     999,
	}

	zeroQty := base
	zeroQty.Quantity = decimal.Zero
	if _, err := PostInventoryBuild(db, zeroQty); err != ErrNegativeQuantity {
		t.Fatalf("zero qty: got %v want ErrNegativeQuantity", err)
	}

	negLabor := base
	negLabor.LaborCostBase = decimal.NewFromInt(-1)
	if _, err := PostInventoryBuild(db, negLabor); err == nil {
		t.Fatalf("negative labor: expected error")
	}

	badOverride := base
	badOverride.ComponentOverrides = []BuildComponentInput{
		{ItemID: 1, PerUnitQuantity: decimal.Zero},
	}
	if _, err := PostInventoryBuild(db, badOverride); err == nil {
		t.Fatalf("zero per-unit override: expected error")
	}
}

// Atomicity: when wrapped in a caller-supplied transaction, a mid-build
// failure (insufficient stock on a later component) must roll the entire
// build back — every earlier component issue and any produce leg gone.
//
// Setup: board has plenty of stock (100), screw has only 5. Building 5
// widgets needs 10 boards + 20 screws. The board leg succeeds, the screw
// leg fails, and the whole transaction must roll back, leaving balances
// exactly as they were before the build attempt.
func TestPostInventoryBuild_TransactionRollsBackPartialConsumption(t *testing.T) {
	db := testDB(t)
	companyID, parentID, boardID, screwID, warehouseID := seedAssemblyFixture(t, db)
	seedReceive(t, db, companyID, boardID, warehouseID, 100, 3)
	seedReceive(t, db, companyID, screwID, warehouseID, 5, 1) // too few

	// Snapshot movement count pre-attempt so we can assert no residue.
	var preCount int64
	db.Model(&models.InventoryMovement{}).Where("company_id = ?", companyID).Count(&preCount)

	txErr := db.Transaction(func(tx *gorm.DB) error {
		_, err := PostInventoryBuild(tx, PostInventoryBuildInput{
			CompanyID:    companyID,
			ParentItemID: parentID,
			WarehouseID:  warehouseID,
			Quantity:     decimal.NewFromInt(5),
			BuildDate:    time.Now(),
			BuildRef:     9001,
		})
		return err // non-nil → rollback
	})
	if !errors.Is(txErr, ErrInsufficientStock) {
		t.Fatalf("tx error: got %v, want ErrInsufficientStock", txErr)
	}

	// Board on-hand must still be 100 (consumption of the board leg rolled back).
	assertOnHand(t, db, companyID, boardID, warehouseID, decimal.NewFromInt(100))
	// Screw unchanged at 5.
	assertOnHand(t, db, companyID, screwID, warehouseID, decimal.NewFromInt(5))
	// Parent never received any built units.
	var parentBal models.InventoryBalance
	err := db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?",
		companyID, parentID, warehouseID).First(&parentBal).Error
	if err == nil && parentBal.QuantityOnHand.IsPositive() {
		t.Fatalf("parent should not have received units: got %s", parentBal.QuantityOnHand)
	}

	// No residual movements from the build attempt.
	var postCount int64
	db.Model(&models.InventoryMovement{}).Where("company_id = ?", companyID).Count(&postCount)
	if postCount != preCount {
		t.Fatalf("movement count after rollback: got %d, want %d", postCount, preCount)
	}
}

// assertOnHand is a small per-package helper used across build tests to
// keep balance assertions tight and readable.
func assertOnHand(t *testing.T, db *gorm.DB, companyID, itemID, warehouseID uint, want decimal.Decimal) {
	t.Helper()
	var bal models.InventoryBalance
	err := db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?",
		companyID, itemID, warehouseID).First(&bal).Error
	if err != nil {
		t.Fatalf("load balance for item %d: %v", itemID, err)
	}
	if !bal.QuantityOnHand.Equal(want) {
		t.Fatalf("on-hand for item %d: got %s want %s", itemID, bal.QuantityOnHand, want)
	}
}
