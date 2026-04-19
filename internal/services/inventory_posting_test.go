// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"gobooks/internal/models"
	"gobooks/internal/services/inventory"
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
		&models.InventoryCostLayer{},
		&models.InventoryLayerConsumption{}, // Phase E2: ReceiveStock writes a layer per receipt
		&models.AuditLog{},
		&models.PaymentTransaction{},    // required by VoidInvoice payment-transaction guard
		&models.SettlementAllocation{},  // required by VoidInvoice settlement-allocation guard
		&models.CreditNoteApplication{}, // required by VoidInvoice credit-application reversal
		&models.APCreditApplication{},   // required by VoidBill credit-application reversal
		&models.TaskInvoiceSource{},     // required by task invoice source release hook
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

// Authoritative cost invariant: after PostInvoice, the JE COGS debit must
// EXACTLY equal the inventory movement's UnitCostBase × |QuantityDelta|.
//
// Pre-E0.2, COGS was computed from GetCostingPreview (read-before-apply)
// and the inventory movement used a fresh read inside IssueStock. Any
// concurrent balance shift between those two reads produced a sub-cent
// divergence — the JE said $X and the movement said $X±0.01, which is a
// real accounting-truth bug.
//
// E0.2 flips the flow: IssueStock runs first, the returned UnitCostBase
// drives the JE. This test locks that invariant in place.
func TestPostInvoice_COGSAgreesWithMovementUnitCostBase(t *testing.T) {
	db := testInventoryPostingDB(t)
	s := setupInventoryPosting(t, db)

	// Two opening receipts at different prices exercise the weighted-avg
	// blend so UnitCostBase is a non-trivial decimal (not the same as any
	// single receipt price).
	CreateOpeningBalance(db, OpeningBalanceInput{
		CompanyID: s.companyID, ItemID: s.stockItemID,
		Quantity: decimal.NewFromInt(10), UnitCost: decimal.RequireFromString("4.25"),
		AsOfDate: time.Now(),
	})
	// Can't CreateOpeningBalance twice for same item, so emit a second
	// receipt via ReceiveStock directly with a distinct source id.
	_, err := inventory.ReceiveStock(db, inventory.ReceiveStockInput{
		CompanyID: s.companyID, ItemID: s.stockItemID, WarehouseID: 0,
		Quantity:     decimal.NewFromInt(30),
		MovementDate: time.Now(),
		UnitCost:     decimal.RequireFromString("4.75"),
		ExchangeRate: decimal.NewFromInt(1),
		SourceType:   "bill", SourceID: 9999,
		IdempotencyKey: "test-seed-second-receipt",
	})
	if err != nil {
		t.Fatalf("seed second receipt: %v", err)
	}
	// Blended avg = (10×4.25 + 30×4.75) / 40 = 185 / 40 = 4.625

	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-AUTH-COGS",
		CustomerID: s.customerID, InvoiceDate: time.Now(),
		Status:   models.InvoiceStatusDraft,
		Subtotal: decimal.NewFromInt(700), TaxTotal: decimal.Zero,
		Amount:   decimal.NewFromInt(700), BalanceDue: decimal.NewFromInt(700),
		CustomerNameSnapshot: "Customer",
	}
	db.Create(&inv)
	db.Create(&models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &s.stockItemID, Description: "Widget",
		Qty:              decimal.NewFromInt(7),
		UnitPrice:        decimal.NewFromInt(100),
		LineNet:          decimal.NewFromInt(700),
		LineTax:          decimal.Zero,
		LineTotal:        decimal.NewFromInt(700),
	})

	if err := PostInvoice(db, s.companyID, inv.ID, "test", nil); err != nil {
		t.Fatalf("PostInvoice: %v", err)
	}

	// Load the sale movement.
	var mov models.InventoryMovement
	if err := db.Where("company_id = ? AND source_type = ?", s.companyID, "invoice").
		First(&mov).Error; err != nil {
		t.Fatalf("load sale movement: %v", err)
	}
	if mov.UnitCostBase == nil {
		t.Fatalf("movement missing UnitCostBase")
	}

	// Load the COGS journal line.
	var cogsLine models.JournalLine
	if err := db.Where("company_id = ? AND account_id = ?", s.companyID, s.cogsAcctID).
		First(&cogsLine).Error; err != nil {
		t.Fatalf("load COGS line: %v", err)
	}

	// Invariant: COGS debit == |QuantityDelta| × UnitCostBase, rounded per
	// the JE rounding convention (RoundBank(2)). Because BuildCOGSFragments
	// uses exactly these inputs from the CreateSaleMovements result, the
	// numbers MUST match exactly.
	absQty := mov.QuantityDelta.Abs()
	expectedCOGS := absQty.Mul(*mov.UnitCostBase).RoundBank(2)
	if !cogsLine.Debit.Equal(expectedCOGS) {
		t.Fatalf(
			"COGS debit %s must equal |qty|(%s) × unit_cost_base(%s) = %s",
			cogsLine.Debit, absQty, mov.UnitCostBase, expectedCOGS,
		)
	}

	// Cross-check against the Inventory Asset credit for double-entry
	// symmetry within the COGS sub-pair.
	var invLine models.JournalLine
	if err := db.Where("company_id = ? AND account_id = ?", s.companyID, s.invAssetID).
		First(&invLine).Error; err != nil {
		t.Fatalf("load inventory asset line: %v", err)
	}
	if !invLine.Credit.Equal(cogsLine.Debit) {
		t.Fatalf("inventory asset credit %s must mirror COGS debit %s",
			invLine.Credit, cogsLine.Debit)
	}
}

