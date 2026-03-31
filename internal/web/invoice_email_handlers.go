// 遵循project_guide.md
package web

import (
	"log/slog"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
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

	// Query parameters
	toEmail := c.Query("to_email", "")
	templateType := c.Query("template_type", "invoice")
	ccEmails := c.Query("cc_emails", "")

	var userIDPtr *uint
	user := UserFromCtx(c)
	if user != nil {
		uid := uint(0) // placeholder — services layer uses uint, not uuid
		userIDPtr = &uid
	}

	// Build request
	req := services.SendInvoiceEmailRequest{
		CompanyID:         companyID,
		InvoiceID:         uint(invoiceID),
		ToEmail:           toEmail,
		CCEmails:          ccEmails,
		TemplateType:      templateType,
		Subject:           "", // Use default
		TriggeredByUserID: userIDPtr,
	}

	// Send email
	emailLog, err := services.SendInvoiceByEmail(s.DB, req)
	if err != nil {
		slog.Error("send invoice email failed",
			"company_id", companyID,
			"invoice_id", invoiceID,
			"error", err.Error(),
		)
		statusCode := fiber.StatusInternalServerError
		if err.Error() == "invoice not found" {
			statusCode = fiber.StatusNotFound
		}
		return c.Status(statusCode).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	return c.JSON(fiber.Map{
		"status":        "sent",
		"email_log_id":  emailLog.ID,
		"to_email":      emailLog.ToEmail,
		"cc_emails":     emailLog.CCEmails,
		"template_type": emailLog.TemplateType,
		"sent_at":       emailLog.SentAt,
	})
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
