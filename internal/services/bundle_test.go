// 遵循project_guide.md
package services

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testBundleDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:bundle_%s?mode=memory&cache=shared", t.Name())
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
		&models.ItemComponent{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.Bill{},
		&models.BillLine{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.AuditLog{},
		&models.InventoryMovement{},
		&models.InventoryBalance{},
		&models.InventoryCostLayer{},
		&models.InventoryLayerConsumption{},
		&models.PaymentTransaction{},   // required by VoidInvoice payment-transaction guard
		&models.SettlementAllocation{},   // required by VoidInvoice settlement-allocation guard
		&models.CreditNoteApplication{}, // required by VoidInvoice credit-application reversal
		&models.APCreditApplication{},   // required by VoidBill credit-application reversal
		&models.TaskInvoiceSource{},    // required by task invoice source release hook
	)
	return db
}

type bundleSetup struct {
	companyID    uint
	customerID   uint
	revAcctID    uint
	cogsAcctID   uint
	invAssetID   uint
	arAcctID     uint
	widgetID     uint // inventory item: component 1
	gadgetID     uint // inventory item: component 2
	bundleID     uint // bundle item
	serviceID    uint // service item (non-stock)
}

func setupBundle(t *testing.T, db *gorm.DB) bundleSetup {
	t.Helper()

	co := models.Company{Name: "Bundle Co", IsActive: true, BaseCurrencyCode: "CAD"}
	db.Create(&co)
	cust := models.Customer{CompanyID: co.ID, Name: "Customer", AddrStreet1: "1 St"}
	db.Create(&cust)

	ar := models.Account{CompanyID: co.ID, Code: "1100", Name: "AR", RootAccountType: models.RootAsset, DetailAccountType: models.DetailAccountsReceivable, IsActive: true}
	db.Create(&ar)
	rev := models.Account{CompanyID: co.ID, Code: "4000", Name: "Revenue", RootAccountType: models.RootRevenue, DetailAccountType: "sales_revenue", IsActive: true}
	db.Create(&rev)
	cogs := models.Account{CompanyID: co.ID, Code: "5000", Name: "COGS", RootAccountType: models.RootCostOfSales, DetailAccountType: models.DetailCostOfGoodsSold, IsActive: true}
	db.Create(&cogs)
	invAsset := models.Account{CompanyID: co.ID, Code: "1300", Name: "Inventory", RootAccountType: models.RootAsset, DetailAccountType: models.DetailInventory, IsActive: true}
	db.Create(&invAsset)

	// Component 1: Widget (inventory)
	widget := models.ProductService{
		CompanyID: co.ID, Name: "Widget", Type: models.ProductServiceTypeInventory,
		RevenueAccountID: rev.ID, COGSAccountID: &cogs.ID, InventoryAccountID: &invAsset.ID,
		DefaultPrice: decimal.NewFromInt(50), IsActive: true,
	}
	widget.ApplyTypeDefaults()
	db.Create(&widget)

	// Component 2: Gadget (inventory)
	gadget := models.ProductService{
		CompanyID: co.ID, Name: "Gadget", Type: models.ProductServiceTypeInventory,
		RevenueAccountID: rev.ID, COGSAccountID: &cogs.ID, InventoryAccountID: &invAsset.ID,
		DefaultPrice: decimal.NewFromInt(30), IsActive: true,
	}
	gadget.ApplyTypeDefaults()
	db.Create(&gadget)

	// Bundle item: Starter Kit (non-inventory, bundle structure)
	bundle := models.ProductService{
		CompanyID: co.ID, Name: "Starter Kit", Type: models.ProductServiceTypeNonInventory,
		ItemStructureType: models.ItemStructureBundle,
		RevenueAccountID:  rev.ID, DefaultPrice: decimal.NewFromInt(100),
		CanBeSold: true, IsStockItem: false, IsActive: true,
	}
	db.Create(&bundle)

	// Bundle components: 2 Widgets + 1 Gadget
	db.Create(&models.ItemComponent{CompanyID: co.ID, ParentItemID: bundle.ID, ComponentItemID: widget.ID, Quantity: decimal.NewFromInt(2), SortOrder: 1})
	db.Create(&models.ItemComponent{CompanyID: co.ID, ParentItemID: bundle.ID, ComponentItemID: gadget.ID, Quantity: decimal.NewFromInt(1), SortOrder: 2})

	// Service item
	svc := models.ProductService{
		CompanyID: co.ID, Name: "Support", Type: models.ProductServiceTypeService,
		RevenueAccountID: rev.ID, IsActive: true,
	}
	svc.ApplyTypeDefaults()
	db.Create(&svc)

	return bundleSetup{
		companyID: co.ID, customerID: cust.ID,
		revAcctID: rev.ID, cogsAcctID: cogs.ID, invAssetID: invAsset.ID, arAcctID: ar.ID,
		widgetID: widget.ID, gadgetID: gadget.ID, bundleID: bundle.ID, serviceID: svc.ID,
	}
}

