// 遵循project_guide.md
package services

// hosted_payment_service.go — Hosted invoice payment eligibility and attempt management.
//
// ─── Gateway readiness ───────────────────────────────────────────────────────
// selectReadyGateway is the single source of truth for "which gateway can
// actually process a hosted payment right now". Both EvaluateHostedPayability
// and CreateHostedPaymentIntent call it — never independently query gateways.
//
// Readiness rules (per provider type):
//   - Stripe:  is_active=true AND external_account_ref != '' (empty key = guaranteed failure)
//   - Manual:  is_active=true (no credential needed; useful for manual + testing)
//   - PayPal, Other: not supported for hosted pay; excluded from selection
//
// Selection priority among ready gateways: Stripe > Manual, then lowest ID.
// Deterministic: first Stripe by ascending ID, then first Manual by ascending ID.
//
// ─── CanPay five-gate rule (EvaluateHostedPayability) ───────────────────────
//   1. Invoice status is payable (issued / sent / overdue / partially_paid)
//   2. Balance due > 0 (prevents zero-amount checkout sessions)
//   3. Not a channel-origin invoice (ChannelOrderID != nil → collect via channel settlement)
//   4. Currency compatible: invoice currency matches company base currency (or no explicit currency)
//   5. selectReadyGateway succeeds — at least one credentialed, active, supported gateway exists
//
// ─── Idempotency / cancel-retry ─────────────────────────────────────────────
// CreateHostedPaymentIntent:
//   - In-flight check + row creation are wrapped in a DB transaction to serialize
//     concurrent POSTs for the same invoice. The provider call happens OUTSIDE the
//     transaction (network round-trips must not hold a DB connection open).
//
//   - Existing redirected attempt (status=redirected, RedirectURL != "") within the
//     idempotency window → return that attempt directly; caller redirects to its URL.
//     No new row; no second provider call.
//
//   - Existing created attempt (status=created) within window → return
//     ErrHostedPayIdempotency; provider call likely still in flight; caller sends
//     customer to /pay/pending.
//
//   - No in-flight attempt → create new attempt row (status=created), call provider,
//     update to redirected or failed.
//
// CancelActiveHostedPayAttempt:
//   - Called from the cancel return URL handler before showing the cancel page.
//   - Marks all in-flight attempts for the invoice as cancelled.
//   - After cancel, the next POST immediately sees no in-flight attempt and can
//     create a fresh one. This is the correct cancel → retry lifecycle.
//
// ─── Accounting truth ───────────────────────────────────────────────────────
// This service does NOT post journal entries, change invoice status, or record
// payments. HostedPaymentAttempt rows are trace records only. Accounting truth
// remains entirely in Invoice/JournalEntry as before.

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/gorm"
)

var (
	// ErrHostedPayNotEligible is returned by EvaluateHostedPayability when no gate passes.
	ErrHostedPayNotEligible = errors.New("invoice is not eligible for hosted payment")
	// ErrHostedPayIdempotency is returned when a 'created' in-flight attempt exists.
	// The caller should redirect to /pay/pending rather than retry immediately.
	ErrHostedPayIdempotency = errors.New("a payment attempt for this invoice is already being created; please wait")
	// ErrHostedPayProviderFailed is returned when the payment provider call fails.
	ErrHostedPayProviderFailed = errors.New("payment provider could not create a checkout session")
	// ErrNoReadyGateway is returned when no gateway is ready for hosted pay.
	ErrNoReadyGateway = errors.New("no ready payment gateway found for this company")
	// ErrVerifiedCollectionExists is returned when a verified gateway payment already
	// exists for this invoice. New pay attempts are blocked until the existing
	// payment is applied/settled or explicitly cancelled by an operator.
	ErrVerifiedCollectionExists = errors.New("a payment has already been confirmed by the payment provider for this invoice — apply the existing payment before initiating a new one")
)

// idempotencyWindow is the lookback period for duplicate attempt detection.
const idempotencyWindow = 30 * time.Minute

// HasVerifiedGatewayCollectionForInvoice returns true when verified (webhook-confirmed)
// but UNCONSUMED gateway collections already cover the invoice's remaining balance
// — i.e. no additional collection is needed.
//
// "Verified" means the status was set exclusively by webhook ingestion after
// HMAC signature verification — it cannot be set by browser return alone.
//
// Partial-payment logic: only the not-yet-applied portion of payment_succeeded
// attempts counts toward this guard. If prior succeeded collections have already
// been consumed by invoice application, they must not block a later installment.
//
// This is the shared guard used by EvaluateHostedPayability and
// CreateHostedPaymentIntent.
func HasVerifiedGatewayCollectionForInvoice(db *gorm.DB, invoiceID, companyID uint) bool {
	// Load current invoice balance.
	var inv models.Invoice
	if err := db.Select("id, amount, balance_due").
		Where("id = ? AND company_id = ?", invoiceID, companyID).
		First(&inv).Error; err != nil {
		return false
	}

	unconsumed := UnconsumedVerifiedCollectionAmount(db, inv, companyID)

	// Block only if the provider has already confirmed enough unapplied money to
	// fully cover the current remaining balance.
	return unconsumed.IsPositive() && unconsumed.GreaterThanOrEqual(inv.BalanceDue)
}

