// 遵循project_guide.md
package services

// webhook_ingestion_service_test.go — Batch 10 webhook ingestion service tests.
//
// Coverage:
//   TestVerifyStripeSignature_ValidSignature        — correct HMAC accepted
//   TestVerifyStripeSignature_InvalidSignature      — wrong HMAC rejected
//   TestVerifyStripeSignature_MissingHeader         — absent header rejected
//   TestVerifyStripeSignature_NoSecret              — gateway with empty secret rejected
//   TestVerifyStripeSignature_StaleTimestamp        — timestamp >5min old rejected
//   TestVerifyStripeSignature_FutureTimestamp       — timestamp >5min future rejected
//   TestVerifyStripeSignature_MalformedHeader       — missing t= field rejected
//   TestIngestStripeEvent_CheckoutCompleted         — attempt updated, PR+txn created
//   TestIngestStripeEvent_CheckoutCompleted_Dedup   — duplicate event idempotent (no double apply)
//   TestIngestStripeEvent_CheckoutExpired           — attempt marked cancelled
//   TestIngestStripeEvent_UnknownEventType          — stored without error, no status change
//   TestIngestStripeEvent_NoMatchingAttempt         — missing attempt handled gracefully
//   TestIngestStripeEvent_AlreadySucceeded          — re-delivery after success is safe
//   TestLatestAttemptForInvoice                     — returns most recent attempt or nil

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

func webhookTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:wh_%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
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
	); err != nil {
		t.Fatal(err)
	}
	return db
}

// seedWebhookBase creates a company, gateway, invoice, and a redirected attempt.
func seedWebhookBase(t *testing.T, db *gorm.DB) (uint, models.PaymentGatewayAccount, models.Invoice, models.HostedPaymentAttempt) {
	t.Helper()
	co := models.Company{Name: "Webhook Co", BaseCurrencyCode: "CAD", IsActive: true}
	db.Create(&co)

	cust := models.Customer{CompanyID: co.ID, Name: "WH Cust", Email: "wh@test.com"}
	db.Create(&cust)

	je := models.JournalEntry{CompanyID: co.ID, EntryDate: time.Now()}
	db.Create(&je)

	inv := models.Invoice{
		CompanyID:             co.ID,
		InvoiceNumber:         fmt.Sprintf("INV-WH-%d", time.Now().UnixNano()),
		CustomerID:            cust.ID,
		InvoiceDate:           time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Status:                models.InvoiceStatusIssued,
		Amount:                decimal.RequireFromString("100.00"),
		Subtotal:              decimal.RequireFromString("100.00"),
		TaxTotal:              decimal.Zero,
		BalanceDue:            decimal.RequireFromString("100.00"),
		BalanceDueBase:        decimal.RequireFromString("100.00"),
		CustomerNameSnapshot:  "WH Cust",
		CustomerEmailSnapshot: "wh@test.com",
		JournalEntryID:        &je.ID,
	}
	db.Create(&inv)

	gw := models.PaymentGatewayAccount{
		CompanyID:          co.ID,
		ProviderType:       models.ProviderStripe,
		DisplayName:        "Test Stripe",
		ExternalAccountRef: "sk_test_123",
		WebhookSecret:      "whsec_test",
		AuthStatus:         "verified",
		WebhookStatus:      "configured",
		IsActive:           true,
	}
	db.Create(&gw)

	link := models.InvoiceHostedLink{
		CompanyID: co.ID,
		InvoiceID: inv.ID,
		TokenHash: "testhash",
		Status:    models.InvoiceHostedLinkStatusActive,
	}
	db.Create(&link)

	attempt := models.HostedPaymentAttempt{
		CompanyID:        co.ID,
		InvoiceID:        inv.ID,
		HostedLinkID:     link.ID,
		GatewayAccountID: gw.ID,
		ProviderType:     models.ProviderStripe,
		Amount:           decimal.RequireFromString("100.00"),
		CurrencyCode:     "CAD",
		Status:           models.HostedPaymentAttemptRedirected,
		ProviderRef:      "cs_test_session_001",
		RedirectURL:      "https://checkout.stripe.com/test",
	}
	db.Create(&attempt)

	return co.ID, gw, inv, attempt
}

