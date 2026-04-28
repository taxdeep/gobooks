// 遵循project_guide.md
package pages

import "balanciz/internal/models"

// ── CustomerReceipt VMs ───────────────────────────────────────────────────────

// ReceiptsVM is the view model for the Customer Receipts list page.
type ReceiptsVM struct {
	HasCompany bool
	Receipts   []models.CustomerReceipt

	// Echoed filter values — feed back into the form inputs so the URL
	// fully describes the result set and is shareable.
	FilterStatus        string
	FilterCustomer      string // raw customer_id query param
	FilterCustomerLabel string // resolved customer name for SmartPicker echo display
	FilterDateFrom      string // YYYY-MM-DD
	FilterDateTo        string // YYYY-MM-DD

	Created bool
	Saved   bool
}

// ReceiptDetailVM is the view model for a single CustomerReceipt detail / edit page.
type ReceiptDetailVM struct {
	HasCompany   bool
	Receipt      models.CustomerReceipt
	Customers    []models.Customer
	Accounts     []models.Account // bank accounts for picker
	Invoices     []models.Invoice // open invoices for same customer (apply form)
	Applications []models.PaymentApplication
	FormError    string
	Saved        bool
	Confirmed    bool
	Reversed     bool
	Voided       bool
	Applied      bool
	Unapplied    bool
}
