// 遵循project_guide.md
package services

import (
	"fmt"
	"net/mail"
	"strings"
	"time"

	"gobooks/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// SendInvoiceEmailRequest holds parameters for sending an invoice via email.
type SendInvoiceEmailRequest struct {
	CompanyID    uint
	InvoiceID    uint
	ToEmail      string
	CCEmails     string // comma-separated, optional
	Subject      string // if empty, uses default "Invoice #{number}"
	TemplateType string // "invoice", "reminder", "reminder2"
	TriggeredByUserID *uint // if user-triggered; nil for system/automatic
}

// SendInvoiceByEmail generates PDF, sends email, and logs the attempt.
// Creates InvoiceEmailLog with success/failure status.
//
// Preconditions:
// - Invoice exists and is posted (JournalEntryID set)
// - Company SMTP is configured and verified
// - Invoice has customer email
// - Customer email is valid
//
// Postconditions:
// - PDF generated
// - Email sent via SMTP
// - InvoiceEmailLog created with send_status (sent|failed)
// - If failed, error_message populated
//
// Returns the created InvoiceEmailLog record.
func SendInvoiceByEmail(db *gorm.DB, req SendInvoiceEmailRequest) (*models.InvoiceEmailLog, error) {
	// 1. Load invoice with all preloads
	var invoice models.Invoice
	if err := db.Where("id = ? AND company_id = ?", req.InvoiceID, req.CompanyID).
		Preload("Lines.ProductService").
		Preload("Lines.TaxCode").
		Preload("Customer").
		First(&invoice).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("invoice not found")
		}
		return nil, fmt.Errorf("invoice lookup failed: %w", err)
	}

	// 2. Validate recipient email
	if req.ToEmail == "" {
		req.ToEmail = invoice.CustomerEmailSnapshot
	}
	if req.ToEmail == "" {
		return nil, fmt.Errorf("recipient email is required")
	}
	if _, err := mail.ParseAddress(req.ToEmail); err != nil {
		return nil, fmt.Errorf("invalid recipient email: %w", err)
	}

	// 3. Get SMTP configuration
	smtpCfg, ready, err := EffectiveSMTPForCompany(db, req.CompanyID)
	if err != nil {
		return nil, fmt.Errorf("SMTP config lookup failed: %w", err)
	}
	if !ready {
		return nil, fmt.Errorf("SMTP not configured or not verified for company")
	}

	// 4. Build render data
	renderData, err := BuildInvoiceRenderData(db, req.CompanyID, &invoice)
	if err != nil {
		return nil, fmt.Errorf("render data build failed: %w", err)
	}

	// 5. Generate HTML
	htmlContent := RenderInvoiceToHTML(*renderData)

	// 6. Generate PDF
	pdfBytes, err := GenerateInvoicePDF(htmlContent)
	if err != nil {
		logEntry, logErr := createFailedEmailLog(db, req, "PDF generation failed: "+err.Error())
		if logErr != nil {
			return nil, fmt.Errorf("PDF generation failed: %w (log creation also failed: %v)", err, logErr)
		}
		return logEntry, fmt.Errorf("PDF generation failed: %w", err)
	}

	// 7. Build email subject
	subject := req.Subject
	if subject == "" {
		subject = fmt.Sprintf("Invoice #%s", invoice.InvoiceNumber)
	}

	// 8. Build email body
	body := buildInvoiceEmailBody(&invoice, req.TemplateType)

	// 9. Build PDF attachment
	safeNumber := strings.ReplaceAll(invoice.InvoiceNumber, "/", "-")
	safeNumber = strings.ReplaceAll(safeNumber, "\\", "-")
	attachment := &EmailAttachment{
		Filename:    "Invoice-" + safeNumber + ".pdf",
		ContentType: "application/pdf",
		Data:        pdfBytes,
	}

	// 10. Send email with PDF attachment
	err = SendEmailWithAttachment(smtpCfg, req.ToEmail, subject, body, attachment)
	if err != nil {
		logEntry, logErr := createFailedEmailLog(db, req, fmt.Sprintf("SMTP send failed: %v", err))
		if logErr != nil {
			return nil, fmt.Errorf("email send failed: %w (log creation also failed: %v)", err, logErr)
		}
		return logEntry, fmt.Errorf("email send failed: %w", err)
	}

	// 11. Create successful email log
	logEntry, err := createSuccessfulEmailLog(db, req, subject)
	if err != nil {
		return nil, fmt.Errorf("email log creation failed: %w", err)
	}

	// 12. Update invoice sent_at timestamp
	now := time.Now()
	db.Model(&models.Invoice{}).
		Where("id = ? AND company_id = ?", req.InvoiceID, req.CompanyID).
		Updates(map[string]any{
			"sent_at": now,
			"status":  string(models.InvoiceStatusSent),
		})

	// 13. Audit log
	TryWriteAuditLogWithContext(
		db, "invoice.email_sent", "Invoice", req.InvoiceID, "system",
		map[string]any{
			"to_email":        req.ToEmail,
			"template_type":   req.TemplateType,
			"status":          "sent",
			"attachment_size": len(pdfBytes),
		},
		&req.CompanyID, nil,
	)

	return logEntry, nil
}