// buildStripeSignatureHeader creates a valid Stripe-Signature header for the given payload and secret.
func buildStripeSignatureHeader(payload []byte, secret string) string {
	ts := time.Now().Unix()
	signedStr := fmt.Sprintf("%d.%s", ts, payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedStr))
	sig := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("t=%d,v1=%s", ts, sig)
}

// buildCheckoutCompletedPayload builds a minimal Stripe checkout.session.completed event JSON.
func buildCheckoutCompletedPayload(eventID, sessionID, paymentIntentID string) []byte {
	payload, _ := json.Marshal(map[string]any{
		"id":   eventID,
		"type": "checkout.session.completed",
		"data": map[string]any{
			"object": map[string]any{
				"id":             sessionID,
				"payment_intent": paymentIntentID,
				"amount_total":   10000,
				"currency":       "cad",
				"payment_status": "paid",
			},
		},
	})
	return payload
}

// ── VerifyStripeSignature tests ───────────────────────────────────────────────

func TestVerifyStripeSignature_ValidSignature(t *testing.T) {
	secret := "whsec_test_secret"
	payload := []byte(`{"id":"evt_123","type":"checkout.session.completed"}`)
	header := buildStripeSignatureHeader(payload, secret)
	if err := VerifyStripeSignature(payload, header, secret); err != nil {
		t.Errorf("expected valid signature to pass, got: %v", err)
	}
}

func TestVerifyStripeSignature_InvalidSignature(t *testing.T) {
	secret := "whsec_correct"
	payload := []byte(`{"id":"evt_123"}`)
	header := buildStripeSignatureHeader(payload, "whsec_wrong") // signed with wrong secret
	err := VerifyStripeSignature(payload, header, secret)
	if err == nil {
		t.Error("expected signature mismatch to fail, got nil")
	}
}

func TestVerifyStripeSignature_MissingHeader(t *testing.T) {
	err := VerifyStripeSignature([]byte("body"), "", "secret")
	if err != ErrWebhookSignatureMissing {
		t.Errorf("expected ErrWebhookSignatureMissing, got %v", err)
	}
}

func TestVerifyStripeSignature_NoSecret(t *testing.T) {
	err := VerifyStripeSignature([]byte("body"), "t=123,v1=abc", "")
	if err != ErrWebhookSignatureNoSecret {
		t.Errorf("expected ErrWebhookSignatureNoSecret, got %v", err)
	}
}

func TestVerifyStripeSignature_StaleTimestamp(t *testing.T) {
	secret := "whsec_test"
	payload := []byte(`{"id":"evt_old"}`)
	// Build a header with a timestamp 10 minutes in the past.
	staleTS := time.Now().Add(-10 * time.Minute).Unix()
	signedStr := fmt.Sprintf("%d.%s", staleTS, payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedStr))
	sig := hex.EncodeToString(mac.Sum(nil))
	header := fmt.Sprintf("t=%d,v1=%s", staleTS, sig)

	err := VerifyStripeSignature(payload, header, secret)
	if err != ErrWebhookTimestampStale {
		t.Errorf("expected ErrWebhookTimestampStale, got %v", err)
	}
}

func TestVerifyStripeSignature_FutureTimestamp(t *testing.T) {
	secret := "whsec_test"
	payload := []byte(`{"id":"evt_future"}`)
	// Build a header with a timestamp 10 minutes in the future.
	futureTS := time.Now().Add(10 * time.Minute).Unix()
	signedStr := fmt.Sprintf("%d.%s", futureTS, payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedStr))
	sig := hex.EncodeToString(mac.Sum(nil))
	header := fmt.Sprintf("t=%d,v1=%s", futureTS, sig)

	err := VerifyStripeSignature(payload, header, secret)
	if err != ErrWebhookTimestampStale {
		t.Errorf("expected ErrWebhookTimestampStale for far-future timestamp, got %v", err)
	}
}

