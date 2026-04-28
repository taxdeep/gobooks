// 遵循project_guide.md
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"balanciz/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// SendInvoiceEmailRequest holds parameters for sending an invoice via email.
type SendInvoiceEmailRequest struct {
	CompanyID    uint
	InvoiceID    uint
	ToEmail      string
	CCEmails     string // comma-separated, optional
	Subject      string // if empty, resolved from template then hardcoded default
	UserBody     string // if non-empty, overrides template-resolved body (user-edited in send modal)
	TemplateType string // "invoice", "reminder", "reminder2"
	TriggeredByUserID *uint // if user-triggered; nil for system/automatic

	// AttachPDF controls whether a PDF attachment is generated and included.
	//
	// Behaviour:
	//   false (default) — send email only, no PDF attached.
	//   true            — generate PDF and attach it. If the PDF generator is
	//                     unavailable, the send is rejected with a clear error
	//                     rather than silently sending without the attachment.
	//
	// The send handler sets this from the "attach_pdf" form field.
	// The send modal pre-checks the field when PDFAvailable=true so that
	// the default experience on a capable server is to include the PDF.
	AttachPDF bool
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
	// 1. Validate invoice eligibility for sending (status, customer email snapshot).
	//    Pure gate — no side effects. Failure here never produces a log entry.
	if err := ValidateInvoiceForSending(db, req.CompanyID, req.InvoiceID); err != nil {
		return nil, err
	}

	// 2. Load invoice with all preloads.
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

	// 3. Validate recipient email.
	if req.ToEmail == "" {
		req.ToEmail = invoice.CustomerEmailSnapshot
	}
	if req.ToEmail == "" {
		return nil, fmt.Errorf("recipient email is required")
	}
	if _, err := mail.ParseAddress(req.ToEmail); err != nil {
		return nil, fmt.Errorf("invalid recipient email: %w", err)
	}

	// 4. Check SMTP gate — pure readiness check, no side effects.
	//    Returns ErrSMTPNotReady sentinel so callers can distinguish gate failure
	//    from send failure. Failure here never produces a log entry.
	if err := CheckSMTPGate(db, req.CompanyID); err != nil {
		return nil, err
	}

	// 5. Get SMTP configuration for the actual send.
	smtpCfg, _, err := EffectiveSMTPForCompany(db, req.CompanyID)
	if err != nil {
		return nil, fmt.Errorf("SMTP config lookup failed: %w", err)
	}

	// 6. Build render data.
	renderData, err := BuildInvoiceRenderData(db, req.CompanyID, &invoice)
	if err != nil {
		return nil, fmt.Errorf("render data build failed: %w", err)
	}

	// 7. Generate HTML.
	htmlContent := RenderInvoiceToHTML(*renderData)

	// 8. Optional PDF generation.
	//
	// When AttachPDF=true:
	//   - Generator unavailable → reject with clear error (no misleading send).
	//   - Generator available but fails → log failure, return error.
	//   - Success → attach to email.
	// When AttachPDF=false:
	//   - Skip PDF entirely; send plain email; log attachment_included=false.
	var pdfBytes []byte
	var attachment *EmailAttachment
	var attachFilename string

	if req.AttachPDF {
		if !PDFGeneratorAvailable() {
			// User explicitly requested attachment but generator is not installed.
			// Reject the send rather than silently omitting the requested attachment.
			logEntry, logErr := createFailedEmailLog(db, req,
				"PDF attachment requested but wkhtmltopdf is not installed on this server",
				"", nil, "",
				map[string]any{
					"attachment_included": false,
					"attachment_error":    "wkhtmltopdf not installed",
				},
			)
			if logErr != nil {
				return nil, fmt.Errorf("PDF generator unavailable; log also failed: %v", logErr)
			}
			return logEntry, fmt.Errorf("PDF attachment requested but wkhtmltopdf is not installed on this server")
		}

		// Phase 3 G4-cleanup: PDF attachments now go through the chromedp
		// pipeline (block templates + system presets). The email body still
		// uses the legacy HTML renderer (renderData / htmlContent above) —
		// retiring that is a separate "hosted page redesign" pass.
		_ = htmlContent
		pdfBytes, _, err = RenderInvoicePDFV2(context.Background(), db, req.CompanyID, req.InvoiceID)
		if err != nil {
			logEntry, logErr := createFailedEmailLog(db, req,
				"PDF generation failed: "+err.Error(),
				"", nil, "",
				map[string]any{
					"attachment_included": false,
					"attachment_error":    "PDF generation failed: " + err.Error(),
				},
			)
			if logErr != nil {
				return nil, fmt.Errorf("PDF generation failed: %w (log creation also failed: %v)", err, logErr)
			}
			return logEntry, fmt.Errorf("PDF generation failed: %w", err)
		}

		// Reuse the shared safe filename — same rule as internal/hosted PDF download.
		attachFilename = InvoicePDFSafeFilename(invoice.InvoiceNumber)
		attachment = &EmailAttachment{
			Filename:    attachFilename,
			ContentType: "application/pdf",
			Data:        pdfBytes,
		}
	}

	// 9. Resolve email subject and body from template config (with token substitution).
	//    Priority: req.Subject (caller override) → template EmailDefaultSubject → hardcoded default.
	//    Body priority: template EmailDefaultBody → hardcoded default.
	//    For reminder template types, always use the built-in reminder text.
	tokenData := buildEmailTokenData(&invoice, renderData.CompanyName)
	var subject, body string

	isReminder := req.TemplateType == "reminder" || req.TemplateType == "reminder2"

	if isReminder {
		// Reminder paths use built-in text; template body config is ignored.
		subject = req.Subject
		if subject == "" {
			subject = "Payment Reminder — Invoice #" + invoice.InvoiceNumber
		}
		body = buildInvoiceEmailBody(&invoice, req.TemplateType)
	} else {
		// Standard invoice send: load template config for subject/body defaults.
		tmplSubject := ""
		tmplBody := ""
		if tmplCfg, err := resolveTemplateEmailConfig(db, &invoice, req.CompanyID); err == nil {
			tmplSubject = tmplCfg.EmailDefaultSubject
			tmplBody = tmplCfg.EmailDefaultBody
		}

		subject = req.Subject
		if subject == "" {
			subject = tmplSubject
		}
		if subject == "" {
			subject = DefaultEmailSubject(invoice.InvoiceNumber)
		}

		if req.UserBody != "" {
			// User edited the body in the send modal — use as-is (already rendered).
			body = req.UserBody
		} else if tmplBody != "" {
			_, body = RenderEmailTokens("", tmplBody, tokenData)
		} else {
			body = DefaultEmailBodyRendered(tokenData, req.AttachPDF)
		}

		// Apply token substitution to subject as well.
		subject, _ = RenderEmailTokens(subject, "", tokenData)
	}

	// 10. Resolve template identity for send-time snapshot.
	tmplIDSnap, tmplNameSnap := resolveTemplateIdentity(db, &invoice, req.CompanyID)

	// 11. Send email (with or without attachment).
	// SendEmailWithAttachment handles nil attachment by sending plain text.
	err = SendEmailWithAttachment(smtpCfg, req.ToEmail, subject, body, attachment)
	if err != nil {
		meta := map[string]any{
			"attachment_included": attachment != nil,
		}
		if attachment != nil {
			meta["attachment_filename"] = attachFilename
		}
		logEntry, logErr := createFailedEmailLog(db, req,
			fmt.Sprintf("SMTP send failed: %v", err), body, tmplIDSnap, tmplNameSnap, meta)
		if logErr != nil {
			return nil, fmt.Errorf("email send failed: %w (log creation also failed: %v)", err, logErr)
		}
		return logEntry, fmt.Errorf("email send failed: %w", err)
	}

	// 12. Build send-log attachment metadata.
	successMeta := map[string]any{
		"attachment_included": attachment != nil,
	}
	if attachment != nil {
		successMeta["attachment_filename"] = attachFilename
	}

	// 13. Create successful email log.
	logEntry, err := createSuccessfulEmailLog(db, req, subject, body, tmplIDSnap, tmplNameSnap, successMeta)
	if err != nil {
		return nil, fmt.Errorf("email log creation failed: %w", err)
	}

	// 14. Update invoice: sent_at, status, and send_count (lightweight summary counter).
	now := time.Now()
	db.Model(&models.Invoice{}).
		Where("id = ? AND company_id = ?", req.InvoiceID, req.CompanyID).
		Updates(map[string]any{
			"sent_at":    now,
			"status":     string(models.InvoiceStatusSent),
			"send_count": gorm.Expr("send_count + 1"),
		})

	// 15. Audit log.
	attachmentSize := 0
	if pdfBytes != nil {
		attachmentSize = len(pdfBytes)
	}
	TryWriteAuditLogWithContext(
		db, "invoice.email_sent", "Invoice", req.InvoiceID, "system",
		map[string]any{
			"to_email":            req.ToEmail,
			"template_type":       req.TemplateType,
			"status":              "sent",
			"attachment_included": attachment != nil,
			"attachment_size":     attachmentSize,
		},
		&req.CompanyID, nil,
	)

	return logEntry, nil
}

