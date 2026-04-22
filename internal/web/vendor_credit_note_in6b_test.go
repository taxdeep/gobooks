// 遵循project_guide.md
package web

// vendor_credit_note_in6b_test.go — IN.6b UI wiring contract test.
//
// Locks the chain form → handleVendorCreditNoteSave → service Lines
// for stock-return lines on the Vendor Credit Note editor. Without
// this, a refactor that silently drops the line parsing (e.g.
// renames a form field or forgets to pass Lines through the input
// struct) would silently regress IN.6a because the legacy header-
// only path still succeeds.

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
	"gobooks/internal/services"
)

// testVCNIN6bDB extends the editor-flow DB with the tables needed by
// the VCN save handler: vendor_credit_notes, vendor_credit_note_lines,
// warehouses + inventory tables (so PostBill succeeds for the seed).
func testVCNIN6bDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testEditorFlowDB(t)
	if err := db.AutoMigrate(
		&models.Warehouse{},
		&models.InventoryMovement{},
		&models.InventoryBalance{},
		&models.InventoryCostLayer{},
		&models.InventoryLot{},
		&models.InventorySerialUnit{},
		&models.InventoryLayerConsumption{},
		&models.InventoryTrackingConsumption{},
		&models.VendorCreditNote{},
		&models.VendorCreditNoteLine{},
		&models.APCreditApplication{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func vcnIN6bApp(server *Server, user *models.User, companyID uint) *fiber.App {
	app := fiber.New()
	membership := &models.CompanyMembership{Role: models.CompanyRoleAdmin}
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(LocalsUser, user)
		c.Locals(LocalsActiveCompanyID, companyID)
		c.Locals(LocalsCompanyMembership, membership)
		return c.Next()
	})
	app.Post("/vendor-credit-notes/save", server.handleVendorCreditNoteSave)
	return app
}

// TestVendorCreditNoteSave_IN6b_PersistsStockReturnLine proves the
// form-to-service chain: a form with line_* arrays creates a VCN
// with VendorCreditNoteLine rows carrying ProductServiceID and
// OriginalBillLineID.
func TestVendorCreditNoteSave_IN6b_PersistsStockReturnLine(t *testing.T) {
	db := testVCNIN6bDB(t)
	server := &Server{DB: db}
	user := seedEditorFlowUser(t, db)
	fx := seedIN1Fixture(t, db) // reuses company + vendor + stock item + warehouse from IN.1

	// Seed a posted Bill the VCN can trace back to. Simplest path:
	// create + PostBill in-process so the inventory_movements row
	// with source_type='bill' exists for the trace validation.
	bill, billLineID := seedVCNIN6bPostedBill(t, db, fx, 10, 20.00)

	// AP + Offset accounts for the VCN JE. AP was seeded by IN.1;
	// we add a purchase-returns offset.
	offsetID := seedValidationAccount(t, db, fx.CompanyID, "5200",
		models.RootCostOfSales, "purchase_returns")

	app := vcnIN6bApp(server, user, fx.CompanyID)

	form := url.Values{
		"vendor_id":                     {fmt.Sprintf("%d", fx.VendorID)},
		"credit_note_date":              {time.Now().Format("2006-01-02")},
		"currency_code":                 {"CAD"},
		"exchange_rate":                 {"1"},
		"bill_id":                       {fmt.Sprintf("%d", bill.ID)},
		"ap_account_id":                 {fmt.Sprintf("%d", fx.APAccountID)},
		"offset_account_id":             {fmt.Sprintf("%d", offsetID)},
		"amount":                        {"0"}, // will be recomputed from line
		"line_description[]":            {"Return: Widget"},
		"line_product_service_id[]":     {fmt.Sprintf("%d", fx.ItemID)},
		"line_original_bill_line_id[]":  {fmt.Sprintf("%d", billLineID)},
		"line_qty[]":                    {"10"},
		"line_unit_price[]":             {"20.00"},
	}

	resp := performFormRequest(t, app, http.MethodPost, "/vendor-credit-notes/save", form, "")
	if resp.StatusCode != http.StatusSeeOther {
		body := readResponseBody(t, resp)
		t.Fatalf("expected 303 redirect on save; got %d. body: %s", resp.StatusCode, body)
	}

	// One VCN record, one line row, trace + product wired.
	var vcn models.VendorCreditNote
	if err := db.Preload("Lines").Preload("Lines.ProductService").
		Where("company_id = ? AND vendor_id = ?", fx.CompanyID, fx.VendorID).
		Order("id desc").First(&vcn).Error; err != nil {
		t.Fatalf("load saved VCN: %v", err)
	}
	if len(vcn.Lines) != 1 {
		t.Fatalf("VCN.Lines: got %d want 1", len(vcn.Lines))
	}
	line := vcn.Lines[0]
	if line.ProductServiceID == nil || *line.ProductServiceID != fx.ItemID {
		got := "<nil>"
		if line.ProductServiceID != nil {
			got = fmt.Sprintf("%d", *line.ProductServiceID)
		}
		t.Errorf("line.ProductServiceID: got %s want %d", got, fx.ItemID)
	}
	if line.OriginalBillLineID == nil || *line.OriginalBillLineID != billLineID {
		got := "<nil>"
		if line.OriginalBillLineID != nil {
			got = fmt.Sprintf("%d", *line.OriginalBillLineID)
		}
		t.Errorf("line.OriginalBillLineID: got %s want %d", got, billLineID)
	}
	if !line.Qty.Equal(decimal.NewFromInt(10)) {
		t.Errorf("line.Qty: got %s want 10", line.Qty)
	}
	if !line.UnitPrice.Equal(decimal.NewFromFloat(20.00)) {
		t.Errorf("line.UnitPrice: got %s want 20.00", line.UnitPrice)
	}
	// Header Amount derived from line sum: 10 × 20 = 200.
	if !vcn.Amount.Equal(decimal.NewFromInt(200)) {
		t.Errorf("VCN.Amount recomputed: got %s want 200", vcn.Amount)
	}
	if vcn.BillID == nil || *vcn.BillID != bill.ID {
		t.Errorf("VCN.BillID not persisted")
	}
}

