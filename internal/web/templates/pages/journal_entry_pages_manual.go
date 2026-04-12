package pages

import (
	"context"
	"fmt"
	"html"
	"io"
	"strings"

	"github.com/a-h/templ"

	"gobooks/internal/models"
	"gobooks/internal/web/templates/layout"
	"gobooks/internal/web/templates/ui"
)

const journalControlClass = "mt-2 block w-full rounded-md border border-border-input bg-surface px-3 py-2 text-body text-text placeholder:text-text-muted outline-none focus:ring-2 focus:ring-primary-focus"
const journalNumericControlClass = "mt-2 block w-full rounded-md border border-border-input bg-surface px-3 py-2 text-right font-mono tabular-nums text-body text-text placeholder:text-text-muted outline-none focus:ring-2 focus:ring-primary-focus"

func JournalEntryPage(vm JournalEntryVM) templ.Component {
	return layout.Layout(
		"GoBooks - Journal Entry",
		ui.SidebarVM{Active: "Journal Entry", HasCompany: vm.HasCompany},
		templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			_, err := io.WriteString(w, renderJournalEntryPageHTML(vm))
			return err
		}),
	)
}

func JournalEntryListPage(vm JournalEntryListVM) templ.Component {
	return layout.Layout(
		"GoBooks - Journal Entries",
		ui.SidebarVM{Active: "Journal Entry", HasCompany: vm.HasCompany},
		templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			_, err := io.WriteString(w, renderJournalEntryListHTML(vm))
			return err
		}),
	)
}

func JournalEntryDetailPage(vm JournalEntryDetailVM) templ.Component {
	return layout.Layout(
		"GoBooks - Journal Entry",
		ui.SidebarVM{Active: "Journal Entry", HasCompany: vm.HasCompany},
		templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			_, err := io.WriteString(w, renderJournalEntryDetailHTML(vm))
			return err
		}),
	)
}