// createSuccessfulEmailLog creates an InvoiceEmailLog with sent status.
// bodyResolved, tmplIDSnap, tmplNameSnap capture the send-time presentation snapshot.
// meta holds lightweight attachment metadata (attachment_included, attachment_filename).
func createSuccessfulEmailLog(db *gorm.DB, req SendInvoiceEmailRequest, subject, bodyResolved string, tmplIDSnap *uint, tmplNameSnap string, meta map[string]any) (*models.InvoiceEmailLog, error) {
	now := time.Now()

	metaJSON := marshalEmailMeta(meta)
	log := models.InvoiceEmailLog{
		CompanyID:            req.CompanyID,
		InvoiceID:            req.InvoiceID,
		ToEmail:              req.ToEmail,
		CCEmails:             req.CCEmails,
		SendStatus:           models.EmailSendStatusSent,
		Subject:              subject,
		BodyResolved:         bodyResolved,
		TemplateIDSnapshot:   tmplIDSnap,
		TemplateNameSnapshot: tmplNameSnap,
		TemplateType:         req.TemplateType,
		TriggeredByUserID:    req.TriggeredByUserID,
		CreatedAt:            now,
		SentAt:               &now,
		MetadataJSON:         metaJSON,
	}

	if err := db.Create(&log).Error; err != nil {
		return nil, fmt.Errorf("email log creation failed: %w", err)
	}

	return &log, nil
}

