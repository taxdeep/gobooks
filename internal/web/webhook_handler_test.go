// 遵循project_guide.md
package web

// webhook_handler_test.go — Batch 10 webhook HTTP handler tests.
//
// Coverage:
//   TestStripeWebhook_ValidSignature_CheckoutCompleted    — 200, attempt updated
//   TestStripeWebhook_InvalidSignature                   — 400
//   TestStripeWebhook_MissingSignatureHeader             — 400
//   TestStripeWebhook_DuplicateEvent                     — 200 (idempotent)
//   TestStripeWebhook_UnknownEventType                   — 200 (stored, no status change)
//   TestStripeWebhook_UnknownGatewayID                   — 400
//   TestStripeWebhook_InactiveGateway                    — 400
//   TestStripeWebhook_MalformedPayload                   — 500
//   TestHostedPayPending_Provisional                     — shows provisional wording when not confirmed
//   TestHostedPayPending_Confirmed                       — shows confirmed wording after payment_succeeded
//   TestHostedPayPending_InvalidToken                    — returns 410 Gone

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ── DB helpers ────────────────────────────────────────────────────────────────

func webhookWebDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:wh_web_%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
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
		&models.WebhookEvent{},
		&models.Session{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

// webhookTestSeed holds IDs and values used across webhook handler tests.
type webhookTestSeed struct {
	companyID uint
	gw        models.PaymentGatewayAccount
	inv       models.Invoice
	attempt   models.HostedPaymentAttempt
}

const webhookTestSecret = "whsec_webhook_web_test"

func seedWebhookWeb(t *testing.T, db *gorm.DB) webhookTestSeed {
	t.Helper()
	co := models.Company{Name: fmt.Sprintf("WW%d", time.Now().UnixNano()), BaseCurrencyCode: "CAD", IsActive: true}
	db.Create(&co)

	u := models.User{ID: uuid.New(), Email: fmt.Sprintf("ww_%d@test.com", time.Now().UnixNano()), PasswordHash: "x", IsActive: true}
	db.Create(&u)
	db.Create(&models.CompanyMembership{UserID: u.ID, CompanyID: co.ID, Role: "owner"})

	cust := models.Customer{CompanyID: co.ID, Name: "WW Cust", Email: "ww@test.com"}
	db.Create(&cust)

	je := models.JournalEntry{CompanyID: co.ID, EntryDate: time.Now()}
	db.Create(&je)

	inv := models.Invoice{
		CompanyID: co.ID, InvoiceNumber: fmt.Sprintf("INV-WW-%d", time.Now().UnixNano()),
		CustomerID: cust.ID, InvoiceDate: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Status: models.InvoiceStatusIssued, Amount: decimal.RequireFromString("200.00"),
		Subtotal: decimal.RequireFromString("200.00"), TaxTotal: decimal.Zero,
		BalanceDue: decimal.RequireFromString("200.00"), BalanceDueBase: decimal.RequireFromString("200.00"),
		CustomerNameSnapshot: "WW Cust", CustomerEmailSnapshot: "ww@test.com",
		JournalEntryID: &je.ID,
	}
	db.Create(&inv)

	gw := models.PaymentGatewayAccount{
		CompanyID: co.ID, ProviderType: models.ProviderStripe, DisplayName: "WW Stripe",
		ExternalAccountRef: "sk_test_ww", WebhookSecret: webhookTestSecret,
		AuthStatus: "verified", WebhookStatus: "configured", IsActive: true,
	}
	db.Create(&gw)

	link := models.InvoiceHostedLink{
		CompanyID: co.ID, InvoiceID: inv.ID, TokenHash: "wwtesthash",
		Status: models.InvoiceHostedLinkStatusActive,
	}
	db.Create(&link)

	att := models.HostedPaymentAttempt{
		CompanyID: co.ID, InvoiceID: inv.ID, HostedLinkID: link.ID,
		GatewayAccountID: gw.ID, ProviderType: models.ProviderStripe,
		Amount: decimal.RequireFromString("200.00"), CurrencyCode: "CAD",
		Status: models.HostedPaymentAttemptRedirected, ProviderRef: "cs_ww_test_session",
		RedirectURL: "https://checkout.stripe.com/ww",
	}
	db.Create(&att)

	return webhookTestSeed{companyID: co.ID, gw: gw, inv: inv, attempt: att}
}

// makeStripeSignatureHeader builds a valid Stripe-Signature header for the payload+secret.
func makeStripeSignatureHeader(payload []byte, secret string) string {
	ts := time.Now().Unix()
	signed := fmt.Sprintf("%d.%s", ts, payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signed))
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

func webhookApp(server *Server) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: func(c *fiber.Ctx, err error) error {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}})
	app.Post("/webhooks/stripe/:gateway_id", server.handleStripeWebhook)
	return app
}

