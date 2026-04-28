// 遵循project_guide.md
package web

import (
	"errors"

	"github.com/gofiber/fiber/v2"
	"balanciz/internal/logging"
	"balanciz/internal/services"
)

// handleHostedInvoice serves the customer-facing hosted invoice page.
// GET /i/:token
//
// Security design:
//   - No authentication required. Access is controlled by the token alone.
//   - Any failure (invalid token, revoked, expired, not found, render error)
//     returns 410 Gone with a generic error page. The error page reveals no
//     information about whether the invoice, company, or link ever existed.
//   - Internal errors (DB failure, render failure) return the same 410 page
//     to prevent stack trace / internal state leakage.
//   - Company isolation is enforced by the token→link→invoice chain: only the
//     invoice that was linked at creation time is accessible.
//   - access audit (last_viewed_at, view_count) is updated on each successful view.
//
// Hosted content:
//   - Invoice rendered using the same BuildInvoiceRenderData + RenderInvoiceForHosted
//     pipeline as the internal preview. Template resolution follows the same
//     priority chain (pinned → company default → system fallback).
//   - A minimal toolbar is injected: company name, status badge, balance due,
//     Print/Save PDF button, Pay Now placeholder (disabled in Batch 6).
//   - No internal navigation, admin actions, audit logs, or backend data.
func (s *Server) handleHostedInvoice(c *fiber.Ctx) error {
	token := c.Params("token")

	// Validate token. Any failure → generic 410 error page.
	link, err := services.ValidateHostedToken(s.DB, token)
	if err != nil {
		if !errors.Is(err, services.ErrInvalidHostedToken) {
			logging.L().Warn("hosted invoice token validation unexpected error", "error", err.Error())
		}
		return sendHostedErrorPage(c)
	}

	// Load invoice with all preloads needed for rendering.
	invoice, err := loadInvoiceForRender(s.DB, link.CompanyID, link.InvoiceID)
	if err != nil {
		// Invoice load failure → same generic 410 (don't leak company/invoice existence).
		logging.L().Warn("hosted invoice load failed",
			"link_id", link.ID, "invoice_id", link.InvoiceID, "error", err.Error())
		return sendHostedErrorPage(c)
	}

	// Build render data using the shared pipeline (same as internal preview).
	renderData, err := services.BuildInvoiceRenderData(s.DB, link.CompanyID, invoice)
	if err != nil {
		logging.L().Warn("hosted invoice render data failed",
			"link_id", link.ID, "invoice_id", link.InvoiceID, "error", err.Error())
		return sendHostedErrorPage(c)
	}

	// Derive effective payment status for the toolbar.
	effectiveStatus := services.EffectiveInvoiceStatus(*invoice)
	visibility := services.BuildInvoicePaymentVisibility(*invoice)

	currency := invoice.CurrencyCode
	if currency == "" {
		var company struct{ BaseCurrencyCode string }
		s.DB.Table("companies").Select("base_currency_code").
			Where("id = ?", link.CompanyID).First(&company)
		if company.BaseCurrencyCode != "" {
			currency = company.BaseCurrencyCode
		}
	}

	// Evaluate payment eligibility (five-gate check — Batch 7).
	eligibility := services.EvaluateHostedPayability(s.DB, *invoice, link.CompanyID)

	meta := services.HostedPageMeta{
		EffectiveStatus: effectiveStatus,
		BalanceDue:      visibility.BalanceDue,
		Currency:        currency,
		CanDownload:     services.PDFGeneratorAvailable(),
		CanPay:          eligibility.CanPay,
		Token:           token,
	}

	html := services.RenderInvoiceForHosted(*renderData, meta)

	// Best-effort: update access audit. Failures are non-fatal.
	if err := services.RecordHostedLinkView(s.DB, link.ID); err != nil {
		logging.L().Warn("hosted invoice view record failed", "link_id", link.ID, "error", err.Error())
	}

	c.Set("Content-Type", "text/html; charset=utf-8")
	// No-cache: hosted page reflects current invoice state; don't serve stale data.
	c.Set("Cache-Control", "no-store")
	return c.SendString(html)
}

// sendHostedErrorPage sends the generic 410 Gone error page.
// Used for all hosted invoice access failures to avoid leaking internal state.
func sendHostedErrorPage(c *fiber.Ctx) error {
	c.Set("Content-Type", "text/html; charset=utf-8")
	c.Set("Cache-Control", "no-store")
	return c.Status(fiber.StatusGone).SendString(services.RenderHostedErrorPage())
}