// createFailedEmailLog creates an InvoiceEmailLog with failed status.
// bodyResolved, tmplIDSnap, tmplNameSnap capture the send-time presentation state
// (what was attempted, even though delivery failed).
// meta holds lightweight attachment metadata (attachment_included, attachment_error).
func createFailedEmailLog(db *gorm.DB, req SendInvoiceEmailRequest, errorMsg, bodyResolved string, tmplIDSnap *uint, tmplNameSnap string, meta map[string]any) (*models.InvoiceEmailLog, error) {
	metaJSON := marshalEmailMeta(meta)
	log := models.InvoiceEmailLog{
		CompanyID:            req.CompanyID,
		InvoiceID:            req.InvoiceID,
		ToEmail:              req.ToEmail,
		CCEmails:             req.CCEmails,
		SendStatus:           models.EmailSendStatusFailed,
		ErrorMessage:         errorMsg,
		BodyResolved:         bodyResolved,
		TemplateIDSnapshot:   tmplIDSnap,
		TemplateNameSnapshot: tmplNameSnap,
		TemplateType:         req.TemplateType,
		TriggeredByUserID:    req.TriggeredByUserID,
		CreatedAt:            time.Now(),
		MetadataJSON:         metaJSON,
	}

	if err := db.Create(&log).Error; err != nil {
		return nil, fmt.Errorf("failed email log creation failed: %w", err)
	}

	return &log, nil
}