// TestVendorCreditNoteSave_IN6b_HeaderOnlyStillWorks verifies IN.6b
// didn't break the legacy header-only path: a form with no line_*
// fields creates a VCN with zero lines and a manual Amount.
func TestVendorCreditNoteSave_IN6b_HeaderOnlyStillWorks(t *testing.T) {
	db := testVCNIN6bDB(t)
	server := &Server{DB: db}
	user := seedEditorFlowUser(t, db)
	fx := seedIN1Fixture(t, db)
	offsetID := seedValidationAccount(t, db, fx.CompanyID, "5200",
		models.RootCostOfSales, "purchase_returns")

	app := vcnIN6bApp(server, user, fx.CompanyID)

	form := url.Values{
		"vendor_id":         {fmt.Sprintf("%d", fx.VendorID)},
		"credit_note_date":  {time.Now().Format("2006-01-02")},
		"currency_code":     {"CAD"},
		"exchange_rate":     {"1"},
		"ap_account_id":     {fmt.Sprintf("%d", fx.APAccountID)},
		"offset_account_id": {fmt.Sprintf("%d", offsetID)},
		"amount":            {"75.00"},
		"reason":            {"Price adjustment"},
	}

	resp := performFormRequest(t, app, http.MethodPost, "/vendor-credit-notes/save", form, "")
	if resp.StatusCode != http.StatusSeeOther {
		body := readResponseBody(t, resp)
		t.Fatalf("expected 303; got %d. body: %s", resp.StatusCode, body)
	}

	var vcn models.VendorCreditNote
	if err := db.Preload("Lines").
		Where("company_id = ? AND vendor_id = ?", fx.CompanyID, fx.VendorID).
		Order("id desc").First(&vcn).Error; err != nil {
		t.Fatalf("load saved VCN: %v", err)
	}
	if len(vcn.Lines) != 0 {
		t.Errorf("header-only VCN must have zero lines; got %d", len(vcn.Lines))
	}
	if !vcn.Amount.Equal(decimal.NewFromFloat(75.00)) {
		t.Errorf("header-only Amount: got %s want 75.00", vcn.Amount)
	}
}

// seedVCNIN6bPostedBill posts a Bill with one stock line so the
// IN.6b handler test has an existing inventory movement the VCN can
// trace to. Mirror of IN.6a's postBillWithStockLine but in the web
// test package (uses in1Fixture shape).
func seedVCNIN6bPostedBill(t *testing.T, db *gorm.DB, fx in1Fixture, qty int, unitPrice float64) (models.Bill, uint) {
	t.Helper()
	qtyDec := decimal.NewFromInt(int64(qty))
	priceDec := decimal.NewFromFloat(unitPrice)
	lineNet := qtyDec.Mul(priceDec).RoundBank(2)

	bill := models.Bill{
		CompanyID:    fx.CompanyID,
		BillNumber:   fmt.Sprintf("BILL-IN6B-%d", time.Now().UnixNano()),
		VendorID:     fx.VendorID,
		BillDate:     time.Now().UTC(),
		Status:       models.BillStatusDraft,
		CurrencyCode: "",
		ExchangeRate: decimal.NewFromInt(1),
		Subtotal:     lineNet,
		Amount:       lineNet,
		BalanceDue:   lineNet,
	}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatalf("seed bill: %v", err)
	}
	expAcctID := fx.InventoryAccountID // any valid account works for the Bill Expense field
	line := models.BillLine{
		CompanyID:        fx.CompanyID,
		BillID:           bill.ID,
		SortOrder:        1,
		ProductServiceID: &fx.ItemID,
		Description:      "Widget",
		Qty:              qtyDec,
		UnitPrice:        priceDec,
		ExpenseAccountID: &expAcctID,
		LineNet:          lineNet,
		LineTax:          decimal.Zero,
		LineTotal:        lineNet,
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatalf("seed bill line: %v", err)
	}
	if err := services.PostBill(db, fx.CompanyID, bill.ID, "tester", nil); err != nil {
		t.Fatalf("post bill: %v", err)
	}
	var posted models.Bill
	db.First(&posted, bill.ID)
	return posted, line.ID
}
