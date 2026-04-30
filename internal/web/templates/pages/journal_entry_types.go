// 遵循project_guide.md
package pages

import "balanciz/internal/models"

// JournalEntryVM provides data for the Journal Entry page.
type JournalEntryVM struct {
	HasCompany       bool
	ActiveCompanyID  uint // scopes client-side recent-account localStorage
	BaseCurrencyCode string
	// MultiCurrencyEnabled controls whether the JE currency selector is available.
	MultiCurrencyEnabled bool
	// CompanyCurrencies carries the enabled foreign currencies for the company.
	CompanyCurrencies []models.CompanyCurrency
	// TransactionCurrencyOptions is the explicit JE currency option list (base first).
	TransactionCurrencyOptions []string
	// DefaultTransactionCurrency is the initial JE transaction currency code.
	DefaultTransactionCurrency string

	// Dropdown data
	Accounts         []models.Account
	AccountsDataJSON string // script-safe JSON for account combobox (id, code, name, class)
	Customers        []models.Customer
	Vendors          []models.Vendor

	// UI messages
	FormError string
	Saved     bool
	Notice    string

	// Correction/edit flow. Posted journal entries are not mutated in place;
	// a correction form preloads the original values, saves a replacement JE,
	// and reverses the original in the same transaction.
	ReplaceJournalEntryID uint
	InitialDraftJSON      string
	DraftStorageSuffix    string
}