// ── Validation tests ─────────────────────────────────────────────────────────

func TestBundleValidation_RequiresComponents(t *testing.T) {
	db := testBundleDB(t)
	s := setupBundle(t, db)

	err := ValidateBundleComponents(db, s.companyID, s.bundleID, nil)
	if err == nil {
		t.Fatal("Expected error for empty components")
	}
}

func TestBundleValidation_ComponentMustBeInventory(t *testing.T) {
	db := testBundleDB(t)
	s := setupBundle(t, db)

	comps := []models.ItemComponent{
		{ComponentItemID: s.serviceID, Quantity: decimal.NewFromInt(1)},
	}
	err := ValidateBundleComponents(db, s.companyID, s.bundleID, comps)
	if err == nil {
		t.Fatal("Expected error for service component")
	}
}

func TestBundleValidation_SelfReferenceBlocked(t *testing.T) {
	db := testBundleDB(t)
	s := setupBundle(t, db)

	comps := []models.ItemComponent{
		{ComponentItemID: s.bundleID, Quantity: decimal.NewFromInt(1)},
	}
	err := ValidateBundleComponents(db, s.companyID, s.bundleID, comps)
	if err == nil {
		t.Fatal("Expected error for self-reference")
	}
}

func TestBundleValidation_NestedBundleBlocked(t *testing.T) {
	db := testBundleDB(t)
	s := setupBundle(t, db)

	// Create another bundle
	bundle2 := models.ProductService{
		CompanyID: s.companyID, Name: "Bundle2", Type: models.ProductServiceTypeNonInventory,
		ItemStructureType: models.ItemStructureBundle, RevenueAccountID: s.revAcctID,
		CanBeSold: true, IsActive: true,
	}
	db.Create(&bundle2)

	comps := []models.ItemComponent{
		{ComponentItemID: bundle2.ID, Quantity: decimal.NewFromInt(1)},
	}
	err := ValidateBundleComponents(db, s.companyID, s.bundleID, comps)
	if err == nil {
		t.Fatal("Expected error for nested bundle")
	}
}

func TestBundleValidation_PositiveQtyRequired(t *testing.T) {
	db := testBundleDB(t)
	s := setupBundle(t, db)

	comps := []models.ItemComponent{
		{ComponentItemID: s.widgetID, Quantity: decimal.Zero},
	}
	err := ValidateBundleComponents(db, s.companyID, s.bundleID, comps)
	if err == nil {
		t.Fatal("Expected error for zero qty")
	}
}

func TestBundleValidation_DuplicateBlocked(t *testing.T) {
	db := testBundleDB(t)
	s := setupBundle(t, db)

	comps := []models.ItemComponent{
		{ComponentItemID: s.widgetID, Quantity: decimal.NewFromInt(1)},
		{ComponentItemID: s.widgetID, Quantity: decimal.NewFromInt(2)},
	}
	err := ValidateBundleComponents(db, s.companyID, s.bundleID, comps)
	if err == nil {
		t.Fatal("Expected error for duplicate component")
	}
}

// ── Invoice posting with bundle ──────────────────────────────────────────────

