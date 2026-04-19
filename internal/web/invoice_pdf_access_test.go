// 遵循project_guide.md
package web

// invoice_pdf_access_test.go — Batch 8.1 access-control, capability-gating, and
// channel conversion document-number validation tests.
//
// Coverage:
//   TestInvoicePDF_UnauthenticatedRedirects       — GET /invoices/:id/pdf without auth → redirect
//   TestInvoicePDF_CrossCompanyDenied             — authed user of company B cannot download company A's PDF
//   TestInvoicePDF_BadFilenameInContentDisposition— malicious invoice_number is sanitized in header
//   TestInvoiceDetail_PDFLinkHiddenWhenNoPDFEngine — Download PDF link absent when wkhtmltopdf not installed
//   TestInvoiceDetail_PrintLinkShownWhenNoPDFEngine— Print link present when wkhtmltopdf not installed
//   TestChannelConvert_RejectsInvalidDocNumber    — handleChannelOrderConvert rejects invoice numbers
//                                                    with illegal characters (handler layer)
//   TestChannelConvert_RejectsEmptyDocNumber      — empty invoice_number blocked at handler layer
//   TestChannelConvertService_RejectsInvalidDocNumber — ConvertChannelOrderToDraftInvoice service guard
//
// wkhtmltopdf happy-path (TestInvoicePDF_HappyPath):
//   Skipped when wkhtmltopdf is absent. This test should run in a CI lane that
//   has wkhtmltopdf installed. The recommended approach for this repo (Windows dev,
//   Linux prod) is a GitHub Actions job with:
//     - apt-get install wkhtmltopdf
//     - go test ./internal/web/... -run TestInvoicePDF_HappyPath
//   The test is tagged with the standard Go skip mechanism (t.Skip) so that it
//   does not block the default test run while still being a real executable test
//   when the binary is present. No build tag required.

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
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

// ── DB helper ─────────────────────────────────────────────────────────────────

func pdfAccessDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:pdf_access_%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
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
		&models.InvoiceHostedLink{},
		&models.PaymentGatewayAccount{},
		&models.PaymentAccountingMapping{},
		&models.PaymentRequest{},
		&models.HostedPaymentAttempt{},
		&models.Session{},
		// Channel tables needed for conversion tests.
		&models.SalesChannelAccount{},
		&models.ChannelOrder{},
		&models.ChannelOrderLine{},
		&models.ItemChannelMapping{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

// seedPDFBase creates company, user, membership, customer, and issued invoice.
func seedPDFBase(t *testing.T, db *gorm.DB) (models.Company, *models.User, models.Invoice) {
	t.Helper()
	co := models.Company{Name: "PDF Co", BaseCurrencyCode: "USD", IsActive: true}
	db.Create(&co)
	u := models.User{
		ID:           uuid.New(),
		Email:        fmt.Sprintf("pdf_%d@test.com", time.Now().UnixNano()),
		PasswordHash: "x",
		IsActive:     true,
	}
	db.Create(&u)
	db.Create(&models.CompanyMembership{UserID: u.ID, CompanyID: co.ID, Role: "owner"})

	cust := models.Customer{CompanyID: co.ID, Name: "Test Cust"}
	db.Create(&cust)

	inv := models.Invoice{
		CompanyID:     co.ID,
		CustomerID:    cust.ID,
		InvoiceNumber: "INV-PDF-001",
		Status:        models.InvoiceStatusIssued,
		Amount:        decimal.NewFromFloat(100),
		BalanceDue:    decimal.NewFromFloat(100),
	}
	db.Create(&inv)
	return co, &u, inv
}

// authPDFApp builds an app with manual auth injection — simulates a fully-authed user.
func authPDFApp(server *Server, user *models.User, companyID uint) *fiber.App {
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
	app.Get("/invoices/:id/pdf", server.handleInvoicePDF)
	app.Get("/invoices/:id", server.handleInvoiceDetail)
	return app
}

// unauthPDFApp builds an app with the real RequireAuth middleware (no user injected).
func unauthPDFApp(server *Server) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: func(c *fiber.Ctx, err error) error {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}})
	// Wire RequireAuth directly — unauthenticated requests get redirected.
	app.Get("/invoices/:id/pdf", server.RequireAuth(), server.handleInvoicePDF)
	return app
}

// ── Access-control tests ──────────────────────────────────────────────────────

