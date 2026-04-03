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

func testInventoryPostingDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:invpost_%s?mode=memory&cache=shared", t.Name())
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
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.Bill{},
		&models.BillLine{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.InventoryMovement{},
		&models.InventoryBalance{},
		&models.AuditLog{},
		&models.PaymentTransaction{},   // required by VoidInvoice payment-transaction guard
		&models.SettlementAllocation{}, // required by VoidInvoice settlement-allocation guard
	)
	return db
}

type invPostingSetup struct {
	companyID   uint
	customerID  uint
	vendorID    uint
	arAcctID    uint
	apAcctID    uint
	revAcctID   uint
	cogsAcctID  uint
	invAssetID  uint
	expenseID   uint
	stockItemID uint
	svcItemID   uint
}

func setupInventoryPosting(t *testing.T, db *gorm.DB) invPostingSetup {
	t.Helper()

	co := models.Company{Name: "Test Co", IsActive: true, BaseCurrencyCode: "CAD"}
	db.Create(&co)

	cust := models.Customer{CompanyID: co.ID, Name: "Customer", AddrStreet1: "123 St"}
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

	expense := models.Account{CompanyID: co.ID, Code: "6000", Name: "Office Supplies", RootAccountType: models.RootExpense, DetailAccountType: "operating_expense", IsActive: true}
	db.Create(&expense)

	// Stock item (inventory type)
	stockItem := models.ProductService{
		CompanyID: co.ID, Name: "Widget", Type: models.ProductServiceTypeInventory,
		RevenueAccountID: rev.ID, COGSAccountID: &cogs.ID, InventoryAccountID: &invAsset.ID,
		DefaultPrice: decimal.NewFromInt(100), IsActive: true,
	}
	stockItem.ApplyTypeDefaults()
	db.Create(&stockItem)

	// Service item (non-stock)
	svcItem := models.ProductService{
		CompanyID: co.ID, Name: "Consulting", Type: models.ProductServiceTypeService,
		RevenueAccountID: rev.ID, IsActive: true,
	}
	svcItem.ApplyTypeDefaults()
	db.Create(&svcItem)

	return invPostingSetup{
		companyID: co.ID, customerID: cust.ID, vendorID: vend.ID,
		arAcctID: ar.ID, apAcctID: ap.ID, revAcctID: rev.ID,
		cogsAcctID: cogs.ID, invAssetID: invAsset.ID, expenseID: expense.ID,
		stockItemID: stockItem.ID, svcItemID: svcItem.ID,
	}
}

// ── Invoice COGS tests ───────────────────────────────────────────────────────

func TestPostInvoice_StockItem_CreatesCOGSAndMovement(t *testing.T) {
	db := testInventoryPostingDB(t)
	s := setupInventoryPosting(t, db)

	// Seed opening balance: 50 units @ $20
	CreateOpeningBalance(db, OpeningBalanceInput{
		CompanyID: s.companyID, ItemID: s.stockItemID,
		Quantity: decimal.NewFromInt(50), UnitCost: decimal.NewFromInt(20),
		AsOfDate: time.Now(),
	})

	// Create invoice with 10 stock items at $100 each
	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-STOCK-1",
		CustomerID: s.customerID, InvoiceDate: time.Now(),
		Status: models.InvoiceStatusDraft,
		Subtotal: decimal.NewFromInt(1000), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(1000), BalanceDue: decimal.NewFromInt(1000),
		CustomerNameSnapshot: "Customer",
	}
	db.Create(&inv)

	line := models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &s.stockItemID, Description: "Widget",
		Qty: decimal.NewFromInt(10), UnitPrice: decimal.NewFromInt(100),
		LineNet: decimal.NewFromInt(1000), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(1000),
	}
	db.Create(&line)

	// Post invoice
	err := PostInvoice(db, s.companyID, inv.ID, "test", nil)
	if err != nil {
		t.Fatalf("PostInvoice failed: %v", err)
	}

	// Verify COGS journal lines exist
	var jeLines []models.JournalLine
	db.Where("company_id = ?", s.companyID).Find(&jeLines)

	foundCOGS := false
	foundInvAsset := false
	for _, jl := range jeLines {
		if jl.AccountID == s.cogsAcctID && jl.Debit.IsPositive() {
			foundCOGS = true
			// COGS should be 10 × $20 avg cost = $200
			if !jl.Debit.Equal(decimal.NewFromInt(200)) {
				t.Errorf("COGS debit expected 200, got %s", jl.Debit)
			}
		}
		if jl.AccountID == s.invAssetID && jl.Credit.IsPositive() {
			foundInvAsset = true
			if !jl.Credit.Equal(decimal.NewFromInt(200)) {
				t.Errorf("Inventory credit expected 200, got %s", jl.Credit)
			}
		}
	}
	if !foundCOGS {
		t.Error("COGS journal line not found")
	}
	if !foundInvAsset {
		t.Error("Inventory asset credit journal line not found")
	}

	// Verify inventory movement
	var movs []models.InventoryMovement
	db.Where("company_id = ? AND source_type = ?", s.companyID, "invoice").Find(&movs)
	if len(movs) != 1 {
		t.Fatalf("Expected 1 sale movement, got %d", len(movs))
	}
	if !movs[0].QuantityDelta.Equal(decimal.NewFromInt(-10)) {
		t.Errorf("Movement qty expected -10, got %s", movs[0].QuantityDelta)
	}

	// Verify inventory balance updated
	bal, _ := GetBalance(db, s.companyID, s.stockItemID)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(40)) {
		t.Errorf("Balance expected 40, got %s", bal.QuantityOnHand)
	}
}

