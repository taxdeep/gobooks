// 遵循project_guide.md
package pages

import (
	"net/url"
	"strconv"
	"strings"
)

// advSearchSelectClass is the compact filter-bar styling for the
// /advanced-search page. Mirrors salesTxSelectClass density so the two
// QB-style transaction surfaces feel consistent.
func advSearchSelectClass() string {
	return "mt-2 block w-full rounded-md border border-border-input bg-surface px-2.5 py-1 text-small text-text outline-none focus:ring-2 focus:ring-primary-focus"
}

// AdvSearchPageEnd returns the 1-based "to" index for the pager strip
// ("Showing m–n of T"). Capped at Total because the last page may be
// short.
func AdvSearchPageEnd(vm AdvancedSearchVM) int {
	end := vm.Page * vm.PageSize
	if end > vm.Total {
		end = vm.Total
	}
	return end
}

// AdvSearchTotalPages computes the pager's denominator. Returns at least
// 1 so the "1 / 1" label still renders on an empty result set.
func AdvSearchTotalPages(vm AdvancedSearchVM) int {
	if vm.PageSize <= 0 {
		return 1
	}
	n := (vm.Total + vm.PageSize - 1) / vm.PageSize
	if n < 1 {
		return 1
	}
	return n
}

// AdvSearchPageHref builds a /advanced-search URL preserving the current
// filter state with `page` swapped to the requested value. Empty filter
// values are dropped from the query string so the URL stays readable
// when the operator is just paging through unfiltered results.
func AdvSearchPageHref(vm AdvancedSearchVM, page int) string {
	q := url.Values{}
	if vm.Query != "" {
		q.Set("q", vm.Query)
	}
	if vm.EntityType != "" {
		q.Set("type", vm.EntityType)
	}
	if vm.DateFrom != "" {
		q.Set("from", vm.DateFrom)
	}
	if vm.DateTo != "" {
		q.Set("to", vm.DateTo)
	}
	if vm.Status != "" {
		q.Set("status", vm.Status)
	}
	q.Set("page", strconv.Itoa(page))
	if vm.PageSize > 0 && vm.PageSize != 50 {
		q.Set("size", strconv.Itoa(vm.PageSize))
	}
	enc := q.Encode()
	if enc == "" {
		return "/advanced-search"
	}
	return "/advanced-search?" + enc
}

// AdvSearchEntityLabel returns the human display label for an entity type
// key (e.g. "invoice" → "Invoice"). Falls back to the raw key when the
// type isn't in the canonical option list — never empty so the table
// always shows something.
func AdvSearchEntityLabel(entityType string) string {
	for _, opt := range AdvancedSearchEntityOptions() {
		if opt.Value == entityType {
			return opt.Label
		}
	}
	if entityType == "" {
		return ""
	}
	// Fallback: title-case the raw key with underscores → spaces, so a
	// new producer that's not yet in the option list still renders a
	// readable label without code change.
	return strings.Title(strings.ReplaceAll(entityType, "_", " "))
}

// AdvSearchPayload safely extracts a string field from a Candidate's
// Payload map (which is nilable) — used by the templ to render the
// status / amount / currency / doc_num columns without panicking on
// rows that pre-date a payload field.
func AdvSearchPayload(payload map[string]string, key string) string {
	if payload == nil {
		return ""
	}
	return payload[key]
}

// AdvSearchEntityGroups returns the option list pre-bucketed for the
// dropdown's <optgroup> rendering. The order of groups matches the
// dropdown's display order: Transactions → Contacts → Products. Each
// group preserves the input order from AdvancedSearchEntityOptions.
func AdvSearchEntityGroups() []advSearchOptGroup {
	groups := []advSearchOptGroup{
		{Label: "Transactions"},
		{Label: "Contacts"},
		{Label: "Products"},
	}
	for _, opt := range AdvancedSearchEntityOptions() {
		for i := range groups {
			if groups[i].Label == opt.Group {
				groups[i].Options = append(groups[i].Options, opt)
				break
			}
		}
	}
	return groups
}

// advSearchOptGroup is a one-shot bucket struct used by the templ to
// render <optgroup>s. Local to this package because no other surface
// needs the grouped view.
type advSearchOptGroup struct {
	Label   string
	Options []EntityTypeOption
}
