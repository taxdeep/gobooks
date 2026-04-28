// 遵循project_guide.md
package web

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"balanciz/internal/logging"
	"balanciz/internal/models"
	"balanciz/internal/services"
)

// handleCreateShareLink creates an active hosted link for an invoice.
// POST /invoices/:id/share-link
//
// On success: redirects to /invoices/:id?newlink=<plaintext_token>
// On failure (e.g. active link already exists): redirects to /invoices/:id?error=<msg>
//
// The plaintext token appears once in the redirect query string and is shown
// to the authenticated internal user only. It is never stored server-side.
func (s *Server) handleCreateShareLink(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company context required")
	}
	invoiceID, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || invoiceID <= 0 {
		return c.Status(fiber.StatusBadRequest).SendString("invalid invoice ID")
	}
	base := "/invoices/" + c.Params("id")

	plaintext, _, err := services.CreateHostedLink(s.DB, companyID, uint(invoiceID), nil)
	if err != nil {
		logging.L().Warn("create share link failed",
			"company_id", companyID, "invoice_id", invoiceID, "error", err.Error())
		msg := "Could not create share link"
		if errors.Is(err, services.ErrActiveLinkExists) {
			msg = "An active share link already exists. Revoke it first or use Regenerate."
		}
		return c.Redirect(base+"?error="+url.QueryEscape(msg), fiber.StatusSeeOther)
	}

	return c.Redirect(base+"?newlink="+plaintext, fiber.StatusSeeOther)
}

// handleRevokeShareLink revokes the active hosted link for an invoice.
// POST /invoices/:id/share-link/revoke
func (s *Server) handleRevokeShareLink(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company context required")
	}
	invoiceID, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || invoiceID <= 0 {
		return c.Status(fiber.StatusBadRequest).SendString("invalid invoice ID")
	}
	base := "/invoices/" + c.Params("id")

	if err := services.RevokeHostedLink(s.DB, companyID, uint(invoiceID)); err != nil {
		logging.L().Warn("revoke share link failed",
			"company_id", companyID, "invoice_id", invoiceID, "error", err.Error())
		return c.Redirect(base+"?error="+url.QueryEscape("Could not revoke share link: "+err.Error()),
			fiber.StatusSeeOther)
	}

	return c.Redirect(base, fiber.StatusSeeOther)
}

// handleRegenerateShareLink atomically revokes the current active link and
// creates a fresh one. Rotation is atomic: no window where both old and new tokens
// are simultaneously valid.
// POST /invoices/:id/share-link/regenerate
func (s *Server) handleRegenerateShareLink(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company context required")
	}
	invoiceID, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || invoiceID <= 0 {
		return c.Status(fiber.StatusBadRequest).SendString("invalid invoice ID")
	}
	base := "/invoices/" + c.Params("id")

	plaintext, _, err := services.RegenerateHostedLink(s.DB, companyID, uint(invoiceID), nil)
	if err != nil {
		logging.L().Warn("regenerate share link failed",
			"company_id", companyID, "invoice_id", invoiceID, "error", err.Error())
		return c.Redirect(base+"?error="+url.QueryEscape("Could not regenerate share link: "+err.Error()),
			fiber.StatusSeeOther)
	}

	return c.Redirect(base+"?newlink="+plaintext, fiber.StatusSeeOther)
}

// handleUpdateShareLinkExpiry sets or clears the expiry date on the active hosted link.
// POST /invoices/:id/share-link/expiry
//
// Form fields:
//   - expires_at: datetime-local value ("2006-01-02T15:04"), or empty string to clear expiry.
//
// On success: redirect to /invoices/:id
// On failure: redirect to /invoices/:id?error=<msg>
func (s *Server) handleUpdateShareLinkExpiry(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company context required")
	}
	invoiceID, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || invoiceID <= 0 {
		return c.Status(fiber.StatusBadRequest).SendString("invalid invoice ID")
	}
	base := "/invoices/" + c.Params("id")

	// Load the active link first to ensure company isolation.
	link, err := services.GetActiveHostedLink(s.DB, companyID, uint(invoiceID))
	if err != nil {
		if errors.Is(err, services.ErrNoActiveLink) {
			return c.Redirect(base+"?error="+url.QueryEscape("No active share link to update."), fiber.StatusSeeOther)
		}
		return c.Redirect(base+"?error="+url.QueryEscape("Could not load share link."), fiber.StatusSeeOther)
	}

	expiresAtStr := strings.TrimSpace(c.FormValue("expires_at"))

	var expiresAt *time.Time
	if expiresAtStr != "" {
		// datetime-local format: "2006-01-02T15:04"
		t, parseErr := time.ParseInLocation("2006-01-02T15:04", expiresAtStr, time.Local)
		if parseErr != nil {
			return c.Redirect(base+"?error="+url.QueryEscape("Invalid expiry date format."), fiber.StatusSeeOther)
		}
		if t.Before(time.Now()) {
			return c.Redirect(base+"?error="+url.QueryEscape("Expiry date must be in the future."), fiber.StatusSeeOther)
		}
		expiresAt = &t
	}
	// expiresAt == nil → clear expiry

	if err := s.DB.Model(&models.InvoiceHostedLink{}).
		Where("id = ?", link.ID).
		Update("expires_at", expiresAt).Error; err != nil {
		logging.L().Warn("update share link expiry failed",
			"company_id", companyID, "invoice_id", invoiceID, "link_id", link.ID, "error", err.Error())
		return c.Redirect(base+"?error="+url.QueryEscape("Could not update expiry."), fiber.StatusSeeOther)
	}

	return c.Redirect(base, fiber.StatusSeeOther)
}
