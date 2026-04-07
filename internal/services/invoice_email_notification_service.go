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
	Subject      string // if empty, resolved from template then hardcoded default
	UserBody     string // if non-empty, overrides template-resolved body (user-edited in send modal)
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

	// 8. Generate PDF.
	pdfBytes, err := GenerateInvoicePDF(htmlContent)
	if err != nil {
		// PDF failure: no body/template identity resolved yet — pass empty values.
		logEntry, logErr := createFailedEmailLog(db, req, "PDF generation failed: "+err.Error(), "", nil, "")
		if logErr != nil {
			return nil, fmt.Errorf("PDF generation failed: %w (log creation also failed: %v)", err, logErr)
		}
		return logEntry, fmt.Errorf("PDF generation failed: %w", err)
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
			body = DefaultEmailBodyRendered(tokenData)
		}

		// Apply token substitution to subject as well.
		subject, _ = RenderEmailTokens(subject, "", tokenData)
	}

	// 10. Resolve template identity for send-time snapshot.
	// This captures which template ID/name was used so the email log is traceable
	// even after the template is later renamed or deactivated.
	tmplIDSnap, tmplNameSnap := resolveTemplateIdentity(db, &invoice, req.CompanyID)

	// 11. Build PDF attachment.
	safeNumber := strings.ReplaceAll(invoice.InvoiceNumber, "/", "-")
	safeNumber = strings.ReplaceAll(safeNumber, "\\", "-")
	attachment := &EmailAttachment{
		Filename:    "Invoice-" + safeNumber + ".pdf",
		ContentType: "application/pdf",
		Data:        pdfBytes,
	}

	// 12. Send email with PDF attachment.
	err = SendEmailWithAttachment(smtpCfg, req.ToEmail, subject, body, attachment)
	if err != nil {
		logEntry, logErr := createFailedEmailLog(db, req, fmt.Sprintf("SMTP send failed: %v", err), body, tmplIDSnap, tmplNameSnap)
		if logErr != nil {
			return nil, fmt.Errorf("email send failed: %w (log creation also failed: %v)", err, logErr)
		}
		return logEntry, fmt.Errorf("email send failed: %w", err)
	}

	// 13. Create successful email log.
	logEntry, err := createSuccessfulEmailLog(db, req, subject, body, tmplIDSnap, tmplNameSnap)
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
// bodyResolved, tmplIDSnap, tmplNameSnap capture the send-time presentation snapshot.
func createSuccessfulEmailLog(db *gorm.DB, req SendInvoiceEmailRequest, subject, bodyResolved string, tmplIDSnap *uint, tmplNameSnap string) (*models.InvoiceEmailLog, error) {
	now := time.Now()

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
// bodyResolved, tmplIDSnap, tmplNameSnap capture the send-time presentation state
// (what was attempted, even though delivery failed).
func createFailedEmailLog(db *gorm.DB, req SendInvoiceEmailRequest, errorMsg, bodyResolved string, tmplIDSnap *uint, tmplNameSnap string) (*models.InvoiceEmailLog, error) {
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
