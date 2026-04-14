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

// ── Test DB setup ─────────────────────────────────────────────────────────────

func testTransferDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:tr_%s?mode=memory&cache=shared", t.Name())
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
		&models.WarehouseTransfer{},
	)
	return db
}

func seedTransferFixture(t *testing.T, db *gorm.DB) (companyID, itemID, wh1ID, wh2ID uint) {
	t.Helper()
	c := models.Company{Name: "Transfer Co", IsActive: true}
	db.Create(&c)
	companyID = c.ID

	// Accounts
	cogsAcct := models.Account{CompanyID: companyID, Code: "5000", Name: "COGS",
		RootAccountType: models.RootCostOfSales, DetailAccountType: models.DetailCostOfGoodsSold, IsActive: true}
	db.Create(&cogsAcct)
	invAcct := models.Account{CompanyID: companyID, Code: "1300", Name: "Inventory",
		RootAccountType: models.RootAsset, DetailAccountType: models.DetailInventory, IsActive: true}
	db.Create(&invAcct)

	// Item
	item := models.ProductService{
		CompanyID: companyID, Name: "Gadget", Type: models.ProductServiceTypeInventory,
		COGSAccountID: &cogsAcct.ID, InventoryAccountID: &invAcct.ID, IsActive: true,
	}
	item.ApplyTypeDefaults()
	db.Create(&item)
	itemID = item.ID

	// Warehouses
	wh1, _ := CreateWarehouse(db, companyID, WarehouseInput{Code: "WH1", Name: "Main", IsDefault: true, IsActive: true})
	wh2, _ := CreateWarehouse(db, companyID, WarehouseInput{Code: "WH2", Name: "Secondary", IsActive: true})
	return companyID, itemID, wh1.ID, wh2.ID
}

func seedStockInWarehouse(t *testing.T, db *gorm.DB, companyID, itemID, warehouseID uint, qty, unitCost int64) {
	t.Helper()
	engine := &MovingAverageCostingEngine{}
	_, err := engine.ApplyInbound(db, InboundRequest{
		CompanyID:    companyID,
		ItemID:       itemID,
		Quantity:     decimal.NewFromInt(qty),
		UnitCost:     decimal.NewFromInt(unitCost),
		MovementType: models.MovementTypePurchase,
		WarehouseID:  &warehouseID,
	})
	if err != nil {
		t.Fatalf("seedStockInWarehouse: %v", err)
	}
}

// ── Transfer CRUD tests ───────────────────────────────────────────────────────

func TestTransfer_CreateDraft(t *testing.T) {
	db := testTransferDB(t)
	cid, itemID, wh1, wh2 := seedTransferFixture(t, db)

	tr, err := CreateTransfer(db, cid, TransferInput{
		FromWarehouseID: wh1,
		ToWarehouseID:   wh2,
		ItemID:          itemID,
		Quantity:        decimal.NewFromInt(5),
		TransferDate:    time.Now(),
	})
	if err != nil {
		t.Fatalf("CreateTransfer: %v", err)
	}
	if tr.Status != models.TransferStatusDraft {
		t.Errorf("status = %q, want draft", tr.Status)
	}
}

func TestTransfer_RejectSameWarehouse(t *testing.T) {
	db := testTransferDB(t)
	cid, itemID, wh1, _ := seedTransferFixture(t, db)

	_, err := CreateTransfer(db, cid, TransferInput{
		FromWarehouseID: wh1,
		ToWarehouseID:   wh1,
		ItemID:          itemID,
		Quantity:        decimal.NewFromInt(1),
		TransferDate:    time.Now(),
	})
	if err == nil {
		t.Fatal("expected error for same-warehouse transfer")
	}
}

func TestTransfer_RejectZeroQty(t *testing.T) {
	db := testTransferDB(t)
	cid, itemID, wh1, wh2 := seedTransferFixture(t, db)

	_, err := CreateTransfer(db, cid, TransferInput{
		FromWarehouseID: wh1,
		ToWarehouseID:   wh2,
		ItemID:          itemID,
		Quantity:        decimal.Zero,
		TransferDate:    time.Now(),
	})
	if err == nil {
		t.Fatal("expected error for zero quantity")
	}
}

