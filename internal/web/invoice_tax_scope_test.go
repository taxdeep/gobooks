// 遵循project_guide.md
package web

// invoice_tax_scope_test.go — Batch 3.5 Tax Truth Chain web-layer tests.
//
// Coverage:
//   TestProductServiceCreate_RejectsPurchaseOnlyTaxCode   — product default tax blocked if purchase-only
//   TestProductServiceUpdate_RejectsPurchaseOnlyTaxCode   — same for update path
//   TestInvoiceSaveDraft_RejectsPurchaseOnlyTaxCodeOnLine — draft save blocked if purchase-only line tax
//   TestInvoiceEditSave_RejectsPurchaseOnlyTaxCodeOnLine  — edit save blocked if purchase-only line tax
//   TestInvoiceSaveDraft_AcceptsSalesScopedTaxCode        — sales/both tax code is accepted on draft

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// mustTime parses a "2006-01-02" date string; fatal on error.
func mustTime(t *testing.T, raw string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", raw)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// ── DB + seed helpers ─────────────────────────────────────────────────────────

func testTaxScopeDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:web_tax_scope_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.User{},
		&models.CompanyMembership{},
		&models.Customer{},
		&models.Account{},
		&models.TaxCode{},
		&models.ProductService{},
		&models.ItemComponent{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.NumberingSetting{},
		&models.AuditLog{},
		&models.TaskInvoiceSource{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedTaxScopeUser(t *testing.T, db *gorm.DB) *models.User {
	t.Helper()
	u := &models.User{
		ID:           uuid.New(),
		Email:        fmt.Sprintf("%s@example.com", t.Name()),
		PasswordHash: "not-used",
		DisplayName:  "Tax Scope Test",
		IsActive:     true,
	}
	if err := db.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	return u
}

// seedPurchaseOnlyTaxCode creates a TaxCode with Scope = purchase that must be
// rejected in all sales invoice / product paths.
func seedPurchaseOnlyTaxCode(t *testing.T, db *gorm.DB, companyID, salesLiabilityID uint) uint {
	t.Helper()
	tc := models.TaxCode{
		CompanyID:         companyID,
		Name:              "Purchase HST",
		Code:              "HST-PUR",
		TaxType:           "taxable",
		Rate:              mustDecimal(t, "0.13"),
		Scope:             models.TaxScopePurchase, // ← purchase-only
		RecoveryMode:      models.TaxRecoveryNone,
		RecoveryRate:      mustDecimal(t, "0"),
		SalesTaxAccountID: salesLiabilityID,
		IsActive:          true,
	}
	if err := db.Create(&tc).Error; err != nil {
		t.Fatal(err)
	}
	return tc.ID
}

// seedSalesTaxCode creates a TaxCode with Scope = sales.
func seedSalesTaxCode(t *testing.T, db *gorm.DB, companyID, salesLiabilityID uint) uint {
	t.Helper()
	tc := models.TaxCode{
		CompanyID:         companyID,
		Name:              "Sales GST",
		Code:              "GST-SAL",
		TaxType:           "taxable",
		Rate:              mustDecimal(t, "0.05"),
		Scope:             models.TaxScopeSales,
		RecoveryMode:      models.TaxRecoveryNone,
		RecoveryRate:      mustDecimal(t, "0"),
		SalesTaxAccountID: salesLiabilityID,
		IsActive:          true,
	}
	if err := db.Create(&tc).Error; err != nil {
		t.Fatal(err)
	}
	return tc.ID
}

// ── Product/Service default tax code tests ────────────────────────────────────

// TestProductServiceCreate_RejectsPurchaseOnlyTaxCode verifies that the create
// handler refuses a default_tax_code_id whose scope is 'purchase'. Products are
// used on sales invoices where purchase-only codes are not allowed.
func TestProductServiceCreate_RejectsPurchaseOnlyTaxCode(t *testing.T) {
	db := testTaxScopeDB(t)
	server := &Server{DB: db}
	user := seedTaxScopeUser(t, db)

	companyID := seedValidationCompany(t, db, "Scope Co")
	revenueID := seedValidationAccount(t, db, companyID, "4000", models.RootRevenue, models.DetailServiceRevenue)
	liabilityID := seedValidationAccount(t, db, companyID, "2300", models.RootLiability, models.DetailSalesTaxPayable)
	purchaseTaxID := seedPurchaseOnlyTaxCode(t, db, companyID, liabilityID)

	app := productServiceValidationApp(server, user, companyID)

	resp := performFormRequest(t, app, http.MethodPost, "/products-services", url.Values{
		"name":                {"Consulting"},
		"type":                {string(models.ProductServiceTypeService)},
		"structure_type":      {string(models.ItemStructureSingle)},
		"default_price":       {"100.00"},
		"revenue_account_id":  {fmt.Sprintf("%d", revenueID)},
		"default_tax_code_id": {fmt.Sprintf("%d", purchaseTaxID)},
	}, "")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (form re-render), got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "not valid for this company or does not apply to sales") {
		t.Fatalf("expected purchase-only tax code error, got: %q", body)
	}

	var count int64
	db.Model(&models.ProductService{}).Where("company_id = ?", companyID).Count(&count)
	if count != 0 {
		t.Fatalf("expected 0 products created, got %d", count)
	}
}

// TestProductServiceUpdate_RejectsPurchaseOnlyTaxCode verifies that the update
// handler also rejects setting a purchase-only default tax code.
func TestProductServiceUpdate_RejectsPurchaseOnlyTaxCode(t *testing.T) {
	db := testTaxScopeDB(t)
	server := &Server{DB: db}
	user := seedTaxScopeUser(t, db)

	companyID := seedValidationCompany(t, db, "Scope Co Update")
	revenueID := seedValidationAccount(t, db, companyID, "4000", models.RootRevenue, models.DetailServiceRevenue)
	liabilityID := seedValidationAccount(t, db, companyID, "2300", models.RootLiability, models.DetailSalesTaxPayable)

	// Create a valid product first (no default tax code).
	existingPSID := seedValidationProduct(t, db, companyID, revenueID, "Existing Service")
	purchaseTaxID := seedPurchaseOnlyTaxCode(t, db, companyID, liabilityID)

	app := productServiceValidationApp(server, user, companyID)

	resp := performFormRequest(t, app, http.MethodPost, "/products-services/update", url.Values{
		"item_id":             {fmt.Sprintf("%d", existingPSID)},
		"name":                {"Existing Service"},
		"type":                {string(models.ProductServiceTypeService)},
		"structure_type":      {string(models.ItemStructureSingle)},
		"default_price":       {"100.00"},
		"revenue_account_id":  {fmt.Sprintf("%d", revenueID)},
		"default_tax_code_id": {fmt.Sprintf("%d", purchaseTaxID)},
	}, "")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (form re-render), got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "not valid for this company or does not apply to sales") {
		t.Fatalf("expected purchase-only tax code error, got: %q", body)
	}

	// DefaultTaxCodeID must remain nil — update must not have persisted.
	var ps models.ProductService
	if err := db.First(&ps, existingPSID).Error; err != nil {
		t.Fatal(err)
	}
	if ps.DefaultTaxCodeID != nil {
		t.Fatalf("DefaultTaxCodeID should remain nil, got %v", *ps.DefaultTaxCodeID)
	}
}

// ── Invoice draft/edit save tax code scope tests ──────────────────────────────

// TestInvoiceSaveDraft_RejectsPurchaseOnlyTaxCodeOnLine verifies that the
// draft-save handler rejects a line referencing a purchase-only tax code.
// The backend guard in validateInvoiceDraftReferences blocks purchase-only codes.
func TestInvoiceSaveDraft_RejectsPurchaseOnlyTaxCodeOnLine(t *testing.T) {
	db := testTaxScopeDB(t)
	server := &Server{DB: db}
	user := seedTaxScopeUser(t, db)

	companyID := seedValidationCompany(t, db, "Draft Tax Co")
	customerID := seedValidationCustomer(t, db, companyID, "Test Customer")
	revenueID := seedValidationAccount(t, db, companyID, "4000", models.RootRevenue, models.DetailServiceRevenue)
	liabilityID := seedValidationAccount(t, db, companyID, "2300", models.RootLiability, models.DetailSalesTaxPayable)
	productID := seedValidationProduct(t, db, companyID, revenueID, "Web Design")
	purchaseTaxID := seedPurchaseOnlyTaxCode(t, db, companyID, liabilityID)

	app := invoiceProductApp(server, user, companyID)

	resp := performFormRequest(t, app, http.MethodPost, "/invoices/save-draft", url.Values{
		"invoice_number":         {"INV-TAXSCOPE-1"},
		"customer_id":            {fmt.Sprintf("%d", customerID)},
		"invoice_date":           {"2026-01-15"},
		"line_count":             {"1"},
		"line_product_service_id[0]": {fmt.Sprintf("%d", productID)},
		"line_description[0]":    {"Web Design"},
		"line_qty[0]":            {"1"},
		"line_unit_price[0]":     {"500.00"},
		"line_tax_code_id[0]":    {fmt.Sprintf("%d", purchaseTaxID)},
	}, "")

	// Must re-render with error, not redirect.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (form error), got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "invalid tax code") {
		t.Fatalf("expected 'invalid tax code' error in body, got: %q", body)
	}

	// No invoice must have been created.
	var count int64
	db.Model(&models.Invoice{}).Where("company_id = ?", companyID).Count(&count)
	if count != 0 {
		t.Fatalf("expected 0 invoices created, got %d", count)
	}
}

