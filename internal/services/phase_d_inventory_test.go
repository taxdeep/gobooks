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

// ── Test DB setup ──────────────────────────────────────────────────────────────

func testPhaseDDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:phd_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Vendor{},
		&models.Account{},
		&models.TaxCode{},
		&models.ProductService{},
		&models.Warehouse{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.Bill{},
		&models.BillLine{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.InventoryMovement{},
		&models.InventoryBalance{},
		&models.InventoryCostLayer{},
		&models.InventoryLayerConsumption{},
		&models.AuditLog{},
		&models.PaymentTransaction{},
		&models.SettlementAllocation{},
		&models.TaskInvoiceSource{},
	)
	return db
}

type phaseDSetup struct {
	companyID   uint
	customerID  uint
	vendorID    uint
	stockItemID uint
	expenseID   uint
	whAID       uint // Warehouse A
	whBID       uint // Warehouse B
}

func setupPhaseD(t *testing.T, db *gorm.DB) phaseDSetup {
	t.Helper()

	co := models.Company{Name: "PhasD Co", IsActive: true, BaseCurrencyCode: "CAD"}
	db.Create(&co)

	cust := models.Customer{CompanyID: co.ID, Name: "Customer", AddrStreet1: "1 St"}
	db.Create(&cust)

	vend := models.Vendor{CompanyID: co.ID, Name: "Supplier"}
	db.Create(&vend)

	ar := models.Account{CompanyID: co.ID, Code: "1100", Name: "AR", RootAccountType: models.RootAsset, DetailAccountType: models.DetailAccountsReceivable, IsActive: true}
	db.Create(&ar)

	ap := models.Account{CompanyID: co.ID, Code: "2100", Name: "AP", RootAccountType: models.RootLiability, DetailAccountType: models.DetailAccountsPayable, IsActive: true}
	db.Create(&ap)

	rev := models.Account{CompanyID: co.ID, Code: "4000", Name: "Revenue", RootAccountType: models.RootRevenue, DetailAccountType: "sales_revenue", IsActive: true}
	db.Create(&rev)

	cogs := models.Account{CompanyID: co.ID, Code: "5000", Name: "COGS", RootAccountType: models.RootCostOfSales, DetailAccountType: models.DetailCostOfGoodsSold, IsActive: true}
	db.Create(&cogs)

	invAsset := models.Account{CompanyID: co.ID, Code: "1300", Name: "Inventory", RootAccountType: models.RootAsset, DetailAccountType: models.DetailInventory, IsActive: true}
	db.Create(&invAsset)

	expense := models.Account{CompanyID: co.ID, Code: "6000", Name: "Expense", RootAccountType: models.RootExpense, DetailAccountType: "operating_expense", IsActive: true}
	db.Create(&expense)

	stockItem := models.ProductService{
		CompanyID: co.ID, Name: "Widget", Type: models.ProductServiceTypeInventory,
		RevenueAccountID: rev.ID, COGSAccountID: &cogs.ID, InventoryAccountID: &invAsset.ID,
		DefaultPrice: decimal.NewFromInt(100), IsActive: true,
	}
	stockItem.ApplyTypeDefaults()
	db.Create(&stockItem)

	whA := models.Warehouse{CompanyID: co.ID, Code: "WH-A", Name: "Warehouse A", IsDefault: false, IsActive: true}
	db.Create(&whA)

	whB := models.Warehouse{CompanyID: co.ID, Code: "WH-B", Name: "Warehouse B", IsDefault: false, IsActive: true}
	db.Create(&whB)

	return phaseDSetup{
		companyID:   co.ID,
		customerID:  cust.ID,
		vendorID:    vend.ID,
		stockItemID: stockItem.ID,
		expenseID:   expense.ID,
		whAID:       whA.ID,
		whBID:       whB.ID,
	}
}

// ── ResolveInventoryWarehouse ─────────────────────────────────────────────────

func TestResolveInventoryWarehouse_UseDocWarehouseWhenSet(t *testing.T) {
	db := testPhaseDDB(t)
	s := setupPhaseD(t, db)

	// Set WH-A as default
	db.Model(&models.Warehouse{}).Where("id = ?", s.whAID).Update("is_default", true)

	docWH := s.whBID
	got := ResolveInventoryWarehouse(db, s.companyID, &docWH)
	if got == nil || *got != s.whBID {
		t.Errorf("Expected doc warehouse %d, got %v", s.whBID, got)
	}
}

