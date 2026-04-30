package web

import (
	"context"
	"strings"
	"testing"

	"balanciz/internal/models"
	"balanciz/internal/web/templates/pages"
)

func TestJournalEntryPage_UsesFXBlockDarkControlsAndSingleInitPath(t *testing.T) {
	vm := pages.JournalEntryVM{
		HasCompany:                 true,
		ActiveCompanyID:            42,
		BaseCurrencyCode:           "CAD",
		MultiCurrencyEnabled:       true,
		CompanyCurrencies:          []models.CompanyCurrency{{CompanyID: 42, CurrencyCode: "USD", IsActive: true}},
		TransactionCurrencyOptions: []string{"CAD", "USD"},
		DefaultTransactionCurrency: "CAD",
		DefaultJournalNo:           "JE-0001",
		AccountsDataJSON:           "[]",
	}

	var sb strings.Builder
	if err := pages.JournalEntryPage(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render journal entry page: %v", err)
	}
	html := sb.String()

	for _, want := range []string{
		`data-base-currency="CAD"`,
		`name="transaction_currency_code"`,
		`name="exchange_rate_snapshot_id"`,
		`@input="onRateInput()"`,
		`Transaction Difference`,
		`Base Difference`,
		`/static/journal_entry_fx.js?v=5`,
		`data-default-journal-no="JE-0001"`,
		`name="suggested_journal_no" value="JE-0001"`,
		`Auto-assigned by the system. You can edit it before saving.`,
		`text-right font-mono tabular-nums`,
		`bg-surface px-3 py-2 text-body text-text`,
		// JE Date drives FX date: @change handler must be wired on the date input.
		`@change="onDateChange()"`,
		`@click="insertLineBelow(idx)"`,
		`@click="removeLine(idx)"`,
		`:class="line.acctOpen ? 'relative z-50' : 'relative z-0'"`,
		`w-[360px] max-w-[40vw]`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected journal entry HTML to contain %q", want)
		}
	}
	journalNoIndex := strings.Index(html, `>Journal No.</label>`)
	currencyIndex := strings.Index(html, `>Currency</label>`)
	if journalNoIndex < 0 || currencyIndex < 0 || journalNoIndex > currencyIndex {
		t.Fatal("journal entry header should place Journal No. before Currency")
	}
	if strings.Contains(html, `@click="addLine()"><span>+</span><span>Add</span></button>`) {
		t.Fatal("journal entry lines should use per-row insert controls instead of a top-right add button")
	}
	if strings.Contains(html, `x-init="init()"`) {
		t.Fatal("journal entry page should rely on Alpine auto-init and must not call init() twice")
	}
	if strings.Contains(html, `class="mt-2 block w-full rounded-md border border-border-input px-3 py-2 text-body outline-none focus:ring-2 focus:ring-primary-focus"`) {
		t.Fatal("journal entry page should not use legacy white-box control classes without bg-surface/text-text tokens")
	}
}

func TestJournalEntryPage_CompactFXStrip(t *testing.T) {
	vm := pages.JournalEntryVM{
		HasCompany:                 true,
		ActiveCompanyID:            42,
		BaseCurrencyCode:           "CAD",
		MultiCurrencyEnabled:       true,
		CompanyCurrencies:          []models.CompanyCurrency{{CompanyID: 42, CurrencyCode: "USD", IsActive: true}},
		TransactionCurrencyOptions: []string{"CAD", "USD"},
		DefaultTransactionCurrency: "CAD",
		AccountsDataJSON:           "[]",
	}

	var sb strings.Builder
	if err := pages.JournalEntryPage(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render journal entry page: %v", err)
	}
	html := sb.String()

	// Compact FX strip must carry all hidden form inputs the backend requires.
	for _, want := range []string{
		`name="exchange_rate_snapshot_id"`,
		`name="exchange_rate_source"`,
		`name="exchange_rate_date"`,
		`name="exchange_rate"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("compact FX strip: expected hidden input %q", want)
		}
	}

	// Inline FX rate: always-editable input with onRateInput handler and Refresh link.
	for _, want := range []string{
		`@input="onRateInput()"`,
		`@click="refreshFX()"`,
		`x-text="header.transaction_currency_code"`,
		`x-text="baseCurrencyCode"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("compact FX strip: expected attribute/expression %q", want)
		}
	}

	// The old heavy panel heading and the old full Rate label/input pair must be gone.
	if strings.Contains(html, `>Exchange Rate<`) {
		t.Fatal("compact FX strip: old heavy 'Exchange Rate' section heading must not appear")
	}
	if strings.Contains(html, `Manual Override`) {
		t.Fatal("compact FX strip: old 'Manual Override' button label must not appear; use 'Override'")
	}
}

