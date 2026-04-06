package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
	"gobooks/internal/services"
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
		&models.Task{},
		&models.Expense{},
		&models.TaskInvoiceSource{},
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
	app.Get("/bills/new", server.handleBillNew)
	app.Get("/invoices/:id/edit", server.handleInvoiceEdit)
	app.Post("/invoices/save-draft", server.handleInvoiceSaveDraft)
	app.Post("/invoices/:id/delete", server.handleInvoiceDelete)
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

func TestBillEditorTaskDropdownAndSaveDraftRespectTaskRules(t *testing.T) {
	db := testEditorFlowDB(t)
	server := &Server{DB: db}
	user := seedEditorFlowUser(t, db)
	companyID := seedValidationCompany(t, db, "Bill Task Co")
	vendorID := seedEditorFlowVendor(t, db, companyID, "Vendor Tasks")
	expenseAccountID := seedValidationAccount(t, db, companyID, "6100", models.RootExpense, models.DetailOfficeExpense)
	customerA := seedValidationCustomer(t, db, companyID, "Customer A")
	customerB := seedValidationCustomer(t, db, companyID, "Customer B")
	openTaskID := seedTaskForWeb(t, db, companyID, customerA, models.TaskStatusOpen, "Open task")
	completedTaskID := seedTaskForWeb(t, db, companyID, customerB, models.TaskStatusCompleted, "Completed task")
	seedTaskForWeb(t, db, companyID, customerA, models.TaskStatusCancelled, "Cancelled task")
	seedTaskForWeb(t, db, companyID, customerA, models.TaskStatusInvoiced, "Invoiced task")
	app := editorFlowApp(server, user, companyID)

	newResp := performRequest(t, app, "/bills/new", "")
	if newResp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, newResp.StatusCode)
	}
	newBody := readResponseBody(t, newResp)
	if !strings.Contains(newBody, "Open task") || !strings.Contains(newBody, "Completed task") {
		t.Fatalf("expected open/completed tasks in dropdown, got %q", newBody)
	}
	if strings.Contains(newBody, "Cancelled task") || strings.Contains(newBody, "Invoiced task") {
		t.Fatalf("expected cancelled/invoiced tasks to be hidden, got %q", newBody)
	}

	form := url.Values{
		"bill_number":                {"BILL-TASK-001"},
		"vendor_id":                  {fmt.Sprintf("%d", vendorID)},
		"bill_date":                  {"2026-04-04"},
		"line_count":                 {"2"},
		"line_expense_account_id[0]": {fmt.Sprintf("%d", expenseAccountID)},
		"line_description[0]":        {"Hotel"},
		"line_task_id[0]":            {fmt.Sprintf("%d", openTaskID)},
		"line_is_billable[0]":        {"1"},
		"line_amount[0]":             {"80.00"},
		"line_tax_code_id[0]":        {""},
		"line_expense_account_id[1]": {fmt.Sprintf("%d", expenseAccountID)},
		"line_description[1]":        {"Snacks"},
		"line_task_id[1]":            {fmt.Sprintf("%d", completedTaskID)},
		"line_amount[1]":             {"12.00"},
		"line_tax_code_id[1]":        {""},
	}

	saveResp := performFormRequest(t, app, http.MethodPost, "/bills/save-draft", form, "")
	if saveResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, saveResp.StatusCode)
	}

	var bill models.Bill
	if err := db.Where("company_id = ? AND bill_number = ?", companyID, "BILL-TASK-001").First(&bill).Error; err != nil {
		t.Fatalf("expected saved bill, got %v", err)
	}

	var lines []models.BillLine
	if err := db.Where("bill_id = ?", bill.ID).Order("sort_order asc").Find(&lines).Error; err != nil {
		t.Fatalf("load bill lines: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 saved bill lines, got %d", len(lines))
	}
	if lines[0].TaskID == nil || *lines[0].TaskID != openTaskID {
		t.Fatalf("expected first line task %d, got %+v", openTaskID, lines[0].TaskID)
	}
	if lines[0].BillableCustomerID == nil || *lines[0].BillableCustomerID != customerA {
		t.Fatalf("expected first line customer %d, got %+v", customerA, lines[0].BillableCustomerID)
	}
	if lines[0].ReinvoiceStatus != models.ReinvoiceStatusUninvoiced {
		t.Fatalf("expected first line uninvoiced, got %q", lines[0].ReinvoiceStatus)
	}
	if lines[1].TaskID == nil || *lines[1].TaskID != completedTaskID {
		t.Fatalf("expected second line task %d, got %+v", completedTaskID, lines[1].TaskID)
	}
	if lines[1].BillableCustomerID == nil || *lines[1].BillableCustomerID != customerB {
		t.Fatalf("expected second line customer %d, got %+v", customerB, lines[1].BillableCustomerID)
	}
	if lines[1].ReinvoiceStatus != models.ReinvoiceStatusNone {
		t.Fatalf("expected second line empty reinvoice status, got %q", lines[1].ReinvoiceStatus)
	}
}