func TestInvoicePDF_UnauthenticatedRedirects(t *testing.T) {
	db := pdfAccessDB(t)
	_, _, inv := seedPDFBase(t, db)
	srv := &Server{DB: db}
	app := unauthPDFApp(srv)

	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/invoices/%d/pdf", inv.ID), nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	// RequireAuth redirects to /login (303 SeeOther).
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("unauthenticated PDF: expected 303, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/login") {
		t.Errorf("unauthenticated PDF: expected redirect to /login, got %q", loc)
	}
}

func TestInvoicePDF_CrossCompanyDenied(t *testing.T) {
	db := pdfAccessDB(t)

	// Company A owns the invoice.
	coA, _, invA := seedPDFBase(t, db)

	// Company B has a different user.
	coB := models.Company{Name: "Co B", BaseCurrencyCode: "USD", IsActive: true}
	db.Create(&coB)
	uB := models.User{
		ID:           uuid.New(),
		Email:        fmt.Sprintf("b_%d@test.com", time.Now().UnixNano()),
		PasswordHash: "x",
		IsActive:     true,
	}
	db.Create(&uB)
	db.Create(&models.CompanyMembership{UserID: uB.ID, CompanyID: coB.ID, Role: "owner"})

	// User B authenticates into company B but requests company A's invoice ID.
	srv := &Server{DB: db}
	app := authPDFApp(srv, &uB, coB.ID)

	_ = coA // suppress unused warning
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/invoices/%d/pdf", invA.ID), nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	// handleInvoicePDF calls loadInvoiceForRender(db, companyID=coB.ID, invoiceID=invA.ID).
	// This returns not-found because invA.company_id = coA.ID ≠ coB.ID.
	// Handler returns 404 "invoice not found".
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("cross-company PDF: expected 404, got %d; body: %s", resp.StatusCode, body)
	}
}

// ── Filename safety in Content-Disposition ────────────────────────────────────

func TestInvoicePDF_BadFilenameInContentDisposition(t *testing.T) {
	if _, err := exec.LookPath("wkhtmltopdf"); err != nil {
		t.Skip("wkhtmltopdf not installed — Content-Disposition header test skipped; run on CI with wkhtmltopdf")
	}
	db := pdfAccessDB(t)
	co := models.Company{Name: "Bad Name Co", BaseCurrencyCode: "USD", IsActive: true}
	db.Create(&co)
	u := models.User{
		ID:           uuid.New(),
		Email:        fmt.Sprintf("bad_%d@test.com", time.Now().UnixNano()),
		PasswordHash: "x",
		IsActive:     true,
	}
	db.Create(&u)
	db.Create(&models.CompanyMembership{UserID: u.ID, CompanyID: co.ID, Role: "owner"})
	cust := models.Customer{CompanyID: co.ID, Name: "C"}
	db.Create(&cust)

	// Invoice with a dangerous invoice number containing header-injection characters.
	inv := models.Invoice{
		CompanyID:     co.ID,
		CustomerID:    cust.ID,
		InvoiceNumber: `evil"; filename=malware.exe`,
		Status:        models.InvoiceStatusIssued,
		Amount:        decimal.NewFromFloat(50),
		BalanceDue:    decimal.NewFromFloat(50),
	}
	db.Create(&inv)

	srv := &Server{DB: db}
	app := authPDFApp(srv, &u, co.ID)

	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/invoices/%d/pdf", inv.ID), nil)
	resp, err := app.Test(req, 60_000)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	cd := resp.Header.Get("Content-Disposition")
	// The sanitized filename must not contain quotes, semicolons, or injection chars.
	for _, bad := range []string{`"`, `;`, "\r", "\n", "<", ">"} {
		if strings.Contains(cd, bad) {
			t.Errorf("Content-Disposition contains unsafe char %q: %s", bad, cd)
		}
	}
	// Must still be a valid attachment disposition.
	if !strings.HasPrefix(cd, `attachment; filename="`) {
		t.Errorf("Content-Disposition has unexpected format: %s", cd)
	}
}

// ── Internal capability gating tests ─────────────────────────────────────────

func TestInvoiceDetail_PDFLinkHiddenWhenNoPDFEngine(t *testing.T) {
	if _, err := exec.LookPath("wkhtmltopdf"); err == nil {
		t.Skip("wkhtmltopdf is installed — PDFAvailable=true; skip no-engine test")
	}
	db := pdfAccessDB(t)
	_, u, inv := seedPDFBase(t, db)
	srv := &Server{DB: db}
	app := authPDFApp(srv, u, inv.CompanyID)

	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/invoices/%d", inv.ID), nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("invoice detail: expected 200, got %d: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	s := string(body)

	// When wkhtmltopdf is absent, the Download PDF link must not appear.
	if strings.Contains(s, `href="/invoices/`) && strings.Contains(s, `/pdf"`) {
		t.Error("Download PDF link should not appear when wkhtmltopdf is absent")
	}
	// The download PDF text must not appear as a clickable link.
	if strings.Contains(s, "Download PDF") {
		t.Error("'Download PDF' text should not appear when wkhtmltopdf is absent")
	}
}

