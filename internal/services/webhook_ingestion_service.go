// 遵循project_guide.md
package services

// webhook_ingestion_service.go — Provider webhook verification and event ingestion.
//
// Design principles:
//   1. Verified provider data is authoritative.
//      Signature verification is mandatory; unverifiable events are always rejected.
//   2. Idempotency via WebhookEvent deduplication.
//      All writes for a single event happen in one DB transaction. The WebhookEvent
//      insert uses a unique constraint on external_event_id; duplicate delivery from
//      the provider fails the insert with a unique-violation, which the ingestion
//      layer treats as "already processed" → return nil (safe re-delivery).
//   3. No accounting mutation.
//      This layer only transitions HostedPaymentAttempt status and creates a
//      PaymentTransaction record (plus a PaymentRequest to link it to the invoice).
//      Journal entry posting and invoice application remain explicit operator actions
//      via the existing posting / application pipeline.
//   4. No scope beyond Batch 10.
//      Refunds, payouts, disputes, and captures are explicitly excluded.
//
// Stripe event coverage (Batch 10 scope):
//   checkout.session.completed  → attempt: payment_succeeded; PaymentRequest+Transaction created
//   checkout.session.expired    → attempt: cancelled (unblocks retry)
//   <all other types>           → silently accepted (200) but not processed; logged
//
// Signature verification:
//   Stripe uses HMAC-SHA256 over "<timestamp>.<rawBody>" delivered in the
//   Stripe-Signature header as "t=<ts>,v1=<hex>,…".
//   Timestamps older than 5 minutes are rejected (replay protection).

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"balanciz/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Sentinel errors returned by the ingestion layer.
var (
	// ErrWebhookSignatureMissing is returned when the Stripe-Signature header is absent.
	ErrWebhookSignatureMissing = errors.New("webhook: Stripe-Signature header is missing")
	// ErrWebhookSignatureInvalid is returned when signature verification fails.
	ErrWebhookSignatureInvalid = errors.New("webhook: signature verification failed")
	// ErrWebhookTimestampStale is returned when the timestamp is outside the replay window.
	ErrWebhookTimestampStale = errors.New("webhook: timestamp is outside the 5-minute replay window")
	// ErrWebhookSignatureNoSecret is returned when the gateway has no webhook secret configured.
	ErrWebhookSignatureNoSecret = errors.New("webhook: gateway has no webhook secret configured")
)

// stripeReplayWindow is the maximum allowed age of a Stripe webhook timestamp.
const stripeReplayWindow = 5 * time.Minute

// VerifyStripeSignature verifies the Stripe-Signature header for the given raw payload.
//
// Stripe signature format: "t=<unix_timestamp>,v1=<hmac_hex>[,v0=<older_hmac>]"
// The signed string is: "<timestamp>.<rawPayload>"
// HMAC algorithm: SHA-256, key is the webhook endpoint's signing secret (whsec_...).
//
// Returns:
//   - nil on success
//   - ErrWebhookSignatureMissing if sigHeader is empty
//   - ErrWebhookSignatureNoSecret if secret is empty
//   - ErrWebhookTimestampStale if timestamp is outside the replay window
//   - ErrWebhookSignatureInvalid if no v1 signature matches
func VerifyStripeSignature(payload []byte, sigHeader string, secret string) error {
	if sigHeader == "" {
		return ErrWebhookSignatureMissing
	}
	if secret == "" {
		return ErrWebhookSignatureNoSecret
	}

	// Parse header: "t=1234567890,v1=abc123,v1=def456"
	var ts int64
	var v1Sigs []string
	for _, part := range strings.Split(sigHeader, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "t=") {
			var err error
			ts, err = strconv.ParseInt(part[2:], 10, 64)
			if err != nil {
				return fmt.Errorf("%w: malformed timestamp %q", ErrWebhookSignatureInvalid, part[2:])
			}
		} else if strings.HasPrefix(part, "v1=") {
			v1Sigs = append(v1Sigs, part[3:])
		}
	}
	if ts == 0 {
		return fmt.Errorf("%w: missing timestamp in signature header", ErrWebhookSignatureInvalid)
	}
	if len(v1Sigs) == 0 {
		return fmt.Errorf("%w: no v1 signature in header", ErrWebhookSignatureInvalid)
	}

	// Replay protection: reject timestamps outside the window.
	age := time.Since(time.Unix(ts, 0))
	if age > stripeReplayWindow || age < -stripeReplayWindow {
		return ErrWebhookTimestampStale
	}

	// Compute expected HMAC-SHA256.
	signedStr := fmt.Sprintf("%d.%s", ts, payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedStr))
	expected := hex.EncodeToString(mac.Sum(nil))

	// Accept if any v1 signature matches.
	for _, sig := range v1Sigs {
		if hmac.Equal([]byte(expected), []byte(sig)) {
			return nil
		}
	}
	return ErrWebhookSignatureInvalid
}

