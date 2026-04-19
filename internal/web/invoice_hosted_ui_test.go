// 遵循project_guide.md
package web

// invoice_hosted_ui_test.go — Handler tests for Batch 6 Hosted Invoice.
//
// Coverage:
//   TestHandleCreateShareLink_HappyPath           — POST → 303 with ?newlink= token
//   TestHandleCreateShareLink_DuplicateBlocked    — second create → ?error= redirect
//   TestHandleCreateShareLink_CompanyIsolation    — wrong company cannot create link
//   TestHandleRevokeShareLink_HappyPath           — POST → 303 to base invoice URL
//   TestHandleRevokeShareLink_NoActiveLink        — POST → ?error= redirect
//   TestHandleRegenerateShareLink_HappyPath       — POST → 303 with new ?newlink= token
//   TestHandleRegenerateShareLink_CompanyIsolation — wrong company → error
//   TestHandleHostedInvoice_ValidToken            — GET → 200 with invoice number
//   TestHandleHostedInvoice_InvalidToken          — GET → 410 with generic page
//   TestHandleHostedInvoice_RevokedToken          — GET → 410 with generic page
//   TestHandleHostedInvoice_ExpiredToken          — GET → 410 with generic page
//   TestHandleHostedInvoice_NoInternalUI          — hosted page must not expose internal nav
//   TestHandleHostedInvoice_PayNowSlotDisabled    — payable invoice shows disabled Pay Now
//   TestHandleHostedInvoice_CompanyIsolation      — token from co1 cannot expose co2 invoice
//   TestHandleGetInvoiceEmailPreview_HappyPath    — returns subject+body JSON
//   TestHandleGetInvoiceEmailPreview_CompanyIsolation — wrong company → 500

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"gobooks/internal/models"
	"gobooks/internal/services"
)

// ── DB + seed helpers ─────────────────────────────────────────────────────────

func testHostedDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:web_hosted_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.User{},
		&models.CompanyMembership{},
		&models.AuditLog{},
		&models.Customer{},
		&models.Account{},
		&models.TaxCode{},
		&models.ProductService{},
		&models.ItemComponent{},
		&models.PaymentTerm{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.InventoryMovement{},
		&models.InventoryBalance{},
		&models.InventoryCostLayer{},
		&models.InventoryLayerConsumption{},
		&models.PaymentReceipt{},
		&models.SettlementAllocation{},
		&models.PaymentTransaction{},
		&models.TaskInvoiceSource{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.InvoiceTemplate{},
		&models.InvoiceEmailLog{},
		&models.CompanyNotificationSettings{},
		&models.SystemNotificationSettings{},
		&models.NumberingSetting{},
		&models.PaymentGatewayAccount{},
		&models.PaymentAccountingMapping{},
		&models.PaymentRequest{},
		&models.InvoiceHostedLink{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedHostedUser(t *testing.T, db *gorm.DB) *models.User {
	t.Helper()
	u := models.User{
		ID:           uuid.New(),
		Email:        fmt.Sprintf("hst_%d@test.com", time.Now().UnixNano()),
		PasswordHash: "x",
		IsActive:     true,
	}
	db.Create(&u)
	return &u
}

func seedHostedCompany(t *testing.T, db *gorm.DB, name string) *models.Company {
	t.Helper()
	co := models.Company{Name: name, BaseCurrencyCode: "CAD", IsActive: true}
	db.Create(&co)
	return &co
}

func seedHostedMembership(t *testing.T, db *gorm.DB, userID uuid.UUID, companyID uint) {
	t.Helper()
	m := models.CompanyMembership{UserID: userID, CompanyID: companyID, Role: "owner", IsActive: true}
	db.Create(&m)
}

func seedHostedInvoice(t *testing.T, db *gorm.DB, companyID uint, status models.InvoiceStatus, invNumber string) *models.Invoice {
	t.Helper()
	cust := models.Customer{CompanyID: companyID, Name: "Hosted Cust", Email: "hst@example.com"}
	db.Create(&cust)
	inv := models.Invoice{
		CompanyID:             companyID,
		InvoiceNumber:         invNumber,
		CustomerID:            cust.ID,
		InvoiceDate:           time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Status:                status,
		Amount:                decimal.RequireFromString("250.00"),
		Subtotal:              decimal.RequireFromString("250.00"),
		TaxTotal:              decimal.Zero,
		BalanceDue:            decimal.RequireFromString("250.00"),
		BalanceDueBase:        decimal.RequireFromString("250.00"),
		CustomerNameSnapshot:  "Hosted Cust",
		CustomerEmailSnapshot: "hst@example.com",
	}
	db.Create(&inv)
	_ = datatypes.JSON("{}") // keep datatypes import live
	return &inv
}

// hostedApp builds a minimal Fiber app for internal auth-gated hosted link management.
func hostedApp(server *Server, user *models.User, companyID uint) *fiber.App {
	membership := &models.CompanyMembership{UserID: user.ID, CompanyID: companyID, Role: "owner"}
	app := fiber.New(fiber.Config{ErrorHandler: func(c *fiber.Ctx, err error) error {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(LocalsUser, user)
		c.Locals(LocalsActiveCompanyID, companyID)
		c.Locals(LocalsCompanyMembership, membership)
		return c.Next()
	})
	app.Post("/invoices/:id/share-link", server.handleCreateShareLink)
	app.Post("/invoices/:id/share-link/revoke", server.handleRevokeShareLink)
	app.Post("/invoices/:id/share-link/regenerate", server.handleRegenerateShareLink)
	app.Get("/api/invoices/:id/email-preview", server.handleGetInvoiceEmailPreview)
	return app
}

// publicApp builds a no-auth Fiber app for the hosted invoice public route.
func publicApp(server *Server) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: func(c *fiber.Ctx, err error) error {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}})
	app.Get("/i/:token", server.handleHostedInvoice)
	return app
}

func postRequest(t *testing.T, app *fiber.App, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func getRequest(t *testing.T, app *fiber.App, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// ── handleCreateShareLink ────────────────────────────────────────────────────

func TestHandleCreateShareLink_HappyPath(t *testing.T) {
	db := testHostedDB(t)
	user := seedHostedUser(t, db)
	co := seedHostedCompany(t, db, "Co1")
	seedHostedMembership(t, db, user.ID, co.ID)
	inv := seedHostedInvoice(t, db, co.ID, models.InvoiceStatusIssued, "INV-HL-001")

	server := &Server{DB: db}
	app := hostedApp(server, user, co.ID)

	resp := postRequest(t, app, fmt.Sprintf("/invoices/%d/share-link", inv.ID))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", resp.StatusCode)
	}

	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "newlink=") {
		t.Fatalf("expected ?newlink= in redirect, got %q", loc)
	}
	// Token must not be empty (newlink= must have a value).
	parts := strings.SplitN(loc, "newlink=", 2)
	if len(parts) < 2 || parts[1] == "" {
		t.Fatal("newlink query param is empty")
	}
}

func TestHandleCreateShareLink_DuplicateBlocked(t *testing.T) {
	db := testHostedDB(t)
	user := seedHostedUser(t, db)
	co := seedHostedCompany(t, db, "Co1")
	seedHostedMembership(t, db, user.ID, co.ID)
	inv := seedHostedInvoice(t, db, co.ID, models.InvoiceStatusIssued, "INV-HL-002")

	server := &Server{DB: db}
	app := hostedApp(server, user, co.ID)

	// First create — succeeds.
	postRequest(t, app, fmt.Sprintf("/invoices/%d/share-link", inv.ID))

	// Second create — must redirect with ?error=
	resp := postRequest(t, app, fmt.Sprintf("/invoices/%d/share-link", inv.ID))
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "error=") {
		t.Fatalf("expected ?error= redirect on duplicate, got %q", loc)
	}
}

func TestHandleCreateShareLink_CompanyIsolation(t *testing.T) {
	db := testHostedDB(t)
	user := seedHostedUser(t, db)
	co1 := seedHostedCompany(t, db, "Co1")
	co2 := seedHostedCompany(t, db, "Co2")
	seedHostedMembership(t, db, user.ID, co1.ID)
	inv2 := seedHostedInvoice(t, db, co2.ID, models.InvoiceStatusIssued, "INV-HL-003")

	server := &Server{DB: db}
	// Authenticated as co1 but trying to create link for co2's invoice.
	app := hostedApp(server, user, co1.ID)

	resp := postRequest(t, app, fmt.Sprintf("/invoices/%d/share-link", inv2.ID))
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "error=") {
		t.Fatalf("expected ?error= for cross-company create, got %q", loc)
	}
}

