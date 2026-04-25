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

	// OpenDepositsJSON is a JSON array of unapplied Customer Deposits the
	// operator can consume as negative-direction documents in the Apply-
	// to-Invoice table. Each element mirrors the invoice shape so the
	// frontend can render a single unified table:
	//   {id, customer_id, document_number, document_date, original_amount, amount, type: "deposit"}
	// `original_amount` and `amount` are stored as positive strings; the
	// UI renders them with a leading minus sign.
	OpenDepositsJSON string

	// OpenCreditNotesJSON mirrors OpenDepositsJSON for issued/partially-
	// applied credit notes with BalanceRemaining > 0. Same payload shape
	// (type: "credit_note") so the unified Alpine table renders all
	// negative-direction documents through one code path.
	OpenCreditNotesJSON string

	// Form values (for re-render)
	PaymentMethod string
	CustomerID    string
	EntryDate     string
	BankAccountID string
	// InvoiceID / Amount are the legacy single-invoice / manual-amount
	// fields. Kept for the "no invoice selected, enter amount manually"
	// flow (unlinked payment on account).
	InvoiceID string
	Amount    string
	Memo      string
	// NewDepositAmount is the explicit "extra" field: when the operator
	// wants to record overpayment, they enter the excess here and it
	// creates a fresh CustomerDeposit on submit. Positive decimal string.
	NewDepositAmount string

	// InitialAllocations pre-seeds the Alpine allocation map on page
	// load. Lets the invoice detail page deep-link into Receive Payment
	// with a specific invoice pre-selected at its current balance due.
	// Format: JSON object { "invoice_id": "amount_string" }, "" = none.
	InitialAllocationsJSON string

	// Errors
	FormError            string
	PaymentMethodError   string
	CustomerError        string
	DateError            string
	BankError            string
	ARError              string
	InvoiceError         string
	AmountError          string
	NewDepositAmountError string

	Saved bool
}
