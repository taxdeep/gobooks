// 遵循project_guide.md
package pages

import (
	"balanciz/internal/models"
	"balanciz/internal/services"

	"github.com/shopspring/decimal"
)

// VendorDetailVM is the view model for `/vendors/:id`. The AP-side mirror of
// CustomerDetailVM but deliberately leaner: there is no billable-work/tasks
// concept on the vendor side, and no VendorAPSummary service yet, so this
// VM focuses on what's immediately useful — vendor profile + open credit
// totals + recent bill activity — and leaves richer summary metrics for a
// future pass.
type VendorDetailVM struct {
	HasCompany bool

	// Tab drives the active content pane. One of:
	//   "transactions" (default) — unified AP document list
	//   "purchase-orders"        — PO pipeline
	//   "details"                — editable profile form
	//   "notes"                  — future; placeholder for now
	Tab string

	Vendor                  models.Vendor
	DefaultPaymentTermLabel string // human-readable term name (empty when code unset)

	// Transactions is the unified AP-document feed rendered in the
	// Transactions tab. Populated by services.ListPurchaseTransactions
	// with a vendor_id filter; empty when Tab != "transactions" (lazy load).
	Transactions []services.PurchaseTxRow
	// TxFilter* echo the query-string filters into the tab's filter bar
	// so the URL fully describes the current view.
	TxFilterType   string
	TxFilterStatus string
	TxFilterFrom   string // YYYY-MM-DD
	TxFilterTo     string // YYYY-MM-DD

	// Bills lists. Each is capped in the handler to keep the page snappy.
	OutstandingBills []models.Bill // status in {posted, partially_paid} ordered by due_date asc
	RecentBills      []models.Bill // newest-first, capped (any status)

	// Purchase orders — newest-first, capped. PO rows include any status
	// (draft / confirmed / partially_received / received / closed / cancelled)
	// so the page shows the full commitment history at a glance.
	RecentPOs []models.PurchaseOrder

	// Aggregate counts/totals for quick-scan header strip.
	OutstandingBillCount int
	OutstandingTotal     decimal.Decimal // sum of BalanceDue across OutstandingBills (doc currency — company base)
	OverdueBillCount     int             // outstanding bills whose due_date < today

	// Vendor-credit totals (sum of VCN RemainingAmount where status ∈
	// {posted, partially_applied}). Same number the /vendors/:id/credits
	// hub page shows at the top.
	CreditCount     int
	CreditRemaining decimal.Decimal

	// Edit mode — when Editing = true the detail page renders the vendor
	// details card as an inline form instead of static values. Entered via
	// `?edit=1` query; returned via re-render on validation error.
	Editing bool

	// Form state (only meaningful when Editing = true). On validation error
	// these hold the user's posted values so the form preserves them.
	// NameError / FormError drive field- and form-level error banners.
	FormName                   string
	FormEmail                  string
	FormPhone                  string
	FormAddress                string
	FormCurrencyCode           string
	FormNotes                  string
	FormDefaultPaymentTermCode string

	NameError string
	FormError string

	// Saved = true shows a green "Vendor saved" banner after a successful
	// round-trip (POST -> redirect back to GET with ?saved=1).
	Saved bool

	// Dropdown data for the edit form — same shape the /vendors list uses
	// for its inline create form.
	PaymentTerms     []models.PaymentTerm
	MultiCurrency    bool
	BaseCurrencyCode string
	Currencies       []models.Currency

	// Lifecycle state driving the Delete / Deactivate / Reactivate button set.
	// HasRecords = true when any AP document references this vendor.
	HasRecords   bool
	Deactivated  bool
	Reactivated  bool
	LifecycleErr string
}
