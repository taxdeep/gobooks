// 遵循project_guide.md
package web

import (
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"gobooks/internal/models"
	"gobooks/internal/services"
)

// handleInvoiceSendEmail - POST /invoices/:id/send-email
// Sends invoice email to customer(s).
// Permission: ActionInvoiceUpdate
//
// Query Params:
//   - to_email: Override recipient email (optional)
//   - template_type: "invoice", "reminder", "reminder2" (default: "invoice")
//   - cc_emails: Comma-separated CC addresses (optional)
//
// Response: 200 OK with email log details.
// Error Responses: 400 (bad input), 404 (not found), 500 (SMTP/PDF error).
func (s *Server) handleInvoiceSendEmail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "company context required",
		})
	}

	invoiceID, err := strconv.ParseUint(c.Params("id"), 10, 32)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid invoice ID",
		})
	}

	// Form values (POST body — not query string).
	toEmail := strings.TrimSpace(c.FormValue("to_email"))
	templateType := strings.TrimSpace(c.FormValue("template_type"))
	if templateType == "" {
		templateType = "invoice"
	}
	ccEmails := strings.TrimSpace(c.FormValue("cc_emails"))
	// subject: user override from modal; empty means server resolves from template.
	subject := strings.TrimSpace(c.FormValue("subject"))
	// userBody: user-edited body from modal; empty means server resolves from template.
	userBody := strings.TrimSpace(c.FormValue("body"))

	// If no email supplied via form, fall back to the customer's current email.
	if toEmail == "" {
		var inv models.Invoice
		if err := s.DB.Select("id", "customer_id", "customer_email_snapshot").
			Where("id = ? AND company_id = ?", uint(invoiceID), companyID).
			First(&inv).Error; err == nil {
			toEmail = inv.CustomerEmailSnapshot
			if toEmail == "" {
				// Last resort: look up customer's current email.
				var customer models.Customer
				if err2 := s.DB.Select("email").First(&customer, inv.CustomerID).Error; err2 == nil {
					toEmail = strings.TrimSpace(customer.Email)
				}
			}
		}
	}

	detailURL := fmt.Sprintf("/invoices/%d", invoiceID)
	redirectErr := func(msg string) error {
		return c.Redirect(detailURL+"?emailerror="+url.QueryEscape(msg), fiber.StatusSeeOther)
	}

	// TriggeredByUserID is *uint but User.ID is uuid.UUID — no mapping possible.
	// Set to nil; the audit log (written by SendInvoiceByEmail) captures actor context.
	var userIDPtr *uint

	req := services.SendInvoiceEmailRequest{
		CompanyID:         companyID,
		InvoiceID:         uint(invoiceID),
		ToEmail:           toEmail,
		CCEmails:          ccEmails,
		TemplateType:      templateType,
		Subject:           subject,   // user override; empty → server resolves
		UserBody:          userBody,  // user override; empty → server resolves
		TriggeredByUserID: userIDPtr,
	}

	_, err = services.SendInvoiceByEmail(s.DB, req)
	if err != nil {
		slog.Error("send invoice email failed",
			"company_id", companyID,
			"invoice_id", invoiceID,
			"error", err.Error(),
		)
		return redirectErr(err.Error())
	}

	return c.Redirect(detailURL+"?sent=1", fiber.StatusSeeOther)
}

// handleGetInvoiceEmailHistory - GET /invoices/:id/email-history
// Retrieves all email send attempts for an invoice.
func (s *Server) handleGetInvoiceEmailHistory(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "company context required",
		})
	}

	invoiceID, err := strconv.ParseUint(c.Params("id"), 10, 32)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid invoice ID",
		})
	}

	// Get history
	logs, err := services.GetInvoiceEmailHistory(s.DB, companyID, uint(invoiceID))
	if err != nil {
		slog.Error("get invoice email history failed",
			"company_id", companyID,
			"invoice_id", invoiceID,
			"error", err.Error(),
		)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	// Build response
	type EmailLogResponse struct {
		ID           uint       `json:"id"`
		ToEmail      string     `json:"to_email"`
		CCEmails     string     `json:"cc_emails"`
		SendStatus   string     `json:"send_status"`
		TemplateType string     `json:"template_type"`
		Subject      string     `json:"subject"`
		ErrorMessage string     `json:"error_message,omitempty"`
		CreatedAt    *time.Time `json:"created_at"`
		SentAt       *time.Time `json:"sent_at,omitempty"`
	}

	var responses []EmailLogResponse
	for _, log := range logs {
		responses = append(responses, EmailLogResponse{
			ID:           log.ID,
			ToEmail:      log.ToEmail,
			CCEmails:     log.CCEmails,
			SendStatus:   string(log.SendStatus),
			TemplateType: log.TemplateType,
			Subject:      log.Subject,
			ErrorMessage: log.ErrorMessage,
			CreatedAt:    &log.CreatedAt,
			SentAt:       log.SentAt,
		})
	}

	return c.JSON(fiber.Map{
		"email_logs": responses,
	})
}

// handleGetInvoiceSendDefaults - GET /api/invoices/:id/send-defaults
// Returns server-resolved defaults for the send email modal as JSON.
// The resolution uses the same pipeline as SendInvoiceByEmail so callers
// can rely on the data being consistent with what would actually be sent.
func (s *Server) handleGetInvoiceSendDefaults(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "company context required",
		})
	}

	invoiceID, err := strconv.ParseUint(c.Params("id"), 10, 32)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid invoice ID",
		})
	}

	defaults, err := services.GetInvoiceSendDefaults(s.DB, companyID, uint(invoiceID))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	return c.JSON(defaults)
}

// handleBindTemplate - POST /invoices/:id/bind-template
// Binds a template to a draft invoice. Draft-only; backend enforces the constraint.
// On success: redirect to invoice detail with ?tmplbound=1.
// On failure: redirect to invoice detail with ?error=<msg>.
func (s *Server) handleBindTemplate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "company context required",
		})
	}

	invoiceID, err := strconv.ParseUint(c.Params("id"), 10, 32)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid invoice ID",
		})
	}

	detailURL := fmt.Sprintf("/invoices/%d", invoiceID)

	templateIDStr := strings.TrimSpace(c.FormValue("template_id"))
	templateID, err := strconv.ParseUint(templateIDStr, 10, 32)
	if err != nil || templateID == 0 {
		return c.Redirect(detailURL+"?error="+url.QueryEscape("invalid template ID"), fiber.StatusSeeOther)
	}

	if _, err := services.BindTemplateToInvoice(s.DB, companyID, uint(invoiceID), uint(templateID)); err != nil {
		slog.Warn("bind template failed",
			"company_id", companyID,
			"invoice_id", invoiceID,
			"template_id", templateID,
			"error", err.Error(),
		)
		return c.Redirect(detailURL+"?error="+url.QueryEscape(err.Error()), fiber.StatusSeeOther)
	}

	return c.Redirect(detailURL+"?tmplbound=1", fiber.StatusSeeOther)
}

// handleGetInvoiceEmailPreview returns the resolved email subject and body for
// an invoice without sending it. Reuses the same GetInvoiceSendDefaults pipeline.
// GET /api/invoices/:id/email-preview
// Requires internal auth + company scope (same as send-defaults).
func (s *Server) handleGetInvoiceEmailPreview(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "company context required"})
	}
	invoiceID, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil || invoiceID == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid invoice ID"})
	}

	defaults, err := services.GetInvoiceSendDefaults(s.DB, companyID, uint(invoiceID))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(fiber.Map{
		"subject": defaults.Subject,
		"body":    defaults.Body,
	})
}