func TestVerifyStripeSignature_MalformedHeader(t *testing.T) {
	// Header without t= field.
	err := VerifyStripeSignature([]byte("body"), "v1=abc123", "secret")
	if err == nil {
		t.Error("expected error for header missing t=, got nil")
	}
}

// ── IngestStripeEvent tests ───────────────────────────────────────────────────

func TestIngestStripeEvent_CheckoutCompleted(t *testing.T) {
	db := webhookTestDB(t)
	_, gw, inv, attempt := seedWebhookBase(t, db)

	payload := buildCheckoutCompletedPayload("evt_001", attempt.ProviderRef, "pi_001")
	if err := IngestStripeEvent(db, gw.ID, payload); err != nil {
		t.Fatalf("IngestStripeEvent failed: %v", err)
	}

	// Attempt must be payment_succeeded.
	var updated models.HostedPaymentAttempt
	db.First(&updated, attempt.ID)
	if updated.Status != models.HostedPaymentAttemptPaymentSucceeded {
		t.Errorf("attempt status: expected payment_succeeded, got %q", updated.Status)
	}

	// A PaymentRequest must have been created and linked to the invoice.
	var pr models.PaymentRequest
	if err := db.Where("invoice_id = ? AND gateway_account_id = ?", inv.ID, gw.ID).First(&pr).Error; err != nil {
		t.Fatalf("PaymentRequest not found: %v", err)
	}
	if pr.Status != models.PaymentRequestPaid {
		t.Errorf("PaymentRequest status: expected paid, got %q", pr.Status)
	}

	// A PaymentTransaction must have been created.
	var txn models.PaymentTransaction
	if err := db.Where("payment_request_id = ?", pr.ID).First(&txn).Error; err != nil {
		t.Fatalf("PaymentTransaction not found: %v", err)
	}
	if txn.TransactionType != models.TxnTypeCharge {
		t.Errorf("transaction type: expected charge, got %q", txn.TransactionType)
	}
	if txn.Status != "completed" {
		t.Errorf("transaction status: expected completed, got %q", txn.Status)
	}
	if txn.ExternalTxnRef != "pi_001" {
		t.Errorf("ExternalTxnRef: expected pi_001, got %q", txn.ExternalTxnRef)
	}

	// WebhookEvent must be stored.
	var we models.WebhookEvent
	if err := db.Where("external_event_id = ?", "evt_001").First(&we).Error; err != nil {
		t.Fatalf("WebhookEvent not stored: %v", err)
	}
	if we.EventType != "checkout.session.completed" {
		t.Errorf("WebhookEvent type: expected checkout.session.completed, got %q", we.EventType)
	}

	// Accounting: no journal entries posted, no invoice balance changed.
	var invAfter models.Invoice
	db.First(&invAfter, inv.ID)
	if invAfter.BalanceDue.Cmp(inv.BalanceDue) != 0 {
		t.Errorf("invoice BalanceDue must not change from webhook ingestion — got %s", invAfter.BalanceDue)
	}
	if invAfter.Status != inv.Status {
		t.Errorf("invoice status must not change from webhook ingestion — got %q", invAfter.Status)
	}
}