func TestInvoiceDetail_PrintLinkShownWhenNoPDFEngine(t *testing.T) {
	if _, err := exec.LookPath("wkhtmltopdf"); err == nil {
		t.Skip("wkhtmltopdf is installed — PDFAvailable=true; this test is for absent-engine path")
	}
	db := pdfAccessDB(t)
	_, u, inv := seedPDFBase(t, db)
	srv := &Server{DB: db}
	app := authPDFApp(srv, u, inv.CompanyID)

	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/invoices/%d", inv.ID), nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	s := string(body)

	// Print / Save PDF fallback link must be shown.
	if !strings.Contains(s, "/print") {
		t.Error("Print link should appear when wkhtmltopdf is absent")
	}
	if !strings.Contains(s, "Print / Save PDF") {
		t.Error("'Print / Save PDF' text should appear when wkhtmltopdf is absent")
	}
}

// ── Channel conversion document-number validation ─────────────────────────────

// seedChannelConvertBase creates the minimum data for a convertible channel order.
func seedChannelConvertBase(t *testing.T, db *gorm.DB) (models.Company, *models.User, models.Customer, models.ChannelOrder) {
	t.Helper()
	co := models.Company{Name: "Chan Co", BaseCurrencyCode: "USD", IsActive: true}
	db.Create(&co)
	u := models.User{
		ID:           uuid.New(),
		Email:        fmt.Sprintf("chan_%d@test.com", time.Now().UnixNano()),
		PasswordHash: "x",
		IsActive:     true,
	}
	db.Create(&u)
	db.Create(&models.CompanyMembership{UserID: u.ID, CompanyID: co.ID, Role: "owner"})

	cust := models.Customer{CompanyID: co.ID, Name: "Chan Cust"}
	db.Create(&cust)

	ch := models.SalesChannelAccount{CompanyID: co.ID, ChannelType: models.ChannelTypeShopify, DisplayName: "My Shop", IsActive: true}
	db.Create(&ch)

	order := models.ChannelOrder{
		CompanyID:        co.ID,
		ChannelAccountID: ch.ID,
		ExternalOrderID:  "ORD-123",
		RawPayload:       datatypes.JSON(`{}`),
	}
	db.Create(&order)

	return co, &u, cust, order
}

// channelConvertApp builds a Fiber app with the channel convert handler and auth injected.
func channelConvertApp(server *Server, user *models.User, companyID uint) *fiber.App {
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
	app.Post("/settings/channels/orders/:id/convert", server.handleChannelOrderConvert)
	return app
}

func TestChannelConvert_RejectsInvalidDocNumber(t *testing.T) {
	db := pdfAccessDB(t)
	co, u, cust, order := seedChannelConvertBase(t, db)
	srv := &Server{DB: db}
	app := channelConvertApp(srv, u, co.ID)

	invalidNumbers := []string{
		"INV 001",     // space not allowed
		"INV/001",     // slash not allowed
		"INV\\001",    // backslash not allowed
		`INV"001`,     // quote not allowed
		"INV;001",     // semicolon not allowed
		"INV<001>",    // angle brackets not allowed
		"INV\r\n001",  // control chars not allowed
	}

	for _, num := range invalidNumbers {
		form := url.Values{}
		form.Set("customer_id", fmt.Sprint(cust.ID))
		form.Set("invoice_number", num)

		req, _ := http.NewRequest(http.MethodPost,
			fmt.Sprintf("/settings/channels/orders/%d/convert", order.ID),
			strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("invoice_number %q: request failed: %v", num, err)
		}

		// Handler redirects with ?error= parameter when validation fails.
		if resp.StatusCode != http.StatusSeeOther {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("invoice_number %q: expected 303 redirect, got %d: %s", num, resp.StatusCode, body)
			continue
		}
		loc := resp.Header.Get("Location")
		if !strings.Contains(loc, "error=") {
			t.Errorf("invoice_number %q: redirect location missing ?error=: %s", num, loc)
		}

		// Verify the invoice was NOT created in the database.
		var count int64
		db.Model(&models.Invoice{}).Where("company_id = ? AND invoice_number = ?", co.ID, num).Count(&count)
		if count > 0 {
			t.Errorf("invoice_number %q: dirty invoice_number was written to DB", num)
		}
	}
}

