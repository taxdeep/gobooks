// 遵循project_guide.md
package web

import (
	"errors"

	"github.com/gofiber/fiber/v2"
	"balanciz/internal/logging"
	"balanciz/internal/models"
	"balanciz/internal/services"
)

// handleHostedPay initiates a payment session for a hosted invoice.
// POST /i/:token/pay
//
// Security: token validated before any state change. All non-retryable failures
// return 410 Gone so that no information about invoice / company existence leaks.
//
// Canonical public base URL:
//   The return URLs sent to the payment provider (success_url / cancel_url) are
//   built from s.Cfg.PublicBaseURL, which must be set via APP_PUBLIC_URL in
//   production. If unset, the handler falls back to the request host (logged as
//   WARN so operators notice). Using a canonical URL prevents return URL
//   instability behind reverse proxies or when accessed via multiple hostnames.
//
// Idempotency:
//   CreateHostedPaymentIntent handles duplicates inside a transaction.
//   If a redirected attempt exists with a valid URL → reuse that URL (no second
//   provider call). If a created attempt exists → send to /pay/pending.
//   The handler never needs to distinguish reused vs new: it redirects to
//   attempt.RedirectURL in both success cases.
//
// POST is intentional: pay intent creation is state-changing.
func (s *Server) handleHostedPay(c *fiber.Ctx) error {
	token := c.Params("token")

	link, err := services.ValidateHostedToken(s.DB, token)
	if err != nil {
		return sendHostedErrorPage(c)
	}

	invoice, err := loadInvoiceForRender(s.DB, link.CompanyID, link.InvoiceID)
	if err != nil {
		logging.L().Warn("hosted pay: invoice load failed",
			"link_id", link.ID, "invoice_id", link.InvoiceID, "error", err.Error())
		return sendHostedErrorPage(c)
	}

	// Five-gate eligibility check (uses selectReadyGateway internally).
	eligibility := services.EvaluateHostedPayability(s.DB, *invoice, link.CompanyID)
	if !eligibility.CanPay {
		logging.L().Warn("hosted pay: not eligible",
			"link_id", link.ID, "invoice_id", invoice.ID, "reason", eligibility.Reason)
		return sendHostedErrorPage(c)
	}

	// Canonical public base URL: prefer configured value; fall back to request host.
	publicBaseURL := s.Cfg.PublicBaseURL
	if publicBaseURL == "" {
		publicBaseURL = c.Protocol() + "://" + c.Hostname()
		logging.L().Warn("hosted pay: APP_PUBLIC_URL not configured, using request host — set APP_PUBLIC_URL in production",
			"fallback_url", publicBaseURL)
	}

	attempt, err := services.CreateHostedPaymentIntent(
		s.DB, link, *invoice, token, publicBaseURL,
	)
	if err != nil {
		if errors.Is(err, services.ErrHostedPayIdempotency) {
			// A 'created' in-flight attempt exists (provider call may still be running).
			// Send the customer to the pending page rather than creating a duplicate.
			return c.Redirect("/i/"+token+"/pay/pending", fiber.StatusSeeOther)
		}
		logging.L().Warn("hosted pay: create intent failed",
			"link_id", link.ID, "invoice_id", invoice.ID, "error", err.Error())
		return sendHostedErrorPage(c)
	}

	// Redirect to the provider checkout URL.
	// Works for both new attempts and reused existing attempts —
	// in both cases attempt.RedirectURL is the correct destination.
	return c.Redirect(attempt.RedirectURL, fiber.StatusSeeOther)
}

// handleHostedPayPending is the Stripe success_url return target and also the
// generic "awaiting confirmation" page for other providers.
// GET /i/:token/pay/pending
//
// Status-awareness: the handler looks up the most recent HostedPaymentAttempt for
// this invoice from the DB. If the webhook has already confirmed payment (status
// payment_succeeded), the page shows "Payment confirmed". Otherwise it shows the
// provisional "awaiting confirmation" message.
//
// This is safe: the status can only be payment_succeeded if the webhook ingestion
// service has verified and processed a genuine provider event. Browser return alone
// does not set this status.
func (s *Server) handleHostedPayPending(c *fiber.Ctx) error {
	token := c.Params("token")
	link, err := services.ValidateHostedToken(s.DB, token)
	if err != nil {
		return sendHostedErrorPage(c)
	}

	// Look up the most recent attempt to determine confirmed payment status.
	// Read from DB — not from query parameters — so browser manipulation is irrelevant.
	attempt := services.LatestAttemptForInvoice(s.DB, link.InvoiceID, link.CompanyID)

	c.Set("Cache-Control", "no-store")
	c.Set("Content-Type", "text/html; charset=utf-8")
	return c.SendString(renderHostedPayStatusPage(attempt, token))
}

