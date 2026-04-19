// 遵循project_guide.md
package web

// hosted_pay_handler_test.go — Handler tests for Batch 7 + 7.1 hosted payment routes.
//
// Batch 7 coverage (kept):
//   TestHandleHostedPay_HappyPath              — POST /i/:token/pay → 303 to provider URL
//   TestHandleHostedPay_InvalidToken           — POST with bad token → 410
//   TestHandleHostedPay_NotEligible            — draft invoice → 410
//   TestHandleHostedPay_Idempotency            — second POST reuses redirect URL (not pending)
//   TestHandleHostedPayPending_Valid           — GET → 200 with success content
//   TestHandleHostedPayPending_InvalidToken    — GET with bad token → 410
//   TestHandleHostedPayCancel_Valid            — GET → 200 with cancel content + back link
//   TestHandleHostedPayCancel_InvalidToken     — GET with bad token → 410
//   TestHandleUpdateShareLinkExpiry_Set        — POST sets expiry on active link
//   TestHandleUpdateShareLinkExpiry_Clear      — POST clears expiry
//   TestHandleUpdateShareLinkExpiry_NoLink     — POST with no active link → redirect with error
//   TestHandleUpdateShareLinkExpiry_PastDate   — POST with past date → redirect with error
//
// Batch 7.1 coverage (new):
//   TestHandleHostedInvoice_PayNowRendersRealForm — CanPay=true → rendered page contains POST form to /pay
//   TestHandleHostedPay_CancelUnblocksRetry       — GET cancel marks attempt cancelled; next POST creates fresh attempt

import (
	"fmt"
	"io"
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

// ── DB helper ─────────────────────────────────────────────────────────────────

func hostedPayHandlerDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:web_hpay_%s?mode=memory&cache=shared", t.Name())
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

// seedPayBase sets up company + customer + issued invoice + hosted link + manual gateway.
func seedPayBase(t *testing.T, db *gorm.DB) (models.Company, models.Invoice, string) {
	t.Helper()
	co := models.Company{Name: "Pay Co", BaseCurrencyCode: "CAD", IsActive: true}
	db.Create(&co)
	cust := models.Customer{CompanyID: co.ID, Name: "Cust"}
	db.Create(&cust)
	inv := models.Invoice{
		CompanyID:     co.ID,
		CustomerID:    cust.ID,
		InvoiceNumber: "INV-001",
		Status:        models.InvoiceStatusIssued,
		Amount:   decimal.NewFromFloat(150),
		BalanceDue:    decimal.NewFromFloat(150),
	}
	db.Create(&inv)
	gw := models.PaymentGatewayAccount{
		CompanyID: co.ID, ProviderType: models.ProviderManual,
		DisplayName: "Manual", IsActive: true,
	}
	db.Create(&gw)

	plaintext, _, err := services.CreateHostedLink(db, co.ID, inv.ID, nil)
	if err != nil {
		t.Fatalf("CreateHostedLink: %v", err)
	}
	return co, inv, plaintext
}

// payPublicApp builds a no-auth Fiber app with all hosted pay routes.
func payPublicApp(server *Server) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: func(c *fiber.Ctx, err error) error {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}})
	app.Get("/i/:token", server.handleHostedInvoice)
	app.Post("/i/:token/pay", server.handleHostedPay)
	app.Get("/i/:token/pay/pending", server.handleHostedPayPending)
	app.Get("/i/:token/pay/cancel", server.handleHostedPayCancel)
	return app
}

// expiryApp builds an internal auth Fiber app with the expiry route.
func expiryApp(server *Server, user *models.User, companyID uint) *fiber.App {
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
	app.Post("/invoices/:id/share-link/expiry", server.handleUpdateShareLinkExpiry)
	return app
}

func seedPayUser(t *testing.T, db *gorm.DB) *models.User {
	t.Helper()
	u := models.User{ID: uuid.New(), Email: fmt.Sprintf("pay_%d@t.com", time.Now().UnixNano()), PasswordHash: "x", IsActive: true}
	db.Create(&u)
	return &u
}

