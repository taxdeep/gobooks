// 遵循project_guide.md
package pages

import "gobooks/internal/models"

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

	// Dropdown data.
	Vendors      []models.Vendor
	Accounts     []models.Account
	TaxCodes     []models.TaxCode
	PaymentTerms []models.PaymentTerm

	// Alpine initialisation JSON (set by handler, consumed by bill_editor.js).
	AccountsJSON     string
	TaxCodesJSON     string
	InitialLinesJSON string
	// PaymentTermsJSON is a JSON array [{code, netDays}] for Alpine due-date calc.
	PaymentTermsJSON string
	// VendorsTermsJSON is a JSON object {"vendorId": "termCode"} for auto-fill.
	VendorsTermsJSON string

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
}

// BillLineFormRow carries one line's form values (and optional error) for
// re-rendering after a validation failure.
type BillLineFormRow struct {
	ExpenseAccountID string
	Description      string
	Amount           string
	TaxCodeID        string
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