// TestInvoiceEditSave_RejectsPurchaseOnlyTaxCodeOnLine verifies that editing an
// existing draft and setting a purchase-only tax code on a line is also rejected.
func TestInvoiceEditSave_RejectsPurchaseOnlyTaxCodeOnLine(t *testing.T) {
	db := testTaxScopeDB(t)
	server := &Server{DB: db}
	user := seedTaxScopeUser(t, db)

	companyID := seedValidationCompany(t, db, "Edit Tax Co")
	customerID := seedValidationCustomer(t, db, companyID, "Test Customer")
	revenueID := seedValidationAccount(t, db, companyID, "4000", models.RootRevenue, models.DetailServiceRevenue)
	liabilityID := seedValidationAccount(t, db, companyID, "2300", models.RootLiability, models.DetailSalesTaxPayable)
	productID := seedValidationProduct(t, db, companyID, revenueID, "Web Design")
	purchaseTaxID := seedPurchaseOnlyTaxCode(t, db, companyID, liabilityID)

	// Seed an existing draft invoice with no tax code (valid state).
	inv := models.Invoice{
		CompanyID:             companyID,
		InvoiceNumber:         "INV-EDIT-TAX-1",
		CustomerID:            customerID,
		CustomerNameSnapshot:  "Test Customer",
		CustomerEmailSnapshot: "test@example.com",
		InvoiceDate:           mustTime(t, "2026-01-15"),
		Status:                models.InvoiceStatusDraft,
		Subtotal:              mustDecimal(t, "500.00"),
		TaxTotal:              mustDecimal(t, "0"),
		Amount:                mustDecimal(t, "500.00"),
		BalanceDue:            mustDecimal(t, "500.00"),
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}
	line := models.InvoiceLine{
		CompanyID:        companyID,
		InvoiceID:        inv.ID,
		SortOrder:        1,
		ProductServiceID: &productID,
		Description:      "Web Design",
		Qty:              mustDecimal(t, "1"),
		UnitPrice:        mustDecimal(t, "500.00"),
		LineNet:          mustDecimal(t, "500.00"),
		LineTax:          mustDecimal(t, "0"),
		LineTotal:        mustDecimal(t, "500.00"),
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}

	// Build an app with the edit save route.
	app := invoiceEditApp(server, user, companyID)

	resp := performFormRequest(t, app, http.MethodPost, "/invoices/save-draft", url.Values{
		"invoice_id":              {fmt.Sprintf("%d", inv.ID)},
		"invoice_number":          {"INV-EDIT-TAX-1"},
		"customer_id":             {fmt.Sprintf("%d", customerID)},
		"invoice_date":            {"2026-01-15"},
		"line_count":              {"1"},
		"line_product_service_id[0]": {fmt.Sprintf("%d", productID)},
		"line_description[0]":     {"Web Design"},
		"line_qty[0]":             {"1"},
		"line_unit_price[0]":      {"500.00"},
		"line_tax_code_id[0]":     {fmt.Sprintf("%d", purchaseTaxID)},
	}, "")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (form error), got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "invalid tax code") {
		t.Fatalf("expected 'invalid tax code' error in body, got: %q", body)
	}

	// The original line must be unchanged (no partial re-save).
	var currentLines []models.InvoiceLine
	db.Where("invoice_id = ?", inv.ID).Find(&currentLines)
	if len(currentLines) != 1 {
		t.Fatalf("expected 1 original line, got %d", len(currentLines))
	}
	if currentLines[0].TaxCodeID != nil {
		t.Fatalf("original line TaxCodeID should remain nil, got %v", currentLines[0].TaxCodeID)
	}
}

