// 遵循project_guide.md
package pages

import "gobooks/internal/models"

// PayBillsVM is the view model for the Pay Bills page.
type PayBillsVM struct {
	HasCompany bool

	OpenBills []models.Bill    // preloaded with Vendor; status posted or partially_paid
	Accounts  []models.Account // active accounts for bank + A/P dropdowns

	// Form values (repopulated on validation error)
	EntryDate     string
	BankAccountID string
	APAccountID   string
	Memo          string

	// Per-bill form values: keyed by bill ID string ("123" → "45.00")
	// Used to repopulate payment amount inputs after validation failure.
	BillAmounts map[string]string

	// Errors
	FormError string
	DateError string
	BankError string
	APError   string

	Saved bool
}