// ── handleRevokeShareLink ────────────────────────────────────────────────────

func TestHandleRevokeShareLink_HappyPath(t *testing.T) {
	db := testHostedDB(t)
	user := seedHostedUser(t, db)
	co := seedHostedCompany(t, db, "Co1")
	seedHostedMembership(t, db, user.ID, co.ID)
	inv := seedHostedInvoice(t, db, co.ID, models.InvoiceStatusIssued, "INV-HL-004")
	services.CreateHostedLink(db, co.ID, inv.ID, nil)

	server := &Server{DB: db}
	app := hostedApp(server, user, co.ID)

	resp := postRequest(t, app, fmt.Sprintf("/invoices/%d/share-link/revoke", inv.ID))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if strings.Contains(loc, "error=") {
		t.Fatalf("unexpected error in redirect: %q", loc)
	}

	// Verify no active link remains.
	if _, err := services.GetActiveHostedLink(db, co.ID, inv.ID); err == nil {
		t.Fatal("expected no active link after revoke")
	}
}

func TestHandleRevokeShareLink_NoActiveLink(t *testing.T) {
	db := testHostedDB(t)
	user := seedHostedUser(t, db)
	co := seedHostedCompany(t, db, "Co1")
	seedHostedMembership(t, db, user.ID, co.ID)
	inv := seedHostedInvoice(t, db, co.ID, models.InvoiceStatusIssued, "INV-HL-005")

	server := &Server{DB: db}
	app := hostedApp(server, user, co.ID)

	resp := postRequest(t, app, fmt.Sprintf("/invoices/%d/share-link/revoke", inv.ID))
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "error=") {
		t.Fatalf("expected ?error= when no active link, got %q", loc)
	}
}

// ── handleRegenerateShareLink ────────────────────────────────────────────────

func TestHandleRegenerateShareLink_HappyPath(t *testing.T) {
	db := testHostedDB(t)
	user := seedHostedUser(t, db)
	co := seedHostedCompany(t, db, "Co1")
	seedHostedMembership(t, db, user.ID, co.ID)
	inv := seedHostedInvoice(t, db, co.ID, models.InvoiceStatusIssued, "INV-HL-006")
	pt1, _, _ := services.CreateHostedLink(db, co.ID, inv.ID, nil)

	server := &Server{DB: db}
	app := hostedApp(server, user, co.ID)

	resp := postRequest(t, app, fmt.Sprintf("/invoices/%d/share-link/regenerate", inv.ID))
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "newlink=") {
		t.Fatalf("expected ?newlink= in redirect, got %q", loc)
	}

	// Extract new token from redirect and verify it differs from old.
	parts := strings.SplitN(loc, "newlink=", 2)
	pt2 := parts[1]
	if pt2 == pt1 {
		t.Fatal("regenerated token should differ from original")
	}

	// Old token should now be invalid.
	pubApp := publicApp(server)
	oldResp := getRequest(t, pubApp, "/i/"+pt1)
	if oldResp.StatusCode != http.StatusGone {
		t.Fatalf("old token should return 410 after regenerate, got %d", oldResp.StatusCode)
	}
}

func TestHandleRegenerateShareLink_CompanyIsolation(t *testing.T) {
	db := testHostedDB(t)
	user := seedHostedUser(t, db)
	co1 := seedHostedCompany(t, db, "Co1")
	co2 := seedHostedCompany(t, db, "Co2")
	seedHostedMembership(t, db, user.ID, co1.ID)
	inv2 := seedHostedInvoice(t, db, co2.ID, models.InvoiceStatusIssued, "INV-HL-007")

	server := &Server{DB: db}
	app := hostedApp(server, user, co1.ID)

	resp := postRequest(t, app, fmt.Sprintf("/invoices/%d/share-link/regenerate", inv2.ID))
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "error=") {
		t.Fatalf("expected ?error= for cross-company regenerate, got %q", loc)
	}
}