// UnconsumedVerifiedCollectionAmount returns the portion of verified
// (payment_succeeded) gateway collections that has NOT yet been applied to the
// invoice.  This represents "money the provider confirmed but the operator hasn't
// processed yet" — it should be subtracted from BalanceDue when computing the
// amount for a new checkout session or payment request.
//
// Formula:
//
//	unconsumed = max(0, SUM(payment_succeeded amounts) − (inv.Amount − inv.BalanceDue))
//
// Where (inv.Amount − inv.BalanceDue) is the total already applied to the invoice.
// If all succeeded amounts have been applied the result is zero.
func UnconsumedVerifiedCollectionAmount(db *gorm.DB, inv models.Invoice, companyID uint) decimal.Decimal {
	var row struct{ Total decimal.Decimal }
	db.Model(&models.HostedPaymentAttempt{}).
		Select("COALESCE(SUM(amount), 0) as total").
		Where("invoice_id = ? AND company_id = ? AND status = ?",
			inv.ID, companyID, models.HostedPaymentAttemptPaymentSucceeded).
		Scan(&row)

	alreadyApplied := inv.Amount.Sub(inv.BalanceDue)
	unconsumed := row.Total.Sub(alreadyApplied)
	if unconsumed.IsNegative() {
		return decimal.Zero
	}
	return unconsumed
}

// HostedPayabilityResult carries the outcome of EvaluateHostedPayability.
type HostedPayabilityResult struct {
	CanPay bool
	Reason string // human-readable explanation when CanPay is false
}

// selectReadyGateway returns the best payment gateway that is both active and
// ready to process a hosted payment for the given company.
//
// "Ready" means the gateway can be given a checkout amount right now without a
// guaranteed-to-fail provider call:
//   - Stripe:  is_active=true AND external_account_ref != ” (non-empty secret key)
//   - Manual:  is_active=true (no credential required)
//   - PayPal, Other: excluded — not supported in the current hosted pay implementation
//
// Selection priority: Stripe (lowest ID) > Manual (lowest ID).
// Returns ErrNoReadyGateway if no ready gateway exists.
func selectReadyGateway(db *gorm.DB, companyID uint) (*models.PaymentGatewayAccount, error) {
	var gateways []models.PaymentGatewayAccount
	if err := db.Where("company_id = ? AND is_active = true AND provider_type IN (?,?)",
		companyID, models.ProviderStripe, models.ProviderManual).
		Order("id ASC").
		Find(&gateways).Error; err != nil {
		return nil, fmt.Errorf("query gateways: %w", err)
	}

	// Apply per-provider readiness rules.
	var bestStripe, bestManual *models.PaymentGatewayAccount
	for i := range gateways {
		gw := &gateways[i]
		switch gw.ProviderType {
		case models.ProviderStripe:
			if gw.ExternalAccountRef != "" && bestStripe == nil {
				bestStripe = gw
			}
		case models.ProviderManual:
			if bestManual == nil {
				bestManual = gw
			}
		}
	}

	// Prefer Stripe over Manual.
	if bestStripe != nil {
		return bestStripe, nil
	}
	if bestManual != nil {
		return bestManual, nil
	}
	return nil, ErrNoReadyGateway
}

