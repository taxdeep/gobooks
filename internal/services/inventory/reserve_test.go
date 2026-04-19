// 遵循project_guide.md
package inventory

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"gobooks/internal/models"
)

// Happy path: reserve 4 of 10 on-hand → reserved = 4, available = 6.
func TestReserveStock_HappyPath(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)

	res, err := ReserveStock(db, ReserveStockInput{
		CompanyID:   companyID,
		ItemID:      itemID,
		WarehouseID: warehouseID,
		Quantity:    decimal.NewFromInt(4),
		SourceType:  "sales_order",
		SourceID:    42,
	})
	if err != nil {
		t.Fatalf("ReserveStock: %v", err)
	}
	if !res.QuantityReserved.Equal(decimal.NewFromInt(4)) {
		t.Fatalf("QuantityReserved: got %s want 4", res.QuantityReserved)
	}

	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?",
		companyID, itemID, warehouseID).First(&bal)
	if !bal.QuantityReserved.Equal(decimal.NewFromInt(4)) {
		t.Fatalf("balance.QuantityReserved: got %s want 4", bal.QuantityReserved)
	}
	// On-hand is untouched by a reservation.
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("balance.QuantityOnHand should be unchanged: got %s want 10", bal.QuantityOnHand)
	}
}

// Reserving past available is rejected. The second request asks for more
// than the remaining available quantity.
func TestReserveStock_RejectsInsufficientAvailable(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)

	// First reserve 7 — fine, 3 remain.
	if _, err := ReserveStock(db, ReserveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(7), SourceType: "sales_order", SourceID: 1,
	}); err != nil {
		t.Fatalf("first reserve: %v", err)
	}

	// Second reserve asks for 5 → only 3 available → ErrInsufficientAvailable.
	_, err := ReserveStock(db, ReserveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(5), SourceType: "sales_order", SourceID: 2,
	})
	if !errors.Is(err, ErrInsufficientAvailable) {
		t.Fatalf("got %v, want ErrInsufficientAvailable", err)
	}

	// Guardrail: no partial reservation occurred; reserved stays at 7.
	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?",
		companyID, itemID, warehouseID).First(&bal)
	if !bal.QuantityReserved.Equal(decimal.NewFromInt(7)) {
		t.Fatalf("reserved should be unchanged: got %s want 7", bal.QuantityReserved)
	}
}

// Reserve + Release: a subsequent release frees the counter back up so a
// later reserve for the freed quantity succeeds.
func TestReleaseStock_FreesReserved(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)

	if _, err := ReserveStock(db, ReserveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(6), SourceType: "sales_order", SourceID: 1,
	}); err != nil {
		t.Fatalf("reserve: %v", err)
	}

	if err := ReleaseStock(db, ReleaseStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(4), SourceType: "sales_order", SourceID: 1,
	}); err != nil {
		t.Fatalf("release: %v", err)
	}

	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?",
		companyID, itemID, warehouseID).First(&bal)
	if !bal.QuantityReserved.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("reserved after release: got %s want 2", bal.QuantityReserved)
	}

	// Now that 4 units were released, we can reserve 8 total from the
	// remaining available (10 − 2 = 8).
	if _, err := ReserveStock(db, ReserveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(8), SourceType: "sales_order", SourceID: 2,
	}); err != nil {
		t.Fatalf("reserve after release should succeed: %v", err)
	}
}

// Releasing more than reserved is rejected and leaves the counter intact.
func TestReleaseStock_UnderflowRejected(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)

	if _, err := ReserveStock(db, ReserveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(3), SourceType: "sales_order", SourceID: 1,
	}); err != nil {
		t.Fatalf("reserve: %v", err)
	}

	err := ReleaseStock(db, ReleaseStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(5), SourceType: "sales_order", SourceID: 1,
	})
	if !errors.Is(err, ErrReservationUnderflow) {
		t.Fatalf("got %v, want ErrReservationUnderflow", err)
	}

	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?",
		companyID, itemID, warehouseID).First(&bal)
	if !bal.QuantityReserved.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("reserved should be unchanged on underflow: got %s want 3", bal.QuantityReserved)
	}
}

// GetOnHand surfaces QuantityReserved and QuantityAvailable after a reserve.
// This locks the user-visible contract — reports rely on available != onhand
// once a reservation lands.
func TestGetOnHand_ReflectsReservations(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	seedReceive(t, db, companyID, itemID, warehouseID, 10, 5)

	if _, err := ReserveStock(db, ReserveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(3), SourceType: "sales_order", SourceID: 1,
	}); err != nil {
		t.Fatalf("reserve: %v", err)
	}

	rows, err := GetOnHand(db, OnHandQuery{CompanyID: companyID, ItemID: itemID})
	if err != nil {
		t.Fatalf("GetOnHand: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows: got %d want 1", len(rows))
	}
	r := rows[0]
	if !r.QuantityOnHand.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("OnHand: got %s want 10", r.QuantityOnHand)
	}
	if !r.QuantityReserved.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("Reserved: got %s want 3", r.QuantityReserved)
	}
	if !r.QuantityAvailable.Equal(decimal.NewFromInt(7)) {
		t.Fatalf("Available: got %s want 7", r.QuantityAvailable)
	}
}

// Reserving against a never-received item (no balance row yet) falls
// through to the "available = 0" branch without crashing — ReserveStock
// must not implicitly create on-hand.
func TestReserveStock_RejectsNeverReceivedItem(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)
	// No seedReceive — balance row is created on first read but starts at 0.

	_, err := ReserveStock(db, ReserveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(1), SourceType: "sales_order", SourceID: 1,
	})
	if !errors.Is(err, ErrInsufficientAvailable) {
		t.Fatalf("got %v, want ErrInsufficientAvailable", err)
	}
}

// Non-inventory items (services, bundles) cannot be reserved — caught by
// the shared verifyInventoryItem guard.
func TestReserveStock_RejectsNonInventoryItem(t *testing.T) {
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

	_, err := ReserveStock(db, ReserveStockInput{
		CompanyID: c.ID, ItemID: svc.ID, WarehouseID: wh.ID,
		Quantity: decimal.NewFromInt(1), SourceType: "sales_order", SourceID: 1,
	})
	if err != ErrItemNotTracked {
		t.Fatalf("got %v, want ErrItemNotTracked", err)
	}
}

// Validation: zero / negative qty, missing source_type.
func TestReserveStock_ValidationErrors(t *testing.T) {
	db := testDB(t)
	companyID, itemID, warehouseID := seedTestFixture(t, db)

	base := ReserveStockInput{
		CompanyID: companyID, ItemID: itemID, WarehouseID: warehouseID,
		Quantity: decimal.NewFromInt(1), SourceType: "sales_order", SourceID: 1,
	}

	zeroQty := base
	zeroQty.Quantity = decimal.Zero
	if _, err := ReserveStock(db, zeroQty); err != ErrNegativeQuantity {
		t.Fatalf("zero qty: got %v want ErrNegativeQuantity", err)
	}

	noSource := base
	noSource.SourceType = ""
	if _, err := ReserveStock(db, noSource); err != ErrInvalidSource {
		t.Fatalf("empty source: got %v want ErrInvalidSource", err)
	}
}
