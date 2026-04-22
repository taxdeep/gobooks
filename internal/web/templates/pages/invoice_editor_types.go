// 遵循project_guide.md
package pages

import (
	"gobooks/internal/models"
)

// InvoiceEditorVM is the view-model for the invoice create/edit editor page.
type InvoiceEditorVM struct {
	HasCompany bool
	// IsEdit is true when editing an existing draft; false for new invoices.
	IsEdit    bool
	EditingID uint
	// ReviewLocked is true after a draft save when the editor re-opens in
	// review mode.
	ReviewLocked bool
	// TaskGeneratedReadOnly is true when this draft has active task invoice
	// sources and must stay read-only in the editor.
	TaskGeneratedReadOnly bool
	// SubmitPath is used by the locked-state Submit button.
	SubmitPath string
	// DeletePath is used by read-only task-generated drafts so users can delete
	// the whole draft and regenerate from Billable Work.
	DeletePath string
	// SaveTaskDraftPath is used by task-generated drafts to save the limited
	// editable fields (memo, tax codes, extra lines) without touching locked fields.
	SaveTaskDraftPath string

	// Header fields (form values).
	InvoiceNumber string
	CustomerID    string
	InvoiceDate   string
	TermCode      string
	DueDate       string
	Memo          string

	// CustomerPONumber is the reference number the customer quoted on their PO.
	// Prefilled from the sourcing SalesOrder on new invoices created via the
	// "Create Invoice" shortcut.
	CustomerPONumber string

	// Contact block — editable overrides that snapshot onto the invoice. On
	// the first render these are pre-filled from the Customer record (or SO
	// prefill). Operators can tweak them per-invoice without touching the
	// Customer record; the override-or-update prompt for pushing changes
	// back to Customer lives in a separate Phase 2 step.
	CustomerEmail string
	BillTo        string
	ShipTo        string
	ShipToLabel   string
	// AvailableShippingAddresses lists the customer's named shipping-address
	// rows for the ship-to dropdown (empty if the customer has none).
	AvailableShippingAddresses []ShippingAddressOption

	// SalesOrderID — when non-zero, the editor renders a hidden
	// input `sales_order_id` that the save path persists onto the
	// invoice. Populated on pre-fill via
	// `/invoices/new?sales_order_id=X` and carried forward on
	// draft re-saves. Used by the SO↔Invoice tracking chain
	// (services/sales_order_invoice_tracking.go).
	SalesOrderID uint
	// SalesOrderNumber is displayed as a "from Sales Order" badge
	// when SalesOrderID is set; purely decorative.
	SalesOrderNumber string

	// Header errors.
	InvoiceNumberError string
	CustomerError      string
	DateError          string
	CurrencyError      string
	ExchangeRateError  string
	LinesError         string
	FormError          string

	// WarehouseID is the selected warehouse for inventory routing (empty = company default).
	WarehouseID string

	// Dropdown data.
	Customers    []models.Customer
	PaymentTerms []models.PaymentTerm
	Warehouses   []models.Warehouse
	// Products contains only active ProductServices for this company.
	// Serialised to ProductsJSON for Alpine.
	Products []models.ProductService
	// TaxCodes contains only active TaxCodes for this company.
	// Serialised to TaxCodesJSON for Alpine.
	TaxCodes []models.TaxCode

	// Alpine initialisation JSON (set by handler, consumed by invoice_editor.js).
	ProductsJSON     string
	TaxCodesJSON     string
	InitialLinesJSON string
	// PaymentTermsJSON is a JSON array [{code, netDays}] for Alpine due-date calc.
	PaymentTermsJSON string
	// CustomersTermsJSON is a JSON object {"customerId": "termCode"} for auto-fill.
	CustomersTermsJSON string

	// Line rows — used when re-rendering after a validation error.
	Lines []InvoiceLineFormRow

	// Computed totals shown after server recalculation.
	Subtotal string
	TaxTotal string
	Total    string

	Saved bool

	// ── Multi-currency (Phase 6) ───────────────────────────────────────────
	// MultiCurrencyEnabled is true when the company has multi-currency turned on.
	MultiCurrencyEnabled bool
	// BaseCurrencyCode is the company's home currency (e.g. "CAD").
	BaseCurrencyCode string
	// CompanyCurrencies lists foreign currencies enabled for the company.
	CompanyCurrencies []models.CompanyCurrency
	// CurrencyCode is the currency selected for this invoice (empty = base).
	CurrencyCode string
	// ExchangeRate is the manually-entered rate (base per 1 foreign unit).
	// Optional: if empty the posting service looks it up from exchange_rates.
	ExchangeRate string
	// QuickCreateCurrenciesJSON is a JSON array of currency codes available for the
	// Quick Create Customer drawer, e.g. ["CAD","USD"]. Empty when multi-currency
	// is disabled (drawer hides the currency field in that case).
	QuickCreateCurrenciesJSON string
}

// InvoiceLineFormRow carries one line's form values (and optional error) for
// re-rendering after a validation failure or after a successful save.
type InvoiceLineFormRow struct {
	// LineID is the DB primary key of an existing InvoiceLine; empty for new lines.
	// Populated only when loading an existing draft for the edit page.
	// Used by the task-draft save handler to match locked lines for tax-code updates.
	LineID           string
	ProductServiceID string
	// ProductServiceLabel is the human-readable name shown in the Items
	// SmartPicker's visible input on edit-page rehydration. Populated server-
	// side from the Preloaded ProductService; empty for lines where the picker
	// should show the placeholder instead.
	ProductServiceLabel string
	Description         string
	Qty                 string
	UnitPrice           string
	TaxCodeID           string
	// Computed by server after save (shown read-only on re-render).
	LineNet   string
	LineTax   string
	LineTotal string
	Error     string
}

// ShippingAddressOption is one row in the customer's shipping-address catalogue
// rendered into the Invoice editor's ship-to dropdown.
type ShippingAddressOption struct {
	Label     string
	Address   string
	IsDefault bool
}

// InvoiceEditorTitle returns the page / drawer title.
func InvoiceEditorTitle(vm InvoiceEditorVM) string {
	if vm.IsEdit {
		return "Edit Invoice"
	}
	return "New Invoice"
}

// InvoiceEditorCustomerLabel returns the display name for the currently selected
// customer, used to pre-populate the SmartPicker visible input on edit pages.
// Returns an empty string (placeholder) when no matching customer is found.
func InvoiceEditorCustomerLabel(vm InvoiceEditorVM) string {
	for _, c := range vm.Customers {
		if Uitoa(c.ID) == vm.CustomerID {
			return c.Name
		}
	}
	return ""
}
