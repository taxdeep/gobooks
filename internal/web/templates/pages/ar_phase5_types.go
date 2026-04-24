// 遵循project_guide.md
package pages

import "gobooks/internal/models"

// ── ARReturn VMs ──────────────────────────────────────────────────────────────

// ReturnsVM is the view model for the AR Returns list page.
type ReturnsVM struct {
	HasCompany bool
	Returns    []models.ARReturn

	FilterStatus        string
	FilterCustomer      string // raw customer_id query param
	FilterCustomerLabel string // resolved customer name for SmartPicker echo display
	FilterDateFrom      string // YYYY-MM-DD
	FilterDateTo        string // YYYY-MM-DD

	Created bool
}

// ReturnDetailVM is the view model for a single ARReturn detail / edit page.
type ReturnDetailVM struct {
	HasCompany bool
	Return     models.ARReturn
	Customers  []models.Customer
	Invoices   []models.Invoice // open invoices for same customer
	FormError  string
	Saved      bool
	Submitted  bool
	Approved   bool
	Rejected   bool
	Cancelled  bool
	Processed  bool
}

// ── ARRefund VMs ──────────────────────────────────────────────────────────────

// RefundsVM is the view model for the AR Refunds list page.
type RefundsVM struct {
	HasCompany bool
	Refunds    []models.ARRefund

	FilterStatus        string
	FilterCustomer      string // raw customer_id query param
	FilterCustomerLabel string // resolved customer name for SmartPicker echo display
	FilterDateFrom      string // YYYY-MM-DD
	FilterDateTo        string // YYYY-MM-DD

	Created bool
}

// RefundDetailVM is the view model for a single ARRefund detail / edit page.
type RefundDetailVM struct {
	HasCompany bool
	Refund     models.ARRefund
	Customers  []models.Customer
	Accounts   []models.Account // all active accounts for debit + bank pickers
	FormError  string
	Saved      bool
	Posted     bool
	Voided     bool
	Reversed   bool
}
