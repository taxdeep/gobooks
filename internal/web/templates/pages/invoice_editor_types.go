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

	// Header errors.
	InvoiceNumberError string
	CustomerError      string
	DateError          string
	CurrencyError      string
	ExchangeRateError  string
	LinesError         string
	FormError          string

	// Dropdown data.
	Customers    []models.Customer
	PaymentTerms []models.PaymentTerm
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
}

// InvoiceLineFormRow carries one line's form values (and optional error) for
// re-rendering after a validation failure or after a successful save.
type InvoiceLineFormRow struct {
	// LineID is the DB primary key of an existing InvoiceLine; empty for new lines.
	// Populated only when loading an existing draft for the edit page.
	// Used by the task-draft save handler to match locked lines for tax-code updates.
	LineID           string
	ProductServiceID string
	Description      string
	Qty              string
	UnitPrice        string
	TaxCodeID        string
	// Computed by server after save (shown read-only on re-render).
	LineNet   string
	LineTax   string
	LineTotal string
	Error     string
}

// InvoiceEditorTitle returns the page / drawer title.
func InvoiceEditorTitle(vm InvoiceEditorVM) string {
	if vm.IsEdit {
		return "Edit Invoice"
	}
	return "New Invoice"
}