func TestIngestStripeEvent_CheckoutCompleted_Dedup(t *testing.T) {
	db := webhookTestDB(t)
	_, gw, _, attempt := seedWebhookBase(t, db)

	payload := buildCheckoutCompletedPayload("evt_dedup_001", attempt.ProviderRef, "pi_dedup_001")

	// First ingestion.
	if err := IngestStripeEvent(db, gw.ID, payload); err != nil {
		t.Fatalf("first ingest failed: %v", err)
	}

	// Second ingestion of the same event — must be idempotent.
	if err := IngestStripeEvent(db, gw.ID, payload); err != nil {
		t.Fatalf("second ingest (duplicate) should not error: %v", err)
	}

	// Only one WebhookEvent row should exist.
	var count int64
	db.Model(&models.WebhookEvent{}).Where("external_event_id = ?", "evt_dedup_001").Count(&count)
	if count != 1 {
		t.Errorf("expected 1 WebhookEvent for dedup test, got %d", count)
	}

	// Only one PaymentRequest.
	var prCount int64
	db.Model(&models.PaymentRequest{}).Where("external_ref = ?", attempt.ProviderRef).Count(&prCount)
	if prCount != 1 {
		t.Errorf("expected 1 PaymentRequest after dedup, got %d", prCount)
	}

	// Attempt still payment_succeeded (not changed back).
	var att models.HostedPaymentAttempt
	db.First(&att, attempt.ID)
	if att.Status != models.HostedPaymentAttemptPaymentSucceeded {
		t.Errorf("attempt status after dedup: expected payment_succeeded, got %q", att.Status)
	}
}

func TestIngestStripeEvent_CheckoutExpired(t *testing.T) {
	db := webhookTestDB(t)
	_, gw, _, attempt := seedWebhookBase(t, db)

	expiredPayload, _ := json.Marshal(map[string]any{
		"id":   "evt_expired_001",
		"type": "checkout.session.expired",
		"data": map[string]any{
			"object": map[string]any{
				"id":     attempt.ProviderRef,
				"status": "expired",
			},
		},
	})

	if err := IngestStripeEvent(db, gw.ID, expiredPayload); err != nil {
		t.Fatalf("IngestStripeEvent (expired) failed: %v", err)
	}

	var updated models.HostedPaymentAttempt
	db.First(&updated, attempt.ID)
	if updated.Status != models.HostedPaymentAttemptCancelled {
		t.Errorf("attempt status after expired: expected cancelled, got %q", updated.Status)
	}

	// No PaymentRequest created for expired sessions.
	var prCount int64
	db.Model(&models.PaymentRequest{}).Where("gateway_account_id = ?", gw.ID).Count(&prCount)
	if prCount != 0 {
		t.Errorf("expected no PaymentRequest for expired session, got %d", prCount)
	}
}

func TestIngestStripeEvent_CheckoutExpired_Dedup(t *testing.T) {
	db := webhookTestDB(t)
	_, gw, _, attempt := seedWebhookBase(t, db)

	expiredPayload, _ := json.Marshal(map[string]any{
		"id":   "evt_expired_dedup",
		"type": "checkout.session.expired",
		"data": map[string]any{"object": map[string]any{"id": attempt.ProviderRef}},
	})

	IngestStripeEvent(db, gw.ID, expiredPayload)
	// Second delivery.
	if err := IngestStripeEvent(db, gw.ID, expiredPayload); err != nil {
		t.Fatalf("duplicate expired event should not error: %v", err)
	}

	var count int64
	db.Model(&models.WebhookEvent{}).Where("external_event_id = ?", "evt_expired_dedup").Count(&count)
	if count != 1 {
		t.Errorf("expected 1 WebhookEvent for expired dedup, got %d", count)
	}
}

func TestIngestStripeEvent_UnknownEventType(t *testing.T) {
	db := webhookTestDB(t)
	_, gw, _, _ := seedWebhookBase(t, db)

	unknown, _ := json.Marshal(map[string]any{
		"id":   "evt_unknown_001",
		"type": "payment_intent.created", // not handled in Batch 10
		"data": map[string]any{"object": map[string]any{"id": "pi_001"}},
	})

	if err := IngestStripeEvent(db, gw.ID, unknown); err != nil {
		t.Fatalf("unknown event type should not error: %v", err)
	}

	// WebhookEvent stored for traceability.
	var we models.WebhookEvent
	if err := db.Where("external_event_id = ?", "evt_unknown_001").First(&we).Error; err != nil {
		t.Fatalf("unknown event should be stored in webhook_events: %v", err)
	}
}

