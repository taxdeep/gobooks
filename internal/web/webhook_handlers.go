// 遵循project_guide.md
package web

// webhook_handlers.go — Inbound webhook handlers for payment providers.
//
// Security model:
//   - No session or auth middleware: authentication is the provider's HMAC signature.
//   - Raw body is read before any JSON parsing so the signature can be verified
//     against the exact bytes Stripe signed.
//   - Any signature verification failure returns 400 immediately.
//   - All other errors return 500 so the provider retries delivery.
//   - Idempotent: duplicate delivery of the same event returns 200.
//
// Routes (registered in routes.go, no auth middleware):
//   POST /webhooks/stripe/:gateway_id
//
// The :gateway_id in the URL is the internal PaymentGatewayAccount.ID.
// Including it in the URL lets companies register a unique endpoint per gateway
// account, which is necessary when each account has a distinct webhook signing secret.

import (
	"strconv"

	"github.com/gofiber/fiber/v2"
	"balanciz/internal/logging"
	"balanciz/internal/services"
)

// handleStripeWebhook processes incoming Stripe webhook events.
// POST /webhooks/stripe/:gateway_id
//
// Authentication: Stripe-Signature header HMAC-SHA256 (no session/auth cookie).
// The handler reads the raw request body before any parsing (Fiber's body parser
// must NOT run before this handler — raw body is consumed once).
func (s *Server) handleStripeWebhook(c *fiber.Ctx) error {
	// 1. Parse and validate :gateway_id.
	gatewayIDStr := c.Params("gateway_id")
	gatewayID64, err := strconv.ParseUint(gatewayIDStr, 10, 32)
	if err != nil || gatewayID64 == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid gateway_id",
		})
	}
	gatewayID := uint(gatewayID64)

	// 2. Load the gateway account to get the webhook signing secret.
	//    We do NOT return 404 if not found — return 400 to avoid oracle attacks on IDs.
	var gw struct {
		WebhookSecret string
		CompanyID     uint
		ProviderType  string
		IsActive      bool
	}
	if err := s.DB.Table("payment_gateway_accounts").
		Select("webhook_secret, company_id, provider_type, is_active").
		Where("id = ?", gatewayID).
		Scan(&gw).Error; err != nil || gw.CompanyID == 0 {
		logging.L().Warn("stripe webhook: gateway not found", "gateway_id", gatewayID)
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "gateway not found",
		})
	}
	if !gw.IsActive {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "gateway is not active",
		})
	}
	// Strict provider-type guard: this endpoint is Stripe-only.
	// A non-Stripe gateway should never have its webhook URL pointed here;
	// returning 400 prevents accidental cross-provider event routing.
	if gw.ProviderType != "stripe" {
		logging.L().Warn("stripe webhook: gateway is not a Stripe account", "gateway_id", gatewayID, "provider_type", gw.ProviderType)
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "gateway is not a Stripe account",
		})
	}

	// 3. Read raw body — must happen before any body parsing.
	rawBody := c.Body()

	// 4. Verify Stripe signature.
	sigHeader := c.Get("Stripe-Signature")
	if err := services.VerifyStripeSignature(rawBody, sigHeader, gw.WebhookSecret); err != nil {
		logging.L().Warn("stripe webhook: signature verification failed",
			"gateway_id", gatewayID, "error", err.Error())
		// Return 400 so the provider does NOT retry (a signature failure is not transient).
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "signature verification failed",
		})
	}

	// 5. Ingest the event (idempotent, all writes in one transaction).
	if err := services.IngestStripeEvent(s.DB, gatewayID, rawBody); err != nil {
		logging.L().Error("stripe webhook: ingestion failed",
			"gateway_id", gatewayID, "error", err.Error())
		// Return 500 so Stripe retries delivery — the event was not stored.
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "event processing failed",
		})
	}

	// 6. Always return 200 on success or idempotent re-delivery.
	return c.Status(fiber.StatusOK).JSON(fiber.Map{"received": true})
}
