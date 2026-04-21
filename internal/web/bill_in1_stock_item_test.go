// 遵循project_guide.md
package web

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// bill_in1_stock_item_test.go — IN.1 contract test.
//
// Locks the Rule #4 invariant for the Bill path at the UI level:
// a Bill line carrying a stock-item ProductServiceID (plus Qty +
// UnitPrice) MUST produce an inventory_movements row on bill post.
// Before IN.1 the bill editor had no product picker, so the Bill
// save path hardcoded ProductServiceID=nil, Qty=1, UnitPrice=Amount
// and CreatePurchaseMovements silently skipped every stock line.
// This is the regression lock that fails loud if that class of
// silent-swallowing behavior ever comes back.

func testBillIN1DB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testEditorFlowDB(t)
	// Extend with inventory + warehouse tables so we can observe
	// the stock-movement side-effect of bill post.
	if err := db.AutoMigrate(
		&models.Warehouse{},
		&models.InventoryMovement{},
		&models.InventoryBalance{},
		&models.InventoryCostLayer{},
		&models.InventoryLot{},
		&models.InventorySerialUnit{},
		&models.InventoryLayerConsumption{},
		&models.InventoryTrackingConsumption{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

type in1Fixture struct {
	CompanyID          uint
	VendorID           uint
	ItemID             uint
	WarehouseID        uint
	InventoryAccountID uint
	APAccountID        uint
}

func seedIN1Fixture(t *testing.T, db *gorm.DB) in1Fixture {
	t.Helper()
	companyID := seedValidationCompany(t, db, "IN.1 Bill Co")
	vendorID := seedEditorFlowVendor(t, db, companyID, "Vendor IN.1")

	// Accounts: inventory asset + AP (and a revenue account, required
	// by ProductService.RevenueAccountID non-null constraint).
	invAcctID := seedValidationAccount(t, db, companyID, "1300",
		models.RootAsset, models.DetailInventory)
	apAcctID := seedValidationAccount(t, db, companyID, "2000",
		models.RootLiability, models.DetailAccountsPayable)
	_ = apAcctID
	revAcctID := seedValidationAccount(t, db, companyID, "4000",
		models.RootRevenue, "sales_revenue")

	// Stock item with InventoryAccountID configured.
	item := models.ProductService{
		CompanyID:          companyID,
		Name:               "Widget",
		Type:               models.ProductServiceTypeInventory,
		RevenueAccountID:   revAcctID,
		InventoryAccountID: &invAcctID,
		IsActive:           true,
	}
	item.ApplyTypeDefaults()
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("seed item: %v", err)
	}

	// Warehouse: IN.1 doesn't require the header warehouse field to be
	// set (company default fallback), but having at least one warehouse
	// row in the DB exercises the movement-routing path end-to-end.
	wh := models.Warehouse{
		CompanyID: companyID,
		Name:      "Main",
		Code:      "MAIN",
		IsActive:  true,
		IsDefault: true,
	}
	if err := db.Create(&wh).Error; err != nil {
		t.Fatalf("seed warehouse: %v", err)
	}

	return in1Fixture{
		CompanyID:          companyID,
		VendorID:           vendorID,
		ItemID:             item.ID,
		WarehouseID:        wh.ID,
		InventoryAccountID: invAcctID,
		APAccountID:        apAcctID,
	}
}

// TestBillSaveAndPost_IN1_StockItemFormsInventoryMovement proves the
// end-to-end Rule #4 chain: form → BillLine row with
// ProductServiceID/Qty/UnitPrice → PostBill → inventory_movements
// row with source_type='bill' and the right quantity_delta.
func TestBillSaveAndPost_IN1_StockItemFormsInventoryMovement(t *testing.T) {
	db := testBillIN1DB(t)
	server := &Server{DB: db}
	user := seedEditorFlowUser(t, db)
	fx := seedIN1Fixture(t, db)
	app := editorFlowApp(server, user, fx.CompanyID)

	// Save bill draft with one stock-item line: qty=5, unit_price=12.00
	// (amount derived server-side as 60.00). This mirrors what the
	// bill editor posts when operator picks an item.
	form := url.Values{
		"bill_number":                {"BILL-IN1-001"},
		"vendor_id":                  {fmt.Sprintf("%d", fx.VendorID)},
		"bill_date":                  {"2026-04-01"},
		"terms":                      {"N30"},
		"due_date":                   {"2026-05-01"},
		"warehouse_id":               {fmt.Sprintf("%d", fx.WarehouseID)},
		"line_count":                 {"1"},
		"line_product_service_id[0]": {fmt.Sprintf("%d", fx.ItemID)},
		"line_expense_account_id[0]": {fmt.Sprintf("%d", fx.InventoryAccountID)},
		"line_description[0]":        {"Widget stock purchase"},
		"line_qty[0]":                {"5"},
		"line_unit_price[0]":         {"12.00"},
		"line_amount[0]":             {"60.00"},
		"line_tax_code_id[0]":        {""},
	}

	saveResp := performFormRequest(t, app, http.MethodPost, "/bills/save-draft", form, "")
	if saveResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save draft: got status %d", saveResp.StatusCode)
	}

	var bill models.Bill
	if err := db.Where("company_id = ? AND bill_number = ?", fx.CompanyID, "BILL-IN1-001").
		First(&bill).Error; err != nil {
		t.Fatalf("load draft bill: %v", err)
	}
	if bill.Status != models.BillStatusDraft {
		t.Fatalf("bill status: got %q want draft", bill.Status)
	}

	// Load lines. The line MUST have ProductServiceID set (Rule #4
	// chain intact), Qty=5, UnitPrice=12 — NOT the legacy fallback
	// Qty=1, UnitPrice=60.
	var lines []models.BillLine
	if err := db.Where("bill_id = ?", bill.ID).Order("sort_order asc").
		Find(&lines).Error; err != nil {
		t.Fatalf("load lines: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("lines: got %d want 1", len(lines))
	}
	if lines[0].ProductServiceID == nil || *lines[0].ProductServiceID != fx.ItemID {
		t.Fatalf("BillLine.ProductServiceID: got %v want %d", lines[0].ProductServiceID, fx.ItemID)
	}
	if !lines[0].Qty.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("BillLine.Qty: got %s want 5 (IN.1 must stop hardcoding Qty=1)", lines[0].Qty)
	}
	if !lines[0].UnitPrice.Equal(decimal.NewFromInt(12)) {
		t.Fatalf("BillLine.UnitPrice: got %s want 12 (IN.1 must read from form, not amount)", lines[0].UnitPrice)
	}

	// Post the bill and verify inventory_movements row landed.
	postResp := performFormRequest(t, app, http.MethodPost,
		fmt.Sprintf("/bills/%d/post", bill.ID), nil, "")
	if postResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("post bill: got status %d", postResp.StatusCode)
	}

	// Bill should be posted + JE linked.
	if err := db.First(&bill, bill.ID).Error; err != nil {
		t.Fatalf("reload bill: %v", err)
	}
	if bill.Status != models.BillStatusPosted {
		t.Fatalf("bill status after post: got %q want posted", bill.Status)
	}
	if bill.JournalEntryID == nil {
		t.Fatal("bill.journal_entry_id: nil — expected posted JE")
	}

	// The Rule #4 invariant: inventory_movements has one row with
	// source_type='bill' + source_id=bill.ID + quantity_delta=+5.
	var movs []models.InventoryMovement
	if err := db.Where("company_id = ? AND source_type = ? AND source_id = ?",
		fx.CompanyID, "bill", bill.ID).Find(&movs).Error; err != nil {
		t.Fatalf("load movements: %v", err)
	}
	if len(movs) != 1 {
		t.Fatalf("inventory_movements rows: got %d want 1 (Rule #4 violation — stock line did not form inventory)", len(movs))
	}
	if !movs[0].QuantityDelta.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("quantity_delta: got %s want +5", movs[0].QuantityDelta)
	}
	if !movs[0].UnitCostBase.Equal(decimal.NewFromInt(12)) {
		t.Fatalf("unit_cost_base: got %s want 12 (authoritative cost from BillLine.UnitPrice)", movs[0].UnitCostBase)
	}

	// Inventory balance reflects the receipt.
	var bal models.InventoryBalance
	if err := db.Where("company_id = ? AND item_id = ?",
		fx.CompanyID, fx.ItemID).First(&bal).Error; err != nil {
		t.Fatalf("load balance: %v", err)
	}
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("on_hand: got %s want 5", bal.QuantityOnHand)
	}
}