func postFormRequest(t *testing.T, app *fiber.App, path string, fields url.Values) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, path, strings.NewReader(fields.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// ── handleHostedPay ───────────────────────────────────────────────────────────

func TestHandleHostedPay_HappyPath(t *testing.T) {
	db := hostedPayHandlerDB(t)
	_, _, token := seedPayBase(t, db)
	srv := &Server{DB: db}
	app := payPublicApp(srv)

	resp := postFormRequest(t, app, "/i/"+token+"/pay", nil)
	// Should redirect to provider URL (ManualProvider redirects to /i/:token/pay/pending).
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		t.Fatal("expected Location header")
	}
}

func TestHandleHostedPay_InvalidToken(t *testing.T) {
	db := hostedPayHandlerDB(t)
	srv := &Server{DB: db}
	app := payPublicApp(srv)

	resp := postFormRequest(t, app, "/i/notavalidtoken/pay", nil)
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("expected 410, got %d", resp.StatusCode)
	}
}

func TestHandleHostedPay_NotEligible(t *testing.T) {
	db := hostedPayHandlerDB(t)
	// Seed a draft invoice — not payable.
	co := models.Company{Name: "Co", BaseCurrencyCode: "CAD", IsActive: true}
	db.Create(&co)
	cust := models.Customer{CompanyID: co.ID, Name: "C"}
	db.Create(&cust)
	inv := models.Invoice{
		CompanyID: co.ID, CustomerID: cust.ID,
		InvoiceNumber: "INV-D", Status: models.InvoiceStatusDraft,
		Amount: decimal.NewFromFloat(100),
	}
	db.Create(&inv)
	gw := models.PaymentGatewayAccount{CompanyID: co.ID, ProviderType: models.ProviderManual, IsActive: true}
	db.Create(&gw)
	token, _, err := services.CreateHostedLink(db, co.ID, inv.ID, nil)
	if err != nil {
		t.Fatal(err)
	}

	srv := &Server{DB: db}
	app := payPublicApp(srv)

	resp := postFormRequest(t, app, "/i/"+token+"/pay", nil)
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("expected 410 for ineligible invoice, got %d", resp.StatusCode)
	}
}

// Batch 7.1: second POST within the idempotency window must REUSE the existing
// redirected attempt and redirect to its URL — not return an error or diverge.
// ManualProvider always sets RedirectURL to .../pay/pending so the assertion
// still checks for /pay/pending in the Location header.
func TestHandleHostedPay_Idempotency(t *testing.T) {
	db := hostedPayHandlerDB(t)
	_, _, token := seedPayBase(t, db)
	srv := &Server{DB: db}
	app := payPublicApp(srv)

	// First POST → creates new attempt, redirect to provider URL.
	resp1 := postFormRequest(t, app, "/i/"+token+"/pay", nil)
	if resp1.StatusCode != http.StatusSeeOther {
		t.Fatalf("first POST: expected 303, got %d", resp1.StatusCode)
	}
	loc1 := resp1.Header.Get("Location")

	// Second POST within window → reuses existing redirected attempt.
	// Redirect must go to the same URL (reuse), not to /pay/pending separately.
	resp2 := postFormRequest(t, app, "/i/"+token+"/pay", nil)
	if resp2.StatusCode != http.StatusSeeOther {
		t.Fatalf("second POST: expected 303, got %d", resp2.StatusCode)
	}
	loc2 := resp2.Header.Get("Location")
	if loc2 != loc1 {
		t.Fatalf("second POST should reuse same redirect URL as first\n  first:  %q\n  second: %q", loc1, loc2)
	}
	// ManualProvider redirects to /pay/pending — both should contain it.
	if !strings.Contains(loc2, "/pay/pending") {
		t.Fatalf("expected /pay/pending in reused redirect URL, got %q", loc2)
	}
}

// ── handleHostedPayPending ────────────────────────────────────────────────────