func TestPostInvoice_BundleSale_CreatesComponentMovements(t *testing.T) {
	db := testBundleDB(t)
	s := setupBundle(t, db)

	// Opening: Widget 100 @ $10, Gadget 50 @ $20
	CreateOpeningBalance(db, OpeningBalanceInput{CompanyID: s.companyID, ItemID: s.widgetID, Quantity: decimal.NewFromInt(100), UnitCost: decimal.NewFromInt(10), AsOfDate: time.Now()})
	CreateOpeningBalance(db, OpeningBalanceInput{CompanyID: s.companyID, ItemID: s.gadgetID, Quantity: decimal.NewFromInt(50), UnitCost: decimal.NewFromInt(20), AsOfDate: time.Now()})

	// Invoice: sell 3 Starter Kits (= 6 Widgets + 3 Gadgets)
	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-BUNDLE-1",
		CustomerID: s.customerID, InvoiceDate: time.Now(),
		Status: models.InvoiceStatusDraft,
		Subtotal: decimal.NewFromInt(300), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(300), BalanceDue: decimal.NewFromInt(300),
		CustomerNameSnapshot: "Customer",
	}
	db.Create(&inv)
	db.Create(&models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &s.bundleID, Description: "Starter Kit",
		Qty: decimal.NewFromInt(3), UnitPrice: decimal.NewFromInt(100),
		LineNet: decimal.NewFromInt(300), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(300),
	})

	err := PostInvoice(db, s.companyID, inv.ID, "test", nil)
	if err != nil {
		t.Fatalf("PostInvoice: %v", err)
	}

	// Widget balance: 100 - 6 = 94
	widgetBal, _ := GetBalance(db, s.companyID, s.widgetID)
	if !widgetBal.QuantityOnHand.Equal(decimal.NewFromInt(94)) {
		t.Errorf("Widget qty expected 94, got %s", widgetBal.QuantityOnHand)
	}

	// Gadget balance: 50 - 3 = 47
	gadgetBal, _ := GetBalance(db, s.companyID, s.gadgetID)
	if !gadgetBal.QuantityOnHand.Equal(decimal.NewFromInt(47)) {
		t.Errorf("Gadget qty expected 47, got %s", gadgetBal.QuantityOnHand)
	}

	// Verify movements created for components (not bundle)
	var movs []models.InventoryMovement
	db.Where("company_id = ? AND source_type = ?", s.companyID, "invoice").Find(&movs)
	if len(movs) != 2 {
		t.Fatalf("Expected 2 component movements, got %d", len(movs))
	}

	// COGS should be: 6×$10 (widgets) + 3×$20 (gadgets) = $60 + $60 = $120
	var cogsTotal decimal.Decimal
	var jeLines []models.JournalLine
	db.Where("company_id = ? AND account_id = ?", s.companyID, s.cogsAcctID).Find(&jeLines)
	for _, jl := range jeLines {
		cogsTotal = cogsTotal.Add(jl.Debit)
	}
	if !cogsTotal.Equal(decimal.NewFromInt(120)) {
		t.Errorf("Total COGS expected 120, got %s", cogsTotal)
	}

	// Revenue should still be on bundle line (= AR $300)
	var arLine models.JournalLine
	db.Where("company_id = ? AND account_id = ?", s.companyID, s.arAcctID).First(&arLine)
	if !arLine.Debit.Equal(decimal.NewFromInt(300)) {
		t.Errorf("AR debit expected 300, got %s", arLine.Debit)
	}
}

func TestPostInvoice_BundleInsufficientComponent_Blocked(t *testing.T) {
	db := testBundleDB(t)
	s := setupBundle(t, db)

	// Only 1 Widget (bundle needs 2 per kit)
	CreateOpeningBalance(db, OpeningBalanceInput{CompanyID: s.companyID, ItemID: s.widgetID, Quantity: decimal.NewFromInt(1), UnitCost: decimal.NewFromInt(10), AsOfDate: time.Now()})
	CreateOpeningBalance(db, OpeningBalanceInput{CompanyID: s.companyID, ItemID: s.gadgetID, Quantity: decimal.NewFromInt(50), UnitCost: decimal.NewFromInt(20), AsOfDate: time.Now()})

	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-BUNDLE-FAIL",
		CustomerID: s.customerID, InvoiceDate: time.Now(),
		Status: models.InvoiceStatusDraft,
		Subtotal: decimal.NewFromInt(100), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(100), BalanceDue: decimal.NewFromInt(100),
		CustomerNameSnapshot: "Customer",
	}
	db.Create(&inv)
	db.Create(&models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &s.bundleID, Description: "Starter Kit",
		Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(100),
		LineNet: decimal.NewFromInt(100), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(100),
	})

	err := PostInvoice(db, s.companyID, inv.ID, "test", nil)
	if err == nil {
		t.Fatal("Expected insufficient stock error")
	}
}