// TestBillSaveDraft_IN1_AmountOnlyLinePreservesLegacyBehavior locks
// the Q1 decision: an amount-only line (no ProductServiceID) must
// keep the legacy Qty=1, UnitPrice=Amount shape. No silent behavior
// change for users who never touch the Item picker.
func TestBillSaveDraft_IN1_AmountOnlyLinePreservesLegacyBehavior(t *testing.T) {
	db := testBillIN1DB(t)
	server := &Server{DB: db}
	user := seedEditorFlowUser(t, db)
	fx := seedIN1Fixture(t, db)
	// Add a pure expense account — the bill will book to this, not
	// touch inventory.
	expenseAcctID := seedValidationAccount(t, db, fx.CompanyID, "6100",
		models.RootExpense, models.DetailOfficeExpense)
	app := editorFlowApp(server, user, fx.CompanyID)

	form := url.Values{
		"bill_number":                {"BILL-IN1-002"},
		"vendor_id":                  {fmt.Sprintf("%d", fx.VendorID)},
		"bill_date":                  {"2026-04-01"},
		"terms":                      {"N30"},
		"due_date":                   {"2026-05-01"},
		"line_count":                 {"1"},
		"line_product_service_id[0]": {""},
		"line_expense_account_id[0]": {fmt.Sprintf("%d", expenseAcctID)},
		"line_description[0]":        {"Office supplies"},
		"line_qty[0]":                {""},
		"line_unit_price[0]":         {""},
		"line_amount[0]":             {"87.45"},
		"line_tax_code_id[0]":        {""},
	}

	resp := performFormRequest(t, app, http.MethodPost, "/bills/save-draft", form, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save draft: got status %d", resp.StatusCode)
	}

	var bill models.Bill
	if err := db.Where("company_id = ? AND bill_number = ?", fx.CompanyID, "BILL-IN1-002").
		First(&bill).Error; err != nil {
		t.Fatalf("load bill: %v", err)
	}
	var line models.BillLine
	if err := db.Where("bill_id = ?", bill.ID).First(&line).Error; err != nil {
		t.Fatalf("load line: %v", err)
	}
	if line.ProductServiceID != nil {
		t.Fatalf("ProductServiceID: got %v want nil (amount-only line must not link a product)", *line.ProductServiceID)
	}
	if !line.Qty.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("Qty: got %s want 1 (legacy fallback)", line.Qty)
	}
	if !line.UnitPrice.Equal(decimal.RequireFromString("87.45")) {
		t.Fatalf("UnitPrice: got %s want 87.45 (legacy UnitPrice=Amount fallback)", line.UnitPrice)
	}
	if !line.LineNet.Equal(decimal.RequireFromString("87.45")) {
		t.Fatalf("LineNet: got %s want 87.45", line.LineNet)
	}
}
