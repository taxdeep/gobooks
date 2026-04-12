package pages

type JournalEntryDetailLineItem struct {
	AccountCode string
	AccountName string
	Memo        string
	PartyLabel  string
	TxDebit     string
	TxCredit    string
	Debit       string
	Credit      string
}

type JournalEntryDetailVM struct {
	HasCompany                 bool
	ID                         uint
	JournalNo                  string
	EntryDate                  string
	Status                     string
	BaseCurrencyCode           string
	TransactionCurrencyCode    string
	TransactionCurrencyDisplay string
	ExchangeRate               string
	ExchangeRateDate           string
	ExchangeRateSource         string
	ExchangeRateSourceLabel    string
	IsForeignCurrency          bool
	TransactionAmountsPresent  bool
	FXSnapshotNote             string
	Lines                      []JournalEntryDetailLineItem
	TxDebitTotal               string
	TxCreditTotal              string
	BaseDebitTotal             string
	BaseCreditTotal            string
}