func renderJournalEntryPageHTML(vm JournalEntryVM) string {
	var b strings.Builder
	write := func(s string) { _, _ = b.WriteString(s) }
	esc := html.EscapeString

	write(`<div class="max-w-[95%]" x-data="gobooksJournalEntryDraft()"`)
	write(` data-company-id="` + esc(Uitoa(vm.ActiveCompanyID)) + `"`)
	write(` data-base-currency="` + esc(vm.BaseCurrencyCode) + `"`)
	write(` data-default-currency="` + esc(vm.DefaultTransactionCurrency) + `">`)
	write(`<script type="application/json" id="gobooks-journal-accounts-data">` + vm.AccountsDataJSON + `</script>`)
	write(`<script type="application/json" id="gobooks-journal-currency-options">[`)
	for i, code := range vm.TransactionCurrencyOptions {
		if i > 0 {
			write(",")
		}
		write(`"` + esc(code) + `"`)
	}
	write(`]</script>`)
	write(`<div><div class="flex items-center justify-between gap-4"><div><h1 class="text-title font-semibold text-text">Journal Entry</h1><p class="mt-2 text-text-muted2">Posted journal entries now preserve an explicit transaction currency and immutable FX snapshot. The backend derives base-currency truth on save.</p></div><a href="/journal-entry/list" class="rounded-md border border-border-input px-3 py-2 text-body font-medium text-text-muted3 hover:bg-background">View Entries</a></div></div>`)
	if vm.Saved {
		write(`<div class="mt-4 rounded-md border border-success-border bg-success-soft p-4 text-body text-success-hover">Journal entry posted.</div>`)
	}
	if vm.FormError != "" {
		write(`<div class="mt-4 rounded-md border border-border-danger bg-danger-soft p-4 text-body text-danger-hover">` + esc(vm.FormError) + `</div>`)
	}

	write(`<form method="post" action="/journal-entry" class="mt-6 space-y-6" @submit="beforeSubmit($event)">`)
	write(`<div class="rounded-lg border border-border bg-surface p-6">`)

	// Header: 3-column grid — Currency | Date | Journal No.
	write(`<div class="grid grid-cols-1 gap-6 sm:grid-cols-2 lg:grid-cols-3">`)
	write(`<div><label class="block text-body font-medium text-text">Currency</label>`)
	write(`<select name="transaction_currency_code" x-model="header.transaction_currency_code" @change="onCurrencyChange($event)" class="` + journalControlClass + `">`)
	for _, code := range vm.TransactionCurrencyOptions {
		write(`<option value="` + esc(code) + `">` + esc(code) + `</option>`)
	}
	write(`</select>`)
	write(`<p class="mt-1 text-small text-text-muted2">Every journal entry stores an explicit transaction currency, including base-currency entries.</p></div>`)

	write(`<div><label class="block text-body font-medium text-text">Date *</label><input type="date" name="entry_date" x-model="header.entry_date" @change="onDateChange()" class="` + journalControlClass + `"/></div>`)

	write(`<div><label class="block text-body font-medium text-text">Journal No.</label><input type="text" name="journal_no" x-model="header.journal_no" placeholder="Optional, e.g. JE-001" class="` + journalControlClass + `"/><p class="mt-1 text-small text-text-muted2">Used as the immutable read-only reference after posting.</p></div>`)
	write(`</div>`)

	// Compact inline FX strip — visible only when a foreign currency is selected.
	// Hidden form inputs carry the snapshot values the backend validates on save.
	// Manual mode exposes editable rate and date inputs inline; non-manual shows a
	// read-only summary with Refresh and Override actions.
	write(`<div x-show="showFXBlock" x-cloak class="mt-4 flex flex-wrap items-center gap-x-4 gap-y-2 rounded-md border border-border-subtle bg-background/70 px-4 py-2.5">`)
	write(`<input type="hidden" name="exchange_rate_snapshot_id" :value="fx.snapshot_id || ''"/>`)
	write(`<input type="hidden" name="exchange_rate_source" :value="fx.source"/>`)
	write(`<input type="hidden" name="exchange_rate_date" :value="fx.date"/>`)
	write(`<input type="hidden" name="exchange_rate" :value="fx.rate"/>`)
	// Loading state
	write(`<span x-show="fx.loading" class="text-small text-text-muted2">Loading rate…</span>`)
	// Non-manual: compact summary using fxSummary()
	write(`<span x-show="!fx.loading && !fx.manual" class="font-mono text-body font-medium text-text" x-text="fxSummary()"></span>`)
	// Manual: editable rate input
	write(`<span x-show="!fx.loading && fx.manual" class="flex items-center gap-1.5">`)
	write(`<span class="font-mono text-body text-text">1 <span x-text="header.transaction_currency_code"></span> =</span>`)
	write(`<input type="text" x-model="fx.rate" inputmode="decimal" @input="recalc(); persist();" placeholder="0.00000000" class="w-28 rounded-md border border-border-input bg-surface px-2 py-1 text-right font-mono text-body text-text outline-none focus:ring-2 focus:ring-primary-focus"/>`)
	write(`<span class="font-mono text-body text-text" x-text="baseCurrencyCode"></span>`)
	write(`</span>`)
	// Manual: effective date override
	write(`<span x-show="!fx.loading && fx.manual" class="flex items-center gap-1.5">`)
	write(`<span class="text-small text-text-muted2">Date:</span>`)
	write(`<input type="date" x-model="fx.date" @change="recalc(); persist();" class="rounded-md border border-border-input bg-surface px-2 py-1 text-body text-text outline-none focus:ring-2 focus:ring-primary-focus"/>`)
	write(`</span>`)
	// Source badge
	write(`<span x-show="!fx.loading" class="rounded-full border border-border-input px-2.5 py-0.5 text-small font-medium text-text-muted2" x-text="fx.sourceLabel || 'Stored'"></span>`)
	// Effective date display (non-manual)
	write(`<span x-show="!fx.loading && !fx.manual" class="text-small text-text-muted3" x-text="fx.date"></span>`)
	// Refresh (non-manual only)
	write(`<button type="button" x-show="!fx.manual" @click="refreshFX()" :disabled="fx.loading" class="text-small font-medium text-primary hover:text-primary-hover disabled:opacity-40">Refresh</button>`)
	// Override / Use stored rate toggle
	write(`<button type="button" @click="toggleManualFX()" class="text-small font-medium text-text-muted3 hover:text-text" x-text="fx.manual ? 'Use stored rate' : 'Override'"></button>`)
	write(`</div>`)

	write(`</div>`)

	write(`<div class="rounded-lg border border-border bg-surface p-6"><div class="flex flex-wrap items-center justify-between gap-3"><div><h2 class="text-section font-semibold text-text">Lines</h2><p class="mt-1 text-small text-text-muted2">Enter debit and credit amounts in the selected transaction currency. Footer totals show both transaction and base values.</p></div><button type="button" class="inline-flex items-center gap-2 rounded-md border border-border-input px-3 py-2 text-body font-semibold text-primary hover:bg-background hover:text-primary-hover" @click="addLine()"><span>+</span><span>Add</span></button></div>`)
	write(`<div class="mt-4 overflow-x-auto"><table class="w-full min-w-[980px] table-fixed text-left text-body"><thead class="text-small uppercase tracking-wider text-text-muted"><tr class="border-b border-border"><th class="w-[28%] py-3 pr-4">Account</th><th class="w-[12%] py-3 pr-4" x-text="'Debit (' + header.transaction_currency_code + ')'"></th><th class="w-[12%] py-3 pr-4" x-text="'Credit (' + header.transaction_currency_code + ')'"></th><th class="w-[18%] py-3 pr-4">Name</th><th class="w-[24%] py-3 pr-4">Memo</th><th class="w-[6%] py-3 text-right">Action</th></tr></thead><tbody>`)
	write(`<template x-for="(line, idx) in lines" :key="line.key"><tr class="border-b border-border-subtle align-top">`)
	write(`<td class="py-3 pr-4 align-top"><div class="relative isolate z-0 block w-full min-w-0 max-w-full"><input type="hidden" :name="'lines[' + idx + '][account_id]'" :value="line.account_id"/><input type="text" autocomplete="off" placeholder="Search by code or name..." x-model="line.acctQuery" @focus="openAcctPicker(line)" @blur="onAcctBlur(line)" @input="onAcctQueryInput(line)" @keydown="onAcctKeydown(line, $event)" class="block w-full min-w-0 max-w-full rounded-md border bg-surface px-3 py-2 text-body text-text outline-none focus:ring-2 focus:ring-primary-focus" :class="line.errors.account ? 'border-danger' : 'border-border-input'"/><ul x-show="line.acctOpen" x-cloak class="absolute inset-x-0 top-full z-40 mt-0.5 max-h-48 overflow-y-auto overflow-x-hidden rounded-md border border-border-input bg-surface py-0.5 shadow-md" role="listbox"><template x-if="line.acctOpen && filteredAccounts(line).length === 0"><li class="px-2 py-1.5 text-small text-text-muted2"><span x-show="accounts.length === 0">No accounts available.</span><span x-show="accounts.length > 0">No matching accounts</span></li></template><template x-for="(acc, li) in filteredAccounts(line)" :key="acc.id"><li role="option" @mousedown.prevent="selectAccount(line, acc)" class="flex cursor-pointer items-center justify-between gap-2 px-2 py-1 text-small leading-snug hover:bg-background" :class="line.acctHi === li ? 'bg-primary/10' : ''"><div class="min-w-0 flex-1 truncate"><span class="font-mono font-medium tabular-nums text-text"><template x-for="(seg, si) in highlightSegments(acc.code, line.acctQuery)" :key="'c' + si"><span :class="seg.em ? 'font-semibold text-primary' : ''" x-text="seg.text"></span></template></span><span class="text-text-muted3"> - </span><span class="text-text"><template x-for="(seg, si) in highlightSegments(acc.name, line.acctQuery)" :key="'n' + si"><span :class="seg.em ? 'font-semibold text-primary' : ''" x-text="seg.text"></span></template></span></div><span class="max-w-[42%] shrink-0 truncate pl-1 text-right text-small text-text-muted3" x-text="acc.class" x-show="acc.class"></span></li></template></ul></div><div class="mt-1 text-small text-danger" x-show="line.errors.account" x-text="line.errors.account"></div></td>`)
	write(`<td class="py-3 pr-4 align-top"><input type="text" inputmode="decimal" :name="'lines[' + idx + '][debit]'" x-model="line.debit" @input="onDebitInput(line)" placeholder="0.00" class="block w-full rounded-md border border-border-input bg-surface px-3 py-2 text-right font-mono tabular-nums text-body text-text outline-none focus:ring-2 focus:ring-primary-focus"/><div class="mt-1 text-small text-danger" x-show="line.errors.amount" x-text="line.errors.amount"></div></td>`)
	write(`<td class="py-3 pr-4 align-top"><input type="text" inputmode="decimal" :name="'lines[' + idx + '][credit]'" x-model="line.credit" @input="onCreditInput(line)" placeholder="0.00" class="block w-full rounded-md border border-border-input bg-surface px-3 py-2 text-right font-mono tabular-nums text-body text-text outline-none focus:ring-2 focus:ring-primary-focus"/></td>`)
	write(`<td class="py-3 pr-4 align-top"><select :name="'lines[' + idx + '][party]'" x-model="line.party" class="block w-full rounded-md border border-border-input bg-surface px-3 py-2 text-body text-text outline-none focus:ring-2 focus:ring-primary-focus"><option value="">-- None --</option><optgroup label="Customers">`)
	for _, customer := range vm.Customers {
		write(`<option value="customer:` + esc(Uitoa(customer.ID)) + `">` + esc(customer.Name) + `</option>`)
	}
	write(`</optgroup><optgroup label="Vendors">`)
	for _, vendor := range vm.Vendors {
		write(`<option value="vendor:` + esc(Uitoa(vendor.ID)) + `">` + esc(vendor.Name) + `</option>`)
	}
	write(`</optgroup></select></td>`)
	write(`<td class="py-3 pr-4 align-top"><input type="text" :name="'lines[' + idx + '][memo]'" x-model="line.memo" class="block w-full rounded-md border border-border-input bg-surface px-3 py-2 text-body text-text outline-none focus:ring-2 focus:ring-primary-focus"/></td>`)
	write(`<td class="py-3 text-right align-top"><button type="button" class="rounded-md border border-border-input px-3 py-2 text-body font-semibold text-text-muted3 hover:bg-background hover:text-text disabled:cursor-not-allowed disabled:opacity-50" @click="removeLine(idx)" :disabled="lines.length <= 2">Remove</button></td>`)
	write(`</tr></template></tbody></table></div>`)

	write(`<div class="mt-6 grid grid-cols-1 gap-4 xl:grid-cols-[minmax(0,1fr)_24rem]"><div><div class="text-body text-danger-hover" x-show="primaryError" x-text="primaryError"></div><div class="mt-2 text-small text-text-muted2">Phase 1 FX policy converts each line individually using banker's rounding to 2 decimals and blocks save if base totals do not balance exactly.</div></div><div class="rounded-lg border border-border-subtle bg-background/70 p-4"><div class="space-y-2 text-body"><div class="flex items-center justify-between gap-4"><span class="text-text-muted2" x-text="'Transaction Debits (' + header.transaction_currency_code + ')'"></span><span class="font-mono font-semibold tabular-nums text-text" x-text="formatMoney(totals.txDebits)"></span></div><div class="flex items-center justify-between gap-4"><span class="text-text-muted2" x-text="'Transaction Credits (' + header.transaction_currency_code + ')'"></span><span class="font-mono font-semibold tabular-nums text-text" x-text="formatMoney(totals.txCredits)"></span></div><div class="flex items-center justify-between gap-4"><span class="text-text-muted2">Base Debits (` + esc(vm.BaseCurrencyCode) + `)</span><span class="font-mono font-semibold tabular-nums text-text" x-text="formatMoney(totals.baseDebits)"></span></div><div class="flex items-center justify-between gap-4"><span class="text-text-muted2">Base Credits (` + esc(vm.BaseCurrencyCode) + `)</span><span class="font-mono font-semibold tabular-nums text-text" x-text="formatMoney(totals.baseCredits)"></span></div><div class="border-t border-border-subtle pt-2"><div class="flex items-center justify-between gap-4"><span class="text-text-muted2">Transaction Difference</span><span class="font-mono font-semibold tabular-nums" :class="diffOk ? 'text-text' : 'text-danger-hover'" x-text="formatMoney(difference)"></span></div><div class="mt-2 flex items-center justify-between gap-4"><span class="text-text-muted2">Base Difference</span><span class="font-mono font-semibold tabular-nums" :class="baseDiffOk ? 'text-text' : 'text-danger-hover'" x-text="formatMoney(baseDifference)"></span></div></div></div></div></div>`)
	write(`<div class="mt-6 flex items-center justify-end gap-3"><button type="submit" :disabled="!canSave" :class="canSave ? 'bg-primary text-onPrimary hover:bg-primary-hover' : 'bg-disabled-bg text-disabled-text cursor-not-allowed'" class="rounded-md px-4 py-2 text-body font-semibold">Save</button></div>`)
	write(`</div></form></div><script src="/static/journal_entry_fx.js?v=2"></script>`)
	return b.String()
}

