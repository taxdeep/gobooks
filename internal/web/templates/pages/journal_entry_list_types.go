// 遵循project_guide.md
package pages

type JournalEntryListItem struct {
	ID                         uint
	EntryDate                  string
	JournalNo                  string
	LineCount                  int
	TotalDebit                 string
	TotalCredit                string
	TransactionCurrencyDisplay string
	ExchangeRateSourceLabel    string
	CanReverse                 bool
	ReverseHint                string
}

type JournalEntryListVM struct {
	HasCompany bool
	Active     string
	Items      []JournalEntryListItem
	FormError  string
	Reversed   bool
}
