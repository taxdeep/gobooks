// 遵循project_guide.md
package pages

import "balanciz/internal/models"

type ExpenseListVM struct {
	HasCompany bool

	FormError string
	Created   bool
	Updated   bool

	CanCreate bool
	CanUpdate bool

	Expenses []models.Expense
}

// ExpenseLineFormVM represents one line-item row on the expense form.
// It is used both for server-rendered edit-page rehydration (via Lines slice)
// and as the shape for JS data-initial-lines JSON.
type ExpenseLineFormVM struct {
	ExpenseAccountID string
	// ProductServiceID is the stringified optional catalog linkage.
	// Empty string means no product/service is linked — the line is
	// a pure cost-category entry.
	ProductServiceID string
	Description      string
	// Qty + UnitPrice are IN.2 authoritative fields when
	// ProductServiceID is set. For pure-expense (ProductServiceID="")
	// they may be blank and the service falls back to Qty=1,
	// UnitPrice=Amount.
	Qty       string
	UnitPrice string
	Amount    string // pre-tax net
	TaxCodeID string
	LineTax   string
	LineTotal string
	TaskID    string
	IsBillable bool
	Error      string
}

type ExpenseFormVM struct {
	HasCompany bool
	IsEdit     bool
	EditingID  uint

	// ExpenseNumber is the auto-assigned reference string shown in
	// the page header once the expense has been created. Empty on
	// the New form. Customisable via Settings → Company → Numbering
	// (module key "expense"). Not editable from the form.
	ExpenseNumber string

	ExpenseDate  string
	CurrencyCode string
	VendorID     string
	VendorLabel  string // human-readable label for SmartPicker rehydration; never a raw DB ID
	Notes        string

	// Lines holds the line-item rows. On new forms the handler seeds 2 blank rows.
	Lines []ExpenseLineFormVM

	// ExpenseAccountsJSON is the JSON-encoded list of expense accounts for the
	// line-item category <select>. Shape: [{id, code, name}].
	ExpenseAccountsJSON string

	// ProductsJSON is the JSON-encoded list of active catalog
	// ProductService rows for the per-line optional item picker.
	// Shape: [{id, sku, name, kind}] where kind is "stock" or
	// "service" — matches the PO line-item picker so both surfaces
	// present the same labelling to operators.
	ProductsJSON string

	// SelectableTasksJSON is the JSON-encoded list of selectable tasks for the
	// per-line task <select>. Shape: [{id, title, customer_name}].
	SelectableTasksJSON string

	// TaxCodesJSON is the JSON-encoded list of purchase-scope tax codes.
	// Shape: [{id, code, name, rate}] where rate is a fraction string e.g. "0.05".
	TaxCodesJSON string

	// Payment settlement fields (all optional for draft save;
	// PaymentAccountID required at post time per IN.2).
	PaymentAccountID    string
	PaymentAccountLabel string // human-readable label for SmartPicker rehydration
	PaymentMethod       string
	PaymentReference    string

	// IN.2 lifecycle + Rule #4 support.
	// Status is the IN.2 expense lifecycle: "draft" | "posted" | "voided".
	// Controls which action buttons show on the detail view.
	Status string
	// WarehouseID is the header warehouse string for stock-line routing
	// (Q3). Empty = fall back to company default at post time.
	WarehouseID string
	// Warehouses is the dropdown source for the header warehouse field.
	Warehouses []models.Warehouse
	// ReceiptRequired indicates the company's controlled-mode state.
	// When true, the template warns the operator that stock-item lines
	// will be rejected at post (Q2 invariant).
	ReceiptRequired bool

	// Error fields for service-layer feedback.
	ExpenseAccountError   string
	AmountError           string
	DescriptionError      string

	ExpenseDateError    string
	CurrencyError       string
	VendorError         string
	PaymentAccountError string
	PaymentMethodError  string
	FormError           string

	BaseCurrencyCode string
	MultiCurrency    bool
	CurrencyOptions  []string
}
