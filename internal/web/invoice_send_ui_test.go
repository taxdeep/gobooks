// 遵循project_guide.md
package web

// invoice_send_ui_test.go — Handler tests for Batch 5 Send UI.
//
// Coverage:
//   TestHandleGetInvoiceSendDefaults_IssuedNoSMTP     — returns CanSend=false, EligibilityError set
//   TestHandleGetInvoiceSendDefaults_IssuedWithSMTP   — returns CanSend=true, subject/body populated
//   TestHandleGetInvoiceSendDefaults_CompanyIsolation — cross-company returns 500
//   TestHandleGetInvoiceSendDefaults_DraftCanSendFalse — draft → CanSend=false
//   TestHandleBindTemplate_DraftSuccess               — bind succeeds, redirects with ?tmplbound=1
//   TestHandleBindTemplate_NonDraftRejected           — issued invoice → bind rejected
//   TestHandleBindTemplate_CrossCompanyTemplateRejected — template from another company rejected
//   TestHandleBindTemplate_InvalidTemplateID          — bad template_id → error redirect
//   TestHandleBindTemplate_CompanyIsolation           — wrong company_id for invoice → error redirect
//   TestHandleInvoiceDetail_EmailHistoryShown          — history rows present in rendered page
//   TestHandleInvoiceDetail_SendModalShownForIssued    — send modal rendered for issued invoice
//   TestHandleInvoiceDetail_SendModalNotShownForDraft  — no send modal for draft

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
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/services"
)

// ── DB + seed helpers ─────────────────────────────────────────────────────────

func testSendUIDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:web_send_ui_%s?mode=memory&cache=shared", t.Name())
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
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedSendUIUser(t *testing.T, db *gorm.DB) *models.User {
	t.Helper()
	u := models.User{
		ID:           uuid.New(),
		Email:        fmt.Sprintf("ui_%d@test.com", time.Now().UnixNano()),
		PasswordHash: "x",
		IsActive:     true,
	}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	return &u
}

func seedSendUICompany(t *testing.T, db *gorm.DB, name string) *models.Company {
	t.Helper()
	co := models.Company{Name: name, BaseCurrencyCode: "CAD", IsActive: true}
	if err := db.Create(&co).Error; err != nil {
		t.Fatal(err)
	}
	return &co
}

func seedSendUIMembership(t *testing.T, db *gorm.DB, userID uuid.UUID, companyID uint) *models.CompanyMembership {
	t.Helper()
	m := models.CompanyMembership{UserID: userID, CompanyID: companyID, Role: "owner", IsActive: true}
	if err := db.Create(&m).Error; err != nil {
		t.Fatal(err)
	}
	return &m
}

func seedSendUIInvoice(t *testing.T, db *gorm.DB, companyID uint, status models.InvoiceStatus) *models.Invoice {
	t.Helper()
	cust := models.Customer{CompanyID: companyID, Name: "UI Customer", Email: "ui@example.com"}
	db.Create(&cust)

	inv := models.Invoice{
		CompanyID:             companyID,
		InvoiceNumber:         fmt.Sprintf("INV-UI-%d", time.Now().UnixNano()),
		CustomerID:            cust.ID,
		InvoiceDate:           time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Status:                status,
		Amount:                decimal.RequireFromString("100.00"),
		Subtotal:              decimal.RequireFromString("100.00"),
		TaxTotal:              decimal.Zero,
		BalanceDue:            decimal.RequireFromString("100.00"),
		BalanceDueBase:        decimal.RequireFromString("100.00"),
		CustomerNameSnapshot:  "UI Customer",
		CustomerEmailSnapshot: "ui@example.com",
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}
	return &inv
}