// stripeEvent is the top-level shape of a Stripe webhook event.
type stripeEvent struct {
	ID   string          `json:"id"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// stripeCheckoutSessionObj is the data.object shape for checkout.session events.
type stripeCheckoutSessionObj struct {
	ID            string `json:"id"`             // cs_... session ID
	PaymentIntent string `json:"payment_intent"` // pi_... (may be string or object)
	AmountTotal   int64  `json:"amount_total"`   // cents
	Currency      string `json:"currency"`
	PaymentStatus string `json:"payment_status"` // "paid", "unpaid", etc.
}

// stripeDataWrapper wraps data.object for deserialization.
type stripeDataWrapper struct {
	Object json.RawMessage `json:"object"`
}

// IngestStripeEvent processes one verified Stripe webhook event.
//
// Idempotency: if ExternalEventID already exists in webhook_events, returns nil immediately.
// All writes are atomic: WebhookEvent insert + status transitions are in one transaction.
// Unknown event types are silently accepted (log + return nil).
//
// This function must NOT be called unless the webhook signature has already been verified.
func IngestStripeEvent(db *gorm.DB, gatewayAccountID uint, rawPayload []byte) error {
	// Parse event envelope.
	var evt stripeEvent
	if err := json.Unmarshal(rawPayload, &evt); err != nil {
		return fmt.Errorf("stripe webhook: malformed event JSON: %w", err)
	}
	if evt.ID == "" || evt.Type == "" {
		return fmt.Errorf("stripe webhook: event missing id or type")
	}

	// Delegate to the appropriate handler.
	switch evt.Type {
	case "checkout.session.completed":
		if err := ingestCheckoutSessionCompleted(db, gatewayAccountID, evt, rawPayload); err != nil {
			return err
		}
		// Attempt auto-settlement after the ingestion transaction commits.
		// Best-effort: ineligibility or missing config is logged but never blocks
		// the webhook response. The PaymentTransaction remains unposted/unapplied
		// and is visible to operators for manual post+apply.
		if session, parseErr := parseCheckoutSession(evt); parseErr == nil {
			TryAutoSettleAfterIngestion(db, gatewayAccountID, session.ID)
		}
		return nil
	case "checkout.session.expired":
		return ingestCheckoutSessionExpired(db, gatewayAccountID, evt, rawPayload)
	default:
		// Unknown type: store for traceability but take no payment-side action.
		slog.Info("stripe webhook: unhandled event type — stored without processing",
			"event_id", evt.ID, "event_type", evt.Type, "gateway_account_id", gatewayAccountID)
		return storeWebhookEvent(db, gatewayAccountID, models.ProviderStripe, evt.ID, evt.Type, rawPayload)
	}
}

// ingestCheckoutSessionCompleted processes "checkout.session.completed".
//
// On a verified payment (payment_status == "paid"):
//   - Marks the matching HostedPaymentAttempt as payment_succeeded.
//   - Creates a PaymentRequest (status=paid) linking gateway + invoice.
//   - Creates a PaymentTransaction (charge, completed) for operator post/apply later.
//
// On session.completed but payment_status != "paid" (e.g. "unpaid" for setup mode):
//   - Stores WebhookEvent for traceability but takes no status transition.
//
// No journal entries are posted. No invoice balance is reduced.
// Those remain explicit operator actions via the existing post/apply pipeline.
func ingestCheckoutSessionCompleted(db *gorm.DB, gatewayAccountID uint, evt stripeEvent, rawPayload []byte) error {
	session, err := parseCheckoutSession(evt)
	if err != nil {
		return fmt.Errorf("stripe webhook checkout.session.completed: %w", err)
	}

	return db.Transaction(func(tx *gorm.DB) error {
		// Deduplication: insert WebhookEvent first. Unique constraint violation = already processed.
		if dup, _ := storeWebhookEventTx(tx, gatewayAccountID, models.ProviderStripe,
			evt.ID, evt.Type, rawPayload); dup {
			return nil // already processed — idempotent
		}

		if session.PaymentStatus != "paid" {
			// Non-payment completed session (e.g. setup). No status transition needed.
			slog.Info("stripe webhook: checkout.session.completed with non-paid status — no action",
				"session_id", session.ID, "payment_status", session.PaymentStatus)
			return nil
		}

		// Find the matching hosted payment attempt by provider ref (Stripe session ID)
		// and exact gateway_account_id — strict binding prevents cross-gateway confusion.
		var attempt models.HostedPaymentAttempt
		if err := tx.Where("provider_ref = ? AND gateway_account_id = ?",
			session.ID, gatewayAccountID).First(&attempt).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// No matching attempt: session may be from a test or from before this system existed.
				slog.Warn("stripe webhook: no HostedPaymentAttempt found for session",
					"session_id", session.ID, "gateway_account_id", gatewayAccountID)
				return nil // not an error — just unknown session
			}
			return fmt.Errorf("lookup hosted attempt by provider ref: %w", err)
		}

		// Guard: only transition from terminal-for-webhook-update statuses.
		// If already payment_succeeded (from a prior delivery) we skip status update
		// but the dedup guard above should have caught it first.
		if attempt.Status == models.HostedPaymentAttemptPaymentSucceeded {
			return nil
		}

		// Mark attempt as payment_succeeded.
		if err := tx.Model(&attempt).Update("status", models.HostedPaymentAttemptPaymentSucceeded).Error; err != nil {
			return fmt.Errorf("update attempt status to payment_succeeded: %w", err)
		}

		// Create PaymentRequest in "paid" status to link the gateway transaction to the invoice.
		currency := strings.ToUpper(session.Currency)
		if currency == "" {
			currency = attempt.CurrencyCode
		}
		pr := models.PaymentRequest{
			CompanyID:        attempt.CompanyID,
			GatewayAccountID: gatewayAccountID,
			InvoiceID:        &attempt.InvoiceID,
			Amount:           attempt.Amount,
			CurrencyCode:     currency,
			Status:           models.PaymentRequestPaid,
			Description:      fmt.Sprintf("Stripe checkout payment — session %s", session.ID),
			ExternalRef:      session.ID,
		}
		if err := tx.Create(&pr).Error; err != nil {
			return fmt.Errorf("create payment request from webhook: %w", err)
		}

		// Create PaymentTransaction (charge type, completed) for post/apply pipeline.
		// ExternalTxnRef = Stripe payment_intent ID for traceability.
		extRef := session.PaymentIntent
		if extRef == "" {
			extRef = session.ID // fallback to session ID if payment intent not populated
		}
		txnRaw, _ := json.Marshal(map[string]any{
			"source":         "stripe_webhook",
			"event_id":       evt.ID,
			"session_id":     session.ID,
			"payment_intent": session.PaymentIntent,
		})
		payTxn := models.PaymentTransaction{
			CompanyID:        attempt.CompanyID,
			GatewayAccountID: gatewayAccountID,
			PaymentRequestID: &pr.ID,
			TransactionType:  models.TxnTypeCharge,
			Amount:           attempt.Amount,
			CurrencyCode:     currency,
			Status:           "completed",
			ExternalTxnRef:   extRef,
			RawPayload:       datatypes.JSON(txnRaw),
		}
		if err := tx.Create(&payTxn).Error; err != nil {
			return fmt.Errorf("create payment transaction from webhook: %w", err)
		}

		slog.Info("stripe webhook: checkout.session.completed ingested",
			"event_id", evt.ID, "session_id", session.ID,
			"attempt_id", attempt.ID, "invoice_id", attempt.InvoiceID,
			"payment_request_id", pr.ID, "payment_txn_id", payTxn.ID)
		return nil
	})
}

// ingestCheckoutSessionExpired processes "checkout.session.expired".
// Marks the matching attempt as cancelled so the customer can retry immediately.
func ingestCheckoutSessionExpired(db *gorm.DB, gatewayAccountID uint, evt stripeEvent, rawPayload []byte) error {
	session, err := parseCheckoutSession(evt)
	if err != nil {
		return fmt.Errorf("stripe webhook checkout.session.expired: %w", err)
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if dup, _ := storeWebhookEventTx(tx, gatewayAccountID, models.ProviderStripe,
			evt.ID, evt.Type, rawPayload); dup {
			return nil
		}

		// Find and mark the matching attempt as cancelled — strict gateway_account_id binding.
		result := tx.Model(&models.HostedPaymentAttempt{}).
			Where("provider_ref = ? AND gateway_account_id = ?",
				session.ID, gatewayAccountID).
			Where("status NOT IN (?,?)",
				models.HostedPaymentAttemptPaymentSucceeded,
				models.HostedPaymentAttemptCancelled).
			Update("status", models.HostedPaymentAttemptCancelled)
		if result.Error != nil {
			return fmt.Errorf("mark attempt cancelled on session expired: %w", result.Error)
		}

		slog.Info("stripe webhook: checkout.session.expired ingested",
			"event_id", evt.ID, "session_id", session.ID, "rows_updated", result.RowsAffected)
		return nil
	})
}

// storeWebhookEvent stores a WebhookEvent outside a transaction.
// Returns nil if the event already exists (idempotent).
func storeWebhookEvent(db *gorm.DB, gatewayAccountID uint, providerType models.PaymentProviderType,
	eventID, eventType string, rawPayload []byte) error {
	_, err := storeWebhookEventTx(db, gatewayAccountID, providerType, eventID, eventType, rawPayload)
	return err
}

// storeWebhookEventTx inserts a WebhookEvent within the provided tx (or db).
// Returns (true, nil) if the event already exists (duplicate), (false, nil) on success,
// or (false, err) on a non-duplicate error.
func storeWebhookEventTx(db *gorm.DB, gatewayAccountID uint, providerType models.PaymentProviderType,
	eventID, eventType string, rawPayload []byte) (duplicate bool, err error) {
	now := time.Now()
	we := models.WebhookEvent{
		GatewayAccountID: gatewayAccountID,
		ProviderType:     providerType,
		ExternalEventID:  eventID,
		EventType:        eventType,
		RawPayload:       datatypes.JSON(rawPayload),
		ProcessedAt:      now,
		CreatedAt:        now,
	}
	if createErr := db.Create(&we).Error; createErr != nil {
		// Unique constraint violation = duplicate event = already processed.
		if strings.Contains(createErr.Error(), "UNIQUE") ||
			strings.Contains(createErr.Error(), "unique") ||
			strings.Contains(createErr.Error(), "duplicate") {
			return true, nil
		}
		return false, fmt.Errorf("store webhook event: %w", createErr)
	}
	return false, nil
}

// parseCheckoutSession extracts the session object from a Stripe checkout.session event.
func parseCheckoutSession(evt stripeEvent) (stripeCheckoutSessionObj, error) {
	var wrapper stripeDataWrapper
	if err := json.Unmarshal(evt.Data, &wrapper); err != nil {
		return stripeCheckoutSessionObj{}, fmt.Errorf("parse event data: %w", err)
	}
	var session stripeCheckoutSessionObj
	if err := json.Unmarshal(wrapper.Object, &session); err != nil {
		return stripeCheckoutSessionObj{}, fmt.Errorf("parse session object: %w", err)
	}
	if session.ID == "" {
		return stripeCheckoutSessionObj{}, fmt.Errorf("session object missing id")
	}
	return session, nil
}
