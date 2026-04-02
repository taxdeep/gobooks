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

	var userIDPtr *uint
	user := UserFromCtx(c)
	if user != nil {
		uid := uint(0)
		userIDPtr = &uid
	}

	req := services.SendInvoiceEmailRequest{
		CompanyID:         companyID,
		InvoiceID:         uint(invoiceID),
		ToEmail:           toEmail,
		CCEmails:          ccEmails,
		TemplateType:      templateType,
		Subject:           "",
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
