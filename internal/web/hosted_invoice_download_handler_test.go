// 遵循project_guide.md
package web

// hosted_invoice_download_handler_test.go — Handler tests for Batch 8 hosted PDF download.
//
// Coverage:
//   TestHandleHostedInvoiceDownload_InvalidToken    — GET /i/bad/download → 410 Gone
//   TestHandleHostedInvoiceDownload_ExpiredToken    — expired link → 410 Gone
//   TestHandleHostedInvoiceDownload_RevokedToken    — revoked link → 410 Gone
//   TestHandleHostedInvoiceDownload_NoPDFEngine     — wkhtmltopdf absent → 503
//   TestHandleHostedInvoiceDownload_HappyPath       — wkhtmltopdf present → 200 PDF (skips if absent)
//   TestHostedDownload_CompanyIsolation             — token belongs to company A, company B has no access
//   TestInvoicePDFSafeFilename                      — filename sanitisation unit test
//   TestHandleHostedInvoice_CanDownloadFalseWhenNoPDFEngine — toolbar shows print button, not download link
//   TestHandleHostedInvoice_CanDownloadTrueWhenPDFEnginePresent — toolbar shows download link (skips if absent)
//   TestHostedDownload_CustomerBoundary             — download contains invoice data, not internal-UI elements
//   TestHostedDownload_FutureReadyBoundary          — no accounting records written by download

import (
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
	"gobooks/internal/services"
)

// ── DB + app helpers ──────────────────────────────────────────────────────────

func hostedDownloadDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:web_dl_%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
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
	); err != nil {
		t.Fatal(err)
	}
	return db
}

// downloadApp builds a Fiber app with the download route + the hosted invoice route (for toolbar tests).
func downloadApp(server *Server) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: func(c *fiber.Ctx, err error) error {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}})
	app.Get("/i/:token", server.handleHostedInvoice)
	app.Get("/i/:token/download", server.handleHostedInvoiceDownload)
	return app
}

// seedDownloadBase creates a company, customer, issued invoice, and a hosted link.
func seedDownloadBase(t *testing.T, db *gorm.DB) (models.Company, models.Invoice, string) {
	t.Helper()
	co := models.Company{Name: "DL Co", BaseCurrencyCode: "USD", IsActive: true}
	db.Create(&co)
	cust := models.Customer{CompanyID: co.ID, Name: "DL Customer"}
	db.Create(&cust)
	inv := models.Invoice{
		CompanyID:     co.ID,
		CustomerID:    cust.ID,
		InvoiceNumber: "INV-DL-001",
		Status:        models.InvoiceStatusIssued,
		Amount:        decimal.NewFromFloat(200),
		BalanceDue:    decimal.NewFromFloat(200),
	}
	db.Create(&inv)
	token, _, err := services.CreateHostedLink(db, co.ID, inv.ID, nil)
	if err != nil {
		t.Fatalf("CreateHostedLink: %v", err)
	}
	return co, inv, token
}

// wkhtmltopdfPresent returns true if wkhtmltopdf is installed on this machine.
func wkhtmltopdfPresent() bool {
	_, err := exec.LookPath("wkhtmltopdf")
	return err == nil
}

// ── Filename unit test ────────────────────────────────────────────────────────