// marshalEmailMeta serializes a metadata map to datatypes.JSON.
// Falls back to "{}" on marshal failure (should never happen with simple maps).
func marshalEmailMeta(meta map[string]any) datatypes.JSON {
	if meta == nil {
		return datatypes.JSON("{}")
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return datatypes.JSON("{}")
	}
	return datatypes.JSON(b)
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

	default: // "invoice" — this path is not reached from SendInvoiceByEmail (which uses
		// DefaultEmailBodyRendered instead), but kept consistent for direct callers.
		body.WriteString("Dear ")
		body.WriteString(invoice.CustomerNameSnapshot)
		body.WriteString(",\n\n")
		body.WriteString("Thank you for your business. Please review your invoice details below.\n\n")
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

// ── Email token helpers ───────────────────────────────────────────────────────

// buildEmailTokenData constructs EmailTokenData from a loaded invoice and company name.
// Uses invoice snapshot fields (never live customer/company data) so the email content
// is consistent with what was on the invoice at issue time.
func buildEmailTokenData(invoice *models.Invoice, companyName string) EmailTokenData {
	curr := invoice.CurrencyCode
	if curr == "" {
		curr = "CAD"
	}
	return EmailTokenData{
		CompanyName:   companyName,
		CustomerName:  invoice.CustomerNameSnapshot,
		InvoiceNumber: invoice.InvoiceNumber,
		InvoiceDate:   invoice.InvoiceDate,
		DueDate:       invoice.DueDate,
		BalanceDue:    invoice.BalanceDue,
		Amount:        invoice.Amount,
		Currency:      curr,
	}
}

// resolveTemplateEmailConfig loads the TemplateConfig and template identity for an invoice.
// Resolution order (mirrors resolveRenderTemplate in the render service):
//  1. Template pinned on the invoice (invoice.TemplateID) — must be active and company-scoped
//  2. Company active default template
//
// Returns a zero-value TemplateConfig and nil identity when no template is found.
// Never returns an error — email sending always has a fallback path.
func resolveTemplateEmailConfig(db *gorm.DB, invoice *models.Invoice, companyID uint) (models.TemplateConfig, error) {
	var tmpl models.InvoiceTemplate
	var err error

	if invoice.TemplateID != nil {
		// Pinned template: must be active and belong to this company.
		err = db.Where("id = ? AND company_id = ? AND is_active = true", *invoice.TemplateID, companyID).
			First(&tmpl).Error
		if err != nil {
			// Pinned template inactive or not found — fall through to company default.
			err = db.Where("company_id = ? AND is_default = true AND is_active = true", companyID).
				First(&tmpl).Error
		}
	} else {
		err = db.Where("company_id = ? AND is_default = true AND is_active = true", companyID).
			First(&tmpl).Error
	}
	if err != nil {
		// No template found — return empty config (caller uses defaults).
		return models.TemplateConfig{}, nil
	}

	cfg, parseErr := tmpl.UnmarshalConfig()
	if parseErr != nil || cfg == nil {
		return models.TemplateConfig{}, nil
	}
	return *cfg, nil
}

// resolveTemplateIdentity returns the template ID and name that will be used for
// sending, following the same resolution order as resolveTemplateEmailConfig.
// Used to populate TemplateIDSnapshot and TemplateNameSnapshot in InvoiceEmailLog.
// Returns nil ID and empty name when no template resolves (system fallback).
func resolveTemplateIdentity(db *gorm.DB, invoice *models.Invoice, companyID uint) (*uint, string) {
	var tmpl models.InvoiceTemplate
	var err error

	if invoice.TemplateID != nil {
		err = db.Where("id = ? AND company_id = ? AND is_active = true", *invoice.TemplateID, companyID).
			First(&tmpl).Error
		if err != nil {
			err = db.Where("company_id = ? AND is_default = true AND is_active = true", companyID).
				First(&tmpl).Error
		}
	} else {
		err = db.Where("company_id = ? AND is_default = true AND is_active = true", companyID).
			First(&tmpl).Error
	}
	if err != nil {
		return nil, "" // system fallback — no template identity to record
	}
	return &tmpl.ID, tmpl.Name
}
