// 遵循project_guide.md
package pages

import "gobooks/internal/models"

// InvoiceDetailVM is the view-model for the read-only invoice detail page.
type InvoiceDetailVM struct {
	HasCompany bool

	// Invoice is fully preloaded:
	//   Invoice.Customer
	//   Invoice.Lines (sorted by sort_order)
	//   Invoice.Lines[i].ProductService
	//   Invoice.Lines[i].TaxCode
	//   Invoice.JournalEntry (nil if not yet posted)
	Invoice models.Invoice

	// JournalNo is the human-readable journal entry number (e.g. "INV-IN001").
	// Empty when the invoice has not been posted.
	JournalNo string

	// SMTPReady indicates whether the company has a verified SMTP config.
	// Controls whether the "Send Email" button is enabled or disabled.
	SMTPReady bool

	// Banner flags set via query string on redirect.
	JustVoided bool   // ?voided=1
	JustIssued bool   // ?issued=1
	JustSent   bool   // ?sent=1
	JustPaid   bool   // ?paid=1
	VoidError   string // ?voiderror=...
	EmailError  string // ?emailerror=...

	// Payment requests
	PaymentRequests    []models.PaymentRequest
	GatewayAccounts    []models.PaymentGatewayAccount
	JustPaymentCreated bool // ?paymentcreated=1
}