// handleHostedPayCancel is shown when the customer clicks cancel on the provider
// checkout page and is returned to the application.
// GET /i/:token/pay/cancel
//
// Cancel → retry lifecycle:
//   Before showing the cancel page, all in-flight attempts (created/redirected)
//   for this invoice are marked cancelled. This unblocks immediate retry —
//   the next POST to /i/:token/pay finds no in-flight attempt and creates a new one.
func (s *Server) handleHostedPayCancel(c *fiber.Ctx) error {
	token := c.Params("token")
	link, err := services.ValidateHostedToken(s.DB, token)
	if err != nil {
		return sendHostedErrorPage(c)
	}

	// Mark in-flight attempts as cancelled so retry is immediately available.
	if cancelErr := services.CancelActiveHostedPayAttempt(s.DB, link.InvoiceID, link.CompanyID); cancelErr != nil {
		// Non-fatal: log but do not fail the page — the customer already cancelled.
		logging.L().Warn("hosted pay cancel: failed to mark attempt cancelled",
			"link_id", link.ID, "invoice_id", link.InvoiceID, "error", cancelErr.Error())
	}

	c.Set("Cache-Control", "no-store")
	c.Set("Content-Type", "text/html; charset=utf-8")
	return c.SendString(renderHostedPayCancelPage(token))
}

// ── Status-aware page ─────────────────────────────────────────────────────────

// renderHostedPayStatusPage renders the appropriate status page based on the
// latest attempt status. The authoritative status comes from the DB (set only by
// verified webhook ingestion), not from query parameters.
func renderHostedPayStatusPage(attempt *models.HostedPaymentAttempt, token string) string {
	// Determine display state from the attempt status.
	// Only payment_succeeded (set by verified webhook) triggers the "confirmed" view.
	// All other states — including redirected (browser returned but webhook not yet received),
	// created, failed, cancelled, and nil attempt — show the provisional view.
	type pageContent struct {
		icon    string
		title   string
		message string
		backURL string
	}

	var content pageContent
	if attempt != nil && attempt.Status == models.HostedPaymentAttemptPaymentSucceeded {
		content = pageContent{
			icon:    "&#10003;",
			title:   "Payment Confirmed",
			message: "Your payment has been received and confirmed. Thank you for your business.",
			backURL: "",
		}
	} else {
		// All other states — redirected (browser returned before webhook), created,
		// failed, cancelled, nil, or any future reserved status — show the provisional
		// view. The conservative wording is intentional: only payment_succeeded (set
		// exclusively by verified webhook ingestion) can claim confirmation.
		content = pageContent{
			icon:    "&#8987;",
			title:   "Payment Submitted",
			message: "Your payment details have been received and are being processed. You will be notified once the payment is confirmed.",
			backURL: "",
		}
	}

	backLink := ""
	if content.backURL != "" {
		backLink = `<a href="` + content.backURL + `">Return to Invoice</a>`
	}

	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>` + content.title + `</title>
<style>
*{margin:0;padding:0;box-sizing:border-box;}
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Arial,sans-serif;color:#374151;background:#f9fafb;display:flex;align-items:center;justify-content:center;min-height:100vh;padding:24px;}
.card{background:#fff;border:1px solid #e5e7eb;border-radius:8px;padding:40px 48px;max-width:480px;text-align:center;}
.icon{font-size:40px;margin-bottom:16px;}
h1{font-size:20px;font-weight:600;color:#111827;margin-bottom:8px;}
p{font-size:14px;color:#6b7280;line-height:1.6;}
a{display:inline-block;margin-top:20px;padding:8px 20px;background:#1d4ed8;color:#fff;border-radius:4px;text-decoration:none;font-size:14px;font-weight:500;}
a:hover{background:#1e40af;}
</style>
</head>
<body>
<div class="card">
<div class="icon">` + content.icon + `</div>
<h1>` + content.title + `</h1>
<p>` + content.message + `</p>
` + backLink + `
</div>
</body>
</html>`
}

func renderHostedPayCancelPage(token string) string {
	backURL := "/i/" + token
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Payment Cancelled</title>
<style>
*{margin:0;padding:0;box-sizing:border-box;}
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Arial,sans-serif;color:#374151;background:#f9fafb;display:flex;align-items:center;justify-content:center;min-height:100vh;padding:24px;}
.card{background:#fff;border:1px solid #e5e7eb;border-radius:8px;padding:40px 48px;max-width:480px;text-align:center;}
.icon{font-size:40px;margin-bottom:16px;}
h1{font-size:20px;font-weight:600;color:#111827;margin-bottom:8px;}
p{font-size:14px;color:#6b7280;line-height:1.6;}
a{display:inline-block;margin-top:20px;padding:8px 20px;background:#1d4ed8;color:#fff;border-radius:4px;text-decoration:none;font-size:14px;font-weight:500;}
a:hover{background:#1e40af;}
</style>
</head>
<body>
<div class="card">
<div class="icon">↩️</div>
<h1>Payment Cancelled</h1>
<p>You cancelled the payment. Your invoice remains unpaid.</p>
<a href="` + backURL + `">Back to Invoice</a>
</div>
</body>
</html>`
}