func TestTransfer_UpdateDraft(t *testing.T) {
	db := testTransferDB(t)
	cid, itemID, wh1, wh2 := seedTransferFixture(t, db)

	tr, _ := CreateTransfer(db, cid, TransferInput{
		FromWarehouseID: wh1, ToWarehouseID: wh2, ItemID: itemID,
		Quantity: decimal.NewFromInt(5), TransferDate: time.Now(),
	})

	updated, err := UpdateTransfer(db, cid, tr.ID, TransferInput{
		FromWarehouseID: wh1, ToWarehouseID: wh2, ItemID: itemID,
		Quantity: decimal.NewFromInt(10), TransferDate: time.Now(), Notes: "updated",
	})
	if err != nil {
		t.Fatalf("UpdateTransfer: %v", err)
	}
	if !updated.Quantity.Equal(decimal.NewFromInt(10)) {
		t.Errorf("quantity = %s, want 10", updated.Quantity)
	}
}

func TestTransfer_CancelDraft(t *testing.T) {
	db := testTransferDB(t)
	cid, itemID, wh1, wh2 := seedTransferFixture(t, db)

	tr, _ := CreateTransfer(db, cid, TransferInput{
		FromWarehouseID: wh1, ToWarehouseID: wh2, ItemID: itemID,
		Quantity: decimal.NewFromInt(5), TransferDate: time.Now(),
	})
	if err := CancelTransfer(db, cid, tr.ID); err != nil {
		t.Fatalf("CancelTransfer: %v", err)
	}
	got, _ := GetTransfer(db, cid, tr.ID)
	if got.Status != models.TransferStatusCancelled {
		t.Errorf("status = %q, want cancelled", got.Status)
	}
}

// ── Posting tests ─────────────────────────────────────────────────────────────

func TestTransfer_PostMovesStock(t *testing.T) {
	db := testTransferDB(t)
	cid, itemID, wh1, wh2 := seedTransferFixture(t, db)

	// Seed 20 units in WH1 @ $10.
	seedStockInWarehouse(t, db, cid, itemID, wh1, 20, 10)

	tr, _ := CreateTransfer(db, cid, TransferInput{
		FromWarehouseID: wh1, ToWarehouseID: wh2, ItemID: itemID,
		Quantity: decimal.NewFromInt(8), TransferDate: time.Now(),
	})

	if err := PostTransfer(db, cid, tr.ID, "test@example.com", nil); err != nil {
		t.Fatalf("PostTransfer: %v", err)
	}

	engine := &MovingAverageCostingEngine{}

	// WH1 should have 12 remaining.
	r1, err := engine.PreviewOutbound(db, OutboundRequest{
		CompanyID: cid, ItemID: itemID, Quantity: decimal.NewFromInt(12),
		MovementType: models.MovementTypeSale, WarehouseID: &wh1,
	})
	if err != nil {
		t.Fatalf("WH1 preview: %v", err)
	}
	if !r1.NewQuantityOnHand.Equal(decimal.Zero) {
		t.Errorf("WH1 remaining after preview = %s, want 0", r1.NewQuantityOnHand)
	}

	// WH2 should have 8 units.
	r2, err := engine.PreviewOutbound(db, OutboundRequest{
		CompanyID: cid, ItemID: itemID, Quantity: decimal.NewFromInt(8),
		MovementType: models.MovementTypeSale, WarehouseID: &wh2,
	})
	if err != nil {
		t.Fatalf("WH2 preview: %v", err)
	}
	if !r2.UnitCostUsed.Equal(decimal.NewFromInt(10)) {
		t.Errorf("WH2 cost = %s, want 10", r2.UnitCostUsed)
	}

	// Transfer should be posted.
	got, _ := GetTransfer(db, cid, tr.ID)
	if got.Status != models.TransferStatusPosted {
		t.Errorf("transfer status = %q, want posted", got.Status)
	}
}