func TestPostInvoice_InsufficientStock_Blocked(t *testing.T) {
	db := testInventoryPostingDB(t)
	s := setupInventoryPosting(t, db)

	// Seed only 5 units
	CreateOpeningBalance(db, OpeningBalanceInput{
		CompanyID: s.companyID, ItemID: s.stockItemID,
		Quantity: decimal.NewFromInt(5), UnitCost: decimal.NewFromInt(20),
		AsOfDate: time.Now(),
	})

	// Invoice wants 10
	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-NOSTOCK",
		CustomerID: s.customerID, InvoiceDate: time.Now(),
		Status: models.InvoiceStatusDraft,
		Subtotal: decimal.NewFromInt(1000), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(1000), BalanceDue: decimal.NewFromInt(1000),
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
		t.Fatal("Expected insufficient stock error")
	}

	// Verify no JE was created
	var jeCount int64
	db.Model(&models.JournalEntry{}).Where("company_id = ?", s.companyID).Count(&jeCount)
	if jeCount != 0 {
		t.Error("No JE should be created when stock is insufficient")
	}

	// Balance unchanged
	bal, _ := GetBalance(db, s.companyID, s.stockItemID)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(5)) {
		t.Errorf("Balance should remain 5, got %s", bal.QuantityOnHand)
	}
}

func TestPostInvoice_ServiceItem_NoMovement(t *testing.T) {
	db := testInventoryPostingDB(t)
	s := setupInventoryPosting(t, db)

	// Invoice with service item only (no stock)
	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-SVC",
		CustomerID: s.customerID, InvoiceDate: time.Now(),
		Status: models.InvoiceStatusDraft,
		Subtotal: decimal.NewFromInt(500), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(500), BalanceDue: decimal.NewFromInt(500),
		CustomerNameSnapshot: "Customer",
	}
	db.Create(&inv)
	db.Create(&models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &s.svcItemID, Description: "Consulting",
		Qty: decimal.NewFromInt(5), UnitPrice: decimal.NewFromInt(100),
		LineNet: decimal.NewFromInt(500), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(500),
	})

	err := PostInvoice(db, s.companyID, inv.ID, "test", nil)
	if err != nil {
		t.Fatalf("PostInvoice failed: %v", err)
	}

	// No inventory movement for service items
	var movCount int64
	db.Model(&models.InventoryMovement{}).Where("company_id = ?", s.companyID).Count(&movCount)
	if movCount != 0 {
		t.Error("Service items should not create inventory movements")
	}
}

// ── Bill inventory tests ─────────────────────────────────────────────────────

func TestPostBill_StockItem_CreatesMovementAndUpdatesBalance(t *testing.T) {
	db := testInventoryPostingDB(t)
	s := setupInventoryPosting(t, db)

	// Seed initial balance: 10 @ $15
	CreateOpeningBalance(db, OpeningBalanceInput{
		CompanyID: s.companyID, ItemID: s.stockItemID,
		Quantity: decimal.NewFromInt(10), UnitCost: decimal.NewFromInt(15),
		AsOfDate: time.Now(),
	})

	// Create bill with 20 stock items at $25 each
	bill := models.Bill{
		CompanyID: s.companyID, BillNumber: "BILL-STOCK-1",
		VendorID: s.vendorID, BillDate: time.Now(),
		Status: models.BillStatusDraft,
		Subtotal: decimal.NewFromInt(500), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(500), BalanceDue: decimal.NewFromInt(500),
	}
	db.Create(&bill)

	expID := s.expenseID // expense account on line (will be redirected to inventory)
	db.Create(&models.BillLine{
		CompanyID: s.companyID, BillID: bill.ID, SortOrder: 1,
		ProductServiceID: &s.stockItemID, ExpenseAccountID: &expID,
		Description: "Widget", Qty: decimal.NewFromInt(20), UnitPrice: decimal.NewFromInt(25),
		LineNet: decimal.NewFromInt(500), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(500),
	})

	err := PostBill(db, s.companyID, bill.ID, "test", nil)
	if err != nil {
		t.Fatalf("PostBill failed: %v", err)
	}

	// Verify inventory movement
	var movs []models.InventoryMovement
	db.Where("company_id = ? AND source_type = ?", s.companyID, "bill").Find(&movs)
	if len(movs) != 1 {
		t.Fatalf("Expected 1 purchase movement, got %d", len(movs))
	}
	if !movs[0].QuantityDelta.Equal(decimal.NewFromInt(20)) {
		t.Errorf("Movement qty expected +20, got %s", movs[0].QuantityDelta)
	}

	// Verify balance: 10 + 20 = 30, avg = (10*15 + 20*25) / 30 = 650/30 ≈ 21.6667
	bal, _ := GetBalance(db, s.companyID, s.stockItemID)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(30)) {
		t.Errorf("Balance qty expected 30, got %s", bal.QuantityOnHand)
	}
	expectedAvg := decimal.NewFromInt(650).Div(decimal.NewFromInt(30)).RoundBank(4)
	if !bal.AverageCost.Equal(expectedAvg) {
		t.Errorf("Avg cost expected %s, got %s", expectedAvg, bal.AverageCost)
	}

	// Verify JE debits Inventory Asset (not Expense)
	var jeLines []models.JournalLine
	db.Where("company_id = ?", s.companyID).Find(&jeLines)
	foundInvDebit := false
	for _, jl := range jeLines {
		if jl.AccountID == s.invAssetID && jl.Debit.IsPositive() {
			foundInvDebit = true
		}
		// Should NOT debit expense for inventory items
		if jl.AccountID == s.expenseID && jl.Debit.IsPositive() {
			t.Error("Expense account should not be debited for inventory items")
		}
	}
	if !foundInvDebit {
		t.Error("Inventory asset debit not found in JE")
	}
}