func seedSendUIVerifiedSMTP(t *testing.T, db *gorm.DB, companyID uint) {
	t.Helper()
	row := models.CompanyNotificationSettings{
		CompanyID:              companyID,
		EmailEnabled:           true,
		SMTPHost:               "smtp.example.com",
		SMTPPort:               587,
		SMTPFromEmail:          "from@example.com",
		EmailVerificationReady: true,
		AllowSystemFallback:    false,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
}

func seedSendUIEmailLog(t *testing.T, db *gorm.DB, companyID, invoiceID uint, status models.EmailSendStatus) *models.InvoiceEmailLog {
	t.Helper()
	log := models.InvoiceEmailLog{
		CompanyID:            companyID,
		InvoiceID:            invoiceID,
		ToEmail:              "ui@example.com",
		SendStatus:           status,
		Subject:              "Invoice #INV-UI",
		BodyResolved:         "Dear UI Customer,\n\nYour invoice is attached.",
		TemplateNameSnapshot: "Classic",
		TemplateType:         "invoice",
		CreatedAt:            time.Now(),
		MetadataJSON:         datatypes.JSON("{}"),
	}
	if status == models.EmailSendStatusFailed {
		log.ErrorMessage = "connection refused"
	}
	if err := db.Create(&log).Error; err != nil {
		t.Fatal(err)
	}
	return &log
}

func seedSendUITemplate(t *testing.T, db *gorm.DB, companyID uint, name string, isDefault bool) *models.InvoiceTemplate {
	t.Helper()
	cfg := models.DefaultTemplateConfig("classic")
	cfgJSON, _ := json.Marshal(cfg)
	tmpl := models.InvoiceTemplate{
		CompanyID:  companyID,
		Name:       name,
		ConfigJSON: cfgJSON,
		IsDefault:  isDefault,
		IsActive:   true,
	}
	if err := db.Create(&tmpl).Error; err != nil {
		t.Fatal(err)
	}
	return &tmpl
}

// sendUIApp builds a minimal Fiber app with auth locals injected.
func sendUIApp(server *Server, user *models.User, companyID uint) *fiber.App {
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

	app.Get("/invoices/:id", server.handleInvoiceDetail)
	app.Get("/api/invoices/:id/send-defaults", server.handleGetInvoiceSendDefaults)
	app.Post("/invoices/:id/bind-template", server.handleBindTemplate)

	return app
}

// ── send-defaults handler tests ───────────────────────────────────────────────

func TestHandleGetInvoiceSendDefaults_IssuedNoSMTP(t *testing.T) {
	db := testSendUIDB(t)
	user := seedSendUIUser(t, db)
	co := seedSendUICompany(t, db, "Defaults Co")
	seedSendUIMembership(t, db, user.ID, co.ID)
	inv := seedSendUIInvoice(t, db, co.ID, models.InvoiceStatusIssued)

	server := &Server{DB: db}
	app := sendUIApp(server, user, co.ID)

	resp := performRequest(t, app, fmt.Sprintf("/api/invoices/%d/send-defaults", inv.ID), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body := readResponseBody(t, resp)
	var defaults services.InvoiceSendDefaults
	if err := json.Unmarshal([]byte(body), &defaults); err != nil {
		t.Fatalf("failed to parse JSON: %v\nbody: %s", err, body)
	}

	if defaults.CanSend {
		t.Error("CanSend must be false when SMTP is not configured")
	}
	if !strings.Contains(defaults.EligibilityError, "SMTP") {
		t.Errorf("EligibilityError should mention SMTP; got: %q", defaults.EligibilityError)
	}
}

func TestHandleGetInvoiceSendDefaults_IssuedWithSMTP(t *testing.T) {
	db := testSendUIDB(t)
	user := seedSendUIUser(t, db)
	co := seedSendUICompany(t, db, "SMTP Co")
	seedSendUIMembership(t, db, user.ID, co.ID)
	inv := seedSendUIInvoice(t, db, co.ID, models.InvoiceStatusIssued)
	seedSendUIVerifiedSMTP(t, db, co.ID)

	server := &Server{DB: db}
	app := sendUIApp(server, user, co.ID)

	resp := performRequest(t, app, fmt.Sprintf("/api/invoices/%d/send-defaults", inv.ID), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body := readResponseBody(t, resp)
	var defaults services.InvoiceSendDefaults
	json.Unmarshal([]byte(body), &defaults) //nolint:errcheck

	if !defaults.CanSend {
		t.Errorf("CanSend must be true; EligibilityError=%q", defaults.EligibilityError)
	}
	if defaults.Subject == "" {
		t.Error("Subject must be populated")
	}
	if defaults.Body == "" {
		t.Error("Body must be populated")
	}
	if defaults.ToEmail != "ui@example.com" {
		t.Errorf("ToEmail: want ui@example.com, got %q", defaults.ToEmail)
	}
}

func TestHandleGetInvoiceSendDefaults_DraftCanSendFalse(t *testing.T) {
	db := testSendUIDB(t)
	user := seedSendUIUser(t, db)
	co := seedSendUICompany(t, db, "Draft Co")
	seedSendUIMembership(t, db, user.ID, co.ID)
	inv := seedSendUIInvoice(t, db, co.ID, models.InvoiceStatusDraft)
	seedSendUIVerifiedSMTP(t, db, co.ID)

	server := &Server{DB: db}
	app := sendUIApp(server, user, co.ID)

	resp := performRequest(t, app, fmt.Sprintf("/api/invoices/%d/send-defaults", inv.ID), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body := readResponseBody(t, resp)
	var defaults services.InvoiceSendDefaults
	json.Unmarshal([]byte(body), &defaults) //nolint:errcheck
	if defaults.CanSend {
		t.Error("draft invoice: CanSend must be false")
	}
}

func TestHandleGetInvoiceSendDefaults_CompanyIsolation(t *testing.T) {
	db := testSendUIDB(t)
	user := seedSendUIUser(t, db)
	co1 := seedSendUICompany(t, db, "Co1")
	co2 := seedSendUICompany(t, db, "Co2")
	seedSendUIMembership(t, db, user.ID, co2.ID)
	inv := seedSendUIInvoice(t, db, co1.ID, models.InvoiceStatusIssued) // belongs to co1

	server := &Server{DB: db}
	// App is for co2 trying to access co1's invoice.
	app := sendUIApp(server, user, co2.ID)

	resp := performRequest(t, app, fmt.Sprintf("/api/invoices/%d/send-defaults", inv.ID), "")
	// Must return error (500 from lookup failure) — not co1's data.
	if resp.StatusCode == http.StatusOK {
		body := readResponseBody(t, resp)
		t.Fatalf("expected non-200 for cross-company access, got 200 with body: %s", body)
	}
}

// ── bind-template handler tests ───────────────────────────────────────────────

func TestHandleBindTemplate_DraftSuccess(t *testing.T) {
	db := testSendUIDB(t)
	user := seedSendUIUser(t, db)
	co := seedSendUICompany(t, db, "Bind Co")
	seedSendUIMembership(t, db, user.ID, co.ID)
	inv := seedSendUIInvoice(t, db, co.ID, models.InvoiceStatusDraft)
	tmpl := seedSendUITemplate(t, db, co.ID, "Classic", false)

	server := &Server{DB: db}
	app := sendUIApp(server, user, co.ID)

	resp := performFormRequest(t, app, http.MethodPost,
		fmt.Sprintf("/invoices/%d/bind-template", inv.ID),
		url.Values{"template_id": {fmt.Sprintf("%d", tmpl.ID)}}, "")

	// Expect redirect to ?tmplbound=1
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "tmplbound=1") {
		t.Errorf("redirect should have ?tmplbound=1; got %q", loc)
	}

	// Verify DB binding.
	var reloaded models.Invoice
	db.First(&reloaded, inv.ID)
	if reloaded.TemplateID == nil || *reloaded.TemplateID != tmpl.ID {
		t.Errorf("TemplateID not persisted; want %d, got %v", tmpl.ID, reloaded.TemplateID)
	}
}

func TestHandleBindTemplate_NonDraftRejected(t *testing.T) {
	db := testSendUIDB(t)
	user := seedSendUIUser(t, db)
	co := seedSendUICompany(t, db, "Bind Issued Co")
	seedSendUIMembership(t, db, user.ID, co.ID)
	inv := seedSendUIInvoice(t, db, co.ID, models.InvoiceStatusIssued)
	tmpl := seedSendUITemplate(t, db, co.ID, "Classic", false)

	server := &Server{DB: db}
	app := sendUIApp(server, user, co.ID)

	resp := performFormRequest(t, app, http.MethodPost,
		fmt.Sprintf("/invoices/%d/bind-template", inv.ID),
		url.Values{"template_id": {fmt.Sprintf("%d", tmpl.ID)}}, "")

	// Must redirect with ?error= (not tmplbound).
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "error=") {
		t.Errorf("expected ?error= in redirect for non-draft; got %q", loc)
	}
	if strings.Contains(loc, "tmplbound") {
		t.Errorf("must not have tmplbound in error redirect; got %q", loc)
	}
}

func TestHandleBindTemplate_CrossCompanyTemplateRejected(t *testing.T) {
	db := testSendUIDB(t)
	user := seedSendUIUser(t, db)
	co1 := seedSendUICompany(t, db, "Co1")
	co2 := seedSendUICompany(t, db, "Co2")
	seedSendUIMembership(t, db, user.ID, co1.ID)
	inv := seedSendUIInvoice(t, db, co1.ID, models.InvoiceStatusDraft)
	// Template belongs to co2, not co1.
	foreignTmpl := seedSendUITemplate(t, db, co2.ID, "Foreign", false)

	server := &Server{DB: db}
	app := sendUIApp(server, user, co1.ID)

	resp := performFormRequest(t, app, http.MethodPost,
		fmt.Sprintf("/invoices/%d/bind-template", inv.ID),
		url.Values{"template_id": {fmt.Sprintf("%d", foreignTmpl.ID)}}, "")

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "error=") {
		t.Errorf("cross-company template must produce ?error=; got %q", loc)
	}
}

func TestHandleBindTemplate_InvalidTemplateID(t *testing.T) {
	db := testSendUIDB(t)
	user := seedSendUIUser(t, db)
	co := seedSendUICompany(t, db, "BadID Co")
	seedSendUIMembership(t, db, user.ID, co.ID)
	inv := seedSendUIInvoice(t, db, co.ID, models.InvoiceStatusDraft)

	server := &Server{DB: db}
	app := sendUIApp(server, user, co.ID)

	resp := performFormRequest(t, app, http.MethodPost,
		fmt.Sprintf("/invoices/%d/bind-template", inv.ID),
		url.Values{"template_id": {"not-a-number"}}, "")

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 for invalid ID, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "error=") {
		t.Errorf("invalid template_id must produce ?error=; got %q", loc)
	}
}

