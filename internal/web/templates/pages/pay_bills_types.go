// 遵循project_guide.md
package pages

import "balanciz/internal/models"

// PayBillsVM is the view model for the Pay Bills page.
type PayBillsVM struct {
	HasCompany bool

	OpenBills    []models.Bill    // preloaded with Vendor; status posted or partially_paid
	Accounts     []models.Account // active bank/credit-card accounts for Pay From dropdown
	BaseCurrency string           // company base currency code (e.g. "CAD")

	// AccountCurrencies maps account ID → currency code for fixed_foreign accounts.
	// Empty string means the account is base-currency (base_only).
	AccountCurrencies map[uint]string

	// Form values (repopulated on validation error)
	EntryDate     string
	BankAccountID string
	ExchangeRate  string // user-supplied override rate (bill currency → base); empty = auto-lookup
	Memo          string

	// Per-bill form values: keyed by bill ID string ("123" → "45.00")
	// Used to repopulate payment amount inputs after validation failure.
	BillAmounts map[string]string

	// Errors
	FormError         string
	DateError         string
	BankError         string
	ExchangeRateError string

	Saved bool
}