func TestInvoiceEditorTaskGeneratedDraftIsReadOnlyAndDeleteStillReleasesSources(t *testing.T) {
	db := testEditorFlowDB(t)
	server := &Server{DB: db}
	user := seedEditorFlowUser(t, db)
	companyID := seedValidationCompany(t, db, "Invoice Task Lock Co")
	customerID := seedValidationCustomer(t, db, companyID, "Customer A")
	app := editorFlowApp(server, user, companyID)

	invoice, line, task := seedTaskGeneratedDraftInvoice(t, db, companyID, customerID, "INV-TASK-LOCK-001")

	editResp := performRequest(t, app, fmt.Sprintf("/invoices/%d/edit", invoice.ID), "")
	if editResp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, editResp.StatusCode)
	}
	editBody := readResponseBody(t, editResp)
	if !strings.Contains(editBody, "This draft is read-only in the editor.") {
		t.Fatalf("expected read-only banner, got %q", editBody)
	}
	if !strings.Contains(editBody, "Delete Draft") {
		t.Fatalf("expected delete action for task-generated draft, got %q", editBody)
	}
	if strings.Contains(editBody, "Save Draft") || strings.Contains(editBody, "← Edit") {
		t.Fatalf("expected task-generated draft editor to hide editable actions, got %q", editBody)
	}

	form := url.Values{
		"invoice_id":                {fmt.Sprintf("%d", invoice.ID)},
		"invoice_number":            {invoice.InvoiceNumber},
		"customer_id":               {fmt.Sprintf("%d", customerID)},
		"invoice_date":              {"2026-04-04"},
		"terms":                     {"DOC"},
		"memo":                      {"should not save"},
		"line_count":                {"1"},
		"line_description[0]":       {"Changed line"},
		"line_qty[0]":               {"1"},
		"line_unit_price[0]":        {"125.00"},
		"line_tax_code_id[0]":       {""},
		"line_product_service_id[0]": {""},
	}
	saveResp := performFormRequest(t, app, http.MethodPost, "/invoices/save-draft", form, "")
	if saveResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, saveResp.StatusCode)
	}
	location := saveResp.Header.Get("Location")
	if !strings.Contains(location, fmt.Sprintf("/invoices/%d/edit?error=", invoice.ID)) {
		t.Fatalf("expected redirect back to edit with error, got %q", location)
	}

	blockedResp := performRequest(t, app, location, "")
	if blockedResp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, blockedResp.StatusCode)
	}
	blockedBody := readResponseBody(t, blockedResp)
	if !strings.Contains(blockedBody, services.ErrTaskGeneratedDraftReadOnly.Error()) {
		t.Fatalf("expected read-only error message, got %q", blockedBody)
	}

	var reloaded models.Invoice
	if err := db.Preload("Lines").First(&reloaded, invoice.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.Memo == "should not save" {
		t.Fatalf("expected task-generated draft memo to remain unchanged, got %q", reloaded.Memo)
	}
	if len(reloaded.Lines) != 1 || reloaded.Lines[0].Description != line.Description {
		t.Fatalf("expected task-generated draft lines to remain unchanged, got %+v", reloaded.Lines)
	}

	deleteResp := performFormRequest(t, app, http.MethodPost, fmt.Sprintf("/invoices/%d/delete", invoice.ID), nil, "")
	if deleteResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, deleteResp.StatusCode)
	}
	if got := deleteResp.Header.Get("Location"); got != "/invoices?deleted=1" {
		t.Fatalf("expected delete redirect, got %q", got)
	}

	var invoiceCount int64
	if err := db.Model(&models.Invoice{}).Where("id = ?", invoice.ID).Count(&invoiceCount).Error; err != nil {
		t.Fatal(err)
	}
	if invoiceCount != 0 {
		t.Fatalf("expected draft invoice deleted, count=%d", invoiceCount)
	}

	if err := db.First(&task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.Status != models.TaskStatusCompleted || task.InvoiceID != nil || task.InvoiceLineID != nil {
		t.Fatalf("expected task released after delete, got %+v", task)
	}
}

