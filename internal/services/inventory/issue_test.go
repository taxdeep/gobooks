// 遵循project_guide.md
package inventory

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// Happy path: after a receipt of 20 @ $5, an issue of 7 decrements qty to 13,
// books unit cost = $5, total cost of issue = $35, and leaves average
// unchanged. The movement is written with a negative QuantityDelta.
func TestIssueStock_HappyPath(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)

	seedReceive(t, db, companyID, itemID, warehouseID, 20, 5)

	result, err := IssueStock(db, IssueStockInput{
		CompanyID:    companyID,
		ItemID:       itemID,
		WarehouseID:  warehouseID,
		Quantity:     decimal.NewFromInt(7),
		MovementDate: time.Now(),
		SourceType:   "invoice",
		SourceID:     100,
		Memo:         "Sale INV-0001",
	})
	if err != nil {
		t.Fatalf("IssueStock: %v", err)
	}
	if !result.UnitCostBase.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("UnitCostBase: got %s want 5", result.UnitCostBase)
	}
	if !result.CostOfIssueBase.Equal(decimal.NewFromInt(35)) {
		t.Fatalf("CostOfIssueBase: got %s want 35", result.CostOfIssueBase)
	}

	var mov models.InventoryMovement
	db.First(&mov, result.MovementID)
	if !mov.QuantityDelta.Equal(decimal.NewFromInt(-7)) {
		t.Fatalf("QuantityDelta: got %s want -7", mov.QuantityDelta)
	}
	if mov.MovementType != models.MovementTypeSale {
		t.Fatalf("MovementType: got %s want sale", mov.MovementType)
	}

	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).First(&bal)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(13)) {
		t.Fatalf("QuantityOnHand: got %s want 13", bal.QuantityOnHand)
	}
	if !bal.AverageCost.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("AverageCost: got %s want 5 (unchanged on outbound)", bal.AverageCost)
	}
}

// Average cost stays pinned to the moment of receipt even across multiple
// outflows — that's the weighted-average rule. After two issues of 5 units
// each from a 20 @ $5 balance, avg remains $5 and qty drops to 10.
func TestIssueStock_AverageUnchangedAcrossMultipleIssues(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 20, 5)

	issue := func(srcID uint, qty int64) {
		t.Helper()
		_, err := IssueStock(db, IssueStockInput{
			CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
			Quantity: decimal.NewFromInt(qty), MovementDate: time.Now(),
			SourceType: "invoice", SourceID: srcID,
		})
		if err != nil {
			t.Fatalf("IssueStock: %v", err)
		}
	}
	issue(1, 5)
	issue(2, 5)

	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).First(&bal)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("QuantityOnHand: got %s want 10", bal.QuantityOnHand)
	}
	if !bal.AverageCost.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("AverageCost: got %s want 5", bal.AverageCost)
	}
}

// Attempting to issue more than on-hand returns ErrInsufficientStock and
// leaves the balance row untouched.
func TestIssueStock_RejectsInsufficient(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 3, 10)

	_, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(5), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
	})
	if err == nil || !errors.Is(err, ErrInsufficientStock) {
		t.Fatalf("got %v, want ErrInsufficientStock", err)
	}

	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).First(&bal)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("balance mutated on rejected issue: got %s want 3", bal.QuantityOnHand)
	}
}

// AllowNegative overrides the insufficient-stock guard; balance goes negative.
func TestIssueStock_AllowNegativeOverride(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 3, 10)

	_, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(5), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1, AllowNegative: true,
	})
	if err != nil {
		t.Fatalf("IssueStock with AllowNegative: %v", err)
	}
	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).First(&bal)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(-2)) {
		t.Fatalf("QuantityOnHand: got %s want -2", bal.QuantityOnHand)
	}
}

// Idempotency: same key replayed returns the cached result and doesn't issue
// stock twice.
func TestIssueStock_IdempotencyReplay(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 20, 5)

	in := IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(7), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 100,
		IdempotencyKey: "invoice:100:line:1:v1",
	}
	first, err := IssueStock(db, in)
	if err != nil {
		t.Fatalf("first issue: %v", err)
	}
	second, err := IssueStock(db, in)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if first.MovementID != second.MovementID {
		t.Fatalf("replay returned different MovementID")
	}

	var movCount int64
	db.Model(&models.InventoryMovement{}).
		Where("idempotency_key = ?", in.IdempotencyKey).Count(&movCount)
	if movCount != 1 {
		t.Fatalf("movement count: got %d want 1", movCount)
	}
	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?", companyID, itemID).First(&bal)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(13)) {
		t.Fatalf("balance after replay: got %s want 13", bal.QuantityOnHand)
	}
}

// Idempotency key collision with a prior RECEIPT (positive delta) is a
// programming error — surfaces as ErrDuplicateIdempotency.
func TestIssueStock_IdempotencyCollisionWithReceipt(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	// Use the same key on both a receive and an issue — caller bug.
	key := "weird-shared-key"
	if _, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(20), MovementDate: time.Now(),
		UnitCost: decimal.NewFromInt(5), ExchangeRate: decimal.NewFromInt(1),
		SourceType: "bill", SourceID: 1, IdempotencyKey: key,
	}); err != nil {
		t.Fatalf("seed receive: %v", err)
	}
	_, err := IssueStock(db, IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(1), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1, IdempotencyKey: key,
	})
	if !errors.Is(err, ErrDuplicateIdempotency) {
		t.Fatalf("got %v, want ErrDuplicateIdempotency", err)
	}
}

// Validation errors.
func TestIssueStock_ValidationErrors(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)

	base := IssueStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(1), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
	}

	neg := base
	neg.Quantity = decimal.NewFromInt(-1)
	if _, err := IssueStock(db, neg); err != ErrNegativeQuantity {
		t.Fatalf("neg qty: got %v want ErrNegativeQuantity", err)
	}

	empty := base
	empty.SourceType = ""
	if _, err := IssueStock(db, empty); err != ErrInvalidSource {
		t.Fatalf("empty source: got %v want ErrInvalidSource", err)
	}

	specific := base
	specific.CostingMethod = CostingMethodSpecific
	specific.SpecificLotID = nil
	if _, err := IssueStock(db, specific); err == nil {
		t.Fatalf("specific without lot id: expected error")
	}
}

// Non-inventory item rejected.
func TestIssueStock_RejectsNonInventoryItem(t *testing.T) {
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

	_, err := IssueStock(db, IssueStockInput{
		CompanyID: c.ID, ItemID: svc.ID, WarehouseID: wh.ID,
		Quantity: decimal.NewFromInt(1), MovementDate: time.Now(),
		SourceType: "invoice", SourceID: 1,
	})
	if err != ErrItemNotTracked {
		t.Fatalf("got %v, want ErrItemNotTracked", err)
	}
}

// seedReceive primes the ledger with an initial receipt so subsequent issues
// have something to draw on. Used as a fixture helper across issue tests.
func seedReceive(t *testing.T, db *gorm.DB, companyID, itemID, warehouseID uint, qty, unitCost int64) {
	t.Helper()
	_, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID:    companyID,
		ItemID:       itemID,
		WarehouseID:  warehouseID,
		Quantity:     decimal.NewFromInt(qty),
		MovementDate: time.Now(),
		UnitCost:     decimal.NewFromInt(unitCost),
		ExchangeRate: decimal.NewFromInt(1),
		SourceType:   "bill",
		SourceID:     uint(qty*1000 + unitCost), // distinct-ish id per seed
	})
	if err != nil {
		t.Fatalf("seed receive: %v", err)
	}
}