func postWebhook(t *testing.T, app *fiber.App, gatewayID uint, payload []byte, sigHeader string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost,
		fmt.Sprintf("/webhooks/stripe/%d", gatewayID),
		bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	if sigHeader != "" {
		req.Header.Set("Stripe-Signature", sigHeader)
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readBody(resp *http.Response) string {
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// ── Webhook handler tests ─────────────────────────────────────────────────────

func TestStripeWebhook_ValidSignature_CheckoutCompleted(t *testing.T) {
	db := webhookWebDB(t)
	seed := seedWebhookWeb(t, db)
	app := webhookApp(&Server{DB: db})

	payload, _ := json.Marshal(map[string]any{
		"id": "evt_valid_001", "type": "checkout.session.completed",
		"data": map[string]any{"object": map[string]any{
			"id": seed.attempt.ProviderRef, "payment_intent": "pi_valid_001",
			"amount_total": 20000, "currency": "cad", "payment_status": "paid",
		}},
	})
	resp := postWebhook(t, app, seed.gw.ID, payload, makeStripeSignatureHeader(payload, webhookTestSecret))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, readBody(resp))
	}

	var att models.HostedPaymentAttempt
	db.First(&att, seed.attempt.ID)
	if att.Status != models.HostedPaymentAttemptPaymentSucceeded {
		t.Errorf("attempt status: expected payment_succeeded, got %q", att.Status)
	}

	var prCount int64
	db.Model(&models.PaymentRequest{}).Where("gateway_account_id = ?", seed.gw.ID).Count(&prCount)
	if prCount != 1 {
		t.Errorf("expected 1 PaymentRequest, got %d", prCount)
	}

	// Accounting must NOT be touched — invoice balance unchanged.
	var invAfter models.Invoice
	db.First(&invAfter, seed.inv.ID)
	if invAfter.BalanceDue.Cmp(seed.inv.BalanceDue) != 0 {
		t.Errorf("invoice BalanceDue must not change from webhook; got %s", invAfter.BalanceDue)
	}
}

func TestStripeWebhook_InvalidSignature(t *testing.T) {
	db := webhookWebDB(t)
	seed := seedWebhookWeb(t, db)
	app := webhookApp(&Server{DB: db})

	payload := []byte(`{"id":"evt_badsig","type":"checkout.session.completed","data":{"object":{}}}`)
	wrongSig := makeStripeSignatureHeader(payload, "whsec_wrong")

	resp := postWebhook(t, app, seed.gw.ID, payload, wrongSig)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("wrong signature: expected 400, got %d", resp.StatusCode)
	}

	var count int64
	db.Model(&models.WebhookEvent{}).Where("external_event_id = ?", "evt_badsig").Count(&count)
	if count != 0 {
		t.Errorf("invalid signature must not store event, found %d", count)
	}
}

func TestStripeWebhook_MissingSignatureHeader(t *testing.T) {
	db := webhookWebDB(t)
	seed := seedWebhookWeb(t, db)
	app := webhookApp(&Server{DB: db})

	payload := []byte(`{"id":"evt_nosig","type":"checkout.session.completed","data":{"object":{}}}`)
	resp := postWebhook(t, app, seed.gw.ID, payload, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing sig header: expected 400, got %d", resp.StatusCode)
	}
}

func TestStripeWebhook_DuplicateEvent(t *testing.T) {
	db := webhookWebDB(t)
	seed := seedWebhookWeb(t, db)
	app := webhookApp(&Server{DB: db})

	payload, _ := json.Marshal(map[string]any{
		"id": "evt_dup_web_001", "type": "checkout.session.completed",
		"data": map[string]any{"object": map[string]any{
			"id": seed.attempt.ProviderRef, "payment_intent": "pi_dup",
			"currency": "cad", "payment_status": "paid",
		}},
	})
	sig := makeStripeSignatureHeader(payload, webhookTestSecret)

	resp1 := postWebhook(t, app, seed.gw.ID, payload, sig)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first delivery: expected 200, got %d", resp1.StatusCode)
	}
	resp2 := postWebhook(t, app, seed.gw.ID, payload, sig)
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("duplicate delivery: expected 200 (idempotent), got %d", resp2.StatusCode)
	}

	var count int64
	db.Model(&models.WebhookEvent{}).Where("external_event_id = ?", "evt_dup_web_001").Count(&count)
	if count != 1 {
		t.Errorf("dedup: expected 1 WebhookEvent, got %d", count)
	}

	var prCount int64
	db.Model(&models.PaymentRequest{}).Where("gateway_account_id = ?", seed.gw.ID).Count(&prCount)
	if prCount != 1 {
		t.Errorf("dedup: expected 1 PaymentRequest, got %d", prCount)
	}
}