// TestInvoiceSaveDraft_AcceptsSalesScopedTaxCode verifies that a tax code with
// scope = 'sales' is accepted on a sales invoice draft line. This is the
// baseline positive test for the scope filter.
func TestInvoiceSaveDraft_AcceptsSalesScopedTaxCode(t *testing.T) {
	db := testTaxScopeDB(t)
	server := &Server{DB: db}
	user := seedTaxScopeUser(t, db)

	companyID := seedValidationCompany(t, db, "Accept Tax Co")
	customerID := seedValidationCustomer(t, db, companyID, "Test Customer")
	revenueID := seedValidationAccount(t, db, companyID, "4000", models.RootRevenue, models.DetailServiceRevenue)
	liabilityID := seedValidationAccount(t, db, companyID, "2300", models.RootLiability, models.DetailSalesTaxPayable)
	productID := seedValidationProduct(t, db, companyID, revenueID, "Web Design")
	salesTaxID := seedSalesTaxCode(t, db, companyID, liabilityID)

	app := invoiceProductApp(server, user, companyID)

	resp := performFormRequest(t, app, http.MethodPost, "/invoices/save-draft", url.Values{
		"invoice_number":             {"INV-SALESTAX-1"},
		"customer_id":                {fmt.Sprintf("%d", customerID)},
		"invoice_date":               {"2026-01-15"},
		"line_count":                 {"1"},
		"line_product_service_id[0]": {fmt.Sprintf("%d", productID)},
		"line_description[0]":        {"Web Design"},
		"line_qty[0]":                {"1"},
		"line_unit_price[0]":         {"500.00"},
		"line_tax_code_id[0]":        {fmt.Sprintf("%d", salesTaxID)},
	}, "")

	if resp.StatusCode != http.StatusSeeOther {
		body := readResponseBody(t, resp)
		t.Fatalf("expected 303 redirect for sales-scoped tax, got %d — body: %q", resp.StatusCode, body)
	}

	// Verify line saved with TaxCodeID.
	var savedLine models.InvoiceLine
	if err := db.Where("company_id = ? AND tax_code_id = ?", companyID, salesTaxID).
		First(&savedLine).Error; err != nil {
		t.Fatalf("expected InvoiceLine with sales tax code, got: %v", err)
	}
}

// ── Helper: build an app wired for edit-path save ────────────────────────────

