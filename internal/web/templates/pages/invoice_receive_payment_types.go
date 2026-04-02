// 遵循project_guide.md
package pages

import "gobooks/internal/models"

// InvoiceReceivePaymentVM is the view-model for the invoice-specific
// Receive Payment page (/invoices/:id/receive-payment).
type InvoiceReceivePaymentVM struct {
	HasCompany bool

	// Invoice is the pre-loaded invoice being paid.
	// Preloaded: Invoice.Customer
	Invoice models.Invoice

	// Accounts is all active accounts, used to populate the Bank and AR dropdowns.
	Accounts []models.Account

	// Form values (re-populated on validation error)
	EntryDate     string
	BankAccountID string
	ARAccountID   string
	Memo          string

	// Errors
	FormError string
	DateError string
	BankError string
	ARError   string

	// JustSaved is set on redirect after a successful recording.
	JustSaved bool
}