func TestStripeWebhook_UnknownEventType(t *testing.T) {
	db := webhookWebDB(t)
	seed := seedWebhookWeb(t, db)
	app := webhookApp(&Server{DB: db})

	payload, _ := json.Marshal(map[string]any{
		"id": "evt_unknown_web", "type": "customer.created",
		"data": map[string]any{"object": map[string]any{"id": "cus_123"}},
	})
	sig := makeStripeSignatureHeader(payload, webhookTestSecret)

	resp := postWebhook(t, app, seed.gw.ID, payload, sig)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("unknown event: expected 200, got %d", resp.StatusCode)
	}

	var we models.WebhookEvent
	if err := db.Where("external_event_id = ?", "evt_unknown_web").First(&we).Error; err != nil {
		t.Errorf("unknown event should be stored: %v", err)
	}

	// Attempt status unchanged.
	var att models.HostedPaymentAttempt
	db.First(&att, seed.attempt.ID)
	if att.Status != models.HostedPaymentAttemptRedirected {
		t.Errorf("attempt status should be unchanged; got %q", att.Status)
	}
}

func TestStripeWebhook_UnknownGatewayID(t *testing.T) {
	db := webhookWebDB(t)
	app := webhookApp(&Server{DB: db})

	payload := []byte(`{"id":"evt_badgw","type":"checkout.session.completed","data":{"object":{}}}`)
	resp := postWebhook(t, app, 99999, payload, makeStripeSignatureHeader(payload, "anysecret"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown gateway: expected 400, got %d", resp.StatusCode)
	}
}

func TestStripeWebhook_InactiveGateway(t *testing.T) {
	db := webhookWebDB(t)
	seed := seedWebhookWeb(t, db)
	db.Model(&seed.gw).Update("is_active", false)
	app := webhookApp(&Server{DB: db})

	payload := []byte(`{"id":"evt_inactive","type":"checkout.session.completed","data":{"object":{}}}`)
	resp := postWebhook(t, app, seed.gw.ID, payload, makeStripeSignatureHeader(payload, webhookTestSecret))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("inactive gateway: expected 400, got %d", resp.StatusCode)
	}
}

func TestStripeWebhook_MalformedPayload(t *testing.T) {
	db := webhookWebDB(t)
	seed := seedWebhookWeb(t, db)
	app := webhookApp(&Server{DB: db})

	bad := []byte("not json at all {{{{")
	resp := postWebhook(t, app, seed.gw.ID, bad, makeStripeSignatureHeader(bad, webhookTestSecret))
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("malformed payload: expected 500, got %d", resp.StatusCode)
	}
}

// ── Payment status page tests ─────────────────────────────────────────────────

// hostedPendingApp creates a minimal Fiber app wired to the pending handler.
func hostedPendingApp(server *Server) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: func(c *fiber.Ctx, err error) error {
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}})
	app.Get("/i/:token/pay/pending", server.handleHostedPayPending)
	return app
}