func TestPostInvoice_MixedBundleAndStock_AggregatesCorrectly(t *testing.T) {
	db := testBundleDB(t)
	s := setupBundle(t, db)

	// Stock: Widget 100 @ $10, Gadget 50 @ $20
	CreateOpeningBalance(db, OpeningBalanceInput{CompanyID: s.companyID, ItemID: s.widgetID, Quantity: decimal.NewFromInt(100), UnitCost: decimal.NewFromInt(10), AsOfDate: time.Now()})
	CreateOpeningBalance(db, OpeningBalanceInput{CompanyID: s.companyID, ItemID: s.gadgetID, Quantity: decimal.NewFromInt(50), UnitCost: decimal.NewFromInt(20), AsOfDate: time.Now()})

	// Invoice with: 1 bundle (2 Widgets + 1 Gadget) AND 5 standalone Widgets
	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-MIXED",
		CustomerID: s.customerID, InvoiceDate: time.Now(),
		Status: models.InvoiceStatusDraft,
		Subtotal: decimal.NewFromInt(350), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(350), BalanceDue: decimal.NewFromInt(350),
		CustomerNameSnapshot: "Customer",
	}
	db.Create(&inv)
	db.Create(&models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &s.bundleID, Description: "Starter Kit",
		Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(100),
		LineNet: decimal.NewFromInt(100), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(100),
	})
	db.Create(&models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: inv.ID, SortOrder: 2,
		ProductServiceID: &s.widgetID, Description: "Widget (standalone)",
		Qty: decimal.NewFromInt(5), UnitPrice: decimal.NewFromInt(50),
		LineNet: decimal.NewFromInt(250), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(250),
	})

	err := PostInvoice(db, s.companyID, inv.ID, "test", nil)
	if err != nil {
		t.Fatalf("PostInvoice: %v", err)
	}

	// Widget: 100 - 2 (bundle) - 5 (standalone) = 93
	widgetBal, _ := GetBalance(db, s.companyID, s.widgetID)
	if !widgetBal.QuantityOnHand.Equal(decimal.NewFromInt(93)) {
		t.Errorf("Widget qty expected 93, got %s", widgetBal.QuantityOnHand)
	}

	// Gadget: 50 - 1 (bundle) = 49
	gadgetBal, _ := GetBalance(db, s.companyID, s.gadgetID)
	if !gadgetBal.QuantityOnHand.Equal(decimal.NewFromInt(49)) {
		t.Errorf("Gadget qty expected 49, got %s", gadgetBal.QuantityOnHand)
	}
}

// ── Void / reverse tests ─────────────────────────────────────────────────────

func TestVoidInvoice_BundleSale_RestoresComponentInventory(t *testing.T) {
	db := testBundleDB(t)
	s := setupBundle(t, db)

	CreateOpeningBalance(db, OpeningBalanceInput{CompanyID: s.companyID, ItemID: s.widgetID, Quantity: decimal.NewFromInt(100), UnitCost: decimal.NewFromInt(10), AsOfDate: time.Now()})
	CreateOpeningBalance(db, OpeningBalanceInput{CompanyID: s.companyID, ItemID: s.gadgetID, Quantity: decimal.NewFromInt(50), UnitCost: decimal.NewFromInt(20), AsOfDate: time.Now()})

	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-BUNDLE-VOID",
		CustomerID: s.customerID, InvoiceDate: time.Now(),
		Status: models.InvoiceStatusDraft,
		Subtotal: decimal.NewFromInt(200), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(200), BalanceDue: decimal.NewFromInt(200),
		CustomerNameSnapshot: "Customer",
	}
	db.Create(&inv)
	db.Create(&models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &s.bundleID, Description: "Starter Kit",
		Qty: decimal.NewFromInt(2), UnitPrice: decimal.NewFromInt(100),
		LineNet: decimal.NewFromInt(200), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(200),
	})

	PostInvoice(db, s.companyID, inv.ID, "test", nil)

	// After sale: Widget 96, Gadget 48
	VoidInvoice(db, s.companyID, inv.ID, "test", nil)

	// After void: Widget 100, Gadget 50
	widgetBal, _ := GetBalance(db, s.companyID, s.widgetID)
	if !widgetBal.QuantityOnHand.Equal(decimal.NewFromInt(100)) {
		t.Errorf("Widget qty expected 100 after void, got %s", widgetBal.QuantityOnHand)
	}
	gadgetBal, _ := GetBalance(db, s.companyID, s.gadgetID)
	if !gadgetBal.QuantityOnHand.Equal(decimal.NewFromInt(50)) {
		t.Errorf("Gadget qty expected 50 after void, got %s", gadgetBal.QuantityOnHand)
	}
}

