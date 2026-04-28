// 遵循project_guide.md
package web

// invoice_product_edit_test.go — HTTP round-trip tests for the invoice
// save-draft handler when editing an existing draft that carries
// ProductServiceID on its lines.
//
// Scope: edit path only (invoice_id set in POST body).
// Tests create a real draft invoice in the DB, then POST to /invoices/save-draft
// and verify both the HTTP response and the resulting DB state.

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

	"balanciz/internal/models"
)

// ── shared test setup ─────────────────────────────────────────────────────────

func testInvoiceEditDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:web_inv_edit_%s?mode=memory&cache=shared", t.Name())
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
		&models.TaskInvoiceSource{}, // required by HasActiveTaskInvoiceSources
		&models.NumberingSetting{},
		&models.AuditLog{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedInvoiceEditUser(t *testing.T, db *gorm.DB) *models.User {
	t.Helper()

	u := &models.User{
		ID:           uuid.New(),
		Email:        fmt.Sprintf("%s@example.com", t.Name()),
		PasswordHash: "not-used",
		DisplayName:  "Invoice Edit Test",
		IsActive:     true,
	}
	if err := db.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	return u
}

func invoiceEditApp(server *Server, user *models.User, companyID uint) *fiber.App {
	app := fiber.New(fiber.Config{
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

// seedDraftInvoiceWithLine creates a draft invoice with one line and returns
// both the invoice ID and the line ID. Used by edit-path tests.
func seedDraftInvoiceWithLine(
	t *testing.T, db *gorm.DB,
	companyID, customerID uint,
	productID *uint, // nil = free-form line
	description string,
) (invoiceID uint, lineID uint) {
	t.Helper()

	inv := models.Invoice{
		CompanyID:     companyID,
		InvoiceNumber: "SEED-001",
		CustomerID:    customerID,
		InvoiceDate:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Status:        models.InvoiceStatusDraft,
		Amount:        decimal.RequireFromString("100.00"),
		Subtotal:      decimal.RequireFromString("100.00"),
		BalanceDue:    decimal.RequireFromString("100.00"),
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}

	line := models.InvoiceLine{
		CompanyID:        companyID,
		InvoiceID:        inv.ID,
		SortOrder:        1,
		ProductServiceID: productID,
		Description:      description,
		Qty:              decimal.RequireFromString("1"),
		UnitPrice:        decimal.RequireFromString("100.00"),
		LineNet:          decimal.RequireFromString("100.00"),
		LineTax:          decimal.Zero,
		LineTotal:        decimal.RequireFromString("100.00"),
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}

	return inv.ID, line.ID
}

// ── Edit path round-trip tests ────────────────────────────────────────────────

// TestInvoiceEditDraft_PersistsValidProductServiceID verifies that editing an
// existing draft with a valid ProductServiceID succeeds and the line is
// re-saved with the correct ProductServiceID bound to the correct invoice.
func TestInvoiceEditDraft_PersistsValidProductServiceID(t *testing.T) {
	db := testInvoiceEditDB(t)
	server := &Server{DB: db}
	user := seedInvoiceEditUser(t, db)

	companyID := seedValidationCompany(t, db, "Edit Persist Co")
	customerID := seedValidationCustomer(t, db, companyID, "Test Customer")
	revenueID := seedValidationAccount(t, db, companyID, "4000", models.RootRevenue, models.DetailServiceRevenue)
	productID := seedValidationProduct(t, db, companyID, revenueID, "Web Design")

	pID := productID
	invoiceID, _ := seedDraftInvoiceWithLine(t, db, companyID, customerID, &pID, "Web Design")

	app := invoiceEditApp(server, user, companyID)

	resp := performFormRequest(t, app, http.MethodPost, "/invoices/save-draft", url.Values{
		"invoice_id":                 {fmt.Sprintf("%d", invoiceID)},
		"invoice_number":             {"EDIT-001"},
		"customer_id":                {fmt.Sprintf("%d", customerID)},
		"invoice_date":               {"2026-01-15"},
		"line_count":                 {"1"},
		"line_product_service_id[0]": {fmt.Sprintf("%d", productID)},
		"line_description[0]":        {"Web Design revised"},
		"line_qty[0]":                {"2"},
		"line_unit_price[0]":         {"150.00"},
	}, "")

	if resp.StatusCode != http.StatusSeeOther {
		body := readResponseBody(t, resp)
		t.Fatalf("expected 303 redirect on successful edit, got %d — body: %q", resp.StatusCode, body)
	}

	// After edit, lines are deleted and re-inserted for this invoice.
	// Verify the new line has the correct ProductServiceID.
	var lines []models.InvoiceLine
	if err := db.Where("invoice_id = ? AND company_id = ?", invoiceID, companyID).
		Find(&lines).Error; err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 line after edit, got %d", len(lines))
	}
	if lines[0].ProductServiceID == nil || *lines[0].ProductServiceID != productID {
		t.Fatalf("expected ProductServiceID=%d, got %v", productID, lines[0].ProductServiceID)
	}
	if lines[0].Description != "Web Design revised" {
		t.Fatalf("expected updated description, got %q", lines[0].Description)
	}
}

// TestInvoiceEditDraft_RejectsCrossCompanyProduct verifies that editing a draft
// and submitting a line with a cross-company product is rejected, and the
// original lines are not replaced.
func TestInvoiceEditDraft_RejectsCrossCompanyProduct(t *testing.T) {
	db := testInvoiceEditDB(t)
	server := &Server{DB: db}
	user := seedInvoiceEditUser(t, db)

	companyA := seedValidationCompany(t, db, "Acme")
	companyB := seedValidationCompany(t, db, "Beta")
	customerA := seedValidationCustomer(t, db, companyA, "Customer A")
	revenueA := seedValidationAccount(t, db, companyA, "4000", models.RootRevenue, models.DetailServiceRevenue)
	revenueB := seedValidationAccount(t, db, companyB, "4000", models.RootRevenue, models.DetailServiceRevenue)
	productA := seedValidationProduct(t, db, companyA, revenueA, "Acme Service")
	productB := seedValidationProduct(t, db, companyB, revenueB, "Beta Service")

	pID := productA
	invoiceID, _ := seedDraftInvoiceWithLine(t, db, companyA, customerA, &pID, "Acme Service")

	// Capture original line ID to verify it is not modified.
	var originalLines []models.InvoiceLine
	if err := db.Where("invoice_id = ?", invoiceID).Find(&originalLines).Error; err != nil {
		t.Fatal(err)
	}
	originalLineCount := len(originalLines)

	app := invoiceEditApp(server, user, companyA)

	resp := performFormRequest(t, app, http.MethodPost, "/invoices/save-draft", url.Values{
		"invoice_id":                 {fmt.Sprintf("%d", invoiceID)},
		"invoice_number":             {"EDIT-002"},
		"customer_id":                {fmt.Sprintf("%d", customerA)},
		"invoice_date":               {"2026-01-15"},
		"line_count":                 {"1"},
		"line_product_service_id[0]": {fmt.Sprintf("%d", productB)}, // cross-company
		"line_description[0]":        {"Beta Service"},
		"line_qty[0]":                {"1"},
		"line_unit_price[0]":         {"100.00"},
	}, "")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (form error), got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "invalid product/service") {
		t.Fatalf("expected cross-company product error in body, got: %q", body)
	}

	// Original lines must be intact — the transaction must not have committed.
	var currentLines []models.InvoiceLine
	if err := db.Where("invoice_id = ?", invoiceID).Find(&currentLines).Error; err != nil {
		t.Fatal(err)
	}
	if len(currentLines) != originalLineCount {
		t.Fatalf("expected %d original lines to be preserved, got %d", originalLineCount, len(currentLines))
	}
	if currentLines[0].ProductServiceID == nil || *currentLines[0].ProductServiceID != productA {
		t.Fatalf("expected original productA line preserved, got product_service_id=%v", currentLines[0].ProductServiceID)
	}
}

// TestInvoiceEditDraft_RejectsInactiveProduct verifies that editing a draft
// with an inactive product on a line is rejected and the original lines survive.
func TestInvoiceEditDraft_RejectsInactiveProduct(t *testing.T) {
	db := testInvoiceEditDB(t)
	server := &Server{DB: db}
	user := seedInvoiceEditUser(t, db)

	companyID := seedValidationCompany(t, db, "Inactive Edit Co")
	customerID := seedValidationCustomer(t, db, companyID, "Test Customer")
	revenueID := seedValidationAccount(t, db, companyID, "4000", models.RootRevenue, models.DetailServiceRevenue)
	activeProductID := seedValidationProduct(t, db, companyID, revenueID, "Active Service")
	inactiveProductID := seedValidationProduct(t, db, companyID, revenueID, "Archived Service")

	// Deactivate one product.
	if err := db.Model(&models.ProductService{}).Where("id = ?", inactiveProductID).
		Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}

	// Draft was created with the active product.
	pID := activeProductID
	invoiceID, _ := seedDraftInvoiceWithLine(t, db, companyID, customerID, &pID, "Active Service")

	app := invoiceEditApp(server, user, companyID)

	// Attempt to switch the line to the inactive product.
	resp := performFormRequest(t, app, http.MethodPost, "/invoices/save-draft", url.Values{
		"invoice_id":                 {fmt.Sprintf("%d", invoiceID)},
		"invoice_number":             {"EDIT-003"},
		"customer_id":                {fmt.Sprintf("%d", customerID)},
		"invoice_date":               {"2026-01-15"},
		"line_count":                 {"1"},
		"line_product_service_id[0]": {fmt.Sprintf("%d", inactiveProductID)},
		"line_description[0]":        {"Archived Service"},
		"line_qty[0]":                {"1"},
		"line_unit_price[0]":         {"100.00"},
	}, "")

	// Must be rejected — inactive product fails validateInvoiceDraftReferences.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (form error) for inactive product, got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "invalid product/service") {
		t.Fatalf("expected inactive product error in body, got: %q", body)
	}

	// Original line with activeProductID must still be there.
	var lines []models.InvoiceLine
	if err := db.Where("invoice_id = ?", invoiceID).Find(&lines).Error; err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected 1 original line preserved, got %d", len(lines))
	}
	if lines[0].ProductServiceID == nil || *lines[0].ProductServiceID != activeProductID {
		t.Fatalf("expected original activeProductID=%d preserved, got %v", activeProductID, lines[0].ProductServiceID)
	}
}

