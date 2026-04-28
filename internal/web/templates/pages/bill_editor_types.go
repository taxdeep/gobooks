// 遵循project_guide.md
package pages

import "balanciz/internal/models"

// BillEditorVM is the view-model for the bill create/edit editor page.
type BillEditorVM struct {
	HasCompany bool
	// IsEdit is true when editing an existing draft; false for new bills.
	IsEdit    bool
	EditingID uint
	// ReviewLocked is true after a draft save when the editor re-opens in
	// review mode.
	ReviewLocked bool
	// SubmitPath is used by the locked-state Submit button.
	SubmitPath string

	// Header fields (form values).
	BillNumber string
	VendorID   string
	BillDate   string
	TermCode   string
	DueDate    string
	Memo       string

	// Header errors.
	BillNumberError   string
	VendorError       string
	DateError         string
	CurrencyError     string
	ExchangeRateError string
	LinesError        string
	FormError         string

	// WarehouseID is the selected warehouse for inventory routing (empty = company default).
	WarehouseID string

	// Dropdown data.
	Vendors         []models.Vendor
	Accounts        []models.Account
	TaxCodes        []models.TaxCode
	PaymentTerms    []models.PaymentTerm
	SelectableTasks []models.Task
	Warehouses      []models.Warehouse
	// Products is the catalog of ProductService rows for the line-level
	// Item picker (IN.1 / Rule #4 item-aware bill lines). Consumed as
	// the ProductsJSON dataset by the Alpine bill editor.
	Products []models.ProductService

	// Alpine initialisation JSON (set by handler, consumed by bill_editor.js).
	AccountsJSON     string
	TaxCodesJSON     string
	TasksJSON        string
	ProductsJSON     string
	InitialLinesJSON string
	// PaymentTermsJSON is a JSON array [{code, netDays}] for Alpine due-date calc.
	PaymentTermsJSON string
	// VendorsTermsJSON is a JSON object {"vendorId": "termCode"} for auto-fill.
	VendorsTermsJSON string
	// WarehousesJSON is a JSON array [{id, code, name, label, search}] used
	// by the type-ahead warehouse combobox. Present only when the company
	// has 2+ warehouses; the single-warehouse case skips the combobox.
	WarehousesJSON string

	// Line rows — used when re-rendering after a validation error.
	Lines []BillLineFormRow

	Saved bool

	// ── Multi-currency (Phase 6) ───────────────────────────────────────────
	// MultiCurrencyEnabled is true when the company has multi-currency turned on.
	MultiCurrencyEnabled bool
	// BaseCurrencyCode is the company's home currency (e.g. "CAD").
	BaseCurrencyCode string
	// CompanyCurrencies lists foreign currencies enabled for the company.
	CompanyCurrencies []models.CompanyCurrency
	// CurrencyCode is the currency selected for this bill (empty = base).
	CurrencyCode string
	// ExchangeRate is the manually-entered rate (base per 1 foreign unit).
	// Optional: if empty the posting service looks it up from exchange_rates.
	ExchangeRate string
	// QuickCreateCurrenciesJSON is a JSON array of currency codes available for
	// the inline New Vendor drawer.
	QuickCreateCurrenciesJSON string
}

// BillLineFormRow carries one line's form values (and optional error) for
// re-rendering after a validation failure.
//
// Rule #4 (Item-Nature Invariant) IN.1 additions:
//   - ProductServiceID: when set, this bill line is item-aware. Blank →
//     amount-only legacy line (no inventory effect, no Rule #4 action).
//   - Qty + Unit + UnitPrice: meaningful only when ProductServiceID is set.
//     The Bill save path uses these as the authoritative quantity / price
//     on the underlying BillLine row (replaces the hardcoded Qty=1,
//     UnitPrice=Amount fallback). When ProductServiceID is blank, these
//     fields are ignored and legacy Qty=1, UnitPrice=Amount persists.
type BillLineFormRow struct {
	ProductServiceID string
	ExpenseAccountID string
	Description      string
	Qty              string
	Unit             string
	UnitPrice        string
	Amount           string
	TaxCodeID        string
	TaskID           string
	IsBillable       bool
	// Computed by server after save.
	LineNet   string
	LineTax   string
	LineTotal string
	Error     string
}

// BillEditorTitle returns the page title.
func BillEditorTitle(vm BillEditorVM) string {
	if vm.IsEdit {
		return "Edit Bill"
	}
	return "New Bill"
}