func TestTransfer_PostFailsInsufficientStock(t *testing.T) {
	db := testTransferDB(t)
	cid, itemID, wh1, wh2 := seedTransferFixture(t, db)

	// Only 5 units in WH1.
	seedStockInWarehouse(t, db, cid, itemID, wh1, 5, 10)

	tr, _ := CreateTransfer(db, cid, TransferInput{
		FromWarehouseID: wh1, ToWarehouseID: wh2, ItemID: itemID,
		Quantity: decimal.NewFromInt(10), TransferDate: time.Now(),
	})
	err := PostTransfer(db, cid, tr.ID, "test@example.com", nil)
	if err == nil {
		t.Fatal("expected insufficient stock error")
	}

	// Transfer should still be draft.
	got, _ := GetTransfer(db, cid, tr.ID)
	if got.Status != models.TransferStatusDraft {
		t.Errorf("transfer status = %q, want draft after failed post", got.Status)
	}
}

func TestTransfer_CannotPostTwice(t *testing.T) {
	db := testTransferDB(t)
	cid, itemID, wh1, wh2 := seedTransferFixture(t, db)
	seedStockInWarehouse(t, db, cid, itemID, wh1, 20, 10)

	tr, _ := CreateTransfer(db, cid, TransferInput{
		FromWarehouseID: wh1, ToWarehouseID: wh2, ItemID: itemID,
		Quantity: decimal.NewFromInt(5), TransferDate: time.Now(),
	})
	PostTransfer(db, cid, tr.ID, "actor@example.com", nil)

	err := PostTransfer(db, cid, tr.ID, "actor@example.com", nil)
	if err == nil {
		t.Fatal("expected error posting already-posted transfer")
	}
}

func TestTransfer_CannotUpdatePosted(t *testing.T) {
	db := testTransferDB(t)
	cid, itemID, wh1, wh2 := seedTransferFixture(t, db)
	seedStockInWarehouse(t, db, cid, itemID, wh1, 20, 10)

	tr, _ := CreateTransfer(db, cid, TransferInput{
		FromWarehouseID: wh1, ToWarehouseID: wh2, ItemID: itemID,
		Quantity: decimal.NewFromInt(5), TransferDate: time.Now(),
	})
	PostTransfer(db, cid, tr.ID, "actor@example.com", nil)

	_, err := UpdateTransfer(db, cid, tr.ID, TransferInput{
		FromWarehouseID: wh1, ToWarehouseID: wh2, ItemID: itemID,
		Quantity: decimal.NewFromInt(3), TransferDate: time.Now(),
	})
	if err == nil {
		t.Fatal("expected error updating posted transfer")
	}
}

// ── Stock report tests ────────────────────────────────────────────────────────

func TestStockReport_ReflectsTransfer(t *testing.T) {
	db := testTransferDB(t)
	cid, itemID, wh1, wh2 := seedTransferFixture(t, db)
	seedStockInWarehouse(t, db, cid, itemID, wh1, 30, 5)

	tr, _ := CreateTransfer(db, cid, TransferInput{
		FromWarehouseID: wh1, ToWarehouseID: wh2, ItemID: itemID,
		Quantity: decimal.NewFromInt(10), TransferDate: time.Now(),
	})
	PostTransfer(db, cid, tr.ID, "actor@example.com", nil)

	report, err := GetStockReport(db, cid)
	if err != nil {
		t.Fatalf("GetStockReport: %v", err)
	}
	if len(report.Rows) != 2 {
		t.Errorf("expected 2 rows (WH1 + WH2), got %d", len(report.Rows))
	}

	// Find WH1 row.
	var wh1Row, wh2Row *StockRow
	for i := range report.Rows {
		r := &report.Rows[i]
		if r.WarehouseID != nil && *r.WarehouseID == wh1 {
			wh1Row = r
		}
		if r.WarehouseID != nil && *r.WarehouseID == wh2 {
			wh2Row = r
		}
	}
	if wh1Row == nil || wh2Row == nil {
		t.Fatalf("missing expected rows; got %+v", report.Rows)
	}
	if !wh1Row.QuantityOnHand.Equal(decimal.NewFromInt(20)) {
		t.Errorf("WH1 qty = %s, want 20", wh1Row.QuantityOnHand)
	}
	if !wh2Row.QuantityOnHand.Equal(decimal.NewFromInt(10)) {
		t.Errorf("WH2 qty = %s, want 10", wh2Row.QuantityOnHand)
	}
	_ = itemID
}
