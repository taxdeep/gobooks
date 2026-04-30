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
	CanCorrect                 bool
	CanReverse                 bool
	ReverseHint                string
}

type JournalEntryListVM struct {
	HasCompany bool
	Active     string
	Items      []JournalEntryListItem
	FormError  string
	Reversed   bool
	Voided     bool
	Corrected  bool

	// Filter state — echoed back into the filter bar so the URL fully
	// describes the result set.
	FilterQ            string // substring match against journal_no + line memos
	FilterAccount      string // raw account_id query param ("" = no filter)
	FilterAccountLabel string // resolved account "Name (Code)" for SmartPicker echo display
	FilterDateFrom     string // YYYY-MM-DD
	FilterDateTo       string // YYYY-MM-DD
}