// hostedTokenHash mirrors the sha256 logic in invoice_hosted_link_service.go.
func hostedTokenHash(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// seedForPendingPage creates a hosted link+invoice with the given attempt status (or no attempt if "").
func seedForPendingPage(t *testing.T, db *gorm.DB, attemptStatus models.HostedPaymentAttemptStatus) (token string) {
	t.Helper()
	co := models.Company{Name: fmt.Sprintf("PendCo%d", time.Now().UnixNano()), BaseCurrencyCode: "CAD", IsActive: true}
	db.Create(&co)
	cust := models.Customer{CompanyID: co.ID, Name: "PC"}
	db.Create(&cust)
	inv := models.Invoice{
		CompanyID: co.ID, CustomerID: cust.ID, Status: models.InvoiceStatusIssued,
		Amount: decimal.RequireFromString("50.00"), BalanceDue: decimal.RequireFromString("50.00"),
		InvoiceNumber: fmt.Sprintf("INV-PEND-%d", time.Now().UnixNano()),
	}
	db.Create(&inv)

	token = fmt.Sprintf("testpendtoken%d", time.Now().UnixNano())
	link := models.InvoiceHostedLink{
		CompanyID: co.ID, InvoiceID: inv.ID,
		TokenHash: hostedTokenHash(token),
		Status:    models.InvoiceHostedLinkStatusActive,
	}
	db.Create(&link)

	if attemptStatus != "" {
		gw := models.PaymentGatewayAccount{CompanyID: co.ID, ProviderType: models.ProviderStripe,
			DisplayName: "PGW", IsActive: true}
		db.Create(&gw)
		att := models.HostedPaymentAttempt{
			CompanyID: co.ID, InvoiceID: inv.ID, HostedLinkID: link.ID,
			GatewayAccountID: gw.ID, ProviderType: models.ProviderStripe,
			Amount: decimal.RequireFromString("50.00"), CurrencyCode: "CAD",
			Status: attemptStatus, ProviderRef: "cs_pend_test",
		}
		db.Create(&att)
	}
	return token
}

// ── Batch 10.1: provider-type assertion ──────────────────────────────────────

// TestStripeWebhook_NonStripeGateway verifies that posting to a gateway that is not
// of provider_type="stripe" returns 400 — the endpoint is Stripe-only.
func TestStripeWebhook_NonStripeGateway(t *testing.T) {
	db := webhookWebDB(t)
	// Create a Manual provider gateway (not Stripe).
	co := models.Company{Name: fmt.Sprintf("ManCo%d", time.Now().UnixNano()), BaseCurrencyCode: "CAD", IsActive: true}
	db.Create(&co)
	gw := models.PaymentGatewayAccount{
		CompanyID:     co.ID,
		ProviderType:  models.ProviderManual,
		DisplayName:   "Manual GW",
		WebhookSecret: "anysecret",
		IsActive:      true,
	}
	db.Create(&gw)

	app := webhookApp(&Server{DB: db})
	payload := []byte(`{"id":"evt_manual","type":"checkout.session.completed","data":{"object":{}}}`)
	sig := makeStripeSignatureHeader(payload, "anysecret")

	resp := postWebhook(t, app, gw.ID, payload, sig)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("non-Stripe gateway: expected 400, got %d: %s", resp.StatusCode, readBody(resp))
	}
}

func TestHostedPayPending_Provisional(t *testing.T) {
	db := webhookWebDB(t)
	token := seedForPendingPage(t, db, models.HostedPaymentAttemptRedirected)
	app := hostedPendingApp(&Server{DB: db})

	req, _ := http.NewRequest(http.MethodGet, "/i/"+token+"/pay/pending", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	html := readBody(resp)

	// Must NOT say "Payment Confirmed" — webhook not yet received.
	if strings.Contains(html, "Payment Confirmed") {
		t.Errorf("provisional page must not say 'Payment Confirmed' before webhook")
	}
	// Must show provisional wording.
	if !strings.Contains(html, "Payment Submitted") {
		t.Errorf("provisional page should say 'Payment Submitted'; got: %s", html)
	}
}

func TestHostedPayPending_Confirmed(t *testing.T) {
	db := webhookWebDB(t)
	token := seedForPendingPage(t, db, models.HostedPaymentAttemptPaymentSucceeded)
	app := hostedPendingApp(&Server{DB: db})

	req, _ := http.NewRequest(http.MethodGet, "/i/"+token+"/pay/pending", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	html := readBody(resp)

	if !strings.Contains(html, "Payment Confirmed") {
		t.Errorf("confirmed page should say 'Payment Confirmed'; got: %s", html)
	}
	// Must not overclaim accounting settlement.
	for _, banned := range []string{"invoice is paid", "fully settled", "accounting", "journal"} {
		if strings.Contains(strings.ToLower(html), banned) {
			t.Errorf("page must not mention %q (accounting settlement scope): %s", banned, html)
		}
	}
}

func TestHostedPayPending_InvalidToken(t *testing.T) {
	db := webhookWebDB(t)
	app := hostedPendingApp(&Server{DB: db})

	req, _ := http.NewRequest(http.MethodGet, "/i/completelyinvalidtoken/pay/pending", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusGone {
		t.Errorf("invalid token: expected 410 Gone, got %d", resp.StatusCode)
	}
}