func TestResolveInventoryWarehouse_FallsBackToDefault(t *testing.T) {
	db := testPhaseDDB(t)
	s := setupPhaseD(t, db)

	// Set WH-A as default
	db.Model(&models.Warehouse{}).Where("id = ?", s.whAID).Update("is_default", true)

	got := ResolveInventoryWarehouse(db, s.companyID, nil)
	if got == nil || *got != s.whAID {
		t.Errorf("Expected default warehouse %d, got %v", s.whAID, got)
	}
}

func TestResolveInventoryWarehouse_ReturnsNilWhenNoDefault(t *testing.T) {
	db := testPhaseDDB(t)
	s := setupPhaseD(t, db)
	// No default warehouse set — both WH-A and WH-B have is_default=false

	got := ResolveInventoryWarehouse(db, s.companyID, nil)
	if got != nil {
		t.Errorf("Expected nil (legacy path), got %v", *got)
	}
}

// ── Bill posting routes to warehouse ──────────────────────────────────────────

func TestPostBill_RoutesToDocWarehouse(t *testing.T) {
	db := testPhaseDDB(t)
	s := setupPhaseD(t, db)

	// Seed 50 units into WH-A directly (using costing engine inbound).
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

	// Create bill pointing at WH-B.
	bill := models.Bill{
		CompanyID: s.companyID, BillNumber: "BILL-WHB",
		VendorID: s.vendorID, BillDate: time.Now(),
		Status:      models.BillStatusDraft,
		WarehouseID: &s.whBID,
		Subtotal:    decimal.NewFromInt(200), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(200), BalanceDue: decimal.NewFromInt(200),
	}
	db.Create(&bill)

	expID := s.expenseID
	db.Create(&models.BillLine{
		CompanyID: s.companyID, BillID: bill.ID, SortOrder: 1,
		ProductServiceID: &s.stockItemID, ExpenseAccountID: &expID,
		Description: "Widget", Qty: decimal.NewFromInt(20), UnitPrice: decimal.NewFromInt(10),
		LineNet: decimal.NewFromInt(200), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(200),
	})

	if err := PostBill(db, s.companyID, bill.ID, "test", nil); err != nil {
		t.Fatalf("PostBill failed: %v", err)
	}

	// Movement should have WarehouseID = WH-B.
	var mov models.InventoryMovement
	db.Where("source_type = ? AND source_id = ?", "bill", bill.ID).First(&mov)
	if mov.WarehouseID == nil || *mov.WarehouseID != s.whBID {
		t.Errorf("Expected movement warehouse=%d, got %v", s.whBID, mov.WarehouseID)
	}

	// WH-B balance should be 20 units.
	var whBBal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?", s.companyID, s.stockItemID, s.whBID).First(&whBBal)
	if !whBBal.QuantityOnHand.Equal(decimal.NewFromInt(20)) {
		t.Errorf("WH-B balance expected 20, got %s", whBBal.QuantityOnHand)
	}

	// WH-A balance should be unchanged at 50.
	var whABal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?", s.companyID, s.stockItemID, s.whAID).First(&whABal)
	if !whABal.QuantityOnHand.Equal(decimal.NewFromInt(50)) {
		t.Errorf("WH-A balance should remain 50, got %s", whABal.QuantityOnHand)
	}
}

func TestPostBill_FallsBackToDefaultWarehouse(t *testing.T) {
	db := testPhaseDDB(t)
	s := setupPhaseD(t, db)

	// Set WH-A as default.
	db.Model(&models.Warehouse{}).Where("id = ?", s.whAID).Update("is_default", true)

	// Bill without WarehouseID — should route to company default (WH-A).
	bill := models.Bill{
		CompanyID: s.companyID, BillNumber: "BILL-DEF",
		VendorID: s.vendorID, BillDate: time.Now(),
		Status:   models.BillStatusDraft,
		Subtotal: decimal.NewFromInt(100), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(100), BalanceDue: decimal.NewFromInt(100),
	}
	db.Create(&bill)

	expID := s.expenseID
	db.Create(&models.BillLine{
		CompanyID: s.companyID, BillID: bill.ID, SortOrder: 1,
		ProductServiceID: &s.stockItemID, ExpenseAccountID: &expID,
		Description: "Widget", Qty: decimal.NewFromInt(10), UnitPrice: decimal.NewFromInt(10),
		LineNet: decimal.NewFromInt(100), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(100),
	})

	if err := PostBill(db, s.companyID, bill.ID, "test", nil); err != nil {
		t.Fatalf("PostBill failed: %v", err)
	}

	// Movement should route to WH-A (default).
	var mov models.InventoryMovement
	db.Where("source_type = ? AND source_id = ?", "bill", bill.ID).First(&mov)
	if mov.WarehouseID == nil || *mov.WarehouseID != s.whAID {
		t.Errorf("Expected movement warehouse=%d (default), got %v", s.whAID, mov.WarehouseID)
	}
}