func TestHandleBindTemplate_CompanyIsolation(t *testing.T) {
	db := testSendUIDB(t)
	user := seedSendUIUser(t, db)
	co1 := seedSendUICompany(t, db, "Owner Co")
	co2 := seedSendUICompany(t, db, "Attacker Co")
	seedSendUIMembership(t, db, user.ID, co2.ID)
	inv := seedSendUIInvoice(t, db, co1.ID, models.InvoiceStatusDraft) // owned by co1
	tmpl := seedSendUITemplate(t, db, co2.ID, "Attacker Template", false)

	server := &Server{DB: db}
	// co2 tries to bind to co1's invoice.
	app := sendUIApp(server, user, co2.ID)

	resp := performFormRequest(t, app, http.MethodPost,
		fmt.Sprintf("/invoices/%d/bind-template", inv.ID),
		url.Values{"template_id": {fmt.Sprintf("%d", tmpl.ID)}}, "")

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "error=") {
		t.Errorf("cross-company invoice access must produce ?error=; got %q", loc)
	}
}

// ── invoice detail page tests ─────────────────────────────────────────────────

func TestHandleInvoiceDetail_EmailHistoryShown(t *testing.T) {
	db := testSendUIDB(t)
	user := seedSendUIUser(t, db)
	co := seedSendUICompany(t, db, "History Co")
	seedSendUIMembership(t, db, user.ID, co.ID)
	inv := seedSendUIInvoice(t, db, co.ID, models.InvoiceStatusIssued)
	seedSendUIEmailLog(t, db, co.ID, inv.ID, models.EmailSendStatusSent)
	seedSendUIEmailLog(t, db, co.ID, inv.ID, models.EmailSendStatusFailed)

	server := &Server{DB: db}
	app := sendUIApp(server, user, co.ID)

	resp := performRequest(t, app, fmt.Sprintf("/invoices/%d", inv.ID), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body := readResponseBody(t, resp)

	// Email History section must be rendered.
	if !strings.Contains(body, "Email History") {
		t.Error("Email History section not found in page")
	}
	// Both statuses must be visible.
	if !strings.Contains(body, "Sent") {
		t.Error("Sent status badge not found")
	}
	if !strings.Contains(body, "Failed") {
		t.Error("Failed status badge not found")
	}
	// BodyResolved expandable must be present.
	if !strings.Contains(body, "View body") {
		t.Error("'View body' expand link not found")
	}
	// Historical body content must appear.
	if !strings.Contains(body, "Your invoice is attached") {
		t.Error("BodyResolved content not found in page — history shows wrong content")
	}
}

func TestHandleInvoiceDetail_SendModalShownForIssued(t *testing.T) {
	db := testSendUIDB(t)
	user := seedSendUIUser(t, db)
	co := seedSendUICompany(t, db, "Modal Co")
	seedSendUIMembership(t, db, user.ID, co.ID)
	inv := seedSendUIInvoice(t, db, co.ID, models.InvoiceStatusIssued)

	server := &Server{DB: db}
	app := sendUIApp(server, user, co.ID)

	resp := performRequest(t, app, fmt.Sprintf("/invoices/%d", inv.ID), "")
	body := readResponseBody(t, resp)

	// Send modal must be in the page for issued invoices.
	if !strings.Contains(body, "sendModal") {
		t.Error("send modal dialog not found for issued invoice")
	}
	// Send form action must point to correct endpoint.
	if !strings.Contains(body, "/send-email") {
		t.Error("send form action not found in modal")
	}
}

func TestHandleInvoiceDetail_SendModalIncludesDefaultBodySyncMarkup(t *testing.T) {
	db := testSendUIDB(t)
	user := seedSendUIUser(t, db)
	co := seedSendUICompany(t, db, "Body Sync Co")
	seedSendUIMembership(t, db, user.ID, co.ID)
	inv := seedSendUIInvoice(t, db, co.ID, models.InvoiceStatusIssued)

	server := &Server{DB: db}
	app := sendUIApp(server, user, co.ID)

	resp := performRequest(t, app, fmt.Sprintf("/invoices/%d", inv.ID), "")
	body := readResponseBody(t, resp)

	if !strings.Contains(body, "sendBodyDefaultAttachPDF") {
		t.Error("default attach-PDF body source not found in send modal")
	}
	if !strings.Contains(body, "sendBodyDefaultNoPDF") {
		t.Error("default no-PDF body source not found in send modal")
	}
	if !strings.Contains(body, "Please find your invoice attached.") {
		t.Error("attach-PDF default wording not found in send modal markup")
	}
	if !strings.Contains(body, "Please review your invoice details below.") {
		t.Error("no-PDF default wording not found in send modal markup")
	}
}

func TestHandleInvoiceDetail_SendModalUsesEmailAssistStateGuards(t *testing.T) {
	db := testSendUIDB(t)
	user := seedSendUIUser(t, db)
	co := seedSendUICompany(t, db, "Email Assist State Co")
	seedSendUIMembership(t, db, user.ID, co.ID)
	inv := seedSendUIInvoice(t, db, co.ID, models.InvoiceStatusIssued)
	seedSendUIVerifiedSMTP(t, db, co.ID)

	server := &Server{DB: db}
	app := sendUIApp(server, user, co.ID)

	resp := performRequest(t, app, fmt.Sprintf("/invoices/%d", inv.ID), "")
	body := readResponseBody(t, resp)

	for _, want := range []string{
		`x-data="balancizEmailAssist()"`,
		`:disabled="emailAssist.loading || emailAssist.visible"`,
		`@input="onBodyEdited()"`,
		`x-ref="bodyDefaultAttachPDF"`,
		`x-ref="bodyDefaultNoPDF"`,
		`/static/invoice_email_assist.js?v=1`,
		"No draft available right now.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected send modal markup to contain %q", want)
		}
	}
	if strings.Contains(body, `x-init="init()"`) {
		t.Fatal("email assist modal should rely on Alpine auto-init and must not call init() twice")
	}
	if services.PDFGeneratorAvailable() {
		if !strings.Contains(body, `@change="onAttachPDFToggle()"`) {
			t.Fatalf("expected send modal markup to contain attach-PDF toggle handler when PDF is available")
		}
	}
}

func TestHandleInvoiceDetail_SendModalNotShownForDraft(t *testing.T) {
	db := testSendUIDB(t)
	user := seedSendUIUser(t, db)
	co := seedSendUICompany(t, db, "Draft Modal Co")
	seedSendUIMembership(t, db, user.ID, co.ID)
	inv := seedSendUIInvoice(t, db, co.ID, models.InvoiceStatusDraft)

	server := &Server{DB: db}
	app := sendUIApp(server, user, co.ID)

	resp := performRequest(t, app, fmt.Sprintf("/invoices/%d", inv.ID), "")
	body := readResponseBody(t, resp)

	// No send modal for draft invoices — only Issue and Edit buttons.
	if strings.Contains(body, "sendModal") {
		t.Error("send modal must NOT be shown for draft invoices")
	}
	// Issue button must be present.
	if !strings.Contains(body, "Issue") {
		t.Error("Issue button not found on draft invoice page")
	}
}