// ── handleHostedInvoice (public) ─────────────────────────────────────────────

func TestHandleHostedInvoice_ValidToken(t *testing.T) {
	db := testHostedDB(t)
	co := seedHostedCompany(t, db, "Render Co")
	inv := seedHostedInvoice(t, db, co.ID, models.InvoiceStatusIssued, "INV-PUB-001")

	pt, _, _ := services.CreateHostedLink(db, co.ID, inv.ID, nil)

	server := &Server{DB: db}
	app := publicApp(server)

	resp := getRequest(t, app, "/i/"+pt)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for valid token, got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, "INV-PUB-001") {
		t.Error("hosted page should contain invoice number")
	}
	if !strings.Contains(body, "Render Co") {
		t.Error("hosted page should contain company name")
	}
}

func TestHandleHostedInvoice_InvalidToken(t *testing.T) {
	db := testHostedDB(t)
	server := &Server{DB: db}
	app := publicApp(server)

	resp := getRequest(t, app, "/i/totallyinvalidtoken")
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("expected 410 for invalid token, got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	// Must not reveal internal info, must show generic message.
	if strings.Contains(body, "stack") || strings.Contains(body, "gorm") || strings.Contains(body, "SQL") {
		t.Error("error page must not expose internal error details")
	}
	if !strings.Contains(body, "not available") && !strings.Contains(body, "Not Available") {
		t.Error("error page should contain generic not-available message")
	}
}

func TestHandleHostedInvoice_RevokedToken(t *testing.T) {
	db := testHostedDB(t)
	co := seedHostedCompany(t, db, "Revoke Co")
	inv := seedHostedInvoice(t, db, co.ID, models.InvoiceStatusIssued, "INV-PUB-002")
	pt, _, _ := services.CreateHostedLink(db, co.ID, inv.ID, nil)
	services.RevokeHostedLink(db, co.ID, inv.ID)

	server := &Server{DB: db}
	app := publicApp(server)

	resp := getRequest(t, app, "/i/"+pt)
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("expected 410 for revoked token, got %d", resp.StatusCode)
	}
}

func TestHandleHostedInvoice_ExpiredToken(t *testing.T) {
	db := testHostedDB(t)
	co := seedHostedCompany(t, db, "Expired Co")
	inv := seedHostedInvoice(t, db, co.ID, models.InvoiceStatusIssued, "INV-PUB-003")
	pt, link, _ := services.CreateHostedLink(db, co.ID, inv.ID, nil)
	// Set expires_at to the past.
	past := time.Now().Add(-time.Hour)
	db.Model(&models.InvoiceHostedLink{}).Where("id = ?", link.ID).Update("expires_at", past)

	server := &Server{DB: db}
	app := publicApp(server)

	resp := getRequest(t, app, "/i/"+pt)
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("expected 410 for expired token, got %d", resp.StatusCode)
	}
}

func TestHandleHostedInvoice_NoInternalUI(t *testing.T) {
	db := testHostedDB(t)
	co := seedHostedCompany(t, db, "Clean Co")
	inv := seedHostedInvoice(t, db, co.ID, models.InvoiceStatusIssued, "INV-PUB-004")
	pt, _, _ := services.CreateHostedLink(db, co.ID, inv.ID, nil)

	server := &Server{DB: db}
	app := publicApp(server)

	resp := getRequest(t, app, "/i/"+pt)
	body := readResponseBody(t, resp)

	// Must not contain internal navigation or admin UI markers.
	internalMarkers := []string{
		"/invoices/" + fmt.Sprintf("%d", inv.ID) + "/void",
		"/invoices/" + fmt.Sprintf("%d", inv.ID) + "/edit",
		"handleInvoiceDetail",
		"receive-payment",
		"send-email",
	}
	for _, marker := range internalMarkers {
		if strings.Contains(body, marker) {
			t.Errorf("hosted page must not contain internal UI marker: %q", marker)
		}
	}
	// Must not expose internal IDs as text (invoice row ID in admin tables etc.)
	// The invoice number is OK (customer-facing); raw DB IDs should not appear.
	if strings.Contains(body, `"id":`) {
		t.Error("hosted page must not expose JSON id fields")
	}
}

