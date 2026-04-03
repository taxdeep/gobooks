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

	// BankAccounts are active bank accounts available for the receipt.
	BankAccounts []models.Account

	// Form values (re-populated on validation error)
	PaymentMethod string
	EntryDate     string
	BankAccountID string
	Amount        string
	Memo          string

	// Errors
	FormError          string
	DateError          string
	PaymentMethodError string
	BankError          string
	ARError            string
	AmountError        string

	// JustSaved is set on redirect after a successful recording.
	JustSaved bool
}