func TestChannelConvert_RejectsEmptyDocNumber(t *testing.T) {
	db := pdfAccessDB(t)
	co, u, cust, order := seedChannelConvertBase(t, db)
	srv := &Server{DB: db}
	app := channelConvertApp(srv, u, co.ID)

	form := url.Values{}
	form.Set("customer_id", fmt.Sprint(cust.ID))
	form.Set("invoice_number", "")

	req, _ := http.NewRequest(http.MethodPost,
		fmt.Sprintf("/settings/channels/orders/%d/convert", order.ID),
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("empty invoice_number: expected 303, got %d: %s", resp.StatusCode, body)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "error=") {
		t.Errorf("empty invoice_number: redirect missing ?error=: %s", loc)
	}
}

// ── Channel conversion service-layer guard ────────────────────────────────────

func TestChannelConvertService_RejectsInvalidDocNumber(t *testing.T) {
	// Tests the service-layer guard directly — independent of the handler.
	// This verifies that the DB is protected even when the handler is bypassed
	// (e.g., from tests, CLI tools, or future API endpoints).
	db := pdfAccessDB(t)
	co := models.Company{Name: "Svc Co", BaseCurrencyCode: "USD", IsActive: true}
	db.Create(&co)
	cust := models.Customer{CompanyID: co.ID, Name: "Svc Cust"}
	db.Create(&cust)

	ch := models.SalesChannelAccount{CompanyID: co.ID, ChannelType: models.ChannelTypeShopify, DisplayName: "Shop", IsActive: true}
	db.Create(&ch)
	order := models.ChannelOrder{
		CompanyID: co.ID, ChannelAccountID: ch.ID, ExternalOrderID: "SVC-456",
		RawPayload: datatypes.JSON(`{}`),
	}
	db.Create(&order)

	invalidNumbers := []string{"INV 001", "INV/001", `INV"001`, "INV;001", ""}
	for _, num := range invalidNumbers {
		_, err := services.ConvertChannelOrderToDraftInvoice(db, services.ConvertOptions{
			CompanyID:      co.ID,
			ChannelOrderID: order.ID,
			CustomerID:     cust.ID,
			InvoiceNumber:  num,
			InvoiceDate:    time.Now(),
		})
		if err == nil {
			t.Errorf("ConvertChannelOrderToDraftInvoice with invoice_number=%q: expected error, got nil", num)
		}

		// Verify nothing was written.
		var count int64
		db.Model(&models.Invoice{}).Where("company_id = ?", co.ID).Count(&count)
		if count > 0 {
			t.Errorf("invoice_number=%q: invoice row was created despite invalid document number", num)
		}
	}
}

// ── wkhtmltopdf happy-path ────────────────────────────────────────────────────
//
// This test runs only when wkhtmltopdf is installed. In this repository's dev
// environment (Windows), wkhtmltopdf is typically absent, so the test is skipped.
//
// CI strategy (recommended for GitHub Actions):
//
//   jobs:
//     test-with-pdf:
//       runs-on: ubuntu-latest
//       steps:
//         - uses: actions/checkout@v4
//         - run: sudo apt-get install -y wkhtmltopdf
//         - run: go test ./internal/web/... -run TestInvoicePDF_HappyPath -timeout 120s
//
// This approach is preferable to a build tag because:
//   - The test is always compiled and can be reviewed as live code.
//   - It self-documents its requirement via t.Skip.
//   - CI can run it unconditionally in the right environment without code changes.
//   - No risk of drift between tagged and untagged code paths.

func TestInvoicePDF_HappyPath(t *testing.T) {
	if _, err := exec.LookPath("wkhtmltopdf"); err != nil {
		t.Skip("wkhtmltopdf not installed — skipped; run on CI with: apt-get install wkhtmltopdf")
	}
	db := pdfAccessDB(t)
	_, u, inv := seedPDFBase(t, db)
	srv := &Server{DB: db}
	app := authPDFApp(srv, u, inv.CompanyID)

	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("/invoices/%d/pdf", inv.ID), nil)
	resp, err := app.Test(req, 60_000)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("Content-Type = %q, want application/pdf", ct)
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, `attachment; filename="Invoice-`) {
		t.Errorf("Content-Disposition = %q, unexpected format", cd)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(body), "%PDF") {
		t.Error("response body does not start with %PDF")
	}
}