// Phase G.2: invoice preview rejects tracked items up-front instead of
// letting IssueStock's tracking guard bubble a raw sentinel at post
// time. Operator sees a remediation-actionable error mentioning the
// Phase I shipment-driven flow, not an opaque ErrSerialSelectionMissing.
func TestValidateStockForInvoice_RejectsTrackedSingleLine(t *testing.T) {
	db := testInventoryPostingDB(t)
	s := setupInventoryPosting(t, db)

	// Flip the stock item to serial tracking. Capability gate is not
	// exercised here — ValidateStockForInvoice reads tracking_mode
	// directly; the gate polices mode CHANGES, not already-tracked
	// items coming through the preview.
	db.Model(&models.ProductService{}).
		Where("id = ?", s.stockItemID).
		Update("tracking_mode", models.TrackingSerial)

	// Reload so the in-memory ProductService in the invoice line carries
	// the tracking_mode value.
	var item models.ProductService
	db.First(&item, s.stockItemID)

	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-TRACKED",
		CustomerID: s.customerID, InvoiceDate: time.Now(),
		Status:   models.InvoiceStatusDraft,
		Amount:   decimal.NewFromInt(100), BalanceDue: decimal.NewFromInt(100),
		CustomerNameSnapshot: "Customer",
	}
	db.Create(&inv)
	line := models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &s.stockItemID, ProductService: &item,
		Description: "Tracked widget",
		Qty:         decimal.NewFromInt(1),
		UnitPrice:   decimal.NewFromInt(100),
		LineNet:     decimal.NewFromInt(100),
		LineTotal:   decimal.NewFromInt(100),
	}
	_, _, err := ValidateStockForInvoice(db, s.companyID, []models.InvoiceLine{line}, nil)
	if !errors.Is(err, ErrTrackedItemNotSupportedByInvoice) {
		t.Fatalf("got %v, want ErrTrackedItemNotSupportedByInvoice", err)
	}
}