func TestIngestStripeEvent_NoMatchingAttempt(t *testing.T) {
	db := webhookTestDB(t)
	_, gw, _, _ := seedWebhookBase(t, db)

	// Use a session ID that doesn't match any HostedPaymentAttempt.ProviderRef.
	payload := buildCheckoutCompletedPayload("evt_nomatch", "cs_unknown_session", "pi_unknown")

	// Should not error — just logs a warning and stores the event.
	if err := IngestStripeEvent(db, gw.ID, payload); err != nil {
		t.Fatalf("missing attempt should be handled gracefully: %v", err)
	}
}

func TestIngestStripeEvent_AlreadySucceeded(t *testing.T) {
	db := webhookTestDB(t)
	_, gw, _, attempt := seedWebhookBase(t, db)

	// Pre-set the attempt to payment_succeeded.
	db.Model(&attempt).Update("status", models.HostedPaymentAttemptPaymentSucceeded)

	// First delivery (already succeeded but different event ID).
	payload := buildCheckoutCompletedPayload("evt_resuccess", attempt.ProviderRef, "pi_x")
	if err := IngestStripeEvent(db, gw.ID, payload); err != nil {
		t.Fatalf("already-succeeded attempt should be handled gracefully: %v", err)
	}

	// Status unchanged (still payment_succeeded, not reset).
	var updated models.HostedPaymentAttempt
	db.First(&updated, attempt.ID)
	if updated.Status != models.HostedPaymentAttemptPaymentSucceeded {
		t.Errorf("status should remain payment_succeeded; got %q", updated.Status)
	}
}

func TestIngestStripeEvent_MalformedPayload(t *testing.T) {
	db := webhookTestDB(t)
	_, gw, _, _ := seedWebhookBase(t, db)

	err := IngestStripeEvent(db, gw.ID, []byte("not json"))
	if err == nil {
		t.Error("expected error for malformed JSON payload, got nil")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Errorf("error should mention 'malformed'; got: %v", err)
	}
}

// ── LatestAttemptForInvoice tests ─────────────────────────────────────────────

func TestLatestAttemptForInvoice(t *testing.T) {
	db := webhookTestDB(t)
	_, gw, inv, first := seedWebhookBase(t, db)

	// Latest returns the seeded attempt.
	got := LatestAttemptForInvoice(db, inv.ID, first.CompanyID)
	if got == nil {
		t.Fatal("expected an attempt, got nil")
	}
	if got.ID != first.ID {
		t.Errorf("expected attempt ID %d, got %d", first.ID, got.ID)
	}

	// Add a second attempt (newer).
	second := models.HostedPaymentAttempt{
		CompanyID:        first.CompanyID,
		InvoiceID:        inv.ID,
		HostedLinkID:     first.HostedLinkID,
		GatewayAccountID: gw.ID,
		ProviderType:     models.ProviderStripe,
		Amount:           decimal.RequireFromString("100.00"),
		CurrencyCode:     "CAD",
		Status:           models.HostedPaymentAttemptPaymentSucceeded,
		ProviderRef:      "cs_newer",
	}
	db.Create(&second)

	latest := LatestAttemptForInvoice(db, inv.ID, first.CompanyID)
	if latest == nil || latest.ID != second.ID {
		t.Errorf("expected latest attempt ID %d, got %v", second.ID, latest)
	}
}

func TestLatestAttemptForInvoice_NoAttempt(t *testing.T) {
	db := webhookTestDB(t)
	got := LatestAttemptForInvoice(db, 99999, 99999) // non-existent
	if got != nil {
		t.Errorf("expected nil for missing invoice, got %+v", got)
	}
}

func TestLatestAttemptForInvoice_CompanyIsolation(t *testing.T) {
	db := webhookTestDB(t)
	_, _, _, attempt := seedWebhookBase(t, db)

	// Query with a different company ID — must not return the other company's attempt.
	got := LatestAttemptForInvoice(db, attempt.InvoiceID, attempt.CompanyID+999)
	if got != nil {
		t.Errorf("company isolation violated: got attempt %+v", got)
	}
}