// createSuccessfulEmailLog creates an InvoiceEmailLog with sent status.
func createSuccessfulEmailLog(db *gorm.DB, req SendInvoiceEmailRequest, subject string) (*models.InvoiceEmailLog, error) {
	now := time.Now()

	log := models.InvoiceEmailLog{
		CompanyID:        req.CompanyID,
		InvoiceID:        req.InvoiceID,
		ToEmail:          req.ToEmail,
		CCEmails:         req.CCEmails,
		SendStatus:       models.EmailSendStatusSent,
		Subject:          subject,
		TemplateType:     req.TemplateType,
		TriggeredByUserID: req.TriggeredByUserID,
		CreatedAt:        now,
		SentAt:           &now,
		MetadataJSON:     datatypes.JSON("{}"),
	}

	if err := db.Create(&log).Error; err != nil {
		return nil, fmt.Errorf("email log creation failed: %w", err)
	}

	return &log, nil
}

// createFailedEmailLog creates an InvoiceEmailLog with failed status.
func createFailedEmailLog(db *gorm.DB, req SendInvoiceEmailRequest, errorMsg string) (*models.InvoiceEmailLog, error) {
	log := models.InvoiceEmailLog{
		CompanyID:        req.CompanyID,
		InvoiceID:        req.InvoiceID,
		ToEmail:          req.ToEmail,
		CCEmails:         req.CCEmails,
		SendStatus:       models.EmailSendStatusFailed,
		ErrorMessage:     errorMsg,
		TemplateType:     req.TemplateType,
		TriggeredByUserID: req.TriggeredByUserID,
		CreatedAt:        time.Now(),
		MetadataJSON:     datatypes.JSON("{}"),
	}

	if err := db.Create(&log).Error; err != nil {
		return nil, fmt.Errorf("failed email log creation failed: %w", err)
	}

	return &log, nil
}