// Phase G.4 — Bill line with lot_number on a lot-tracked item posts
// successfully and creates the corresponding inventory_lots row.
// End-to-end: Bill line tracking data → CreatePurchaseMovements →
// ReceiveStock → inventory_lots.
func TestCreatePurchaseMovements_LotTrackedBillLine_PersistsLot(t *testing.T) {
	db := testInventoryPostingDB(t)
	if err := db.AutoMigrate(&models.InventoryLot{}); err != nil {
		t.Fatalf("automigrate lots: %v", err)
	}
	s := setupInventoryPosting(t, db)

	// Flip stock item to lot tracking + enable company capability.
	db.Model(&models.Company{}).Where("id = ?", s.companyID).
		Update("tracking_enabled", true)
	db.Model(&models.ProductService{}).Where("id = ?", s.stockItemID).
		Update("tracking_mode", models.TrackingLot)

	// Reload item so the tracking_mode is carried into the Bill row.
	var item models.ProductService
	db.First(&item, s.stockItemID)

	expiry := time.Date(2027, 6, 30, 0, 0, 0, 0, time.UTC)
	bill := models.Bill{
		CompanyID:  s.companyID,
		VendorID:   s.vendorID,
		BillNumber: "BILL-LOT",
		BillDate:   time.Now(),
		Status:     models.BillStatusDraft,
		Amount:     decimal.NewFromInt(50),
	}
	db.Create(&bill)
	line := models.BillLine{
		CompanyID: s.companyID, BillID: bill.ID, SortOrder: 1,
		ProductServiceID: &s.stockItemID, ProductService: &item,
		Description: "Lot-tracked widget",
		Qty:         decimal.NewFromInt(10),
		UnitPrice:   decimal.NewFromInt(5),
		LineNet:     decimal.NewFromInt(50),
		LineTotal:   decimal.NewFromInt(50),
		LotNumber:   "LOT-G4",
		LotExpiryDate: &expiry,
	}
	db.Create(&line)
	// Reload bill so Lines preloaded and carrying the new fields.
	db.Preload("Lines.ProductService").First(&bill, bill.ID)

	if err := CreatePurchaseMovements(db, s.companyID, bill, nil); err != nil {
		t.Fatalf("CreatePurchaseMovements: %v", err)
	}

	// inventory_lots row materialised with the captured data.
	var lots []models.InventoryLot
	db.Where("company_id = ? AND item_id = ?", s.companyID, s.stockItemID).Find(&lots)
	if len(lots) != 1 {
		t.Fatalf("lots: got %d want 1", len(lots))
	}
	if lots[0].LotNumber != "LOT-G4" {
		t.Fatalf("lot_number: got %q want LOT-G4", lots[0].LotNumber)
	}
	if lots[0].ExpiryDate == nil || !lots[0].ExpiryDate.Equal(expiry) {
		t.Fatalf("expiry: got %v want %v", lots[0].ExpiryDate, expiry)
	}
	if !lots[0].OriginalQuantity.Equal(decimal.NewFromInt(10)) ||
		!lots[0].RemainingQuantity.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("lot qtys: got orig=%s rem=%s want 10/10",
			lots[0].OriginalQuantity, lots[0].RemainingQuantity)
	}
}

// Phase G.4 — Bill line on a lot-tracked item WITHOUT lot_number fails
// loud at the inventory guard (ErrTrackingDataMissing bubbled up with
// a "receive stock for item X" wrapper). Locks the pre-existing F2
// guard as the backstop for the G.4 integration.
func TestCreatePurchaseMovements_LotTrackedBillLine_MissingLotRejected(t *testing.T) {
	db := testInventoryPostingDB(t)
	if err := db.AutoMigrate(&models.InventoryLot{}); err != nil {
		t.Fatalf("automigrate lots: %v", err)
	}
	s := setupInventoryPosting(t, db)

	db.Model(&models.Company{}).Where("id = ?", s.companyID).
		Update("tracking_enabled", true)
	db.Model(&models.ProductService{}).Where("id = ?", s.stockItemID).
		Update("tracking_mode", models.TrackingLot)
	var item models.ProductService
	db.First(&item, s.stockItemID)

	bill := models.Bill{
		CompanyID: s.companyID, VendorID: s.vendorID,
		BillNumber: "BILL-LOT-MISSING", BillDate: time.Now(),
		Status: models.BillStatusDraft, Amount: decimal.NewFromInt(50),
	}
	db.Create(&bill)
	db.Create(&models.BillLine{
		CompanyID: s.companyID, BillID: bill.ID, SortOrder: 1,
		ProductServiceID: &s.stockItemID, ProductService: &item,
		Description: "Lot-tracked widget no lot",
		Qty:         decimal.NewFromInt(10),
		UnitPrice:   decimal.NewFromInt(5),
		LineNet:     decimal.NewFromInt(50), LineTotal: decimal.NewFromInt(50),
		// LotNumber intentionally blank — bug the operator forgot to fill
	})
	db.Preload("Lines.ProductService").First(&bill, bill.ID)

	err := CreatePurchaseMovements(db, s.companyID, bill, nil)
	if err == nil {
		t.Fatalf("expected tracking-data-missing rejection")
	}
	if !errors.Is(err, inventory.ErrTrackingDataMissing) {
		t.Fatalf("got %v, want ErrTrackingDataMissing wrap", err)
	}
}

