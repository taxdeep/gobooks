package web

import (
	"context"
	"strings"
	"testing"

	"gobooks/internal/models"
	"gobooks/internal/web/templates/pages"
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
		`x-text="fxSummary()"`,
		`Transaction Difference`,
		`Base Difference`,
		`/static/journal_entry_fx.js?v=2`,
		`text-right font-mono tabular-nums`,
		`bg-surface px-3 py-2 text-body text-text`,
		// JE Date drives FX date: @change handler must be wired on the date input.
		`@change="onDateChange()"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected journal entry HTML to contain %q", want)
		}
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

	// The strip renders the compact summary via fxSummary() and exposes
	// Override / Use stored rate toggle actions.
	for _, want := range []string{
		`x-text="fxSummary()"`,
		`x-text="fx.manual ? 'Use stored rate' : 'Override'"`,
		`@click="refreshFX()"`,
		`@click="toggleManualFX()"`,
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
		TxDebitTotal:   "100.00",
		TxCreditTotal:  "100.00",
		BaseDebitTotal: "137.00",
		BaseCreditTotal:"137.00",
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
