package pages

import (
	"context"
	"fmt"
	"html"
	"io"
	"strings"

	"github.com/a-h/templ"

	"balanciz/internal/models"
	"balanciz/internal/web/templates/layout"
	"balanciz/internal/web/templates/ui"
)

const journalControlClass = "mt-2 block w-full rounded-md border border-border-input bg-surface px-3 py-2 text-body text-text placeholder:text-text-muted outline-none focus:ring-2 focus:ring-primary-focus"
const journalNumericControlClass = "mt-2 block w-full rounded-md border border-border-input bg-surface px-3 py-2 text-right font-mono tabular-nums text-body text-text placeholder:text-text-muted outline-none focus:ring-2 focus:ring-primary-focus"

func JournalEntryPage(vm JournalEntryVM) templ.Component {
	return layout.Layout(
		"Balanciz - Journal Entry",
		ui.SidebarVM{Active: "Journal Entry", HasCompany: vm.HasCompany},
		templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			_, err := io.WriteString(w, renderJournalEntryPageHTML(vm))
			return err
		}),
	)
}

func JournalEntryListPage(vm JournalEntryListVM) templ.Component {
	return layout.Layout(
		"Balanciz - Journal Entries",
		ui.SidebarVM{Active: "Journal Entry", HasCompany: vm.HasCompany},
		templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			_, err := io.WriteString(w, renderJournalEntryListHTML(vm))
			return err
		}),
	)
}

