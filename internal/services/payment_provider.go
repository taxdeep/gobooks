// 遵循project_guide.md
package services

// payment_provider.go — Provider-agnostic payment abstraction layer.
//
// Design:
//   - PaymentProvider interface exposes one operation: CreateCheckoutSession.
//   - ManualProvider: returns a pending confirmation page URL; no external call.
//   - StripeProvider: real HTTPS POST to Stripe Checkout API using ExternalAccountRef
//     as the Stripe secret key.
//   - GetPaymentProvider is the factory: switches on gw.ProviderType.
//   - No other code should reference provider implementations directly; always use
//     GetPaymentProvider so future providers require only a new case in the factory.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gobooks/internal/models"
)

// CheckoutSessionInput is the provider-agnostic input for creating a checkout session.
type CheckoutSessionInput struct {
	// Amount in the invoice's currency, formatted as a decimal string (e.g. "123.45").
	Amount      string
	CurrencyCode string

	// Token is the hosted invoice token, used to build return URLs.
	Token string

	// PublicBaseURL is the scheme+host used to build return URLs.
	// Example: "https://app.example.com"
	PublicBaseURL string

	// SuccessURL and CancelURL override the auto-built return URLs when non-empty.
	SuccessURL string
	CancelURL  string

	// InvoiceRef is a human-readable invoice number for the checkout description.
	InvoiceRef string
}

// CheckoutSessionResult is returned by a successful CreateCheckoutSession call.
type CheckoutSessionResult struct {
	// ProviderRef is the provider's session/charge identifier (Stripe session ID, etc.)
	ProviderRef string
	// RedirectURL is the URL the customer should visit to complete payment.
	RedirectURL string
}

// PaymentProvider is the interface satisfied by each payment provider implementation.
type PaymentProvider interface {
	CreateCheckoutSession(input CheckoutSessionInput) (CheckoutSessionResult, error)
}

// ── ManualProvider ───────────────────────────────────────────────────────────

// ManualProvider is a no-op provider for companies that have not configured an
// online gateway. It returns the hosted pending-confirmation page as the redirect URL
// without making any external call. Useful for testing and manual payment flows.
type ManualProvider struct{}

func (ManualProvider) CreateCheckoutSession(input CheckoutSessionInput) (CheckoutSessionResult, error) {
	ref := fmt.Sprintf("manual-%d", time.Now().UnixNano())
	redirect := input.SuccessURL
	if redirect == "" {
		redirect = input.PublicBaseURL + "/i/" + input.Token + "/pay/pending"
	}
	return CheckoutSessionResult{ProviderRef: ref, RedirectURL: redirect}, nil
}

// ── StripeProvider ───────────────────────────────────────────────────────────

// StripeProvider creates a real Stripe Checkout session via the Stripe API.
// ExternalAccountRef on the gateway account must be a Stripe secret key
// (sk_test_... or sk_live_...).
//
// Scope: Batch 7 wire-up only — no webhook handling, no refunds, no captures.
// The provider creates the session and returns the redirect URL. Completion
// tracking is deferred to the future webhook / payment application layer.
type StripeProvider struct {
	SecretKey  string
	HTTPClient *http.Client
}

func (p StripeProvider) CreateCheckoutSession(input CheckoutSessionInput) (CheckoutSessionResult, error) {
	if p.SecretKey == "" {
		return CheckoutSessionResult{}, errors.New("stripe: no secret key configured")
	}

	client := p.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}

	successURL := input.SuccessURL
	cancelURL := input.CancelURL
	if successURL == "" {
		successURL = input.PublicBaseURL + "/i/" + input.Token + "/pay/pending"
	}
	if cancelURL == "" {
		cancelURL = input.PublicBaseURL + "/i/" + input.Token + "/pay/cancel"
	}

	amountInt, err := stripeAmountCents(input.Amount)
	if err != nil {
		return CheckoutSessionResult{}, fmt.Errorf("stripe: amount conversion: %w", err)
	}

	data := url.Values{}
	data.Set("payment_method_types[]", "card")
	data.Set("line_items[0][price_data][currency]", strings.ToLower(input.CurrencyCode))
	data.Set("line_items[0][price_data][unit_amount]", fmt.Sprintf("%d", amountInt))
	data.Set("line_items[0][price_data][product_data][name]", fmt.Sprintf("Invoice %s", input.InvoiceRef))
	data.Set("line_items[0][quantity]", "1")
	data.Set("mode", "payment")
	data.Set("success_url", successURL)
	data.Set("cancel_url", cancelURL)

	req, err := http.NewRequest(http.MethodPost,
		"https://api.stripe.com/v1/checkout/sessions",
		strings.NewReader(data.Encode()))
	if err != nil {
		return CheckoutSessionResult{}, fmt.Errorf("stripe: build request: %w", err)
	}
	req.SetBasicAuth(p.SecretKey, "")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return CheckoutSessionResult{}, fmt.Errorf("stripe: http request: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		ID    string `json:"id"`
		URL   string `json:"url"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return CheckoutSessionResult{}, fmt.Errorf("stripe: decode response: %w", err)
	}
	if result.Error != nil {
		return CheckoutSessionResult{}, fmt.Errorf("stripe: api error: %s", result.Error.Message)
	}
	if result.ID == "" || result.URL == "" {
		return CheckoutSessionResult{}, errors.New("stripe: empty session ID or URL in response")
	}

	return CheckoutSessionResult{ProviderRef: result.ID, RedirectURL: result.URL}, nil
}

// stripeAmountCents converts a decimal amount string like "123.45" to Stripe's
// integer unit (cents for most currencies, i.e. assumes 2 decimal places).
// Negative amounts are rejected because Stripe does not accept them for charges.
func stripeAmountCents(amount string) (int64, error) {
	if strings.HasPrefix(amount, "-") {
		return 0, fmt.Errorf("negative amount not allowed: %q", amount)
	}
	parts := strings.SplitN(amount, ".", 2)
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid amount %q: %w", amount, err)
	}
	var cents int64
	if len(parts) == 2 {
		frac := parts[1]
		if len(frac) > 2 {
			frac = frac[:2]
		} else if len(frac) == 1 {
			frac += "0"
		}
		cents, err = strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid amount cents %q: %w", amount, err)
		}
	}
	return whole*100 + cents, nil
}

// ── Factory ──────────────────────────────────────────────────────────────────

// GetPaymentProvider returns the PaymentProvider implementation for the given gateway.
// Falls back to ManualProvider for unrecognised or manual provider types.
func GetPaymentProvider(gw models.PaymentGatewayAccount) PaymentProvider {
	switch gw.ProviderType {
	case models.ProviderStripe:
		return StripeProvider{SecretKey: gw.ExternalAccountRef}
	default:
		return ManualProvider{}
	}
}
