// 遵循project_guide.md
package pages

import (
	"balanciz/internal/services/search_engine"
)

// AdvancedSearchVM is the view model for the /advanced-search page.
// Driven by GET query string — every field is operator-tweakable and
// echoes back to repopulate the form on re-render.
type AdvancedSearchVM struct {
	HasCompany bool

	// Echoed filter values — feed back into the form inputs so the URL
	// reflects current state and the page is shareable.
	Query      string
	EntityType string
	DateFrom   string // YYYY-MM-DD
	DateTo     string
	Status     string

	// Result page from the engine.
	Rows     []search_engine.Candidate
	Total    int
	Page     int
	PageSize int

	// EntityTypeOptions is the dropdown choice list — entity types we
	// have producers for, in display order (transactions first).
	EntityTypeOptions []EntityTypeOption

	// Mode is the engine mode that served this request (ent / legacy /
	// dual). Surfaced as a small footer label so the operator knows
	// when they're looking at a degraded fallback.
	Mode string
}

// EntityTypeOption represents one row in the entity-type dropdown.
type EntityTypeOption struct {
	Value string // raw entity_type key sent in the query string
	Label string // human label shown in the option
	Group string // grouping header in the dropdown ("Transactions" / "Contacts" / "Products")
}

// AdvancedSearchEntityOptions returns the canonical option list. Single
// source of truth so handler + templ + tests all see the same set.
// Order intentionally surfaces transaction types first — they're 90%
// of "advanced search" intent.
func AdvancedSearchEntityOptions() []EntityTypeOption {
	return []EntityTypeOption{
		// Transactions
		{Value: "invoice", Label: "Invoice", Group: "Transactions"},
		{Value: "bill", Label: "Bill", Group: "Transactions"},
		{Value: "quote", Label: "Quote", Group: "Transactions"},
		{Value: "sales_order", Label: "Sales Order", Group: "Transactions"},
		{Value: "purchase_order", Label: "Purchase Order", Group: "Transactions"},
		{Value: "customer_receipt", Label: "Payment", Group: "Transactions"},
		{Value: "expense", Label: "Expense", Group: "Transactions"},
		{Value: "journal_entry", Label: "Journal Entry", Group: "Transactions"},
		{Value: "credit_note", Label: "Credit Memo", Group: "Transactions"},
		{Value: "vendor_credit_note", Label: "Vendor Credit", Group: "Transactions"},
		{Value: "ar_return", Label: "Customer Return", Group: "Transactions"},
		{Value: "vendor_return", Label: "Vendor Return", Group: "Transactions"},
		{Value: "ar_refund", Label: "Customer Refund", Group: "Transactions"},
		{Value: "vendor_refund", Label: "Vendor Refund", Group: "Transactions"},
		{Value: "customer_deposit", Label: "Customer Deposit", Group: "Transactions"},
		{Value: "vendor_prepayment", Label: "Vendor Prepayment", Group: "Transactions"},
		// Contacts
		{Value: "customer", Label: "Customer", Group: "Contacts"},
		{Value: "vendor", Label: "Vendor", Group: "Contacts"},
		// Products
		{Value: "product_service", Label: "Product / Service", Group: "Products"},
	}
}