// buildInvoiceEmailBody creates plain-text email body.
// Different templates for invoice, reminder, etc.
func buildInvoiceEmailBody(invoice *models.Invoice, templateType string) string {
	var body strings.Builder

	switch templateType {
	case "reminder":
		body.WriteString("Dear ")
		body.WriteString(invoice.CustomerNameSnapshot)
		body.WriteString(",\n\n")
		body.WriteString("This is a friendly reminder that invoice #")
		body.WriteString(invoice.InvoiceNumber)
		body.WriteString(" is still outstanding.\n\n")
		body.WriteString("Invoice #: ")
		body.WriteString(invoice.InvoiceNumber)
		body.WriteString("\n")
		body.WriteString("Amount Due: $")
		body.WriteString(invoice.BalanceDue.String())
		body.WriteString("\n")
		if invoice.DueDate != nil {
			body.WriteString("Due Date: ")
			body.WriteString(invoice.DueDate.Format("January 2, 2006"))
			body.WriteString("\n")
		}
		body.WriteString("\nPlease arrange payment at your earliest convenience.\n\n")

	case "reminder2":
		body.WriteString("Dear ")
		body.WriteString(invoice.CustomerNameSnapshot)
		body.WriteString(",\n\n")
		body.WriteString("URGENT: Invoice #")
		body.WriteString(invoice.InvoiceNumber)
		body.WriteString(" is now overdue.\n\n")
		body.WriteString("Invoice #: ")
		body.WriteString(invoice.InvoiceNumber)
		body.WriteString("\n")
		body.WriteString("Amount Due: $")
		body.WriteString(invoice.BalanceDue.String())
		body.WriteString("\n")
		if invoice.DueDate != nil {
			body.WriteString("Original Due Date: ")
			body.WriteString(invoice.DueDate.Format("January 2, 2006"))
			body.WriteString("\n")
		}
		body.WriteString("\nImmediate payment is required to avoid further action.\n\n")

	default: // "invoice"
		body.WriteString("Dear ")
		body.WriteString(invoice.CustomerNameSnapshot)
		body.WriteString(",\n\n")
		body.WriteString("Thank you for your business. Please find your invoice attached.\n\n")
		body.WriteString("Invoice #: ")
		body.WriteString(invoice.InvoiceNumber)
		body.WriteString("\n")
		body.WriteString("Date: ")
		body.WriteString(invoice.InvoiceDate.Format("January 2, 2006"))
		body.WriteString("\n")
		body.WriteString("Amount Due: $")
		body.WriteString(invoice.Amount.String())
		body.WriteString("\n")
		if invoice.DueDate != nil {
			body.WriteString("Due Date: ")
			body.WriteString(invoice.DueDate.Format("January 2, 2006"))
			body.WriteString("\n")
		}
		body.WriteString("\n")

		if invoice.Memo != "" {
			body.WriteString("Notes:\n")
			body.WriteString(invoice.Memo)
			body.WriteString("\n\n")
		}

		body.WriteString("Please remit payment to the address listed on the invoice.\n\n")
	}

	body.WriteString("Thank you!\n")

	return body.String()
}

// GetInvoiceEmailHistory retrieves all email send attempts for an invoice.
func GetInvoiceEmailHistory(db *gorm.DB, companyID, invoiceID uint) ([]models.InvoiceEmailLog, error) {
	var logs []models.InvoiceEmailLog
	if err := db.Where("company_id = ? AND invoice_id = ?", companyID, invoiceID).
		Order("created_at DESC").
		Find(&logs).Error; err != nil {
		return nil, fmt.Errorf("email history lookup failed: %w", err)
	}
	return logs, nil
}

// GetCompanyEmailStatistics returns email send statistics for a company.
type EmailStatistics struct {
	TotalSent   int64
	TotalFailed int64
	LastSentAt  *time.Time
}

func GetCompanyEmailStatistics(db *gorm.DB, companyID uint) (*EmailStatistics, error) {
	var stats EmailStatistics

	// Count sent emails
	if err := db.Model(&models.InvoiceEmailLog{}).
		Where("company_id = ? AND send_status = ?", companyID, models.EmailSendStatusSent).
		Count(&stats.TotalSent).Error; err != nil {
		return nil, fmt.Errorf("sent count failed: %w", err)
	}

	// Count failed emails
	if err := db.Model(&models.InvoiceEmailLog{}).
		Where("company_id = ? AND send_status = ?", companyID, models.EmailSendStatusFailed).
		Count(&stats.TotalFailed).Error; err != nil {
		return nil, fmt.Errorf("failed count failed: %w", err)
	}

	// Get last sent time
	var lastLog models.InvoiceEmailLog
	lastErr := db.Where("company_id = ? AND send_status = ?", companyID, models.EmailSendStatusSent).
		Order("created_at DESC").
		First(&lastLog).Error
	if lastErr != nil && lastErr != gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("last sent lookup failed: %w", lastErr)
	}
	if lastErr == nil {
		stats.LastSentAt = &lastLog.CreatedAt
	}

	return &stats, nil
}