func TestInvoicePDFSafeFilename(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"INV-001", "Invoice-INV-001.pdf"},
		{"2024/001", "Invoice-2024-001.pdf"},
		{"2024\\001", "Invoice-2024-001.pdf"},
		{"A/B\\C", "Invoice-A-B-C.pdf"},
		{"", "Invoice-unknown.pdf"}, // empty → fallback (Batch 8.1 hardening)
	}
	for _, tc := range cases {
		got := services.InvoicePDFSafeFilename(tc.input)
		if got != tc.want {
			t.Errorf("InvoicePDFSafeFilename(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ── Token validation tests ────────────────────────────────────────────────────

func TestHandleHostedInvoiceDownload_InvalidToken(t *testing.T) {
	db := hostedDownloadDB(t)
	srv := &Server{DB: db}
	app := downloadApp(srv)

	req, _ := http.NewRequest(http.MethodGet, "/i/notavalidtoken/download", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("expected 410, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Link Not Available") {
		t.Error("expected generic error page")
	}
}

func TestHandleHostedInvoiceDownload_ExpiredToken(t *testing.T) {
	db := hostedDownloadDB(t)
	_, _, token := seedDownloadBase(t, db)

	// Expire the link by setting ExpiresAt in the past.
	past := time.Now().Add(-1 * time.Hour)
	db.Model(&models.InvoiceHostedLink{}).
		Where("token_hash IS NOT NULL").
		Update("expires_at", past)

	srv := &Server{DB: db}
	app := downloadApp(srv)

	req, _ := http.NewRequest(http.MethodGet, "/i/"+token+"/download", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("expected 410 for expired token, got %d", resp.StatusCode)
	}
}

func TestHandleHostedInvoiceDownload_RevokedToken(t *testing.T) {
	db := hostedDownloadDB(t)
	_, _, token := seedDownloadBase(t, db)

	// Revoke: set status=revoked.
	db.Model(&models.InvoiceHostedLink{}).
		Where("status = ?", models.InvoiceHostedLinkStatusActive).
		Update("status", models.InvoiceHostedLinkStatusRevoked)

	srv := &Server{DB: db}
	app := downloadApp(srv)

	req, _ := http.NewRequest(http.MethodGet, "/i/"+token+"/download", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("expected 410 for revoked token, got %d", resp.StatusCode)
	}
}

// ── PDF engine tests ──────────────────────────────────────────────────────────

func TestHandleHostedInvoiceDownload_NoPDFEngine(t *testing.T) {
	if wkhtmltopdfPresent() {
		t.Skip("wkhtmltopdf is installed — this test only applies when it is absent")
	}
	db := hostedDownloadDB(t)
	_, _, token := seedDownloadBase(t, db)
	srv := &Server{DB: db}
	app := downloadApp(srv)

	req, _ := http.NewRequest(http.MethodGet, "/i/"+token+"/download", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when wkhtmltopdf absent, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "PDF generation is not available") {
		t.Errorf("expected 'PDF generation is not available' in body, got: %s", string(body))
	}
}

func TestHandleHostedInvoiceDownload_HappyPath(t *testing.T) {
	if !wkhtmltopdfPresent() {
		t.Skip("wkhtmltopdf not installed")
	}
	db := hostedDownloadDB(t)
	_, inv, token := seedDownloadBase(t, db)
	srv := &Server{DB: db}
	app := downloadApp(srv)

	req, _ := http.NewRequest(http.MethodGet, "/i/"+token+"/download", nil)
	resp, err := app.Test(req, 60_000)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("expected Content-Type: application/pdf, got %s", ct)
	}
	wantFilename := `attachment; filename="Invoice-INV-DL-001.pdf"`
	if cd := resp.Header.Get("Content-Disposition"); cd != wantFilename {
		t.Errorf("Content-Disposition = %q, want %q", cd, wantFilename)
	}
	_ = inv
	body, _ := io.ReadAll(resp.Body)
	// PDF magic bytes: %PDF
	if !strings.HasPrefix(string(body), "%PDF") {
		t.Errorf("response body does not start with %%PDF — not a valid PDF")
	}
}

// ── Company isolation ─────────────────────────────────────────────────────────

func TestHostedDownload_CompanyIsolation(t *testing.T) {
	// Company A creates the link. Company B's invoice should not be served
	// via company A's token (enforced by the token→link→invoice chain).
	db := hostedDownloadDB(t)

	coA := models.Company{Name: "Co A", BaseCurrencyCode: "USD", IsActive: true}
	coB := models.Company{Name: "Co B", BaseCurrencyCode: "USD", IsActive: true}
	db.Create(&coA)
	db.Create(&coB)

	custA := models.Customer{CompanyID: coA.ID, Name: "A Cust"}
	custB := models.Customer{CompanyID: coB.ID, Name: "B Cust"}
	db.Create(&custA)
	db.Create(&custB)

	invA := models.Invoice{
		CompanyID: coA.ID, CustomerID: custA.ID,
		InvoiceNumber: "A-001", Status: models.InvoiceStatusIssued,
		Amount: decimal.NewFromFloat(10), BalanceDue: decimal.NewFromFloat(10),
	}
	db.Create(&invA)

	// Token for company A's invoice.
	tokenA, _, err := services.CreateHostedLink(db, coA.ID, invA.ID, nil)
	if err != nil {
		t.Fatalf("CreateHostedLink A: %v", err)
	}

	srv := &Server{DB: db}
	app := downloadApp(srv)

	// Accessing /download with token A resolves to company A's invoice — this is fine.
	// Company B has no hosted link, so there is no way to access its invoice
	// via the hosted download route. The isolation guarantee is that
	// token → link → company_id = A → loadInvoiceForRender(db, A, invA.ID).
	// A token for coA cannot reach coB's data.
	req, _ := http.NewRequest(http.MethodGet, "/i/"+tokenA+"/download", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	// Either 200 (wkhtmltopdf present) or 503 (absent) — never a leak to coB.
	if resp.StatusCode == http.StatusGone {
		t.Fatal("token A should not result in 410 — company isolation should not block valid token")
	}
	// Additionally: the response must not contain coB's name anywhere.
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "Co B") {
		t.Error("company B data leaked into company A's download response")
	}
}

// ── Toolbar / CanDownload flag tests ─────────────────────────────────────────

func TestHandleHostedInvoice_CanDownloadFalseWhenNoPDFEngine(t *testing.T) {
	if wkhtmltopdfPresent() {
		t.Skip("wkhtmltopdf is installed — CanDownload will be true; skip no-engine test")
	}
	db := hostedDownloadDB(t)
	_, _, token := seedDownloadBase(t, db)
	srv := &Server{DB: db}
	app := downloadApp(srv)

	req, _ := http.NewRequest(http.MethodGet, "/i/"+token, nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for hosted invoice page, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	s := string(body)

	// When CanDownload=false, toolbar shows the print button, not a download link.
	if strings.Contains(s, "/download") {
		t.Error("toolbar should not contain /download link when wkhtmltopdf is absent")
	}
	if !strings.Contains(s, "window.print()") {
		t.Error("toolbar should contain window.print() fallback when wkhtmltopdf is absent")
	}
}

func TestHandleHostedInvoice_CanDownloadTrueWhenPDFEnginePresent(t *testing.T) {
	if !wkhtmltopdfPresent() {
		t.Skip("wkhtmltopdf not installed — CanDownload will be false")
	}
	db := hostedDownloadDB(t)
	_, _, token := seedDownloadBase(t, db)
	srv := &Server{DB: db}
	app := downloadApp(srv)

	req, _ := http.NewRequest(http.MethodGet, "/i/"+token, nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for hosted invoice page, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	s := string(body)

	// When CanDownload=true, toolbar shows real download link.
	wantURL := "/i/" + token + "/download"
	if !strings.Contains(s, wantURL) {
		t.Errorf("toolbar should contain %q download link when wkhtmltopdf is present", wantURL)
	}
	if strings.Contains(s, "window.print()") {
		t.Error("toolbar should not show print-fallback button when download link is present")
	}
}

// ── Customer boundary test ────────────────────────────────────────────────────

func TestHostedDownload_CustomerBoundary(t *testing.T) {
	// The download handler must NOT include internal-admin elements (session info,
	// audit log links, edit buttons, etc.) in the rendered invoice HTML.
	// We verify by confirming that the 503 response (no wkhtmltopdf) or HTML render
	// does not contain admin route strings.
	if wkhtmltopdfPresent() {
		t.Skip("when PDF engine present, response is binary PDF — boundary checked via render unit test")
	}
	db := hostedDownloadDB(t)
	_, _, token := seedDownloadBase(t, db)
	srv := &Server{DB: db}
	app := downloadApp(srv)

	req, _ := http.NewRequest(http.MethodGet, "/i/"+token+"/download", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	// 503 expected (no PDF engine). The error text must not leak admin paths.
	body, _ := io.ReadAll(resp.Body)
	s := string(body)
	adminPaths := []string{"/invoices/", "/companies/", "/auth/", "audit_log"}
	for _, p := range adminPaths {
		if strings.Contains(s, p) {
			t.Errorf("admin path %q leaked in download response", p)
		}
	}
}

// ── Future-ready boundary ─────────────────────────────────────────────────────

func TestHostedDownload_FutureReadyBoundary(t *testing.T) {
	// Download must not write any accounting records (journal entries, ledger
	// entries, invoice status changes, payment receipts). This validates that the
	// handler is read-only — consistent with the spec's accounting-truth boundary.
	db := hostedDownloadDB(t)
	_, inv, token := seedDownloadBase(t, db)
	srv := &Server{DB: db}
	app := downloadApp(srv)

	// Count records before.
	var jeBefore, leBefore, prBefore int64
	db.Model(&models.JournalEntry{}).Count(&jeBefore)
	db.Model(&models.LedgerEntry{}).Count(&leBefore)
	db.Model(&models.PaymentReceipt{}).Count(&prBefore)

	req, _ := http.NewRequest(http.MethodGet, "/i/"+token+"/download", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Count records after.
	var jeAfter, leAfter, prAfter int64
	db.Model(&models.JournalEntry{}).Count(&jeAfter)
	db.Model(&models.LedgerEntry{}).Count(&leAfter)
	db.Model(&models.PaymentReceipt{}).Count(&prAfter)

	if jeAfter != jeBefore {
		t.Errorf("JournalEntry count changed by download: before=%d after=%d", jeBefore, jeAfter)
	}
	if leAfter != leBefore {
		t.Errorf("LedgerEntry count changed by download: before=%d after=%d", leBefore, leAfter)
	}
	if prAfter != prBefore {
		t.Errorf("PaymentReceipt count changed by download: before=%d after=%d", prBefore, prAfter)
	}

	// Invoice status must not have changed.
	var refreshed models.Invoice
	db.First(&refreshed, inv.ID)
	if refreshed.Status != models.InvoiceStatusIssued {
		t.Errorf("invoice status changed by download: got %s, want %s", refreshed.Status, models.InvoiceStatusIssued)
	}
}