func renderJournalEntryListHTML(vm JournalEntryListVM) string {
	var b strings.Builder
	esc := html.EscapeString
	b.WriteString(`<div class="max-w-[95%]"><div class="flex items-center justify-between gap-4"><div><h1 class="text-title font-semibold text-text">Journal Entries</h1><p class="mt-2 text-text-muted2">Review posted entries, inspect immutable FX snapshots, and create reversing entries.</p></div><a href="/journal-entry" class="rounded-md bg-primary px-4 py-2 text-body font-semibold text-onPrimary hover:bg-primary-hover">New Entry</a></div>`)
	if vm.Reversed {
		b.WriteString(`<div class="mt-4 rounded-md border border-success-border bg-success-soft p-4 text-body text-success-hover">Reverse entry created successfully.</div>`)
	}
	if vm.FormError != "" {
		b.WriteString(`<div class="mt-4 rounded-md border border-border-danger bg-danger-soft p-4 text-body text-danger-hover">` + esc(vm.FormError) + `</div>`)
	}
	b.WriteString(`<div class="mt-6 rounded-lg border border-border bg-surface p-6"><div class="overflow-x-auto"><table class="w-full text-left text-body"><thead class="text-small uppercase tracking-wider text-text-muted"><tr class="border-b border-border"><th class="py-3 pr-4">ID</th><th class="py-3 pr-4">Date</th><th class="py-3 pr-4">Journal No.</th><th class="py-3 pr-4">Tx Currency</th><th class="py-3 pr-4">Lines</th><th class="py-3 pr-4">Debits</th><th class="py-3 pr-4">Credits</th><th class="py-3 pr-0">Actions</th></tr></thead><tbody class="text-text">`)
	for _, item := range vm.Items {
		b.WriteString(`<tr class="border-b border-border-subtle align-top"><td class="py-3 pr-4">` + esc(Uitoa(item.ID)) + `</td><td class="py-3 pr-4 whitespace-nowrap">` + esc(item.EntryDate) + `</td><td class="py-3 pr-4"><a href="/journal-entry/` + esc(Uitoa(item.ID)) + `" class="font-medium text-primary hover:text-primary-hover">` + esc(item.JournalNo) + `</a></td><td class="py-3 pr-4"><div class="font-medium text-text">` + esc(item.TransactionCurrencyDisplay) + `</div>`)
		if strings.TrimSpace(item.ExchangeRateSourceLabel) != "" {
			b.WriteString(`<div class="mt-1 text-small text-text-muted2">` + esc(item.ExchangeRateSourceLabel) + `</div>`)
		}
		b.WriteString(`</td><td class="py-3 pr-4">` + esc(Itoa(item.LineCount)) + `</td><td class="py-3 pr-4 font-mono tabular-nums">` + esc(item.TotalDebit) + `</td><td class="py-3 pr-4 font-mono tabular-nums">` + esc(item.TotalCredit) + `</td><td class="py-3 pr-0"><div class="flex flex-wrap items-center gap-2"><a href="/journal-entry/` + esc(Uitoa(item.ID)) + `" class="rounded-md border border-border-input px-3 py-2 text-body font-medium text-text hover:bg-background">View</a><form method="post" action="/journal-entry/` + esc(Uitoa(item.ID)) + `/reverse" class="flex items-center gap-2"><input type="date" name="reverse_date" class="rounded-md border border-border-input bg-surface px-3 py-2 text-body text-text"`)
		if !item.CanReverse {
			b.WriteString(` disabled`)
		}
		b.WriteString(`/><button type="submit" class="rounded-md border border-border-input px-3 py-2 text-body font-semibold text-text-muted3 hover:bg-background disabled:cursor-not-allowed disabled:bg-disabled-bg disabled:text-disabled-text"`)
		if !item.CanReverse {
			b.WriteString(` disabled`)
		}
		b.WriteString(`>Reverse</button></form></div>`)
		if item.ReverseHint != "" {
			b.WriteString(`<div class="mt-1 text-small text-text-muted2">` + esc(item.ReverseHint) + `</div>`)
		}
		b.WriteString(`</td></tr>`)
	}
	b.WriteString(`</tbody></table></div></div></div>`)
	return b.String()
}

