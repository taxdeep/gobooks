// 遵循project_guide.md
package pages

import "gobooks/internal/models"

// ── Quote VMs ─────────────────────────────────────────────────────────────────

// QuotesVM is the view model for the Quotes list page.
type QuotesVM struct {
	HasCompany bool
	Quotes     []models.Quote

	// Echoed filter values — feed back into the form inputs so the URL
	// fully describes the result set and is shareable.
	FilterStatus        string
	FilterCustomer      string // raw customer_id query param
	FilterCustomerLabel string // resolved customer name for SmartPicker echo display
	FilterDateFrom      string // YYYY-MM-DD
	FilterDateTo        string // YYYY-MM-DD

	Created   bool
	Saved     bool
	FormError string
}

// QuoteDetailVM is the view model for a single Quote detail / edit page.
type QuoteDetailVM struct {
	HasCompany       bool
	Quote            models.Quote
	Customers        []models.Customer
	TaxCodes         []models.TaxCode
	ProductServices  []models.ProductService
	Accounts         []models.Account // revenue accounts
	FormError        string
	Saved            bool
	Sent             bool
	Accepted         bool
	Rejected         bool
	Converted        bool
	Cancelled        bool
}

// ── SalesOrder VMs ────────────────────────────────────────────────────────────

// SalesOrdersVM is the view model for the Sales Orders list page.
type SalesOrdersVM struct {
	HasCompany bool
	Orders     []models.SalesOrder

	// Echoed filter values — feed back into the form inputs so the URL
	// fully describes the result set and is shareable.
	FilterStatus         string
	FilterCustomer       string // raw customer_id query param
	FilterCustomerLabel  string // resolved customer name for SmartPicker echo display
	FilterDateFrom       string // YYYY-MM-DD
	FilterDateTo         string // YYYY-MM-DD

	Created   bool
	Saved     bool
	FormError string
}

// SalesOrderDetailVM is the view model for a single SalesOrder detail / edit page.
type SalesOrderDetailVM struct {
	HasCompany      bool
	Order           models.SalesOrder
	Customers       []models.Customer
	TaxCodes        []models.TaxCode
	ProductServices []models.ProductService
	Accounts        []models.Account // revenue accounts
	FormError       string
	Saved           bool
	Confirmed       bool
	Cancelled       bool

	// LinkedInvoices are invoices raised against this SalesOrder.
	// Populated via invoices.sales_order_id (migration 085).
	// Ordered by invoice_date desc, id desc — most recent first.
	// Empty for Draft SOs and for Confirmed SOs without any
	// invoices yet. Rendered in a table on the read-only view.
	LinkedInvoices []models.Invoice
}