func TestHandleHostedInvoice_PayNowSlotDisabled(t *testing.T) {
	db := testHostedDB(t)
	co := seedHostedCompany(t, db, "Pay Co")
	inv := seedHostedInvoice(t, db, co.ID, models.InvoiceStatusIssued, "INV-PUB-005")
	pt, _, _ := services.CreateHostedLink(db, co.ID, inv.ID, nil)

	server := &Server{DB: db}
	app := publicApp(server)

	resp := getRequest(t, app, "/i/"+pt)
	body := readResponseBody(t, resp)

	// Pay Now slot should be present as a disabled placeholder for payable invoices.
	if !strings.Contains(body, "Pay Now") {
		t.Error("hosted page should show disabled Pay Now slot for payable (issued) invoice")
	}
	// Must be disabled/non-clickable in Batch 6.
	if !strings.Contains(body, "hb-btn-dis") {
		t.Error("Pay Now slot must have disabled CSS class in Batch 6")
	}
}

func TestHandleHostedInvoice_CompanyIsolation(t *testing.T) {
	db := testHostedDB(t)
	co1 := seedHostedCompany(t, db, "Co1")
	co2 := seedHostedCompany(t, db, "Co2")
	inv1 := seedHostedInvoice(t, db, co1.ID, models.InvoiceStatusIssued, "INV-CO1-001")
	inv2 := seedHostedInvoice(t, db, co2.ID, models.InvoiceStatusIssued, "INV-CO2-001")

	// Create a link for co1's invoice.
	pt1, _, _ := services.CreateHostedLink(db, co1.ID, inv1.ID, nil)

	server := &Server{DB: db}
	app := publicApp(server)

	// Valid token for co1's invoice should show co1's invoice content, not co2's.
	resp := getRequest(t, app, "/i/"+pt1)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if strings.Contains(body, inv2.InvoiceNumber) {
		t.Error("hosted page must not expose another company's invoice data")
	}

	// co2's invoice cannot be accessed via co1's token.
	_ = inv2 // access via co1 token is impossible by design (link is bound to inv1)
}

// ── handleGetInvoiceEmailPreview ─────────────────────────────────────────────

func TestHandleGetInvoiceEmailPreview_HappyPath(t *testing.T) {
	db := testHostedDB(t)
	user := seedHostedUser(t, db)
	co := seedHostedCompany(t, db, "Preview Co")
	seedHostedMembership(t, db, user.ID, co.ID)
	inv := seedHostedInvoice(t, db, co.ID, models.InvoiceStatusIssued, "INV-PREV-001")

	server := &Server{DB: db}
	app := hostedApp(server, user, co.ID)

	resp := getRequest(t, app, fmt.Sprintf("/api/invoices/%d/email-preview", inv.ID))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := readResponseBody(t, resp)
	if !strings.Contains(body, `"subject"`) {
		t.Error("email preview response must contain 'subject' key")
	}
	if !strings.Contains(body, `"body"`) {
		t.Error("email preview response must contain 'body' key")
	}
}

func TestHandleGetInvoiceEmailPreview_CompanyIsolation(t *testing.T) {
	db := testHostedDB(t)
	user := seedHostedUser(t, db)
	co1 := seedHostedCompany(t, db, "Co1")
	co2 := seedHostedCompany(t, db, "Co2")
	seedHostedMembership(t, db, user.ID, co1.ID)
	inv2 := seedHostedInvoice(t, db, co2.ID, models.InvoiceStatusIssued, "INV-PREV-002")

	server := &Server{DB: db}
	// Authenticated as co1; trying to preview co2's invoice.
	app := hostedApp(server, user, co1.ID)

	resp := getRequest(t, app, fmt.Sprintf("/api/invoices/%d/email-preview", inv2.ID))
	// Should fail (invoice not found in co1's scope) → 500 or non-200.
	if resp.StatusCode == http.StatusOK {
		t.Fatal("email preview should fail for cross-company invoice")
	}
}