func TestHandleHostedPayPending_Valid(t *testing.T) {
	db := hostedPayHandlerDB(t)
	_, _, token := seedPayBase(t, db)
	srv := &Server{DB: db}
	app := payPublicApp(srv)

	req, _ := http.NewRequest(http.MethodGet, "/i/"+token+"/pay/pending", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Payment Submitted") {
		t.Fatal("expected 'Payment Submitted' in pending page body")
	}
}

func TestHandleHostedPayPending_InvalidToken(t *testing.T) {
	db := hostedPayHandlerDB(t)
	srv := &Server{DB: db}
	app := payPublicApp(srv)

	req, _ := http.NewRequest(http.MethodGet, "/i/notavalidtoken/pay/pending", nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("expected 410, got %d", resp.StatusCode)
	}
}

// ── handleHostedPayCancel ─────────────────────────────────────────────────────

func TestHandleHostedPayCancel_Valid(t *testing.T) {
	db := hostedPayHandlerDB(t)
	_, _, token := seedPayBase(t, db)
	srv := &Server{DB: db}
	app := payPublicApp(srv)

	req, _ := http.NewRequest(http.MethodGet, "/i/"+token+"/pay/cancel", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "Payment Cancelled") {
		t.Fatal("expected 'Payment Cancelled' in cancel page body")
	}
	// Should have a back link to the invoice.
	if !strings.Contains(bodyStr, "/i/"+token) {
		t.Fatalf("expected back link to /i/%s in cancel page", token)
	}
}

func TestHandleHostedPayCancel_InvalidToken(t *testing.T) {
	db := hostedPayHandlerDB(t)
	srv := &Server{DB: db}
	app := payPublicApp(srv)

	req, _ := http.NewRequest(http.MethodGet, "/i/notavalidtoken/pay/cancel", nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("expected 410, got %d", resp.StatusCode)
	}
}

// ── handleUpdateShareLinkExpiry ───────────────────────────────────────────────

func TestHandleUpdateShareLinkExpiry_Set(t *testing.T) {
	db := hostedPayHandlerDB(t)
	co := models.Company{Name: "Exp Co", BaseCurrencyCode: "CAD", IsActive: true}
	db.Create(&co)
	cust := models.Customer{CompanyID: co.ID, Name: "C"}
	db.Create(&cust)
	inv := models.Invoice{
		CompanyID: co.ID, CustomerID: cust.ID,
		InvoiceNumber: "INV-E", Status: models.InvoiceStatusIssued,
		Amount: decimal.NewFromFloat(50),
	}
	db.Create(&inv)
	services.CreateHostedLink(db, co.ID, inv.ID, nil)

	user := seedPayUser(t, db)
	m := models.CompanyMembership{UserID: user.ID, CompanyID: co.ID, Role: "owner", IsActive: true}
	db.Create(&m)

	srv := &Server{DB: db}
	app := expiryApp(srv, user, co.ID)

	future := time.Now().Add(24 * time.Hour).Format("2006-01-02T15:04")
	form := url.Values{"expires_at": {future}}
	resp := postFormRequest(t, app, fmt.Sprintf("/invoices/%d/share-link/expiry", inv.ID), form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", resp.StatusCode)
	}

	// Verify expiry was stored.
	link, err := services.GetActiveHostedLink(db, co.ID, inv.ID)
	if err != nil {
		t.Fatalf("GetActiveHostedLink: %v", err)
	}
	if link.ExpiresAt == nil {
		t.Fatal("expected ExpiresAt to be set")
	}
}

