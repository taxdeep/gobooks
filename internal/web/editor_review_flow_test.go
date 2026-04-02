package web

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

func testEditorFlowDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:web_editor_flow_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.User{},
		&models.CompanyMembership{},
		&models.Customer{},
		&models.Vendor{},
		&models.Account{},
		&models.TaxCode{},
		&models.ProductService{},
		&models.NumberingSetting{},
		&models.PaymentTerm{},
		&models.CompanyCurrency{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.Bill{},
		&models.BillLine{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.AuditLog{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedEditorFlowUser(t *testing.T, db *gorm.DB) *models.User {
	t.Helper()

	user := &models.User{
		ID:           uuid.New(),
		Email:        fmt.Sprintf("%s@example.com", t.Name()),
		PasswordHash: "not-used",
		DisplayName:  "Editor Flow Test",
		IsActive:     true,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	return user
}

func seedEditorFlowVendor(t *testing.T, db *gorm.DB, companyID uint, name string) uint {
	t.Helper()

	row := models.Vendor{CompanyID: companyID, Name: name}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row.ID
}

func editorFlowApp(server *Server, user *models.User, companyID uint) *fiber.App {
	app := fiber.New()
	membership := &models.CompanyMembership{Role: models.CompanyRoleAdmin}
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(LocalsUser, user)
		c.Locals(LocalsActiveCompanyID, companyID)
		c.Locals(LocalsCompanyMembership, membership)
		return c.Next()
	})
	app.Post("/invoices/save-draft", server.handleInvoiceSaveDraft)
	app.Post("/bills/save-draft", server.handleBillSaveDraft)
	app.Post("/bills/:id/post", server.handleBillPost)
	return app
}

func TestHandleInvoiceSaveDraftRedirectsToLockedEdit(t *testing.T) {
	db := testEditorFlowDB(t)
	server := &Server{DB: db}
	user := seedEditorFlowUser(t, db)
	companyID := seedValidationCompany(t, db, "Invoice Flow Co")
	customerID := seedValidationCustomer(t, db, companyID, "Customer A")
	app := editorFlowApp(server, user, companyID)

	form := url.Values{
		"invoice_number":             {"INV-LOCK-001"},
		"customer_id":                {fmt.Sprintf("%d", customerID)},
		"invoice_date":               {"2026-03-31"},
		"terms":                      {"N30"},
		"due_date":                   {"2026-04-30"},
		"memo":                       {"Review mode test"},
		"line_count":                 {"2"},
		"line_description[0]":        {"Draft invoice line"},
		"line_qty[0]":                {"1"},
		"line_unit_price[0]":         {"120.00"},
		"line_tax_code_id[0]":        {""},
		"line_product_service_id[0]": {""},
		"line_description[1]":        {""},
		"line_qty[1]":                {"1"},
		"line_unit_price[1]":         {"0.00"},
		"line_tax_code_id[1]":        {""},
		"line_product_service_id[1]": {""},
	}

	resp := performFormRequest(t, app, http.MethodPost, "/invoices/save-draft", form, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, resp.StatusCode)
	}

	var inv models.Invoice
	if err := db.Where("company_id = ? AND invoice_number = ?", companyID, "INV-LOCK-001").First(&inv).Error; err != nil {
		t.Fatalf("expected saved invoice, got %v", err)
	}
	if inv.Status != models.InvoiceStatusDraft {
		t.Fatalf("expected draft invoice, got %s", inv.Status)
	}

	wantLocation := fmt.Sprintf("/invoices/%d/edit?saved=1&locked=1", inv.ID)
	if got := resp.Header.Get("Location"); got != wantLocation {
		t.Fatalf("expected redirect to %q, got %q", wantLocation, got)
	}
}

func TestHandleBillSaveDraftAndPostFlow(t *testing.T) {
	db := testEditorFlowDB(t)
	server := &Server{DB: db}
	user := seedEditorFlowUser(t, db)
	companyID := seedValidationCompany(t, db, "Bill Flow Co")
	vendorID := seedEditorFlowVendor(t, db, companyID, "Vendor A")
	expenseAccountID := seedValidationAccount(t, db, companyID, "6100", models.RootExpense, models.DetailOfficeExpense)
	_ = seedValidationAccount(t, db, companyID, "2000", models.RootLiability, models.DetailAccountsPayable)
	app := editorFlowApp(server, user, companyID)

	form := url.Values{
		"bill_number":                {"BILL-LOCK-001"},
		"vendor_id":                  {fmt.Sprintf("%d", vendorID)},
		"bill_date":                  {"2026-03-31"},
		"terms":                      {"N30"},
		"due_date":                   {"2026-04-30"},
		"memo":                       {"Review mode test"},
		"line_count":                 {"2"},
		"line_expense_account_id[0]": {fmt.Sprintf("%d", expenseAccountID)},
		"line_description[0]":        {""},
		"line_amount[0]":             {"120.00"},
		"line_tax_code_id[0]":        {""},
		"line_expense_account_id[1]": {""},
		"line_description[1]":        {""},
		"line_amount[1]":             {"0.00"},
		"line_tax_code_id[1]":        {""},
	}

	saveResp := performFormRequest(t, app, http.MethodPost, "/bills/save-draft", form, "")
	if saveResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, saveResp.StatusCode)
	}

	var bill models.Bill
	if err := db.Where("company_id = ? AND bill_number = ?", companyID, "BILL-LOCK-001").First(&bill).Error; err != nil {
		t.Fatalf("expected saved bill, got %v", err)
	}
	if bill.Status != models.BillStatusDraft {
		t.Fatalf("expected draft bill, got %s", bill.Status)
	}

	var lines []models.BillLine
	if err := db.Where("bill_id = ?", bill.ID).Order("sort_order asc").Find(&lines).Error; err != nil {
		t.Fatalf("load bill lines: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected 1 saved bill line, got %d", len(lines))
	}
	if lines[0].Description == "" {
		t.Fatal("expected server to default bill line description when category is selected")
	}

	wantSaveLocation := fmt.Sprintf("/bills/%d/edit?saved=1&locked=1", bill.ID)
	if got := saveResp.Header.Get("Location"); got != wantSaveLocation {
		t.Fatalf("expected redirect to %q, got %q", wantSaveLocation, got)
	}

	postResp := performFormRequest(t, app, http.MethodPost, fmt.Sprintf("/bills/%d/post", bill.ID), nil, "")
	if postResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, postResp.StatusCode)
	}
	if got := postResp.Header.Get("Location"); got != "/bills?posted=1" {
		t.Fatalf("expected redirect to %q, got %q", "/bills?posted=1", got)
	}

	if err := db.First(&bill, bill.ID).Error; err != nil {
		t.Fatalf("reload bill: %v", err)
	}
	if bill.Status != models.BillStatusPosted {
		t.Fatalf("expected posted bill, got %s", bill.Status)
	}
	if bill.JournalEntryID == nil {
		t.Fatal("expected posted bill to have a journal entry")
	}
}