// TestInvoiceEditDraft_FreeFormLineCompatible verifies that a draft with a
// free-form line (nil ProductServiceID) can still be saved via edit, and the
// re-saved line remains free-form.
func TestInvoiceEditDraft_FreeFormLineCompatible(t *testing.T) {
	db := testInvoiceEditDB(t)
	server := &Server{DB: db}
	user := seedInvoiceEditUser(t, db)

	companyID := seedValidationCompany(t, db, "Free Form Edit Co")
	customerID := seedValidationCustomer(t, db, companyID, "Test Customer")

	// Draft was created with no product/service.
	invoiceID, _ := seedDraftInvoiceWithLine(t, db, companyID, customerID, nil, "Custom consulting")

	app := invoiceEditApp(server, user, companyID)

	resp := performFormRequest(t, app, http.MethodPost, "/invoices/save-draft", url.Values{
		"invoice_id":          {fmt.Sprintf("%d", invoiceID)},
		"invoice_number":      {"EDIT-004"},
		"customer_id":         {fmt.Sprintf("%d", customerID)},
		"invoice_date":        {"2026-01-15"},
		"line_count":          {"1"},
		// Intentionally no line_product_service_id — free-form.
		"line_description[0]": {"Custom consulting updated"},
		"line_qty[0]":         {"3"},
		"line_unit_price[0]":  {"50.00"},
	}, "")

	if resp.StatusCode != http.StatusSeeOther {
		body := readResponseBody(t, resp)
		t.Fatalf("expected 303 redirect for free-form edit, got %d — body: %q", resp.StatusCode, body)
	}

	// Re-saved line must have nil ProductServiceID.
	var lines []models.InvoiceLine
	if err := db.Where("invoice_id = ? AND company_id = ?", invoiceID, companyID).
		Find(&lines).Error; err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected 1 line after edit, got %d", len(lines))
	}
	if lines[0].ProductServiceID != nil {
		t.Fatalf("expected nil ProductServiceID for free-form line, got %v", lines[0].ProductServiceID)
	}
	if lines[0].Description != "Custom consulting updated" {
		t.Fatalf("expected updated description, got %q", lines[0].Description)
	}
}