func TestHandleUpdateShareLinkExpiry_Clear(t *testing.T) {
	db := hostedPayHandlerDB(t)
	co := models.Company{Name: "Clr Co", BaseCurrencyCode: "CAD", IsActive: true}
	db.Create(&co)
	cust := models.Customer{CompanyID: co.ID, Name: "C"}
	db.Create(&cust)
	inv := models.Invoice{
		CompanyID: co.ID, CustomerID: cust.ID,
		InvoiceNumber: "INV-CLR", Status: models.InvoiceStatusIssued,
		Amount: decimal.NewFromFloat(50),
	}
	db.Create(&inv)
	exp := time.Now().Add(time.Hour)
	pt, _, _ := services.CreateHostedLink(db, co.ID, inv.ID, nil)
	// Set an initial expiry.
	link, _ := services.GetActiveHostedLink(db, co.ID, inv.ID)
	db.Model(link).Update("expires_at", exp)
	_ = pt

	user := seedPayUser(t, db)
	m := models.CompanyMembership{UserID: user.ID, CompanyID: co.ID, Role: "owner", IsActive: true}
	db.Create(&m)

	srv := &Server{DB: db}
	app := expiryApp(srv, user, co.ID)

	// Submit empty expires_at → clear.
	form := url.Values{"expires_at": {""}}
	resp := postFormRequest(t, app, fmt.Sprintf("/invoices/%d/share-link/expiry", inv.ID), form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", resp.StatusCode)
	}

	updated, _ := services.GetActiveHostedLink(db, co.ID, inv.ID)
	if updated.ExpiresAt != nil {
		t.Fatal("expected ExpiresAt to be cleared (nil)")
	}
}

func TestHandleUpdateShareLinkExpiry_NoLink(t *testing.T) {
	db := hostedPayHandlerDB(t)
	co := models.Company{Name: "NL Co", BaseCurrencyCode: "CAD", IsActive: true}
	db.Create(&co)
	cust := models.Customer{CompanyID: co.ID, Name: "C"}
	db.Create(&cust)
	inv := models.Invoice{
		CompanyID: co.ID, CustomerID: cust.ID,
		InvoiceNumber: "INV-NL", Status: models.InvoiceStatusIssued,
	}
	db.Create(&inv)

	user := seedPayUser(t, db)
	m := models.CompanyMembership{UserID: user.ID, CompanyID: co.ID, Role: "owner", IsActive: true}
	db.Create(&m)

	srv := &Server{DB: db}
	app := expiryApp(srv, user, co.ID)

	form := url.Values{"expires_at": {time.Now().Add(time.Hour).Format("2006-01-02T15:04")}}
	resp := postFormRequest(t, app, fmt.Sprintf("/invoices/%d/share-link/expiry", inv.ID), form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect with error, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "error=") {
		t.Fatalf("expected ?error= in redirect, got %q", loc)
	}
}

func TestHandleUpdateShareLinkExpiry_PastDate(t *testing.T) {
	db := hostedPayHandlerDB(t)
	co := models.Company{Name: "PD Co", BaseCurrencyCode: "CAD", IsActive: true}
	db.Create(&co)
	cust := models.Customer{CompanyID: co.ID, Name: "C"}
	db.Create(&cust)
	inv := models.Invoice{
		CompanyID: co.ID, CustomerID: cust.ID,
		InvoiceNumber: "INV-PD", Status: models.InvoiceStatusIssued,
		Amount: decimal.NewFromFloat(50),
	}
	db.Create(&inv)
	services.CreateHostedLink(db, co.ID, inv.ID, nil)

	user := seedPayUser(t, db)
	m := models.CompanyMembership{UserID: user.ID, CompanyID: co.ID, Role: "owner", IsActive: true}
	db.Create(&m)

	srv := &Server{DB: db}
	app := expiryApp(srv, user, co.ID)

	// Past date should be rejected.
	past := time.Now().Add(-time.Hour).Format("2006-01-02T15:04")
	form := url.Values{"expires_at": {past}}
	resp := postFormRequest(t, app, fmt.Sprintf("/invoices/%d/share-link/expiry", inv.ID), form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "error=") {
		t.Fatalf("expected ?error= for past date, got %q", loc)
	}
}

// ── Batch 7.1 new handler tests ───────────────────────────────────────────────