// ── Batch 10.1: HasVerifiedGatewayCollectionForInvoice ───────────────────────

func TestHasVerifiedGatewayCollectionForInvoice_ReturnsFalse_WhenNone(t *testing.T) {
	db := webhookTestDB(t)
	_, _, inv, _ := seedWebhookBase(t, db)

	if HasVerifiedGatewayCollectionForInvoice(db, inv.ID, inv.CompanyID) {
		t.Error("expected false when no payment_succeeded attempt exists")
	}
}

func TestHasVerifiedGatewayCollectionForInvoice_ReturnsFalse_WhenRedirected(t *testing.T) {
	db := webhookTestDB(t)
	_, _, inv, attempt := seedWebhookBase(t, db)
	// Attempt is seeded as redirected — should not trigger guard.
	if attempt.Status != models.HostedPaymentAttemptRedirected {
		t.Fatalf("seed precondition: expected redirected, got %q", attempt.Status)
	}
	if HasVerifiedGatewayCollectionForInvoice(db, inv.ID, attempt.CompanyID) {
		t.Error("expected false for redirected attempt")
	}
}

func TestHasVerifiedGatewayCollectionForInvoice_ReturnsTrue_WhenSucceeded(t *testing.T) {
	db := webhookTestDB(t)
	_, _, inv, attempt := seedWebhookBase(t, db)
	db.Model(&attempt).Update("status", models.HostedPaymentAttemptPaymentSucceeded)

	if !HasVerifiedGatewayCollectionForInvoice(db, inv.ID, attempt.CompanyID) {
		t.Error("expected true when payment_succeeded attempt exists")
	}
}

func TestHasVerifiedGatewayCollectionForInvoice_CompanyIsolation(t *testing.T) {
	db := webhookTestDB(t)
	_, _, inv, attempt := seedWebhookBase(t, db)
	db.Model(&attempt).Update("status", models.HostedPaymentAttemptPaymentSucceeded)

	// Different company must not see the other company's succeeded attempt.
	if HasVerifiedGatewayCollectionForInvoice(db, inv.ID, attempt.CompanyID+999) {
		t.Error("company isolation violated: guard returned true for wrong company")
	}
}

// ── Batch 10.1: strict gateway_account_id binding in ingestion ───────────────

// TestIngestStripeEvent_StrictGatewayBinding verifies that a checkout.session.completed
// event does NOT update an attempt that belongs to a different gateway_account_id,
// even if the provider_ref (session ID) matches.
func TestIngestStripeEvent_StrictGatewayBinding(t *testing.T) {
	db := webhookTestDB(t)
	_, gw, _, attempt := seedWebhookBase(t, db)

	// Create a second gateway account for the same company.
	otherGW := models.PaymentGatewayAccount{
		CompanyID:          attempt.CompanyID,
		ProviderType:       models.ProviderStripe,
		DisplayName:        "Other Stripe",
		ExternalAccountRef: "sk_other",
		WebhookSecret:      "whsec_other",
		IsActive:           true,
	}
	db.Create(&otherGW)

	// Send the event targeting otherGW.ID — the attempt belongs to gw.ID.
	payload := buildCheckoutCompletedPayload("evt_crossgw_001", attempt.ProviderRef, "pi_crossgw")
	if err := IngestStripeEvent(db, otherGW.ID, payload); err != nil {
		t.Fatalf("IngestStripeEvent failed: %v", err)
	}

	// The attempt linked to gw (not otherGW) must remain in its original status.
	var att models.HostedPaymentAttempt
	db.First(&att, attempt.ID)
	if att.Status == models.HostedPaymentAttemptPaymentSucceeded {
		t.Errorf("strict binding violated: attempt for gateway %d was updated by event for gateway %d",
			gw.ID, otherGW.ID)
	}
	if att.Status != models.HostedPaymentAttemptRedirected {
		t.Errorf("expected attempt to remain redirected, got %q", att.Status)
	}
}