// TestInvoiceEditDraft_RejectDoesNotPartiallyPersistLines verifies that when
// an edit save is rejected (cross-company product), the handler does not
// partially commit any InvoiceLine rows for the rejected submission. The invoice
// must have only its original lines after a rejection.
func TestInvoiceEditDraft_RejectDoesNotPartiallyPersistLines(t *testing.T) {
	db := testInvoiceEditDB(t)
	server := &Server{DB: db}
	user := seedInvoiceEditUser(t, db)

	companyA := seedValidationCompany(t, db, "No Partial Co")
	companyB := seedValidationCompany(t, db, "Beta Corp")
	customerA := seedValidationCustomer(t, db, companyA, "Customer A")
	revenueA := seedValidationAccount(t, db, companyA, "4000", models.RootRevenue, models.DetailServiceRevenue)
	revenueB := seedValidationAccount(t, db, companyB, "4000", models.RootRevenue, models.DetailServiceRevenue)
	productA := seedValidationProduct(t, db, companyA, revenueA, "Acme Service")
	productB := seedValidationProduct(t, db, companyB, revenueB, "Beta Service")

	pID := productA
	invoiceID, originalLineID := seedDraftInvoiceWithLine(t, db, companyA, customerA, &pID, "Acme Service")

	app := invoiceEditApp(server, user, companyA)

	// Submit a 2-line edit: first line uses cross-company product (should cause
	// validateInvoiceDraftReferences to fail and abort the whole save).
	resp := performFormRequest(t, app, http.MethodPost, "/invoices/save-draft", url.Values{
		"invoice_id":                 {fmt.Sprintf("%d", invoiceID)},
		"invoice_number":             {"EDIT-005"},
		"customer_id":                {fmt.Sprintf("%d", customerA)},
		"invoice_date":               {"2026-01-15"},
		"line_count":                 {"2"},
		"line_product_service_id[0]": {fmt.Sprintf("%d", productB)}, // cross-company
		"line_description[0]":        {"Beta Service"},
		"line_qty[0]":                {"1"},
		"line_unit_price[0]":         {"100.00"},
		"line_description[1]":        {"Extra work"},
		"line_qty[1]":                {"1"},
		"line_unit_price[1]":         {"50.00"},
	}, "")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (form error), got %d", resp.StatusCode)
	}

	// Verify: invoice still has exactly 1 original line.
	var lines []models.InvoiceLine
	if err := db.Where("invoice_id = ?", invoiceID).Find(&lines).Error; err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected 1 original line (no partial commit), got %d", len(lines))
	}
	if lines[0].ID != originalLineID {
		t.Fatalf("expected original line ID %d to survive, got %d", originalLineID, lines[0].ID)
	}
}