func renderJournalEntryDetailHTML(vm JournalEntryDetailVM) string {
	var b strings.Builder
	esc := html.EscapeString
	title := vm.JournalNo
	if strings.TrimSpace(title) == "" {
		title = fmt.Sprintf("Journal Entry #%d", vm.ID)
	}
	transactionCurrencyLabel := strings.TrimSpace(vm.TransactionCurrencyDisplay)
	if transactionCurrencyLabel == "" {
		transactionCurrencyLabel = "Unavailable (legacy)"
	}
	rateLabel := "Unavailable"
	if strings.TrimSpace(vm.ExchangeRate) != "" && strings.TrimSpace(vm.TransactionCurrencyCode) != "" {
		rateLabel = "1 " + vm.TransactionCurrencyCode + " = " + vm.ExchangeRate + " " + vm.BaseCurrencyCode
	}
	effectiveDateLabel := strings.TrimSpace(vm.ExchangeRateDate)
	if effectiveDateLabel == "" {
		effectiveDateLabel = "Unavailable"
	}
	b.WriteString(`<div class="max-w-[95%] space-y-6"><div class="flex items-center justify-between gap-4"><div><h1 class="text-title font-semibold text-text">` + esc(title) + `</h1><p class="mt-2 text-text-muted2">Immutable posted journal-entry snapshot. Corrections should be made through reversal or adjustment entries, never silent mutation.</p></div><a href="/journal-entry/list" class="rounded-md border border-border-input px-3 py-2 text-body font-medium text-text-muted3 hover:bg-background">Back to Entries</a></div>`)
	b.WriteString(`<div class="grid grid-cols-1 gap-6 xl:grid-cols-[minmax(0,1fr)_20rem]"><div class="rounded-lg border border-border bg-surface p-6"><div class="grid grid-cols-1 gap-4 md:grid-cols-3"><div><div class="text-small uppercase tracking-wider text-text-muted">Date</div><div class="mt-1 text-body font-medium text-text">` + esc(vm.EntryDate) + `</div></div><div><div class="text-small uppercase tracking-wider text-text-muted">Status</div><div class="mt-1 text-body font-medium text-text">` + esc(vm.Status) + `</div></div><div><div class="text-small uppercase tracking-wider text-text-muted">Transaction Currency</div><div class="mt-1 text-body font-medium text-text">` + esc(transactionCurrencyLabel) + `</div></div></div>`)
	if strings.TrimSpace(vm.FXSnapshotNote) != "" {
		b.WriteString(`<div class="mt-4 rounded-md border border-warning-border bg-warning-soft p-4 text-body text-warning-hover">` + esc(vm.FXSnapshotNote) + `</div>`)
	}
	b.WriteString(`<div class="mt-6 overflow-x-auto"><table class="w-full min-w-[760px] text-left text-body"><thead class="text-small uppercase tracking-wider text-text-muted"><tr class="border-b border-border"><th class="py-3 pr-4">Account</th><th class="py-3 pr-4">Party</th><th class="py-3 pr-4">Memo</th><th class="py-3 pr-4">Tx Debit</th><th class="py-3 pr-4">Tx Credit</th><th class="py-3 pr-4">Base Debit</th><th class="py-3 pr-0">Base Credit</th></tr></thead><tbody class="text-text">`)
	for _, line := range vm.Lines {
		accountLabel := strings.TrimSpace(strings.TrimSpace(line.AccountCode) + " " + strings.TrimSpace(line.AccountName))
		b.WriteString(`<tr class="border-b border-border-subtle align-top"><td class="py-3 pr-4">` + esc(accountLabel) + `</td><td class="py-3 pr-4">` + esc(line.PartyLabel) + `</td><td class="py-3 pr-4">` + esc(line.Memo) + `</td><td class="py-3 pr-4 font-mono tabular-nums">` + esc(line.TxDebit) + `</td><td class="py-3 pr-4 font-mono tabular-nums">` + esc(line.TxCredit) + `</td><td class="py-3 pr-4 font-mono tabular-nums">` + esc(line.Debit) + `</td><td class="py-3 pr-0 font-mono tabular-nums">` + esc(line.Credit) + `</td></tr>`)
	}
	b.WriteString(`</tbody><tfoot><tr class="border-t border-border"><th class="py-3 pr-4 text-left text-text-muted2">Totals</th><th></th><th></th><th class="py-3 pr-4 font-mono tabular-nums text-text">` + esc(vm.TxDebitTotal) + `</th><th class="py-3 pr-4 font-mono tabular-nums text-text">` + esc(vm.TxCreditTotal) + `</th><th class="py-3 pr-4 font-mono tabular-nums text-text">` + esc(vm.BaseDebitTotal) + `</th><th class="py-3 pr-0 font-mono tabular-nums text-text">` + esc(vm.BaseCreditTotal) + `</th></tr></tfoot></table></div></div>`)
	b.WriteString(`<div class="rounded-lg border border-border bg-surface p-6"><h2 class="text-section font-semibold text-text">FX Snapshot</h2><p class="mt-1 text-small text-text-muted2">This snapshot is stored on the posted journal entry and reused by reversal logic.</p><dl class="mt-4 space-y-4 text-body"><div><dt class="text-small uppercase tracking-wider text-text-muted">Base Currency</dt><dd class="mt-1 font-medium text-text">` + esc(vm.BaseCurrencyCode) + `</dd></div><div><dt class="text-small uppercase tracking-wider text-text-muted">Snapshot Source</dt><dd class="mt-1 font-medium text-text">` + esc(vm.ExchangeRateSourceLabel) + `</dd></div><div><dt class="text-small uppercase tracking-wider text-text-muted">Effective Date</dt><dd class="mt-1 font-medium text-text">` + esc(effectiveDateLabel) + `</dd></div><div><dt class="text-small uppercase tracking-wider text-text-muted">Rate</dt><dd class="mt-1 font-mono font-medium tabular-nums text-text">` + esc(rateLabel) + `</dd></div></dl></div></div></div>`)
	return b.String()
}

func JournalPartyLabel(line models.JournalLine, customers map[uint]string, vendors map[uint]string) string {
	switch line.PartyType {
	case models.PartyTypeCustomer:
		if name := customers[line.PartyID]; name != "" {
			return "Customer: " + name
		}
		return "Customer #" + Uitoa(line.PartyID)
	case models.PartyTypeVendor:
		if name := vendors[line.PartyID]; name != "" {
			return "Vendor: " + name
		}
		return "Vendor #" + Uitoa(line.PartyID)
	default:
		return ""
	}
}
