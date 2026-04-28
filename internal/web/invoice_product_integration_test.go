// 遵循project_guide.md
package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── shared test setup ─────────────────────────────────────────────────────────

func testInvoiceProductDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:web_inv_product_%s?mode=memory&cache=shared", t.Name())
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
		&models.PaymentTerm{},
		&models.ProductService{},
		&models.ItemComponent{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.NumberingSetting{},
		&models.AuditLog{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedInvoiceProductUser(t *testing.T, db *gorm.DB) *models.User {
	t.Helper()

	u := &models.User{
		ID:           uuid.New(),
		Email:        fmt.Sprintf("%s@example.com", t.Name()),
		PasswordHash: "not-used",
		DisplayName:  "Invoice Product Test",
		IsActive:     true,
	}
	if err := db.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	return u
}

func invoiceProductApp(server *Server, user *models.User, companyID uint) *fiber.App {
	app := fiber.New(fiber.Config{
		// Disable redirect following so we can inspect 303 responses.
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
		},
	})
	membership := &models.CompanyMembership{Role: models.CompanyRoleAdmin}
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(LocalsUser, user)
		c.Locals(LocalsActiveCompanyID, companyID)
		c.Locals(LocalsCompanyMembership, membership)
		return c.Next()
	})
	app.Post("/invoices/save-draft", server.handleInvoiceSaveDraft)
	return app
}

// ── Batch 2: save draft persists ProductServiceID ─────────────────────────────

// TestInvoiceSaveDraft_PersistsProductServiceID verifies that when an invoice
// draft is saved with a line_product_service_id the InvoiceLine row in the DB
// carries that ProductServiceID.  This is the core Batch 2 persistence check.
func TestInvoiceSaveDraft_PersistsProductServiceID(t *testing.T) {
	db := testInvoiceProductDB(t)
	server := &Server{DB: db}
	user := seedInvoiceProductUser(t, db)

	companyID := seedValidationCompany(t, db, "Persist Co")
	customerID := seedValidationCustomer(t, db, companyID, "Test Customer")
	revenueID := seedValidationAccount(t, db, companyID, "4000", models.RootRevenue, models.DetailServiceRevenue)
	productID := seedValidationProduct(t, db, companyID, revenueID, "Web Design")

	app := invoiceProductApp(server, user, companyID)

	resp := performFormRequest(t, app, http.MethodPost, "/invoices/save-draft", url.Values{
		"invoice_number":                 {"INV-001"},
		"customer_id":                    {fmt.Sprintf("%d", customerID)},
		"invoice_date":                   {"2026-01-15"},
		"line_count":                     {"1"},
		"line_product_service_id[0]":     {fmt.Sprintf("%d", productID)},
		"line_description[0]":            {"Web Design"},
		"line_qty[0]":                    {"1"},
		"line_unit_price[0]":             {"250.00"},
	}, "")

	// Successful save should redirect.
	if resp.StatusCode != http.StatusSeeOther {
		body := readResponseBody(t, resp)
		t.Fatalf("expected 303 redirect, got %d — body: %q", resp.StatusCode, body)
	}

	// Verify the line was saved with ProductServiceID.
	var line models.InvoiceLine
	if err := db.Where("company_id = ? AND product_service_id = ?", companyID, productID).
		First(&line).Error; err != nil {
		t.Fatalf("expected InvoiceLine with product_service_id=%d, got: %v", productID, err)
	}
	if line.Description != "Web Design" {
		t.Fatalf("expected description 'Web Design', got %q", line.Description)
	}
}

// TestInvoiceSaveDraft_RejectsInactiveProductOnNewInvoice verifies that an
// invoice draft cannot be saved when a line references an inactive product.
// This is an HTTP-level guard complementing the unit test on
// validateInvoiceDraftReferences (which already covers the service layer).
func TestInvoiceSaveDraft_RejectsInactiveProductOnNewInvoice(t *testing.T) {
	db := testInvoiceProductDB(t)
	server := &Server{DB: db}
	user := seedInvoiceProductUser(t, db)

	companyID := seedValidationCompany(t, db, "Inactive Check Co")
	customerID := seedValidationCustomer(t, db, companyID, "Test Customer")
	revenueID := seedValidationAccount(t, db, companyID, "4000", models.RootRevenue, models.DetailServiceRevenue)
	inactiveProductID := seedValidationProduct(t, db, companyID, revenueID, "Archived Service")

	// Mark the product inactive.
	if err := db.Model(&models.ProductService{}).Where("id = ?", inactiveProductID).
		Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}

	app := invoiceProductApp(server, user, companyID)

	resp := performFormRequest(t, app, http.MethodPost, "/invoices/save-draft", url.Values{
		"invoice_number":             {"INV-002"},
		"customer_id":                {fmt.Sprintf("%d", customerID)},
		"invoice_date":               {"2026-01-15"},
		"line_count":                 {"1"},
		"line_product_service_id[0]": {fmt.Sprintf("%d", inactiveProductID)},
		"line_description[0]":        {"Archived Service"},
		"line_qty[0]":                {"1"},
		"line_unit_price[0]":         {"100.00"},
	}, "")

	// Must re-render the form with an error (200), not redirect on success.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (form error), got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "invalid product/service") {
		t.Fatalf("expected inactive product error in body, got: %q", body)
	}

	// No invoice must have been created.
	var count int64
	db.Model(&models.Invoice{}).Where("company_id = ?", companyID).Count(&count)
	if count != 0 {
		t.Fatalf("expected 0 invoices created, got %d", count)
	}
}