func TestPostBill_NonInventoryItem_NoMovement(t *testing.T) {
	db := testInventoryPostingDB(t)
	s := setupInventoryPosting(t, db)

	// Bill with service item (non-stock)
	bill := models.Bill{
		CompanyID: s.companyID, BillNumber: "BILL-SVC",
		VendorID: s.vendorID, BillDate: time.Now(),
		Status: models.BillStatusDraft,
		Subtotal: decimal.NewFromInt(300), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(300), BalanceDue: decimal.NewFromInt(300),
	}
	db.Create(&bill)

	expID := s.expenseID
	db.Create(&models.BillLine{
		CompanyID: s.companyID, BillID: bill.ID, SortOrder: 1,
		ProductServiceID: &s.svcItemID, ExpenseAccountID: &expID,
		Description: "Consulting", Qty: decimal.NewFromInt(3), UnitPrice: decimal.NewFromInt(100),
		LineNet: decimal.NewFromInt(300), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(300),
	})

	err := PostBill(db, s.companyID, bill.ID, "test", nil)
	if err != nil {
		t.Fatalf("PostBill failed: %v", err)
	}

	var movCount int64
	db.Model(&models.InventoryMovement{}).Where("company_id = ?", s.companyID).Count(&movCount)
	if movCount != 0 {
		t.Error("Service items should not create inventory movements on bill posting")
	}
}

// ── Consistency tests ────────────────────────────────────────────────────────

func TestPostInvoice_COGSAmountMatchesMovement(t *testing.T) {
	db := testInventoryPostingDB(t)
	s := setupInventoryPosting(t, db)

	// 100 units @ $30 avg
	CreateOpeningBalance(db, OpeningBalanceInput{
		CompanyID: s.companyID, ItemID: s.stockItemID,
		Quantity: decimal.NewFromInt(100), UnitCost: decimal.NewFromInt(30),
		AsOfDate: time.Now(),
	})

	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-CONSIST",
		CustomerID: s.customerID, InvoiceDate: time.Now(),
		Status: models.InvoiceStatusDraft,
		Subtotal: decimal.NewFromInt(2000), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(2000), BalanceDue: decimal.NewFromInt(2000),
		CustomerNameSnapshot: "Customer",
	}
	db.Create(&inv)
	db.Create(&models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &s.stockItemID, Description: "Widget",
		Qty: decimal.NewFromInt(20), UnitPrice: decimal.NewFromInt(100),
		LineNet: decimal.NewFromInt(2000), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(2000),
	})

	PostInvoice(db, s.companyID, inv.ID, "test", nil)

	// COGS JE amount should equal movement total_cost
	var mov models.InventoryMovement
	db.Where("company_id = ? AND source_type = ?", s.companyID, "invoice").First(&mov)

	var cogsLine models.JournalLine
	db.Where("company_id = ? AND account_id = ?", s.companyID, s.cogsAcctID).First(&cogsLine)

	// 20 × $30 = $600
	expectedCOGS := decimal.NewFromInt(600)
	if !cogsLine.Debit.Equal(expectedCOGS) {
		t.Errorf("COGS debit: expected %s, got %s", expectedCOGS, cogsLine.Debit)
	}
	if mov.TotalCost == nil || !mov.TotalCost.Equal(expectedCOGS) {
		t.Errorf("Movement total_cost: expected %s, got %v", expectedCOGS, mov.TotalCost)
	}
}