// EvaluateHostedPayability checks all five gates and returns whether the invoice
// is eligible for online payment via the hosted page.
//
// Read-only: no DB writes. Safe to call on every hosted page render.
func EvaluateHostedPayability(db *gorm.DB, inv models.Invoice, companyID uint) HostedPayabilityResult {
	// Gate 1: invoice status must be payable.
	if !IsInvoicePayable(inv.Status) {
		return HostedPayabilityResult{Reason: "invoice status is not payable"}
	}

	// Gate 2: balance due must be positive.
	visibility := BuildInvoicePaymentVisibility(inv)
	if !visibility.BalanceDue.IsPositive() {
		return HostedPayabilityResult{Reason: "balance due is zero or negative"}
	}

	// Gate 3: block channel-origin invoices.
	if inv.ChannelOrderID != nil {
		return HostedPayabilityResult{Reason: "channel-origin invoices cannot use the payment gateway"}
	}

	// Gate 4: currency must match company base currency (or invoice has no explicit currency).
	if inv.CurrencyCode != "" {
		var company models.Company
		if err := db.Select("base_currency_code").Where("id = ?", companyID).First(&company).Error; err == nil {
			if company.BaseCurrencyCode != "" && inv.CurrencyCode != company.BaseCurrencyCode {
				return HostedPayabilityResult{Reason: "foreign-currency invoices cannot use the payment gateway in this version"}
			}
		}
	}

	// Gate 5: a ready gateway must exist.
	// Uses selectReadyGateway — the unified readiness truth. An active gateway with
	// an empty Stripe key fails here rather than silently failing at the provider call.
	if _, err := selectReadyGateway(db, companyID); err != nil {
		return HostedPayabilityResult{Reason: "no ready payment gateway configured for this company"}
	}

	// Gate 6: block if a verified gateway collection already exists.
	// payment_succeeded can only be set by verified webhook ingestion; this
	// prevents a second checkout session being created against an already-paid invoice.
	if HasVerifiedGatewayCollectionForInvoice(db, inv.ID, companyID) {
		return HostedPayabilityResult{Reason: "a payment has already been confirmed by the payment provider for this invoice"}
	}

	return HostedPayabilityResult{CanPay: true}
}

// CreateHostedPaymentIntent creates or reuses a HostedPaymentAttempt and returns
// a URL the customer should be redirected to.
//
// Idempotency behaviour:
//   - Existing redirected attempt within the window → return it (reuse redirect URL)
//   - Existing created attempt within the window → ErrHostedPayIdempotency
//   - No in-flight attempt → create new row, call provider, return new attempt
//
// The gateway is selected by selectReadyGateway (same truth as EvaluateHostedPayability).
// publicBaseURL must be the canonical application origin (e.g. "https://app.example.com").
func CreateHostedPaymentIntent(
	db *gorm.DB,
	link *models.InvoiceHostedLink,
	inv models.Invoice,
	token string,
	publicBaseURL string,
) (*models.HostedPaymentAttempt, error) {
	// Pre-flight: block if a verified collection already exists for this invoice.
	// This prevents a second checkout session after a payment has been confirmed
	// by a webhook but the invoice has not yet been applied/settled by an operator.
	if HasVerifiedGatewayCollectionForInvoice(db, inv.ID, link.CompanyID) {
		return nil, ErrVerifiedCollectionExists
	}

	// Phase 1: inside a transaction — idempotency check + row creation.
	// Network calls (provider) happen outside the transaction.
	var attempt models.HostedPaymentAttempt
	var reused bool

	txErr := db.Transaction(func(tx *gorm.DB) error {
		cutoff := time.Now().Add(-idempotencyWindow)
		var existing models.HostedPaymentAttempt
		err := tx.Where(
			"invoice_id = ? AND company_id = ? AND status IN (?,?) AND created_at >= ?",
			inv.ID, link.CompanyID,
			models.HostedPaymentAttemptCreated,
			models.HostedPaymentAttemptRedirected,
			cutoff,
		).Order("created_at DESC").First(&existing).Error

		if err == nil {
			// In-flight attempt found.
			if existing.Status == models.HostedPaymentAttemptRedirected && existing.RedirectURL != "" {
				// Reuse: customer is redirected to the same checkout session URL.
				// No new row, no second provider call.
				attempt = existing
				reused = true
				return nil
			}
			// Status is "created": provider call is likely still in progress.
			// Tell the caller to send the customer to /pay/pending.
			return ErrHostedPayIdempotency
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("idempotency check: %w", err)
		}

		// No in-flight attempt. Select the ready gateway.
		gw, gwErr := selectReadyGateway(tx, link.CompanyID)
		if gwErr != nil {
			return fmt.Errorf("%w", ErrNoReadyGateway)
		}

		// Derive amount and currency from invoice (accounting truth, not caller input).
		visibility := BuildInvoicePaymentVisibility(inv)
		currency := inv.CurrencyCode
		if currency == "" {
			var co models.Company
			tx.Select("base_currency_code").Where("id = ?", link.CompanyID).First(&co)
			currency = co.BaseCurrencyCode
		}

		// Compute the amount for this checkout session.
		// Subtract any unconsumed verified collections so we don't double-collect
		// when a prior payment_succeeded attempt hasn't been applied yet.
		payableAmount := visibility.BalanceDue
		if unconsumed := UnconsumedVerifiedCollectionAmount(tx, inv, link.CompanyID); unconsumed.IsPositive() {
			payableAmount = payableAmount.Sub(unconsumed)
			if !payableAmount.IsPositive() {
				// Guard: should never reach here because HasVerifiedGatewayCollectionForInvoice
				// blocks when sum ≥ BalanceDue, but defend anyway.
				payableAmount = visibility.BalanceDue
			}
		}

		// Create the attempt row in 'created' state BEFORE calling the provider.
		// This is the trace anchor: if the process crashes after this line but
		// before the provider responds, the row exists in 'created' state and
		// blocks duplicate submissions during the idempotency window.
		newAttempt := models.HostedPaymentAttempt{
			CompanyID:        link.CompanyID,
			InvoiceID:        inv.ID,
			HostedLinkID:     link.ID,
			GatewayAccountID: gw.ID,
			ProviderType:     gw.ProviderType,
			Amount:           payableAmount,
			CurrencyCode:     currency,
			Status:           models.HostedPaymentAttemptCreated,
		}
		if err := tx.Create(&newAttempt).Error; err != nil {
			return fmt.Errorf("create hosted payment attempt: %w", err)
		}
		attempt = newAttempt
		return nil
	})

	if txErr != nil {
		return nil, txErr
	}

	// Reused existing redirected attempt — no provider call needed.
	if reused {
		return &attempt, nil
	}

	// Phase 2: outside the transaction — call the provider.
	// We have a 'created' row; update it to 'redirected' or 'failed' based on outcome.
	gw := &models.PaymentGatewayAccount{ID: attempt.GatewayAccountID, ProviderType: attempt.ProviderType}
	// Load full gateway for the provider (needs ExternalAccountRef for Stripe).
	if err := db.First(gw, attempt.GatewayAccountID).Error; err != nil {
		db.Model(&attempt).Update("status", models.HostedPaymentAttemptFailed)
		return nil, fmt.Errorf("reload gateway: %w", err)
	}

	provider := GetPaymentProvider(*gw)
	result, provErr := provider.CreateCheckoutSession(CheckoutSessionInput{
		Amount:        attempt.Amount.StringFixed(2),
		CurrencyCode:  attempt.CurrencyCode,
		Token:         token,
		PublicBaseURL: publicBaseURL,
		InvoiceRef:    inv.InvoiceNumber,
	})
	if provErr != nil {
		// Leave a failed trace — do not delete the row.
		db.Model(&attempt).Update("status", models.HostedPaymentAttemptFailed)
		return nil, fmt.Errorf("%w: %s", ErrHostedPayProviderFailed, provErr.Error())
	}

	// Update attempt to redirected with provider details.
	db.Model(&attempt).Updates(map[string]any{
		"provider_ref": result.ProviderRef,
		"redirect_url": result.RedirectURL,
		"status":       models.HostedPaymentAttemptRedirected,
	})
	attempt.ProviderRef = result.ProviderRef
	attempt.RedirectURL = result.RedirectURL
	attempt.Status = models.HostedPaymentAttemptRedirected

	return &attempt, nil
}

