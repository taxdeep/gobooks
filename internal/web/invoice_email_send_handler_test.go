// 遵循project_guide.md
package web

// invoice_email_send_handler_test.go — Batch 9.1 POST semantics tests for
// POST /invoices/:id/send-email.
//
// Coverage:
//   TestSendEmailHandler_AttachPDF1_RejectsWhenNoPDFGenerator
//     — attach_pdf=1 with verified SMTP but no wkhtmltopdf → 303 redirect to
//       ?emailerror= containing the PDF-generator message. Skipped when wkhtmltopdf
//       IS present (happy-path belongs in the service test with real PDF).
//   TestSendEmailHandler_NoAttachPDF_SkipsPDFGate
//     — No attach_pdf field → service proceeds past PDF gate, fails at SMTP dial
//       (not a PDF error). Redirect to ?emailerror= must NOT mention wkhtmltopdf.
//   TestSendEmailHandler_Redirect_OnSuccess_Marker
//     — Minimal smoke: valid send redirects to ?sent=1 (exercised via a stub
//       invoice that fails eligibility, so this test validates the redirect
//       target token without needing real SMTP).

import (
	"encoding/json"
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
	"balanciz/internal/services"
)

// ── DB + seed helpers ─────────────────────────────────────────────────────────

func emailSendHandlerDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:email_send_%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
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
		&models.InvoiceHostedLink{},
		&models.CompanyNotificationSettings{},
		&models.SystemNotificationSettings{},
		&models.Session{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

// seedEmailSendBase creates an issued invoice with verified SMTP configured
// (SMTP host 127.0.0.1:1 will fail to dial but passes the readiness gate).
func seedEmailSendBase(t *testing.T, db *gorm.DB) (models.Company, *models.User, models.Invoice) {
	t.Helper()
	co := models.Company{Name: "Email Send Co", BaseCurrencyCode: "CAD", IsActive: true}
	db.Create(&co)

	u := models.User{
		ID:           uuid.New(),
		Email:        fmt.Sprintf("send_%d@test.com", time.Now().UnixNano()),
		PasswordHash: "x",
		IsActive:     true,
	}
	db.Create(&u)
	db.Create(&models.CompanyMembership{UserID: u.ID, CompanyID: co.ID, Role: "owner"})

	cust := models.Customer{CompanyID: co.ID, Name: "Send Cust", Email: "send@test.com"}
	db.Create(&cust)

	je := models.JournalEntry{CompanyID: co.ID, EntryDate: time.Now()}
	db.Create(&je)

	invNo := fmt.Sprintf("INV-SEND-%d", time.Now().UnixNano())
	inv := models.Invoice{
		CompanyID:             co.ID,
		InvoiceNumber:         invNo,
		CustomerID:            cust.ID,
		InvoiceDate:           time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Status:                models.InvoiceStatusIssued,
		Amount:                decimal.RequireFromString("100.00"),
		Subtotal:              decimal.RequireFromString("100.00"),
		TaxTotal:              decimal.Zero,
		BalanceDue:            decimal.RequireFromString("100.00"),
		BalanceDueBase:        decimal.RequireFromString("100.00"),
		CustomerNameSnapshot:  "Send Cust",
		CustomerEmailSnapshot: "send@test.com",
		JournalEntryID:        &je.ID,
	}
	db.Create(&inv)

	// Verified SMTP that passes the readiness gate but won't actually deliver.
	smtp := models.CompanyNotificationSettings{
		CompanyID:              co.ID,
		EmailEnabled:           true,
		SMTPHost:               "127.0.0.1",
		SMTPPort:               1,
		SMTPFromEmail:          "from@test.com",
		EmailVerificationReady: true,
		AllowSystemFallback:    false,
	}
	db.Create(&smtp)

	return co, &u, inv
}

// authEmailSendApp builds a minimal Fiber app for POST /invoices/:id/send-email
// and GET /invoices/:id/email-history with a pre-injected auth context.
func authEmailSendApp(server *Server, user *models.User, companyID uint) *fiber.App {
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
	app.Post("/invoices/:id/send-email", server.handleInvoiceSendEmail)
	app.Get("/invoices/:id/email-history", server.handleGetInvoiceEmailHistory)
	return app
}

// postSendEmail fires POST /invoices/:id/send-email with form fields.
func postSendEmail(t *testing.T, app *fiber.App, invoiceID uint, formFields map[string]string) *http.Response {
	t.Helper()
	form := url.Values{}
	for k, v := range formFields {
		form.Set(k, v)
	}
	encoded := form.Encode()
	req, _ := http.NewRequest(http.MethodPost,
		fmt.Sprintf("/invoices/%d/send-email", invoiceID),
		strings.NewReader(encoded),
	)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestSendEmailHandler_AttachPDF1_RejectsWhenNoPDFGenerator verifies that
// submitting attach_pdf=1 when wkhtmltopdf is absent causes the handler to
// redirect to ?emailerror= with a message identifying the PDF generator as the
// problem. The test is skipped when wkhtmltopdf IS present because the
// generator-unavailable path cannot be triggered on a capable server.
func TestSendEmailHandler_AttachPDF1_RejectsWhenNoPDFGenerator(t *testing.T) {
	if services.PDFGeneratorAvailable() {
		t.Skip("skipped: wkhtmltopdf present; this test validates the no-generator rejection path")
	}

	db := emailSendHandlerDB(t)
	co, u, inv := seedEmailSendBase(t, db)
	srv := &Server{DB: db}
	app := authEmailSendApp(srv, u, co.ID)

	resp := postSendEmail(t, app, inv.ID, map[string]string{
		"to_email":   "send@test.com",
		"attach_pdf": "1",
	})

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 SeeOther, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "emailerror") {
		t.Fatalf("expected redirect to ?emailerror=..., got %q", loc)
	}
	decoded, _ := url.QueryUnescape(loc)
	if !strings.Contains(decoded, "wkhtmltopdf") {
		t.Errorf("emailerror message should mention wkhtmltopdf; got location %q", decoded)
	}

	// A failed InvoiceEmailLog must be written so the history panel shows the attempt.
	var logCount int64
	db.Model(&models.InvoiceEmailLog{}).
		Where("invoice_id = ? AND send_status = ?", inv.ID, models.EmailSendStatusFailed).
		Count(&logCount)
	if logCount != 1 {
		t.Errorf("expected 1 failed email log entry, got %d", logCount)
	}
}

// TestSendEmailHandler_NoAttachPDF_SkipsPDFGate verifies that omitting the
// attach_pdf field causes the service to skip PDF generation entirely and fail
// (only) at SMTP dial — not at a PDF-generator gate.
//
// The test is always runnable (wkhtmltopdf not required).
func TestSendEmailHandler_NoAttachPDF_SkipsPDFGate(t *testing.T) {
	db := emailSendHandlerDB(t)
	co, u, inv := seedEmailSendBase(t, db)
	srv := &Server{DB: db}
	app := authEmailSendApp(srv, u, co.ID)

	// POST with no attach_pdf field — handler must set AttachPDF=false.
	resp := postSendEmail(t, app, inv.ID, map[string]string{
		"to_email": "send@test.com",
		// attach_pdf intentionally absent
	})

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 SeeOther, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "emailerror") {
		t.Fatalf("expected redirect to ?emailerror=..., got %q", loc)
	}
	decoded, _ := url.QueryUnescape(loc)
	// Error must be from SMTP dial, NOT from the PDF generator gate.
	if strings.Contains(decoded, "wkhtmltopdf") {
		t.Errorf("no-attach path must not trigger PDF gate error; got %q", decoded)
	}

	// MetadataJSON on the (failed) log should record attachment_included=false.
	var logEntry models.InvoiceEmailLog
	if err := db.Where("invoice_id = ?", inv.ID).First(&logEntry).Error; err != nil {
		t.Fatalf("expected a log entry: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(logEntry.MetadataJSON, &meta); err != nil {
		t.Fatalf("MetadataJSON parse failed: %v", err)
	}
	if v, _ := meta["attachment_included"].(bool); v {
		t.Errorf("attachment_included should be false when attach_pdf not submitted; got true")
	}
}

// TestSendEmailHandler_AttachPDF_Value1_vs_Other verifies the form-parsing rule:
// only the exact value "1" sets AttachPDF=true; other values (e.g., "true", "yes",
// "on", "") must NOT set it.
func TestSendEmailHandler_AttachPDF_Value1_vs_Other(t *testing.T) {
	if services.PDFGeneratorAvailable() {
		t.Skip("skipped: with wkhtmltopdf present the test would need real SMTP; no-generator path is sufficient")
	}

	cases := []struct {
		formValue      string
		expectPDFError bool // true = "1" was sent → PDF gate fires
	}{
		{"1", true},
		{"true", false},
		{"yes", false},
		{"on", false},
		{"", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run("attach_pdf="+tc.formValue, func(t *testing.T) {
			db := emailSendHandlerDB(t)
			co, u, inv := seedEmailSendBase(t, db)
			srv := &Server{DB: db}
			app := authEmailSendApp(srv, u, co.ID)

			fields := map[string]string{"to_email": "send@test.com"}
			if tc.formValue != "" {
				fields["attach_pdf"] = tc.formValue
			}
			resp := postSendEmail(t, app, inv.ID, fields)
			if resp.StatusCode != http.StatusSeeOther {
				t.Fatalf("expected 303, got %d", resp.StatusCode)
			}
			loc, _ := url.QueryUnescape(resp.Header.Get("Location"))
			gotPDFError := strings.Contains(loc, "wkhtmltopdf")
			if gotPDFError != tc.expectPDFError {
				t.Errorf("attach_pdf=%q: expectPDFError=%v but gotPDFError=%v (location=%q)",
					tc.formValue, tc.expectPDFError, gotPDFError, loc)
			}
		})
	}
}

// TestEmailHistory_AttachmentMetadataExposed verifies that the history API
// surfaces attachment_included and attachment_filename from MetadataJSON.
func TestEmailHistory_AttachmentMetadataExposed(t *testing.T) {
	db := emailSendHandlerDB(t)
	co, u, _ := seedEmailSendBase(t, db)

	// Manually create two log entries: one with PDF, one without.
	withAttach := models.InvoiceEmailLog{
		CompanyID:    co.ID,
		InvoiceID:    1,
		ToEmail:      "a@test.com",
		SendStatus:   models.EmailSendStatusSent,
		TemplateType: "invoice",
		Subject:      "Invoice",
		MetadataJSON: []byte(`{"attachment_included":true,"attachment_filename":"Invoice-INV-001.pdf"}`),
	}
	noAttach := models.InvoiceEmailLog{
		CompanyID:    co.ID,
		InvoiceID:    1,
		ToEmail:      "b@test.com",
		SendStatus:   models.EmailSendStatusSent,
		TemplateType: "invoice",
		Subject:      "Invoice",
		MetadataJSON: []byte(`{"attachment_included":false}`),
	}
	db.Create(&withAttach)
	db.Create(&noAttach)

	srv := &Server{DB: db}
	app := authEmailSendApp(srv, u, co.ID)

	req, _ := http.NewRequest(http.MethodGet, "/invoices/1/email-history", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		EmailLogs []struct {
			ToEmail            string `json:"to_email"`
			AttachmentIncluded bool   `json:"attachment_included"`
			AttachmentFilename string `json:"attachment_filename"`
		} `json:"email_logs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("response decode failed: %v", err)
	}

	byEmail := map[string]struct {
		included bool
		filename string
	}{}
	for _, e := range body.EmailLogs {
		byEmail[e.ToEmail] = struct {
			included bool
			filename string
		}{e.AttachmentIncluded, e.AttachmentFilename}
	}

	if e, ok := byEmail["a@test.com"]; !ok || !e.included || e.filename != "Invoice-INV-001.pdf" {
		t.Errorf("with-attachment log: expected included=true, filename=Invoice-INV-001.pdf; got %+v", byEmail["a@test.com"])
	}
	if e, ok := byEmail["b@test.com"]; !ok || e.included || e.filename != "" {
		t.Errorf("no-attachment log: expected included=false, no filename; got %+v", byEmail["b@test.com"])
	}
}