// ── Regression: normal items still work ──────────────────────────────────────

func TestPostInvoice_NormalStockItem_StillWorks(t *testing.T) {
	db := testBundleDB(t)
	s := setupBundle(t, db)

	CreateOpeningBalance(db, OpeningBalanceInput{CompanyID: s.companyID, ItemID: s.widgetID, Quantity: decimal.NewFromInt(50), UnitCost: decimal.NewFromInt(10), AsOfDate: time.Now()})

	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-NORMAL",
		CustomerID: s.customerID, InvoiceDate: time.Now(),
		Status: models.InvoiceStatusDraft,
		Subtotal: decimal.NewFromInt(500), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(500), BalanceDue: decimal.NewFromInt(500),
		CustomerNameSnapshot: "Customer",
	}
	db.Create(&inv)
	db.Create(&models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &s.widgetID, Description: "Widget",
		Qty: decimal.NewFromInt(10), UnitPrice: decimal.NewFromInt(50),
		LineNet: decimal.NewFromInt(500), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(500),
	})

	err := PostInvoice(db, s.companyID, inv.ID, "test", nil)
	if err != nil {
		t.Fatalf("PostInvoice: %v", err)
	}

	bal, _ := GetBalance(db, s.companyID, s.widgetID)
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(40)) {
		t.Errorf("Widget qty expected 40, got %s", bal.QuantityOnHand)
	}
}

// ── Bill-side bundle block ────────────────────────────────────────────────────

func TestPostBill_BundleItem_Rejected(t *testing.T) {
	db := testBundleDB(t)
	s := setupBundle(t, db)

	// Create a bill with bundle item
	bill := models.Bill{
		CompanyID: s.companyID, BillNumber: "BILL-BUNDLE",
		VendorID: s.customerID, // reuse customer as vendor for test simplicity
		BillDate: time.Now(), Status: models.BillStatusDraft,
		Subtotal: decimal.NewFromInt(100), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(100), BalanceDue: decimal.NewFromInt(100),
	}
	db.Create(&bill)

	// Need a vendor
	vendor := models.Vendor{CompanyID: s.companyID, Name: "Test Vendor"}
	db.Create(&vendor)
	bill.VendorID = vendor.ID
	db.Save(&bill)

	expID := s.revAcctID // doesn't matter, will fail before posting
	db.Create(&models.BillLine{
		CompanyID: s.companyID, BillID: bill.ID, SortOrder: 1,
		ProductServiceID: &s.bundleID, ExpenseAccountID: &expID,
		Description: "Starter Kit", Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(100),
		LineNet: decimal.NewFromInt(100), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(100),
	})

	err := PostBill(db, s.companyID, bill.ID, "test", nil)
	if err == nil {
		t.Fatal("Expected error posting bill with bundle item")
	}
	if !strings.Contains(err.Error(), "bundle") {
		t.Errorf("Expected bundle-related error, got: %v", err)
	}
}

// ── Regression: service-only invoice still works ─────────────────────────────

func TestPostInvoice_ServiceOnly_StillWorks(t *testing.T) {
	db := testBundleDB(t)
	s := setupBundle(t, db)

	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-SVC-ONLY",
		CustomerID: s.customerID, InvoiceDate: time.Now(),
		Status: models.InvoiceStatusDraft,
		Subtotal: decimal.NewFromInt(100), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(100), BalanceDue: decimal.NewFromInt(100),
		CustomerNameSnapshot: "Customer",
	}
	db.Create(&inv)
	db.Create(&models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &s.serviceID, Description: "Support",
		Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(100),
		LineNet: decimal.NewFromInt(100), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(100),
	})

	err := PostInvoice(db, s.companyID, inv.ID, "test", nil)
	if err != nil {
		t.Fatalf("PostInvoice: %v", err)
	}

	var movCount int64
	db.Model(&models.InventoryMovement{}).Where("company_id = ?", s.companyID).Count(&movCount)
	if movCount != 0 {
		t.Error("Service-only invoice should create zero movements")
	}
}
