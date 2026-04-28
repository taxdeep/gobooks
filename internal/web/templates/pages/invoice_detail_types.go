// 遵循project_guide.md
package pages

import (
	"balanciz/internal/models"
	"balanciz/internal/services"
)

// InvoiceDetailVM is the view-model for the read-only invoice detail page.
type InvoiceDetailVM struct {
	HasCompany bool

	// ActiveLink is the current active hosted invoice share link, or nil if none exists.
	// Shown in the Share Link management section for non-draft, non-voided invoices.
	ActiveLink *models.InvoiceHostedLink

	// NewShareURL holds the plaintext token returned after a create or regenerate action.
	// Populated from the ?newlink= query param on the redirect — shown once to the
	// authenticated user as a copy-this-link banner. Never stored server-side.
	NewShareURL string

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

	// Banner flags set via query string on redirect.
	JustVoided       bool   // ?voided=1
	JustIssued       bool   // ?issued=1
	JustSent         bool   // ?sent=1
	JustPaid         bool   // ?paid=1 or ?received=1
	JustTemplateBound bool  // ?tmplbound=1
	FormError        string // ?error=...
	VoidError        string // ?voiderror=...
	EmailError       string // ?emailerror=...

	// SendDefaults holds the server-resolved send modal defaults.
	// Non-nil for all sendable statuses (issued/sent/paid/partially_paid/overdue).
	// Nil for draft and voided invoices.
	// Uses the same resolution pipeline as SendInvoiceByEmail — what the modal
	// shows is exactly what would be sent.
	SendDefaults *services.InvoiceSendDefaults

	// EmailHistory contains all send attempts for this invoice, newest first.
	// Empty for drafts and freshly issued invoices.
	EmailHistory []models.InvoiceEmailLog

	// Templates contains all active company templates.
	// Populated only for draft invoices (for the bind-template action).
	Templates []models.InvoiceTemplate

	// Payment requests
	PaymentRequests    []models.PaymentRequest
	GatewayAccounts    []models.PaymentGatewayAccount
	JustPaymentCreated bool // ?paymentcreated=1

	// GatewayPaymentStatus reflects the latest HostedPaymentAttempt status for
	// this invoice. Empty string means no attempt exists. Set to
	// "payment_succeeded" when a verified webhook has confirmed a payment so the
	// detail page can show an operator-facing collection warning.
	GatewayPaymentStatus string

	// JustSettled is true when the page is loaded after a successful manual retry
	// (via ?settled=1 redirect). Used to show a one-time success banner.
	JustSettled bool

	// GatewaySettlementStatus is the settlement outcome from the latest
	// payment_succeeded attempt (Batch 12). Values: "" | "applied" |
	// "pending_review" | "failed". Empty means no settlement has been attempted.
	// Distinct from GatewayPaymentStatus (payment-side truth) and invoice Status
	// (accounting-side truth).
	GatewaySettlementStatus string

	// GatewaySettlementReason is the human-readable reason for a non-applied
	// settlement outcome. Empty when GatewaySettlementStatus is "" or "applied".
	GatewaySettlementReason string

	// IsChannelOrigin is true when this invoice was created from a channel order.
	// Payment gateway request buttons are hidden for channel-origin invoices.
	IsChannelOrigin bool

	// PDFAvailable is true when wkhtmltopdf is installed on the server.
	// When false, the Download PDF link is replaced with a Print link so that
	// internal users do not receive a 500 from a missing PDF engine.
	// Mirrors the CanDownload flag used on the hosted invoice page.
	PDFAvailable bool

	// CreditNoteApplications lists all credit note allocations against this invoice.
	// Populated for non-draft invoices.
	CreditNoteApplications []models.CreditNoteApplication
}