// Serial-tracked items via Bill are NOT supported in G.4. The guard
// lives at the inventory layer and locks the unsupported edge — any
// future first-class serial-via-bill work must explicitly opt in.
func TestCreatePurchaseMovements_SerialTrackedBillLine_RejectedAsUnsupported(t *testing.T) {
	db := testInventoryPostingDB(t)
	if err := db.AutoMigrate(&models.InventorySerialUnit{}); err != nil {
		t.Fatalf("automigrate serials: %v", err)
	}
	s := setupInventoryPosting(t, db)

	db.Model(&models.Company{}).Where("id = ?", s.companyID).
		Update("tracking_enabled", true)
	db.Model(&models.ProductService{}).Where("id = ?", s.stockItemID).
		Update("tracking_mode", models.TrackingSerial)
	var item models.ProductService
	db.First(&item, s.stockItemID)

	bill := models.Bill{
		CompanyID: s.companyID, VendorID: s.vendorID,
		BillNumber: "BILL-SERIAL", BillDate: time.Now(),
		Status: models.BillStatusDraft, Amount: decimal.NewFromInt(50),
	}
	db.Create(&bill)
	db.Create(&models.BillLine{
		CompanyID: s.companyID, BillID: bill.ID, SortOrder: 1,
		ProductServiceID: &s.stockItemID, ProductService: &item,
		Description: "Serial widget", Qty: decimal.NewFromInt(1),
		UnitPrice: decimal.NewFromInt(50), LineNet: decimal.NewFromInt(50), LineTotal: decimal.NewFromInt(50),
		// No serial number capture surface on BillLine (intentional).
	})
	db.Preload("Lines.ProductService").First(&bill, bill.ID)

	err := CreatePurchaseMovements(db, s.companyID, bill, nil)
	if !errors.Is(err, inventory.ErrTrackingDataMissing) {
		t.Fatalf("got %v, want ErrTrackingDataMissing for serial-via-bill", err)
	}
}

// A second call to CreatePurchaseMovements for the same bill (simulating a
// post → void → re-post cycle) must pick a fresh idempotency-key version
// and write a new movement instead of colliding with the first call's v1
// key. Exercises the fix for Phase D review issue P1.
func TestCreatePurchaseMovements_VersionsKeysOnRepost(t *testing.T) {
	db := testInventoryPostingDB(t)
	s := setupInventoryPosting(t, db)

	// Build a bill with one stock line so CreatePurchaseMovements has work
	// to do. The bill's Lines collection is what the facade iterates.
	bill := models.Bill{
		CompanyID: s.companyID, VendorID: s.vendorID,
		BillNumber: "BILL-REPOST", BillDate: time.Now(),
		Status: models.BillStatusDraft, Amount: decimal.NewFromInt(100),
	}
	db.Create(&bill)
	line := models.BillLine{
		CompanyID: s.companyID, BillID: bill.ID, SortOrder: 1,
		ProductServiceID: &s.stockItemID, Description: "Widget purchase",
		Qty: decimal.NewFromInt(5), UnitPrice: decimal.NewFromInt(20),
		LineNet: decimal.NewFromInt(100), LineTotal: decimal.NewFromInt(100),
	}
	db.Create(&line)
	// Re-load with association so ProductService is populated on each line.
	db.Preload("Lines.ProductService").First(&bill, bill.ID)

	// First "post" — movements land with :v1 keys.
	if err := CreatePurchaseMovements(db, s.companyID, bill, nil); err != nil {
		t.Fatalf("first CreatePurchaseMovements: %v", err)
	}

	// Second "post" — helper should pick :v2; should not collide.
	if err := CreatePurchaseMovements(db, s.companyID, bill, nil); err != nil {
		t.Fatalf("second CreatePurchaseMovements (simulated re-post): %v", err)
	}

	// Verify exactly two movements now exist, one per version.
	var keys []string
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ? AND source_id = ?",
			s.companyID, "bill", bill.ID).
		Pluck("idempotency_key", &keys)

	seenV1, seenV2 := false, false
	for _, k := range keys {
		switch k {
		case fmt.Sprintf("bill:%d:line:%d:v1", bill.ID, line.ID):
			seenV1 = true
		case fmt.Sprintf("bill:%d:line:%d:v2", bill.ID, line.ID):
			seenV2 = true
		}
	}
	if !seenV1 || !seenV2 {
		t.Fatalf("expected both v1 and v2 keys to be present; got: %v", keys)
	}
}