// LatestAttemptForInvoice returns the most recent HostedPaymentAttempt for the
// invoice (by ID descending), or nil if no attempt exists.
// Used by the payment status page to show provider-side truth without trusting
// browser query parameters.
// companyID is required for company isolation.
func LatestAttemptForInvoice(db *gorm.DB, invoiceID, companyID uint) *models.HostedPaymentAttempt {
	var attempt models.HostedPaymentAttempt
	if err := db.Where("invoice_id = ? AND company_id = ?", invoiceID, companyID).
		Order("id DESC").
		First(&attempt).Error; err != nil {
		return nil
	}
	return &attempt
}

// CancelActiveHostedPayAttempt marks all in-flight attempts for the given invoice
// (within the idempotency window) as cancelled.
//
// Called from the cancel return URL handler so that the customer can immediately
// retry without waiting 30 minutes for the idempotency window to expire.
// companyID is included for company isolation — cannot cancel another company's attempts.
// Not finding any in-flight attempt is not an error.
func CancelActiveHostedPayAttempt(db *gorm.DB, invoiceID uint, companyID uint) error {
	cutoff := time.Now().Add(-idempotencyWindow)
	return db.Model(&models.HostedPaymentAttempt{}).
		Where(
			"invoice_id = ? AND company_id = ? AND status IN (?,?) AND created_at >= ?",
			invoiceID, companyID,
			models.HostedPaymentAttemptCreated,
			models.HostedPaymentAttemptRedirected,
			cutoff,
		).
		Update("status", models.HostedPaymentAttemptCancelled).Error
}
