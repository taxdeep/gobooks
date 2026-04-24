// 遵循project_guide.md
package pages

import "gobooks/internal/models"

// ── CustomerDeposit VMs ───────────────────────────────────────────────────────

// DepositsVM is the view model for the Customer Deposits list page.
type DepositsVM struct {
	HasCompany bool
	Deposits   []models.CustomerDeposit

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

// DepositDetailVM is the view model for a single CustomerDeposit detail / edit page.
type DepositDetailVM struct {
	HasCompany bool
	Deposit    models.CustomerDeposit
	Customers  []models.Customer
	Accounts   []models.Account // bank + liability accounts for pickers
	Invoices   []models.Invoice // posted/sent invoices for the same customer
	FormError  string
	Saved      bool
	Posted     bool
	Voided     bool
	Applied    bool
}