// ── Invoice posting routes to warehouse ───────────────────────────────────────

func TestPostInvoice_RoutesToDocWarehouse(t *testing.T) {
	db := testPhaseDDB(t)
	s := setupPhaseD(t, db)

	// Seed 50 units into WH-A.
	engine, _ := ResolveCostingEngineForCompany(db, s.companyID)
	engine.ApplyInbound(db, InboundRequest{
		CompanyID:    s.companyID,
		ItemID:       s.stockItemID,
		Quantity:     decimal.NewFromInt(50),
		UnitCost:     decimal.NewFromInt(20),
		MovementType: models.MovementTypePurchase,
		WarehouseID:  &s.whAID,
		Date:         time.Now(),
	})

	// Invoice pointing at WH-A.
	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-WH-A",
		CustomerID: s.customerID, InvoiceDate: time.Now(),
		Status:               models.InvoiceStatusDraft,
		WarehouseID:          &s.whAID,
		Subtotal:             decimal.NewFromInt(1000), TaxTotal: decimal.Zero,
		Amount:               decimal.NewFromInt(1000), BalanceDue: decimal.NewFromInt(1000),
		CustomerNameSnapshot: "Customer",
	}
	db.Create(&inv)

	db.Create(&models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &s.stockItemID, Description: "Widget",
		Qty: decimal.NewFromInt(10), UnitPrice: decimal.NewFromInt(100),
		LineNet: decimal.NewFromInt(1000), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(1000),
	})

	if err := PostInvoice(db, s.companyID, inv.ID, "test", nil); err != nil {
		t.Fatalf("PostInvoice failed: %v", err)
	}

	// Movement should have WarehouseID = WH-A.
	var mov models.InventoryMovement
	db.Where("source_type = ? AND source_id = ?", "invoice", inv.ID).First(&mov)
	if mov.WarehouseID == nil || *mov.WarehouseID != s.whAID {
		t.Errorf("Expected movement warehouse=%d, got %v", s.whAID, mov.WarehouseID)
	}

	// WH-A balance should be 40 (50 - 10).
	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ? AND warehouse_id = ?", s.companyID, s.stockItemID, s.whAID).First(&bal)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(40)) {
		t.Errorf("WH-A balance expected 40, got %s", bal.QuantityOnHand)
	}
}

func TestPostInvoice_InsufficientStockInDocWarehouse(t *testing.T) {
	db := testPhaseDDB(t)
	s := setupPhaseD(t, db)

	// Seed 50 units into WH-B only — not WH-A.
	engine, _ := ResolveCostingEngineForCompany(db, s.companyID)
	engine.ApplyInbound(db, InboundRequest{
		CompanyID:    s.companyID,
		ItemID:       s.stockItemID,
		Quantity:     decimal.NewFromInt(50),
		UnitCost:     decimal.NewFromInt(20),
		MovementType: models.MovementTypePurchase,
		WarehouseID:  &s.whBID,
		Date:         time.Now(),
	})

	// Invoice pointing at WH-A (empty).
	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-WH-A-EMPTY",
		CustomerID: s.customerID, InvoiceDate: time.Now(),
		Status:               models.InvoiceStatusDraft,
		WarehouseID:          &s.whAID,
		Subtotal:             decimal.NewFromInt(1000), TaxTotal: decimal.Zero,
		Amount:               decimal.NewFromInt(1000), BalanceDue: decimal.NewFromInt(1000),
		CustomerNameSnapshot: "Customer",
	}
	db.Create(&inv)

	db.Create(&models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &s.stockItemID, Description: "Widget",
		Qty: decimal.NewFromInt(10), UnitPrice: decimal.NewFromInt(100),
		LineNet: decimal.NewFromInt(1000), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(1000),
	})

	err := PostInvoice(db, s.companyID, inv.ID, "test", nil)
	if err == nil {
		t.Fatal("Expected insufficient stock error for WH-A")
	}
}