func TestJournalEntryListPage_UsesSingleRowFilterGrid(t *testing.T) {
	vm := pages.JournalEntryListVM{
		HasCompany:         true,
		FilterQ:            "11039.18",
		FilterDateFrom:     "2026-04-01",
		FilterDateTo:       "2026-04-30",
		FilterAccount:      "7",
		FilterAccountLabel: "Cash (1000)",
		Items: []pages.JournalEntryListItem{
			{ID: 70, EntryDate: "2099-12-31", JournalNo: "JE-0070", LineCount: 2, TotalDebit: "15,270.50", TotalCredit: "15,270.50"},
		},
	}

	var sb strings.Builder
	if err := pages.JournalEntryListPage(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render journal entry list page: %v", err)
	}
	html := sb.String()

	for _, want := range []string{
		`md:grid-cols-[minmax(220px,1fr)_160px_160px_auto]`,
		`md:items-end`,
		`whitespace-nowrap`,
		`Journal no. or line memo`,
		`name="from"`,
		`name="to"`,
		`space-y-3`,
		`overflow-hidden rounded-md border border-border bg-surface shadow-sm`,
		`<table class="w-full min-w-[1120px] border-collapse text-left text-small">`,
		`px-2 py-2 text-right font-mono tabular-nums`,
		`px-2.5 py-1.5 text-small font-semibold`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected journal entry list HTML to contain %q", want)
		}
	}
	if strings.Contains(html, `flex flex-wrap items-end gap-3`) {
		t.Fatal("journal entry list filters should use the single-row grid, not the old wrapping flex strip")
	}
	if !strings.Contains(html, `>Date</th>`) || !strings.Contains(html, `2099-12-31`) {
		t.Fatal("journal entry list table should keep the Date column")
	}
	if strings.Contains(html, `name="reverse_date"`) {
		t.Fatal("journal entry list actions should not render per-row reverse date inputs")
	}
}

func TestJournalEntryDetailPage_RendersImmutableFXSnapshotBlock(t *testing.T) {
	vm := pages.JournalEntryDetailVM{
		HasCompany:              true,
		ID:                      9,
		JournalNo:               "JE-900",
		EntryDate:               "2026-04-10",
		Status:                  "posted",
		BaseCurrencyCode:        "CAD",
		TransactionCurrencyCode: "USD",
		ExchangeRate:            "1.37000000",
		ExchangeRateDate:        "2026-04-10",
		ExchangeRateSourceLabel: "Latest",
		Lines: []pages.JournalEntryDetailLineItem{
			{AccountCode: "1000", AccountName: "Cash", TxDebit: "100.00", Debit: "137.00"},
			{AccountCode: "4000", AccountName: "Revenue", TxCredit: "100.00", Credit: "137.00"},
		},
		TxDebitTotal:    "100.00",
		TxCreditTotal:   "100.00",
		BaseDebitTotal:  "137.00",
		BaseCreditTotal: "137.00",
	}

	var sb strings.Builder
	if err := pages.JournalEntryDetailPage(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render journal entry detail page: %v", err)
	}
	html := sb.String()
	for _, want := range []string{
		`Immutable posted journal-entry snapshot`,
		`FX Snapshot`,
		`1 USD = 1.37000000 CAD`,
		`Latest`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected detail page HTML to contain %q", want)
		}
	}
}