func TestHandleInvoiceSaveDraftPersistsManualExchangeRate(t *testing.T) {
	db := testEditorFlowDB(t)
	server := &Server{DB: db}
	user := seedEditorFlowUser(t, db)
	companyID := seedValidationCompany(t, db, "Invoice FX Co")
	customerID := seedValidationCustomer(t, db, companyID, "Customer FX")
	if err := db.Model(&models.Company{}).
		Where("id = ?", companyID).
		Updates(map[string]any{
			"multi_currency_enabled": true,
			"base_currency_code":     "CAD",
		}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.CompanyCurrency{
		CompanyID:    companyID,
		CurrencyCode: "USD",
		IsActive:     true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	app := editorFlowApp(server, user, companyID)

	form := url.Values{
		"invoice_number":             {"INV-FX-001"},
		"customer_id":                {fmt.Sprintf("%d", customerID)},
		"invoice_date":               {"2026-03-31"},
		"currency_code":              {"USD"},
		"exchange_rate":              {"1.3700"},
		"line_count":                 {"1"},
		"line_description[0]":        {"FX line"},
		"line_qty[0]":                {"1"},
		"line_unit_price[0]":         {"100.00"},
		"line_tax_code_id[0]":        {""},
		"line_product_service_id[0]": {""},
	}

	resp := performFormRequest(t, app, http.MethodPost, "/invoices/save-draft", form, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, resp.StatusCode)
	}

	var inv models.Invoice
	if err := db.Where("company_id = ? AND invoice_number = ?", companyID, "INV-FX-001").First(&inv).Error; err != nil {
		t.Fatalf("expected saved invoice, got %v", err)
	}
	if inv.CurrencyCode != "USD" {
		t.Fatalf("expected USD invoice currency, got %q", inv.CurrencyCode)
	}
	if !inv.ExchangeRate.Equal(decimal.RequireFromString("1.3700")) {
		t.Fatalf("expected exchange rate 1.3700, got %s", inv.ExchangeRate)
	}
}

func TestHandleBillSaveDraftKeepsAdjustedTaxTotalsConsistent(t *testing.T) {
	db := testEditorFlowDB(t)
	server := &Server{DB: db}
	user := seedEditorFlowUser(t, db)
	companyID := seedValidationCompany(t, db, "Bill Tax Co")
	vendorID := seedEditorFlowVendor(t, db, companyID, "Vendor Tax")
	expenseAccountID := seedValidationAccount(t, db, companyID, "6100", models.RootExpense, models.DetailOfficeExpense)
	liabilityAccountID := seedValidationAccount(t, db, companyID, "2200", models.RootLiability, models.DetailSalesTaxPayable)
	taxCodeID := seedValidationTaxCode(t, db, companyID, liabilityAccountID, "GST")
	app := editorFlowApp(server, user, companyID)

	form := url.Values{
		"bill_number":                {"BILL-TAX-001"},
		"vendor_id":                  {fmt.Sprintf("%d", vendorID)},
		"bill_date":                  {"2026-03-31"},
		"line_count":                 {"3"},
		"line_expense_account_id[0]": {fmt.Sprintf("%d", expenseAccountID)},
		"line_description[0]":        {"Line A"},
		"line_amount[0]":             {"0.20"},
		"line_tax_code_id[0]":        {fmt.Sprintf("%d", taxCodeID)},
		"line_expense_account_id[1]": {fmt.Sprintf("%d", expenseAccountID)},
		"line_description[1]":        {"Line B"},
		"line_amount[1]":             {"0.20"},
		"line_tax_code_id[1]":        {fmt.Sprintf("%d", taxCodeID)},
		"line_expense_account_id[2]": {fmt.Sprintf("%d", expenseAccountID)},
		"line_description[2]":        {"Line C"},
		"line_amount[2]":             {"0.20"},
		"line_tax_code_id[2]":        {fmt.Sprintf("%d", taxCodeID)},
		"tax_adj_count":              {"1"},
		"tax_adj_id[0]":              {fmt.Sprintf("%d", taxCodeID)},
		"tax_adj_amount[0]":          {"0.02"},
	}

	resp := performFormRequest(t, app, http.MethodPost, "/bills/save-draft", form, "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, resp.StatusCode)
	}

	var bill models.Bill
	if err := db.Where("company_id = ? AND bill_number = ?", companyID, "BILL-TAX-001").First(&bill).Error; err != nil {
		t.Fatalf("expected saved bill, got %v", err)
	}

	var lines []models.BillLine
	if err := db.Where("bill_id = ?", bill.ID).Order("sort_order asc").Find(&lines).Error; err != nil {
		t.Fatalf("load bill lines: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("expected 3 saved bill lines, got %d", len(lines))
	}

	lineTaxSum := decimal.Zero
	for _, line := range lines {
		lineTaxSum = lineTaxSum.Add(line.LineTax)
	}
	if !bill.TaxTotal.Equal(decimal.RequireFromString("0.02")) {
		t.Fatalf("expected bill tax total 0.02, got %s", bill.TaxTotal)
	}
	if !lineTaxSum.Equal(bill.TaxTotal) {
		t.Fatalf("expected bill tax total %s to equal sum of line taxes %s", bill.TaxTotal, lineTaxSum)
	}
}
