// 遵循project_guide.md
package pages

import "balanciz/internal/models"

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

	// S2 (2026-04-25) — partially-invoiced Qty editing.
	// QtyMaxByLineID maps SalesOrderLine.ID → max-allowed qty (string,
	// formatted per IsStockItem) for the inline Qty input. Computed
	// once in the handler so the templ doesn't need to call the buffer
	// resolver per line.
	QtyMaxByLineID map[uint]string
	// QtyAdjusted is true when the page is loaded with ?qty_adjusted=1
	// (success flash after an adjust POST).
	QtyAdjusted bool
	// QtyError carries the human-readable failure from the last adjust
	// attempt (sent via ?qty_error=...). Empty when no error.
	QtyError string
}