// TestHandleHostedInvoice_PayNowRendersRealForm verifies that when CanPay=true
// (all five gates pass) the hosted invoice page renders a real POST form
// pointing to /i/:token/pay, not a disabled placeholder.
func TestHandleHostedInvoice_PayNowRendersRealForm(t *testing.T) {
	db := hostedPayHandlerDB(t)
	co, inv, token := seedPayBase(t, db)
	// inv is issued with BalanceDue=150, gateway is manual (ready).
	// All five gates should pass → CanPay=true.
	_ = co
	_ = inv

	srv := &Server{DB: db}
	// Use publicApp which includes handleHostedInvoice.
	app := payPublicApp(srv)

	req, _ := http.NewRequest(http.MethodGet, "/i/"+token, nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Must contain a real POST form, not a disabled span.
	payFormMarker := `method="post"`
	payActionMarker := `/i/` + token + `/pay`
	if !strings.Contains(bodyStr, payFormMarker) {
		t.Fatal("expected POST form in hosted page with CanPay=true, got none")
	}
	if !strings.Contains(bodyStr, payActionMarker) {
		t.Fatalf("expected form action %q in hosted page, body excerpt: %s",
			payActionMarker, bodyStr[max(0, len(bodyStr)-500):])
	}
	// Must NOT contain the disabled placeholder span.
	if strings.Contains(bodyStr, `hb-btn-dis`) && strings.Contains(bodyStr, "Pay Now") &&
		!strings.Contains(bodyStr, payFormMarker) {
		t.Fatal("found disabled Pay Now placeholder; expected real POST form")
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// TestHandleHostedPay_CancelUnblocksRetry verifies the cancel → retry lifecycle:
// 1. POST /pay creates attempt → redirected
// 2. GET /pay/cancel marks attempt cancelled
// 3. Next POST /pay creates a fresh attempt (not blocked by idempotency)
func TestHandleHostedPay_CancelUnblocksRetry(t *testing.T) {
	db := hostedPayHandlerDB(t)
	_, _, token := seedPayBase(t, db)
	srv := &Server{DB: db}
	app := payPublicApp(srv)

	// Step 1: create first attempt.
	resp1 := postFormRequest(t, app, "/i/"+token+"/pay", nil)
	if resp1.StatusCode != http.StatusSeeOther {
		t.Fatalf("step 1 POST: expected 303, got %d", resp1.StatusCode)
	}

	// Step 2: cancel.
	cancelReq, _ := http.NewRequest(http.MethodGet, "/i/"+token+"/pay/cancel", nil)
	cancelResp, err := app.Test(cancelReq, -1)
	if err != nil {
		t.Fatal(err)
	}
	if cancelResp.StatusCode != http.StatusOK {
		t.Fatalf("cancel page: expected 200, got %d", cancelResp.StatusCode)
	}

	// Step 3: retry — must create a fresh attempt (not blocked, not reuse).
	resp3 := postFormRequest(t, app, "/i/"+token+"/pay", nil)
	if resp3.StatusCode != http.StatusSeeOther {
		t.Fatalf("step 3 retry POST: expected 303, got %d", resp3.StatusCode)
	}

	// Verify exactly 2 attempt rows exist: one cancelled, one redirected.
	var attempts []models.HostedPaymentAttempt
	db.Find(&attempts)
	if len(attempts) != 2 {
		t.Fatalf("expected 2 attempt rows (cancelled + new), got %d", len(attempts))
	}
	statuses := map[models.HostedPaymentAttemptStatus]int{}
	for _, a := range attempts {
		statuses[a.Status]++
	}
	if statuses[models.HostedPaymentAttemptCancelled] != 1 {
		t.Fatalf("expected 1 cancelled attempt, got %d", statuses[models.HostedPaymentAttemptCancelled])
	}
	if statuses[models.HostedPaymentAttemptRedirected] != 1 {
		t.Fatalf("expected 1 redirected attempt, got %d", statuses[models.HostedPaymentAttemptRedirected])
	}
}