func TestInvoiceEditorOrdinaryDraftRemainsEditable(t *testing.T) {
	db := testEditorFlowDB(t)
	server := &Server{DB: db}
	user := seedEditorFlowUser(t, db)
	companyID := seedValidationCompany(t, db, "Invoice Ordinary Co")
	customerID := seedValidationCustomer(t, db, companyID, "Customer A")
	app := editorFlowApp(server, user, companyID)

	invoice := seedOrdinaryDraftInvoice(t, db, companyID, customerID, "INV-ORD-001")

	editResp := performRequest(t, app, fmt.Sprintf("/invoices/%d/edit", invoice.ID), "")
	if editResp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, editResp.StatusCode)
	}
	editBody := readResponseBody(t, editResp)
	if strings.Contains(editBody, "This draft is read-only in the editor.") {
		t.Fatalf("expected ordinary draft to stay editable, got %q", editBody)
	}
	if !strings.Contains(editBody, "Save Draft") {
		t.Fatalf("expected ordinary draft save action, got %q", editBody)
	}

	form := url.Values{
		"invoice_id":                 {fmt.Sprintf("%d", invoice.ID)},
		"invoice_number":             {invoice.InvoiceNumber},
		"customer_id":                {fmt.Sprintf("%d", customerID)},
		"invoice_date":               {"2026-04-04"},
		"terms":                      {"DOC"},
		"memo":                       {"ordinary updated"},
		"line_count":                 {"1"},
		"line_description[0]":        {"Updated line"},
		"line_qty[0]":                {"2"},
		"line_unit_price[0]":         {"25.00"},
		"line_tax_code_id[0]":        {""},
		"line_product_service_id[0]": {""},
	}
	saveResp := performFormRequest(t, app, http.MethodPost, "/invoices/save-draft", form, "")
	if saveResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected %d, got %d", http.StatusSeeOther, saveResp.StatusCode)
	}
	if got := saveResp.Header.Get("Location"); got != fmt.Sprintf("/invoices/%d/edit?saved=1&locked=1", invoice.ID) {
		t.Fatalf("expected ordinary draft save redirect, got %q", got)
	}

	var reloaded models.Invoice
	if err := db.First(&reloaded, invoice.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.Memo != "ordinary updated" {
		t.Fatalf("expected ordinary draft memo update, got %q", reloaded.Memo)
	}
}

func seedTaskGeneratedDraftInvoice(t *testing.T, db *gorm.DB, companyID, customerID uint, invoiceNumber string) (models.Invoice, models.InvoiceLine, models.Task) {
	t.Helper()

	task := models.Task{
		CompanyID:    companyID,
		CustomerID:   customerID,
		Title:        "Generated task work",
		TaskDate:     time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC),
		Quantity:     decimal.NewFromInt(1),
		UnitType:     models.TaskUnitTypeHour,
		Rate:         decimal.RequireFromString("120.00"),
		CurrencyCode: "CAD",
		IsBillable:   true,
		Status:       models.TaskStatusInvoiced,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}

	invoice := seedOrdinaryDraftInvoice(t, db, companyID, customerID, invoiceNumber)
	var line models.InvoiceLine
	if err := db.Where("invoice_id = ?", invoice.ID).First(&line).Error; err != nil {
		t.Fatal(err)
	}

	if err := db.Model(&task).Updates(map[string]any{
		"invoice_id":      invoice.ID,
		"invoice_line_id": line.ID,
	}).Error; err != nil {
		t.Fatal(err)
	}
	task.InvoiceID = &invoice.ID
	task.InvoiceLineID = &line.ID

	invoiceID := invoice.ID
	lineID := line.ID
	bridge := models.TaskInvoiceSource{
		CompanyID:      companyID,
		InvoiceID:      &invoiceID,
		InvoiceLineID:  &lineID,
		SourceType:     models.TaskInvoiceSourceTask,
		SourceID:       task.ID,
		AmountSnapshot: line.LineNet,
	}
	if err := db.Create(&bridge).Error; err != nil {
		t.Fatal(err)
	}

	return invoice, line, task
}

func seedOrdinaryDraftInvoice(t *testing.T, db *gorm.DB, companyID, customerID uint, invoiceNumber string) models.Invoice {
	t.Helper()

	invoice := models.Invoice{
		CompanyID:               companyID,
		InvoiceNumber:           invoiceNumber,
		CustomerID:              customerID,
		InvoiceDate:             time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC),
		PaymentTermSnapshot:     models.PaymentTermSnapshot{TermCode: "DOC"},
		Status:                  models.InvoiceStatusDraft,
		Memo:                    "draft memo",
		Subtotal:                decimal.RequireFromString("120.00"),
		TaxTotal:                decimal.Zero,
		Amount:                  decimal.RequireFromString("120.00"),
		BalanceDue:              decimal.RequireFromString("120.00"),
		CustomerNameSnapshot:    "Customer A",
		CustomerEmailSnapshot:   "",
		CustomerAddressSnapshot: "",
	}
	if err := db.Create(&invoice).Error; err != nil {
		t.Fatal(err)
	}
	line := models.InvoiceLine{
		CompanyID:   companyID,
		InvoiceID:   invoice.ID,
		SortOrder:   1,
		Description: "Draft line",
		Qty:         decimal.NewFromInt(1),
		UnitPrice:   decimal.RequireFromString("120.00"),
		LineNet:     decimal.RequireFromString("120.00"),
		LineTax:     decimal.Zero,
		LineTotal:   decimal.RequireFromString("120.00"),
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}
	return invoice
}
