package web

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"balanciz/internal/models"
)

func TestBillProductLinkedAccountIDPrefersInventoryThenCOGS(t *testing.T) {
	inventoryID := uint(11)
	cogsID := uint(22)
	productID := uint(7)

	got := billProductLinkedAccountID([]models.ProductService{
		{ID: productID, InventoryAccountID: &inventoryID, COGSAccountID: &cogsID},
	}, &productID)
	if got != inventoryID {
		t.Fatalf("linked account = %d, want inventory account %d", got, inventoryID)
	}

	got = billProductLinkedAccountID([]models.ProductService{
		{ID: productID, COGSAccountID: &cogsID},
	}, &productID)
	if got != cogsID {
		t.Fatalf("linked account = %d, want COGS account %d", got, cogsID)
	}
}

func TestBillProductLinkedAccountIDAllowsManualCategoryWhenItemHasNoAccount(t *testing.T) {
	productID := uint(7)

	got := billProductLinkedAccountID([]models.ProductService{
		{ID: productID},
	}, &productID)
	if got != 0 {
		t.Fatalf("linked account = %d, want 0", got)
	}
}

func TestBillSaveDraftItemLinkedAccountOverridesPostedCategory(t *testing.T) {
	db := testBillIN1DB(t)
	server := &Server{DB: db}
	user := seedEditorFlowUser(t, db)
	fx := seedIN1Fixture(t, db)
	app := editorFlowApp(server, user, fx.CompanyID)

	manualExpenseID := seedValidationAccount(t, db, fx.CompanyID, "6100",
		models.RootExpense, models.DetailOfficeExpense)

	form := url.Values{
		"bill_number":                {"BILL-CAT-LOCK-001"},
		"vendor_id":                  {fmt.Sprintf("%d", fx.VendorID)},
		"bill_date":                  {"2026-04-26"},
		"terms":                      {"N30"},
		"due_date":                   {"2026-05-26"},
		"warehouse_id":               {fmt.Sprintf("%d", fx.WarehouseID)},
		"line_count":                 {"1"},
		"line_product_service_id[0]": {fmt.Sprintf("%d", fx.ItemID)},
		"line_expense_account_id[0]": {fmt.Sprintf("%d", manualExpenseID)},
		"line_description[0]":        {"Locked widget category"},
		"line_qty[0]":                {"2"},
		"line_unit_price[0]":         {"10.00"},
		"line_amount[0]":             {"20.00"},
		"line_tax_code_id[0]":        {""},
	}

	resp := performFormRequest(t, app, http.MethodPost, "/bills/save-draft", form, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save draft: got status %d", resp.StatusCode)
	}

	var bill models.Bill
	if err := db.Where("company_id = ? AND bill_number = ?", fx.CompanyID, "BILL-CAT-LOCK-001").
		First(&bill).Error; err != nil {
		t.Fatalf("load bill: %v", err)
	}
	var line models.BillLine
	if err := db.Where("bill_id = ?", bill.ID).First(&line).Error; err != nil {
		t.Fatalf("load bill line: %v", err)
	}
	if line.ExpenseAccountID == nil || *line.ExpenseAccountID != fx.InventoryAccountID {
		t.Fatalf("ExpenseAccountID = %v, want item inventory account %d", line.ExpenseAccountID, fx.InventoryAccountID)
	}
}
