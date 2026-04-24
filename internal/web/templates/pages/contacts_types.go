// 遵循project_guide.md
package pages

import (
	"github.com/shopspring/decimal"
	"gobooks/internal/models"
	"gobooks/internal/services"
)

type CustomersVM struct {
	HasCompany bool

	FormError string
	Created   bool
	Updated   bool

	// Drawer state
	DrawerOpen bool
	EditingID  uint

	// Editable form fields (repopulated on validation failure)
	Name                   string
	Email                  string
	CurrencyCode           string
	DefaultPaymentTermCode string
	AddrStreet1            string
	AddrStreet2            string
	AddrCity               string
	AddrProvince           string
	AddrPostalCode         string
	AddrCountry            string

	NameError     string
	CurrencyError string

	// MultiCurrency indicates whether the company has multi-currency enabled.
	MultiCurrency    bool
	BaseCurrencyCode string
	Currencies       []models.Currency

	Customers    []models.Customer
	PaymentTerms []models.PaymentTerm

	BillableSummaries map[uint]services.CustomerBillableSummary

	// FilterQ is the search box value — substring match against name + email.
	FilterQ string
	// FilterStatus is one of "active" (default), "inactive", "all". Echoed
	// back into the Status select.
	FilterStatus string
	// InactiveCustomerCount is the total inactive count regardless of the
	// current filter. Surfaces in the Status select option label so the
	// operator sees how many records are hidden by the default filter.
	InactiveCustomerCount int
}

type CustomerNewVM struct {
	HasCompany bool

	Name                   string
	Email                  string
	CurrencyCode           string
	DefaultPaymentTermCode string
	AddrStreet1            string
	AddrStreet2            string
	AddrCity               string
	AddrProvince           string
	AddrPostalCode         string
	AddrCountry            string

	NameError     string
	CurrencyError string
	FormError     string

	// MultiCurrency indicates whether the company has multi-currency enabled.
	MultiCurrency    bool
	BaseCurrencyCode string
	Currencies       []models.Currency

	PaymentTerms []models.PaymentTerm
}

type CustomerDetailVM struct {
	HasCompany bool

	// Tab drives the active content pane. One of:
	//   "transactions" (default) — unified AR document list
	//   "quotes-orders"          — Quote + SalesOrder pipeline
	//   "billable-work"          — unbilled tasks/expenses summary
	//   "addresses"              — shipping address catalogue
	//   "details"                — editable profile + currency policy
	//   "notes"                  — future; placeholder for now
	Tab string

	Customer                models.Customer
	DefaultPaymentTermLabel string
	BillableSummary         services.CustomerBillableSummary
	ARSummary               services.CustomerARSummary
	OutstandingInvoices     []models.Invoice
	RecentInvoices          []models.Invoice
	MostRecentInvoice       *models.Invoice

	// Transactions is the unified AR-document feed rendered in the
	// Transactions tab. Populated by services.ListSalesTransactions with
	// a customer_id filter; empty when Tab != "transactions" (lazy load).
	Transactions []services.SalesTxRow
	// TxFilter* echo the query-string filters into the tab's filter bar
	// so the URL fully describes the current view.
	TxFilterType   string // "" or "all" / "invoices" / "payments" / ...
	TxFilterStatus string
	TxFilterFrom   string // YYYY-MM-DD
	TxFilterTo     string // YYYY-MM-DD

	// Commercial-commitment tables — mirror of vendor detail's Recent POs
	// section. Quotes precede sales orders in the AR chain
	// (Customer → Quote → SalesOrder → Invoice); showing both gives the
	// page a full pre-invoice pipeline view.
	RecentQuotes      []models.Quote
	RecentSalesOrders []models.SalesOrder

	// Batch 16: credit balance visibility
	CreditCount     int
	CreditRemaining decimal.Decimal

	// Refund quick-view — count + sum of POSTED refunds for this customer.
	// Draft / voided / reversed are excluded so the number reflects money
	// that actually went back to the customer. Surfaced next to credits in
	// the "Credits & Refunds" card strip.
	RefundCount int
	RefundTotal decimal.Decimal

	// Phase 12: currency policy management
	AllowedCurrencies []models.CustomerAllowedCurrency
	BaseCurrencyCode  string
	CurrencyPolicySaved bool
	CurrencyPolicyError string

	// Inline edit mode — set by ?edit=1 on the detail route. Mirrors the
	// VendorDetailVM edit fields so the two pages stay aligned.
	Editing bool
	Saved   bool // flash "Customer saved" banner after a successful round-trip

	// Form round-trip state (only populated in Editing mode or after a
	// validation failure re-render).
	FormName           string
	FormEmail          string
	FormCurrencyCode   string
	FormPaymentTerm    string
	FormAddrStreet1    string
	FormAddrStreet2    string
	FormAddrCity       string
	FormAddrProvince   string
	FormAddrPostalCode string
	FormAddrCountry    string

	NameError     string
	CurrencyError string
	FormError     string

	// Dropdown data for the edit form.
	PaymentTerms  []models.PaymentTerm
	MultiCurrency bool
	Currencies    []models.Currency

	// Lifecycle: drives the Delete / Deactivate / Reactivate button set in the
	// page header. HasRecords = true means any AR document references this
	// customer — full deletion is blocked; Deactivate is the only option.
	HasRecords    bool
	Deactivated   bool // flash banner: just deactivated
	Reactivated   bool // flash banner: just reactivated
	LifecycleErr  string

	// Migration 088: multi-shipping-address catalogue. Rendered as a dedicated
	// card on the detail page with inline add form + per-row delete / set-default.
	ShippingAddresses []models.CustomerShippingAddress
}

type VendorsVM struct {
	HasCompany bool

	Name                   string
	Email                  string
	Phone                  string
	Address                string
	CurrencyCode           string
	Notes                  string
	DefaultPaymentTermCode string
	NameError              string
	FormError              string
	Created                bool

	// MultiCurrency indicates whether the company has multi-currency enabled.
	// When false the currency selector is hidden and BaseCurrencyCode is shown read-only.
	MultiCurrency    bool
	BaseCurrencyCode string

	Vendors      []models.Vendor
	PaymentTerms []models.PaymentTerm
	Currencies   []models.Currency // enabled currencies (base + foreign); only used when MultiCurrency == true

	// FilterQ is the search box value — substring match against name + email.
	FilterQ string
	// FilterStatus is one of "active" (default), "inactive", "all".
	FilterStatus string
	// InactiveVendorCount is the total inactive count regardless of the
	// current filter. Surfaces in the Status select option label.
	InactiveVendorCount int
}
