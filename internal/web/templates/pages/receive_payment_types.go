// 遵循project_guide.md
package pages

import "gobooks/internal/models"

type ReceivePaymentVM struct {
	HasCompany bool

	Customers    []models.Customer
	BankAccounts []models.Account // Asset · Bank accounts only

	// OpenInvoicesJSON is a JSON array of open invoices for Alpine.js table.
	// Each element: {id, customer_id, invoice_number, invoice_date, original_amount, amount, due_date}
	// amount = balance due (outstanding)
	OpenInvoicesJSON string

	// Form values (for re-render)
	PaymentMethod string
	CustomerID    string
	EntryDate     string
	BankAccountID string
	InvoiceID     string // optional — links payment to a specific invoice
	Amount        string
	Memo          string

	// Errors
	FormError          string
	PaymentMethodError string
	CustomerError      string
	DateError          string
	BankError          string
	ARError            string
	InvoiceError       string
	AmountError        string

	Saved bool
}