// TestInvoiceSaveDraft_AcceptsFreeFormLine verifies that a line with no
// product_service_id (free-form) is accepted at draft-save time.
// This is the backward-compatibility guard for legacy invoices.
func TestInvoiceSaveDraft_AcceptsFreeFormLine(t *testing.T) {
	db := testInvoiceProductDB(t)
	server := &Server{DB: db}
	user := seedInvoiceProductUser(t, db)

	companyID := seedValidationCompany(t, db, "Free Form Co")
	customerID := seedValidationCustomer(t, db, companyID, "Test Customer")

	app := invoiceProductApp(server, user, companyID)

	resp := performFormRequest(t, app, http.MethodPost, "/invoices/save-draft", url.Values{
		"invoice_number":      {"INV-003"},
		"customer_id":         {fmt.Sprintf("%d", customerID)},
		"invoice_date":        {"2026-01-15"},
		"line_count":          {"1"},
		// No line_product_service_id — free-form line.
		"line_description[0]": {"Custom consulting work"},
		"line_qty[0]":         {"2"},
		"line_unit_price[0]":  {"75.00"},
	}, "")

	// Successful save should redirect.
	if resp.StatusCode != http.StatusSeeOther {
		body := readResponseBody(t, resp)
		t.Fatalf("expected 303 redirect for free-form line, got %d — body: %q", resp.StatusCode, body)
	}

	// Verify line saved with nil ProductServiceID.
	var line models.InvoiceLine
	if err := db.Where("company_id = ? AND description = ?", companyID, "Custom consulting work").
		First(&line).Error; err != nil {
		t.Fatalf("expected InvoiceLine to exist, got: %v", err)
	}
	if line.ProductServiceID != nil {
		t.Fatalf("expected ProductServiceID to be nil for free-form line, got %v", line.ProductServiceID)
	}
}

// TestInvoiceSaveDraft_RejectsCrossCompanyProduct verifies that a line
// referencing a product from a different company is rejected at draft-save time.
func TestInvoiceSaveDraft_RejectsCrossCompanyProduct(t *testing.T) {
	db := testInvoiceProductDB(t)
	server := &Server{DB: db}
	user := seedInvoiceProductUser(t, db)

	companyA := seedValidationCompany(t, db, "Acme")
	companyB := seedValidationCompany(t, db, "Beta")
	customerA := seedValidationCustomer(t, db, companyA, "Customer A")
	revenueB := seedValidationAccount(t, db, companyB, "4000", models.RootRevenue, models.DetailServiceRevenue)
	productB := seedValidationProduct(t, db, companyB, revenueB, "Beta Service")

	app := invoiceProductApp(server, user, companyA)

	resp := performFormRequest(t, app, http.MethodPost, "/invoices/save-draft", url.Values{
		"invoice_number":             {"INV-004"},
		"customer_id":                {fmt.Sprintf("%d", customerA)},
		"invoice_date":               {"2026-01-15"},
		"line_count":                 {"1"},
		"line_product_service_id[0]": {fmt.Sprintf("%d", productB)},
		"line_description[0]":        {"Beta Service"},
		"line_qty[0]":                {"1"},
		"line_unit_price[0]":         {"100.00"},
	}, "")

	// Must re-render with error.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (form error), got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "invalid product/service") {
		t.Fatalf("expected cross-company product error in body, got: %q", body)
	}

	// No invoice must have been created for company A.
	var count int64
	db.Model(&models.Invoice{}).Where("company_id = ?", companyA).Count(&count)
	if count != 0 {
		t.Fatalf("expected 0 invoices created for company A, got %d", count)
	}
}