func JournalEntryDetailPage(vm JournalEntryDetailVM) templ.Component {
	return layout.Layout(
		"Balanciz - Journal Entry",
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

	write(`<div class="max-w-[95%]" x-data="balancizJournalEntryDraft()"`)
	write(` data-company-id="` + esc(Uitoa(vm.ActiveCompanyID)) + `"`)
	write(` data-base-currency="` + esc(vm.BaseCurrencyCode) + `"`)
	write(` data-default-currency="` + esc(vm.DefaultTransactionCurrency) + `"`)
	write(` data-draft-suffix="` + esc(vm.DraftStorageSuffix) + `">`)
	write(`<script type="application/json" id="balanciz-journal-accounts-data">` + vm.AccountsDataJSON + `</script>`)
	if strings.TrimSpace(vm.InitialDraftJSON) != "" {
		write(`<script type="application/json" id="balanciz-journal-initial-draft">` + vm.InitialDraftJSON + `</script>`)
	}
	write(`<script type="application/json" id="balanciz-journal-currency-options">[`)
	for i, code := range vm.TransactionCurrencyOptions {
		if i > 0 {
			write(",")
		}
		write(`"` + esc(code) + `"`)
	}
	write(`]</script>`)
	pageTitle := "Journal Entry"
	pageHelp := "Posted journal entries now preserve an explicit transaction currency and immutable FX snapshot. The backend derives base-currency truth on save."
	if vm.ReplaceJournalEntryID != 0 {
		pageTitle = "Edit Journal Entry"
		pageHelp = "Saving creates a replacement journal entry and voids the original with a reversal, preserving the audit trail."
	}
	write(`<div><div class="flex items-center justify-between gap-4"><div><h1 class="text-title font-semibold text-text">` + esc(pageTitle) + `</h1><p class="mt-2 text-text-muted2">` + esc(pageHelp) + `</p></div><a href="/journal-entry/list" class="rounded-md border border-border-input px-3 py-2 text-body font-medium text-text-muted3 hover:bg-background">View Entries</a></div></div>`)
	if vm.Saved {
		write(`<div class="mt-4 rounded-md border border-success-border bg-success-soft p-4 text-body text-success-hover">Journal entry posted.</div>`)
	}
	if strings.TrimSpace(vm.Notice) != "" {
		write(`<div class="mt-4 rounded-md border border-warning-border bg-warning-soft p-4 text-body text-warning-hover">` + esc(vm.Notice) + `</div>`)
	}
	if vm.FormError != "" {
		write(`<div class="mt-4 rounded-md border border-border-danger bg-danger-soft p-4 text-body text-danger-hover">` + esc(vm.FormError) + `</div>`)
	}

	write(`<form method="post" action="/journal-entry" class="mt-6 space-y-6" @submit="beforeSubmit($event)">`)
	if vm.ReplaceJournalEntryID != 0 {
		write(`<input type="hidden" name="replace_journal_entry_id" value="` + esc(Uitoa(vm.ReplaceJournalEntryID)) + `"/>`)
	}
	write(`<div class="rounded-lg border border-border bg-surface p-6">`)

	// Header: 3-column grid — Currency (+ inline FX rate) | Date | Journal No.
	write(`<div class="grid grid-cols-1 gap-6 sm:grid-cols-2 lg:grid-cols-3">`)

	// Currency column: select + inline "1 TX = [rate] BASE" when foreign currency active.
	write(`<div>`)
	write(`<label class="block text-body font-medium text-text">Currency</label>`)
	write(`<div class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-2">`)
	write(`<select name="transaction_currency_code" x-model="header.transaction_currency_code" @change="onCurrencyChange($event)" class="min-w-0 flex-1 rounded-md border border-border-input bg-surface px-3 py-2 text-body text-text outline-none focus:ring-2 focus:ring-primary-focus">`)
	for _, code := range vm.TransactionCurrencyOptions {
		write(`<option value="` + esc(code) + `">` + esc(code) + `</option>`)
	}
	write(`</select>`)
	// Inline rate display — always editable; auto-populated from stored snapshot.
	write(`<div x-show="showFXBlock" x-cloak class="flex shrink-0 items-center gap-1.5">`)
	write(`<span class="font-mono text-small text-text-muted2">1 <span x-text="header.transaction_currency_code"></span> =</span>`)
	write(`<input type="text" x-model="fx.rate" inputmode="decimal" @input="onRateInput()" placeholder="0.00000000" class="w-24 rounded-md border border-border-input bg-surface px-2 py-1 text-right font-mono text-small text-text outline-none focus:ring-2 focus:ring-primary-focus"/>`)
	write(`<span class="font-mono text-small text-text-muted2" x-text="baseCurrencyCode"></span>`)
	write(`</div>`)
	write(`</div>`)
	// FX meta row: source · effective date · Refresh link
	write(`<div x-show="showFXBlock" x-cloak class="mt-1.5 flex flex-wrap items-center gap-x-1.5 gap-y-1 text-small text-text-muted3">`)
	write(`<span x-show="fx.loading">Loading rate…</span>`)
	write(`<span x-show="!fx.loading" x-text="fx.sourceLabel || 'Stored'"></span>`)
	write(`<span x-show="!fx.loading" aria-hidden="true">·</span>`)
	write(`<span x-show="!fx.loading" x-text="fx.date"></span>`)
	write(`<span x-show="!fx.loading" aria-hidden="true">·</span>`)
	write(`<button type="button" x-show="!fx.loading" @click="refreshFX()" :disabled="fx.loading" class="text-primary hover:text-primary-hover disabled:opacity-40">Refresh</button>`)
	write(`</div>`)
	// Hidden snapshot inputs (always submitted; backend ignores when base currency).
	write(`<input type="hidden" name="exchange_rate_snapshot_id" :value="fx.snapshot_id || ''"/>`)
	write(`<input type="hidden" name="exchange_rate_source" :value="fx.source"/>`)
	write(`<input type="hidden" name="exchange_rate_date" :value="fx.date"/>`)
	write(`<input type="hidden" name="exchange_rate" :value="fx.rate"/>`)
	write(`<p class="mt-1 text-small text-text-muted2">Every journal entry stores an explicit transaction currency, including base-currency entries.</p>`)
	write(`</div>`)

	write(`<div><label class="block text-body font-medium text-text">Date *</label><input type="date" name="entry_date" x-model="header.entry_date" @change="onDateChange()" class="` + journalControlClass + `"/></div>`)

	write(`<div><label class="block text-body font-medium text-text">Journal No.</label><input type="text" name="journal_no" x-model="header.journal_no" placeholder="Optional, e.g. JE-001" class="` + journalControlClass + `"/><p class="mt-1 text-small text-text-muted2">Used as the immutable read-only reference after posting.</p></div>`)
	write(`</div>`)

	write(`</div>`)

	write(`<div class="rounded-lg border border-border bg-surface p-6"><div class="flex flex-wrap items-center justify-between gap-3"><div><h2 class="text-section font-semibold text-text">Lines</h2><p class="mt-1 text-small text-text-muted2">Enter debit and credit amounts in the selected transaction currency. Footer totals show both transaction and base values.</p></div></div>`)
	write(`<div class="mt-4 overflow-visible"><table class="w-full min-w-[980px] table-fixed text-left text-body"><thead class="text-small uppercase tracking-wider text-text-muted"><tr class="border-b border-border"><th class="w-8 py-3"></th><th class="w-[28%] py-3 pr-4">Account</th><th class="w-[12%] py-3 pr-4" x-text="'Debit (' + header.transaction_currency_code + ')'"></th><th class="w-[12%] py-3 pr-4" x-text="'Credit (' + header.transaction_currency_code + ')'"></th><th class="w-[18%] py-3 pr-4">Name</th><th class="w-[24%] py-3 pr-4">Memo</th><th class="w-8 py-3 text-right"></th></tr></thead><tbody>`)
	write(`<template x-for="(line, idx) in lines" :key="line.key"><tr class="border-b border-border-subtle align-top" :class="line.acctOpen ? 'relative z-50' : 'relative z-0'">`)
	write(`<td class="py-3 pr-2 text-center align-top"><button type="button" @click="insertLineBelow(idx)" class="rounded p-1 text-text-muted3 hover:bg-background hover:text-primary" title="Insert line below"><svg xmlns="http://www.w3.org/2000/svg" class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2"><path stroke-linecap="round" stroke-linejoin="round" d="M12 4v16m8-8H4"/></svg></button></td>`)
	write(`<td class="relative overflow-visible py-3 pr-4 align-top"><div class="relative block w-full min-w-0 max-w-full" :class="line.acctOpen ? 'z-50' : 'z-0'"><input type="hidden" :name="'lines[' + idx + '][account_id]'" :value="line.account_id"/><input type="text" autocomplete="off" placeholder="Search by code or name..." x-model="line.acctQuery" @focus="openAcctPicker(line)" @blur="onAcctBlur(line)" @input="onAcctQueryInput(line)" @keydown="onAcctKeydown(line, $event, idx)" class="block w-full min-w-0 max-w-full rounded-md border bg-surface px-3 py-2 text-body text-text outline-none focus:ring-2 focus:ring-primary-focus" :class="line.errors.account ? 'border-danger' : 'border-border-input'"/><ul x-show="line.acctOpen" x-cloak class="absolute left-0 top-full z-50 mt-1 max-h-64 w-[360px] max-w-[40vw] overflow-y-auto overflow-x-hidden rounded-md border border-border-input bg-surface py-1 shadow-xl" role="listbox"><template x-if="line.acctOpen && filteredAccounts(line).length === 0"><li class="px-2 py-1.5 text-small text-text-muted2"><span x-show="accounts.length === 0">No accounts available.</span><span x-show="accounts.length > 0">No matching accounts</span></li></template><template x-for="(acc, li) in filteredAccounts(line)" :key="acc.id"><li role="option" @mousedown.prevent="selectAccount(line, acc, idx)" class="flex cursor-pointer items-center justify-between gap-2 px-2 py-1 text-small leading-snug hover:bg-background" :class="line.acctHi === li ? 'bg-primary/10' : ''"><div class="min-w-0 flex-1 truncate"><span class="font-mono font-medium tabular-nums text-text"><template x-for="(seg, si) in highlightSegments(acc.code, line.acctQuery)" :key="'c' + si"><span :class="seg.em ? 'font-semibold text-primary' : ''" x-text="seg.text"></span></template></span><span class="text-text-muted3"> - </span><span class="text-text"><template x-for="(seg, si) in highlightSegments(acc.name, line.acctQuery)" :key="'n' + si"><span :class="seg.em ? 'font-semibold text-primary' : ''" x-text="seg.text"></span></template></span></div><span class="max-w-[42%] shrink-0 truncate pl-1 text-right text-small text-text-muted3" x-text="acc.class" x-show="acc.class"></span></li></template></ul></div><div class="mt-1 text-small text-danger" x-show="line.errors.account" x-text="line.errors.account"></div></td>`)
	write(`<td class="py-3 pr-4 align-top"><input type="text" inputmode="decimal" :name="'lines[' + idx + '][debit]'" x-model="line.debit" @input="onDebitInput(line, idx)" placeholder="0.00" class="block w-full rounded-md border border-border-input bg-surface px-3 py-2 text-right font-mono tabular-nums text-body text-text outline-none focus:ring-2 focus:ring-primary-focus"/><div class="mt-1 text-small text-danger" x-show="line.errors.amount" x-text="line.errors.amount"></div></td>`)
	write(`<td class="py-3 pr-4 align-top"><input type="text" inputmode="decimal" :name="'lines[' + idx + '][credit]'" x-model="line.credit" @input="onCreditInput(line, idx)" placeholder="0.00" class="block w-full rounded-md border border-border-input bg-surface px-3 py-2 text-right font-mono tabular-nums text-body text-text outline-none focus:ring-2 focus:ring-primary-focus"/></td>`)
	write(`<td class="py-3 pr-4 align-top"><select :name="'lines[' + idx + '][party]'" x-model="line.party" @change="onLineTouched(line, idx)" class="block w-full rounded-md border border-border-input bg-surface px-3 py-2 text-body text-text outline-none focus:ring-2 focus:ring-primary-focus"><option value="">-- None --</option><optgroup label="Customers">`)
	for _, customer := range vm.Customers {
		write(`<option value="customer:` + esc(Uitoa(customer.ID)) + `">` + esc(customer.Name) + `</option>`)
	}
	write(`</optgroup><optgroup label="Vendors">`)
	for _, vendor := range vm.Vendors {
		write(`<option value="vendor:` + esc(Uitoa(vendor.ID)) + `">` + esc(vendor.Name) + `</option>`)
	}
	write(`</optgroup></select></td>`)
	write(`<td class="py-3 pr-4 align-top"><input type="text" :name="'lines[' + idx + '][memo]'" x-model="line.memo" @input="onLineTouched(line, idx)" class="block w-full rounded-md border border-border-input bg-surface px-3 py-2 text-body text-text outline-none focus:ring-2 focus:ring-primary-focus"/></td>`)
	write(`<td class="py-3 text-center align-top"><button type="button" class="rounded p-1 text-text-muted3 hover:text-danger disabled:cursor-not-allowed disabled:opacity-30" @click="removeLine(idx)" :disabled="lines.length <= 2" title="Remove line"><svg xmlns="http://www.w3.org/2000/svg" class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18L18 6M6 6l12 12"/></svg></button></td>`)
	write(`</tr></template></tbody></table></div>`)

	write(`<div class="mt-6 grid grid-cols-1 gap-4 xl:grid-cols-[minmax(0,1fr)_24rem]"><div><div class="text-body text-danger-hover" x-show="primaryError" x-text="primaryError"></div><div class="mt-2 text-small text-text-muted2">Phase 1 FX policy converts each line individually using banker's rounding to 2 decimals and blocks save if base totals do not balance exactly.</div></div><div class="rounded-lg border border-border-subtle bg-background/70 p-4"><div class="space-y-2 text-body"><div class="flex items-center justify-between gap-4"><span class="text-text-muted2" x-text="'Transaction Debits (' + header.transaction_currency_code + ')'"></span><span class="font-mono font-semibold tabular-nums text-text" x-text="formatMoney(totals.txDebits)"></span></div><div class="flex items-center justify-between gap-4"><span class="text-text-muted2" x-text="'Transaction Credits (' + header.transaction_currency_code + ')'"></span><span class="font-mono font-semibold tabular-nums text-text" x-text="formatMoney(totals.txCredits)"></span></div><div class="flex items-center justify-between gap-4"><span class="text-text-muted2">Base Debits (` + esc(vm.BaseCurrencyCode) + `)</span><span class="font-mono font-semibold tabular-nums text-text" x-text="formatMoney(totals.baseDebits)"></span></div><div class="flex items-center justify-between gap-4"><span class="text-text-muted2">Base Credits (` + esc(vm.BaseCurrencyCode) + `)</span><span class="font-mono font-semibold tabular-nums text-text" x-text="formatMoney(totals.baseCredits)"></span></div><div class="border-t border-border-subtle pt-2"><div class="flex items-center justify-between gap-4"><span class="text-text-muted2">Transaction Difference</span><span class="font-mono font-semibold tabular-nums" :class="diffOk ? 'text-text' : 'text-danger-hover'" x-text="formatMoney(difference)"></span></div><div class="mt-2 flex items-center justify-between gap-4"><span class="text-text-muted2">Base Difference</span><span class="font-mono font-semibold tabular-nums" :class="baseDiffOk ? 'text-text' : 'text-danger-hover'" x-text="formatMoney(baseDifference)"></span></div></div></div></div></div>`)
	write(`<div class="mt-6 flex items-center justify-end gap-3"><button type="submit" :disabled="!canSave" :class="canSave ? 'bg-primary text-onPrimary hover:bg-primary-hover' : 'bg-disabled-bg text-disabled-text cursor-not-allowed'" class="rounded-md px-4 py-2 text-body font-semibold">Save</button></div>`)
	write(`</div></form></div><script src="/static/journal_entry_fx.js?v=4"></script>`)
	return b.String()
}

func renderJournalEntryListHTML(vm JournalEntryListVM) string {
	var b strings.Builder
	esc := html.EscapeString
	b.WriteString(`<div class="max-w-[95%] space-y-4"><div class="flex items-center justify-between gap-4"><div><h1 class="text-title font-semibold text-text">Journal Entries</h1><p class="mt-2 text-text-muted2">Dense journal-entry register. Click a row to open the entry; use Edit on the right for manual corrections.</p></div><a href="/journal-entry" class="rounded-md bg-primary px-4 py-2 text-body font-semibold text-onPrimary hover:bg-primary-hover">New Entry</a></div>`)
	if vm.Reversed {
		b.WriteString(`<div class="rounded-md border border-success-border bg-success-soft p-4 text-body text-success-hover">Reverse entry created successfully.</div>`)
	}
	if vm.Voided {
		b.WriteString(`<div class="rounded-md border border-success-border bg-success-soft p-4 text-body text-success-hover">Journal entry voided with a reversal entry.</div>`)
	}
	if vm.Corrected {
		b.WriteString(`<div class="rounded-md border border-success-border bg-success-soft p-4 text-body text-success-hover">Replacement journal entry posted and the original was voided.</div>`)
	}
	if vm.FormError != "" {
		b.WriteString(`<div class="rounded-md border border-border-danger bg-danger-soft p-4 text-body text-danger-hover">` + esc(vm.FormError) + `</div>`)
	}

	b.WriteString(`<form method="get" action="/journal-entry/list" class="rounded-md border border-border bg-surface px-3 py-3">`)
	b.WriteString(`<div class="grid grid-cols-1 gap-3 md:grid-cols-[minmax(220px,1fr)_160px_160px_auto] md:items-end">`)
	b.WriteString(`<label class="block"><span class="text-small font-semibold uppercase tracking-wider text-text-muted">Search</span><input type="search" name="q" value="` + esc(vm.FilterQ) + `" placeholder="Journal no. or line memo..." class="mt-1 block w-full rounded-md border border-border-input bg-background px-3 py-2 text-body text-text outline-none focus:ring-2 focus:ring-primary-focus"/></label>`)
	b.WriteString(`<label class="block"><span class="text-small font-semibold uppercase tracking-wider text-text-muted">From</span><input type="date" name="from" value="` + esc(vm.FilterDateFrom) + `" class="mt-1 block w-full rounded-md border border-border-input bg-background px-3 py-2 text-body text-text outline-none focus:ring-2 focus:ring-primary-focus"/></label>`)
	b.WriteString(`<label class="block"><span class="text-small font-semibold uppercase tracking-wider text-text-muted">To</span><input type="date" name="to" value="` + esc(vm.FilterDateTo) + `" class="mt-1 block w-full rounded-md border border-border-input bg-background px-3 py-2 text-body text-text outline-none focus:ring-2 focus:ring-primary-focus"/></label>`)
	if strings.TrimSpace(vm.FilterAccount) != "" {
		b.WriteString(`<input type="hidden" name="account_id" value="` + esc(vm.FilterAccount) + `"/>`)
	}
	b.WriteString(`<div class="flex items-center gap-2"><button type="submit" class="rounded-md bg-primary px-3 py-2 text-body font-semibold text-onPrimary hover:bg-primary-hover">Apply</button><a href="/journal-entry/list" class="rounded-md border border-border-input px-3 py-2 text-body font-semibold text-text-muted3 hover:bg-background hover:text-text">Reset</a></div>`)
	b.WriteString(`</div>`)
	if strings.TrimSpace(vm.FilterAccountLabel) != "" {
		b.WriteString(`<div class="mt-2 text-small text-text-muted2">Filtered account: <span class="font-medium text-text">` + esc(vm.FilterAccountLabel) + `</span></div>`)
	}
	b.WriteString(`</form>`)

	b.WriteString(`<div class="overflow-hidden rounded-md border border-border bg-surface shadow-sm"><div class="overflow-x-auto"><table class="w-full min-w-[1120px] border-collapse text-left text-small"><thead class="sticky top-0 z-10 bg-background text-[11px] font-bold uppercase tracking-wider text-text-muted"><tr>`)
	b.WriteString(`<th class="border-b border-r border-border px-2 py-2">ID</th>`)
	b.WriteString(`<th class="border-b border-r border-border px-2 py-2">Date</th>`)
	b.WriteString(`<th class="border-b border-r border-border px-2 py-2">Journal No.</th>`)
	b.WriteString(`<th class="border-b border-r border-border px-2 py-2">Tx Currency</th>`)
	b.WriteString(`<th class="border-b border-r border-border px-2 py-2 text-right">Lines</th>`)
	b.WriteString(`<th class="border-b border-r border-border px-2 py-2 text-right">Debits</th>`)
	b.WriteString(`<th class="border-b border-r border-border px-2 py-2 text-right">Credits</th>`)
	b.WriteString(`<th class="border-b border-border px-2 py-2 text-right">Actions</th>`)
	b.WriteString(`</tr></thead><tbody class="text-text">`)
	if len(vm.Items) == 0 {
		b.WriteString(`<tr><td colspan="8" class="px-4 py-10 text-center text-text-muted2">No journal entries match this view.</td></tr>`)
	}
	for _, item := range vm.Items {
		itemID := esc(Uitoa(item.ID))
		b.WriteString(`<tr onclick="window.location.href='/journal-entry/` + itemID + `'" class="cursor-pointer border-b border-border-subtle align-top hover:bg-background/70">`)
		b.WriteString(`<td class="whitespace-nowrap border-r border-border-subtle px-2 py-2 font-mono text-text-muted2">` + itemID + `</td>`)
		b.WriteString(`<td class="whitespace-nowrap border-r border-border-subtle px-2 py-2">` + esc(item.EntryDate) + `</td>`)
		b.WriteString(`<td class="border-r border-border-subtle px-2 py-2"><a href="/journal-entry/` + itemID + `" onclick="event.stopPropagation()" class="font-semibold text-primary hover:text-primary-hover">` + esc(item.JournalNo) + `</a></td>`)
		b.WriteString(`<td class="border-r border-border-subtle px-2 py-2"><div class="font-medium text-text">` + esc(item.TransactionCurrencyDisplay) + `</div>`)
		if strings.TrimSpace(item.ExchangeRateSourceLabel) != "" {
			b.WriteString(`<div class="mt-1 text-small text-text-muted2">` + esc(item.ExchangeRateSourceLabel) + `</div>`)
		}
		b.WriteString(`</td>`)
		b.WriteString(`<td class="border-r border-border-subtle px-2 py-2 text-right font-mono tabular-nums">` + esc(Itoa(item.LineCount)) + `</td>`)
		b.WriteString(`<td class="border-r border-border-subtle px-2 py-2 text-right font-mono tabular-nums">` + esc(item.TotalDebit) + `</td>`)
		b.WriteString(`<td class="border-r border-border-subtle px-2 py-2 text-right font-mono tabular-nums">` + esc(item.TotalCredit) + `</td>`)
		b.WriteString(`<td class="px-2 py-2 text-right" onclick="event.stopPropagation()"><div class="flex flex-wrap items-center justify-end gap-2"><a href="/journal-entry/` + itemID + `" class="rounded-md border border-border-input px-2.5 py-1.5 text-small font-semibold text-text hover:bg-background">View</a>`)
		if item.CanCorrect {
			b.WriteString(`<a href="/journal-entry/` + itemID + `/edit" class="rounded-md bg-primary px-2.5 py-1.5 text-small font-semibold text-onPrimary hover:bg-primary-hover">Edit</a>`)
		} else {
			b.WriteString(`<span class="rounded-md border border-border-subtle px-2.5 py-1.5 text-small font-semibold text-text-muted2">Locked</span>`)
		}
		b.WriteString(`<form method="post" action="/journal-entry/` + itemID + `/void" class="flex items-center gap-1"><input type="date" name="reverse_date" class="w-32 rounded-md border border-border-input bg-surface px-2 py-1.5 text-small text-text"`)
		if !item.CanReverse {
			b.WriteString(` disabled`)
		}
		b.WriteString(`/><button type="submit" class="rounded-md border border-border-input px-2.5 py-1.5 text-small font-semibold text-text-muted3 hover:bg-background disabled:cursor-not-allowed disabled:bg-disabled-bg disabled:text-disabled-text"`)
		if !item.CanReverse {
			b.WriteString(` disabled`)
		}
		b.WriteString(`>Void</button></form></div>`)
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
	b.WriteString(`<div class="max-w-[95%] space-y-6"><div class="flex items-center justify-between gap-4"><div><h1 class="text-title font-semibold text-text">` + esc(title) + `</h1><p class="mt-2 text-text-muted2">Immutable posted journal-entry snapshot. Corrections create a replacement entry and a reversal, preserving the original amounts.</p></div><div class="flex flex-wrap items-center justify-end gap-2"><a href="/journal-entry/list" class="rounded-md border border-border-input px-3 py-2 text-body font-medium text-text-muted3 hover:bg-background">Back to Entries</a>`)
	if vm.CanCorrect {
		b.WriteString(`<a href="/journal-entry/` + esc(Uitoa(vm.ID)) + `/edit" class="rounded-md border border-border-input px-3 py-2 text-body font-semibold text-text hover:bg-background">Edit</a>`)
	}
	b.WriteString(`<form method="post" action="/journal-entry/` + esc(Uitoa(vm.ID)) + `/void" class="inline-flex items-center gap-2"><input type="date" name="reverse_date" class="rounded-md border border-border-input bg-surface px-3 py-2 text-body text-text"`)
	if !vm.CanReverse {
		b.WriteString(` disabled`)
	}
	b.WriteString(`/><button type="submit" class="rounded-md border border-border-danger px-3 py-2 text-body font-semibold text-danger hover:bg-danger-soft disabled:cursor-not-allowed disabled:bg-disabled-bg disabled:text-disabled-text"`)
	if !vm.CanReverse {
		b.WriteString(` disabled`)
	}
	b.WriteString(`>Void</button></form></div></div>`)
	if vm.ReverseHint != "" {
		b.WriteString(`<div class="rounded-md border border-border bg-surface px-4 py-3 text-body text-text-muted2">` + esc(vm.ReverseHint) + `</div>`)
	}
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
