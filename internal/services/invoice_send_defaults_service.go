// 遵循project_guide.md
package services

import (
	"fmt"

	"balanciz/internal/models"
	"gorm.io/gorm"
)

// InvoiceSendDefaults holds the server-resolved defaults for the send email modal.
// Every field is computed using the same resolution pipeline as SendInvoiceByEmail,
// so the modal shows exactly what would be sent — no hidden divergence.
//
// This struct is read-only from the caller's perspective: it describes what the
// system would do if the user clicked "Send" without changing any fields.
type InvoiceSendDefaults struct {
	// Resolved recipient defaults
	ToEmail  string
	CCEmails string // always empty at default; user may add

	// Resolved email content (mirrors SendInvoiceByEmail step 9)
	Subject string // after token substitution
	Body    string // plain text, after token substitution

	// When BodyUsesSystemDefault is true, the modal can safely swap between the
	// attachment and no-attachment fallback copy if the user toggles the PDF
	// checkbox without otherwise editing the body.
	BodyUsesSystemDefault bool   `json:"-"`
	DefaultBodyAttachPDF string `json:"-"`
	DefaultBodyNoPDF     string `json:"-"`

	// Template identity at resolution time
	TemplateID     *uint  // nil when system fallback is used
	TemplateName   string // empty when system fallback is used
	TemplateSource string // "pinned" | "company_default" | "system_fallback"

	// Send gate
	SMTPReady        bool
	CanSend          bool
	EligibilityError string // non-empty when CanSend == false; human-readable

	// Invoice-level send summary
	SendCount int // invoice.SendCount; > 0 means already sent at least once

	// PDFAvailable is true when wkhtmltopdf is installed on the server.
	// When true, the send modal shows the "Attach PDF" checkbox (default checked).
	// When false, no attachment option is shown and email sends without PDF.
	// Uses the same PDFGeneratorAvailable() truth as the internal detail page
	// and the hosted invoice page.
	PDFAvailable bool
}

// GetInvoiceSendDefaults resolves the default values for the send email modal.
//
// Resolution uses the same pipeline as SendInvoiceByEmail so the modal cannot
// show content that differs from what would be sent. Business ineligibility
// (wrong status, missing email, SMTP not ready) is expressed as CanSend=false
// with a human-readable EligibilityError rather than an error return — the
// modal should show even when sending is blocked so the user can see why.
//
// Returns a non-nil error only on fatal DB failures.
func GetInvoiceSendDefaults(db *gorm.DB, companyID, invoiceID uint) (*InvoiceSendDefaults, error) {
	// Load invoice + customer for snapshot and token data.
	var inv models.Invoice
	if err := db.Where("id = ? AND company_id = ?", invoiceID, companyID).
		Preload("Customer").
		First(&inv).Error; err != nil {
		return nil, fmt.Errorf("invoice lookup failed: %w", err)
	}

	// ── SMTP readiness ─────────────────────────────────────────────────────────
	_, smtpReady, _ := EffectiveSMTPForCompany(db, companyID)

	// ── Eligibility (status + customer email snapshot) ─────────────────────────
	eligErr := ValidateInvoiceForSending(db, companyID, invoiceID)

	eligErrMsg := ""
	switch {
	case !smtpReady:
		eligErrMsg = "SMTP not configured or not verified — configure in Settings > Notifications"
	case eligErr != nil:
		eligErrMsg = eligErr.Error()
	}
	canSend := smtpReady && eligErr == nil

	// ── Template identity (mirrors SendInvoiceByEmail step 10) ─────────────────
	tmplID, tmplName := resolveTemplateIdentity(db, &inv, companyID)

	tmplSource := "system_fallback"
	if tmplID != nil {
		if inv.TemplateID != nil && *inv.TemplateID == *tmplID {
			tmplSource = "pinned"
		} else {
			tmplSource = "company_default"
		}
	}

	// ── Company name for token substitution ────────────────────────────────────
	var company models.Company
	if err := db.Select("name, base_currency_code").First(&company, companyID).Error; err != nil {
		company.Name = ""
		company.BaseCurrencyCode = "CAD"
	}

	// ── Subject + body resolution (mirrors SendInvoiceByEmail step 9) ──────────
	tokenData := buildEmailTokenData(&inv, company.Name)
	tmplCfg, _ := resolveTemplateEmailConfig(db, &inv, companyID)

	subject := tmplCfg.EmailDefaultSubject
	if subject == "" {
		subject = DefaultEmailSubject(inv.InvoiceNumber)
	}
	subject, _ = RenderEmailTokens(subject, "", tokenData)

	// Use PDFGeneratorAvailable() as the default send intent so the modal preview
	// shows the same wording the user will see in the actual email.
	pdfAvailable := PDFGeneratorAvailable()

	body := ""
	bodyUsesSystemDefault := tmplCfg.EmailDefaultBody == ""
	defaultBodyAttachPDF := ""
	defaultBodyNoPDF := ""
	if tmplCfg.EmailDefaultBody != "" {
		_, body = RenderEmailTokens("", tmplCfg.EmailDefaultBody, tokenData)
	} else {
		defaultBodyAttachPDF = DefaultEmailBodyRendered(tokenData, true)
		defaultBodyNoPDF = DefaultEmailBodyRendered(tokenData, false)
		body = DefaultEmailBodyRendered(tokenData, pdfAvailable)
	}

	return &InvoiceSendDefaults{
		ToEmail:          inv.CustomerEmailSnapshot,
		CCEmails:         "",
		Subject:          subject,
		Body:             body,
		BodyUsesSystemDefault: bodyUsesSystemDefault,
		DefaultBodyAttachPDF:  defaultBodyAttachPDF,
		DefaultBodyNoPDF:      defaultBodyNoPDF,
		TemplateID:       tmplID,
		TemplateName:     tmplName,
		TemplateSource:   tmplSource,
		SMTPReady:        smtpReady,
		CanSend:          canSend,
		EligibilityError: eligErrMsg,
		SendCount:        inv.SendCount,
		PDFAvailable:     pdfAvailable,
	}, nil
}
