// 遵循project_guide.md
package pages

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"balanciz/internal/services"
)

// salesTxSelectClass is the standard dark-mode-safe select styling for
// the Sales Transactions filter bar. Slightly more compact than the
// generic fieldClass (py-1 instead of py-2) to fit 5+ filter widgets
// on one row without wrapping.
func salesTxSelectClass() string {
	return "mt-2 block w-full rounded-md border border-border-input bg-surface px-2.5 py-1 text-small text-text outline-none focus:ring-2 focus:ring-primary-focus"
}

// salesTxMoneyFmt is a lightweight "$0.00" / "$179,618.04" formatter
// used in the KPI strip. Always prefixes a dollar sign — callers that
// need per-row currency display route through Money() / num-format.js.
func salesTxMoneyFmt(d decimal.Decimal) string {
	return "$" + d.StringFixed(2)
}

// salesTxCountLabel formats a KPI secondary label such as
// "4 overdue invoices" — switches between singular / plural based on
// the count. A zero count still renders (e.g. "0 quotes") so the strip
// keeps its five segments even on a fresh install.
func salesTxCountLabel(n int, singular, plural string) string {
	word := plural
	if n == 1 {
		word = singular
	}
	return strconv.Itoa(n) + " " + word
}

// salesTxKPIShares returns the five segment widths (0..1) used for the
// coloured proportional bar under the KPI strip. Empty / zero-total
// renders as a 4% sliver so every segment stays visible.
func salesTxKPIShares(kpi services.SalesTxKPI) [5]float64 {
	vals := [5]decimal.Decimal{
		kpi.QuotesOpenTotal,
		kpi.UnbilledTotal,
		kpi.OverdueTotal,
		kpi.OpenTotal,
		kpi.RecentlyPaidTotal,
	}
	total := decimal.Zero
	for _, v := range vals {
		if v.IsPositive() {
			total = total.Add(v)
		}
	}
	var out [5]float64
	if total.IsZero() {
		for i := range out {
			out[i] = 0.2
		}
		return out
	}
	totalF, _ := total.Float64()
	for i, v := range vals {
		f, _ := v.Float64()
		if f <= 0 || totalF <= 0 {
			out[i] = 0.04
			continue
		}
		share := f / totalF
		if share < 0.04 {
			share = 0.04
		}
		out[i] = share
	}
	return out
}

// salesTxBarWidth emits a "width:NN%" inline style for one segment of
// the proportional bar.
func salesTxBarWidth(share float64) string {
	pct := share * 100
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return fmt.Sprintf("width:%.1f%%", pct)
}

// salesTxTypeLabel returns the display label for a canonical type string.
// Used in the Type column of the transactions table.
func salesTxTypeLabel(t string) string {
	switch t {
	case services.SalesTxTypeInvoice:
		return "Invoice"
	case services.SalesTxTypeQuote:
		return "Quote"
	case services.SalesTxTypeSalesOrder:
		return "Sales order"
	case services.SalesTxTypePayment:
		return "Payment"
	case services.SalesTxTypeCreditNote:
		return "Credit memo"
	case services.SalesTxTypeReturn:
		return "Return"
	default:
		return t
	}
}

// salesTxStatusKind maps a row's (type, status) pair to a badge-colour
// kind: "success" | "warning" | "danger" | "info" | "neutral".
// Centralised here so the templ doesn't need per-type conditionals.
func salesTxStatusKind(r services.SalesTxRow) string {
	s := strings.ToLower(r.Status)
	switch r.Type {
	case services.SalesTxTypeInvoice:
		switch s {
		case "paid":
			return "success"
		case "overdue":
			return "danger"
		case "voided":
			return "neutral"
		case "draft":
			return "neutral"
		case "partially_paid":
			return "info"
		default:
			// issued / sent / unknown
			return "info"
		}
	case services.SalesTxTypeQuote:
		switch s {
		case "accepted":
			return "success"
		case "rejected", "cancelled":
			return "danger"
		case "sent":
			return "info"
		default:
			return "neutral"
		}
	case services.SalesTxTypeSalesOrder:
		switch s {
		case "confirmed":
			return "info"
		case "partially_invoiced":
			return "info"
		case "invoiced", "closed":
			return "success"
		case "cancelled":
			return "danger"
		default:
			return "neutral"
		}
	case services.SalesTxTypePayment:
		switch s {
		case "confirmed":
			return "success"
		case "draft":
			return "neutral"
		case "voided":
			return "danger"
		default:
			return "neutral"
		}
	case services.SalesTxTypeCreditNote:
		switch s {
		case "issued":
			return "info"
		case "applied", "fully_applied":
			return "success"
		case "voided":
			return "danger"
		default:
			return "neutral"
		}
	case services.SalesTxTypeReturn:
		switch s {
		case "approved", "processed":
			return "success"
		case "rejected", "cancelled":
			return "danger"
		case "submitted":
			return "info"
		default:
			return "neutral"
		}
	}
	return "neutral"
}

// salesTxStatusBadgeClass returns the compact pill class string for a
// row's status. Mirrors the colour map in salesTxStatusKind.
func salesTxStatusBadgeClass(r services.SalesTxRow) string {
	base := "inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium"
	switch salesTxStatusKind(r) {
	case "success":
		return base + " bg-success-soft text-success-hover"
	case "danger":
		return base + " bg-danger-soft text-danger-hover"
	case "warning":
		return base + " bg-warning-soft text-warning-hover"
	case "info":
		return base + " bg-primary-soft text-primary"
	default:
		return base + " bg-background text-text-muted2"
	}
}

// salesTxStatusLabel formats the status text. Invoices with overdue
// status show "Overdue N days" (matches QuickBooks reference) when the
// due date is known; everything else uses a simple TitleCase of the
// native status string.
func salesTxStatusLabel(r services.SalesTxRow) string {
	s := strings.ToLower(r.Status)
	if r.Type == services.SalesTxTypeInvoice && s == "overdue" && r.DueDate != nil {
		days := int(time.Since(*r.DueDate) / (24 * time.Hour))
		if days > 0 {
			return "Overdue " + strconv.Itoa(days) + "d"
		}
	}
	switch s {
	case "":
		return "—"
	case "partially_paid":
		return "Partially paid"
	case "partially_invoiced":
		return "Partially invoiced"
	case "fully_applied":
		return "Fully applied"
	}
	if len(s) == 0 {
		return s
	}
	return strings.ToUpper(s[:1]) + strings.ReplaceAll(s[1:], "_", " ")
}

// salesTxSelectionKey encodes one row's identity into a checkbox value.
// Form submits "type:id" so future batch-action handlers can parse back
// to a (type, id) tuple.
func salesTxSelectionKey(r services.SalesTxRow) string {
	return r.Type + ":" + strconv.FormatUint(uint64(r.ID), 10)
}

// salesTxRowsTotal sums the rendered rows' amounts for the table footer.
// Credit memos / returns already come in negative, so a plain Sum is correct.
func salesTxRowsTotal(rows []services.SalesTxRow) string {
	total := decimal.Zero
	for _, r := range rows {
		total = total.Add(r.Amount)
	}
	return total.StringFixed(2)
}

// salesTxPageEnd returns the index of the last row on the current page
// (1-based) for the "n–m of T" pager label.
func salesTxPageEnd(vm SalesTxVM) int {
	end := vm.Page * vm.PageSize
	if end > vm.Total {
		end = vm.Total
	}
	return end
}

// salesTxPageHref builds a URL preserving the current filter state plus
// a target page number. Preserves every filter the VM echoes back.
func salesTxPageHref(vm SalesTxVM, page int) string {
	q := salesTxQueryValues(vm)
	if vm.PageSize != 0 && vm.PageSize != 50 {
		q.Set("size", strconv.Itoa(vm.PageSize))
	}
	q.Set("page", strconv.Itoa(page))
	return "/sales-transactions?" + q.Encode()
}

func salesTxAPIHref(vm SalesTxVM) string {
	q := salesTxQueryValues(vm)
	if vm.Page > 1 {
		q.Set("page", strconv.Itoa(vm.Page))
	}
	if vm.PageSize != 0 && vm.PageSize != 50 {
		q.Set("size", strconv.Itoa(vm.PageSize))
	}
	if encoded := q.Encode(); encoded != "" {
		return "/api/sales-transactions?" + encoded
	}
	return "/api/sales-transactions"
}

func salesTxSortHref(vm SalesTxVM, field string) string {
	q := salesTxQueryValues(vm)
	q.Set("sort", field)
	q.Set("dir", salesTxNextSortDir(vm, field))
	if vm.PageSize != 0 && vm.PageSize != 50 {
		q.Set("size", strconv.Itoa(vm.PageSize))
	}
	q.Set("page", "1")
	return "/sales-transactions?" + q.Encode()
}

func salesTxQueryValues(vm SalesTxVM) url.Values {
	q := url.Values{}
	if vm.TypeFilter != "" {
		q.Set("type", vm.TypeFilter)
	}
	if vm.DateFilter != "" {
		q.Set("date", vm.DateFilter)
	}
	if vm.DateFrom != "" {
		q.Set("from", vm.DateFrom)
	}
	if vm.DateTo != "" {
		q.Set("to", vm.DateTo)
	}
	if vm.StatusFilter != "" {
		q.Set("status", vm.StatusFilter)
	}
	if vm.CustomerID != 0 {
		q.Set("customer_id", strconv.FormatUint(uint64(vm.CustomerID), 10))
	}
	if vm.Search != "" {
		q.Set("q", vm.Search)
	}
	if vm.SortBy != "" {
		q.Set("sort", vm.SortBy)
	}
	if vm.SortDir != "" {
		q.Set("dir", vm.SortDir)
	}
	return q
}

func salesTxNextSortDir(vm SalesTxVM, field string) string {
	sortBy, sortDir := services.NormalizeSalesTxSort(vm.SortBy, vm.SortDir)
	if sortBy == field {
		if sortDir == services.SalesTxSortAsc {
			return services.SalesTxSortDesc
		}
		return services.SalesTxSortAsc
	}
	switch field {
	case services.SalesTxSortType, services.SalesTxSortNumber, services.SalesTxSortCustomer, services.SalesTxSortStatus:
		return services.SalesTxSortAsc
	default:
		return services.SalesTxSortDesc
	}
}

func salesTxSortIcon(vm SalesTxVM, field string) string {
	sortBy, sortDir := services.NormalizeSalesTxSort(vm.SortBy, vm.SortDir)
	if sortBy != field {
		return ""
	}
	if sortDir == services.SalesTxSortAsc {
		return "↑"
	}
	return "↓"
}

func salesTxSortHeaderClass(extraClass string) string {
	base := "inline-flex items-center gap-1 hover:text-text"
	if strings.Contains(extraClass, "text-right") {
		return base + " w-full justify-end"
	}
	return base
}

func salesTxSortIconClass(vm SalesTxVM, field string) string {
	sortBy, _ := services.NormalizeSalesTxSort(vm.SortBy, vm.SortDir)
	if sortBy == field {
		return "text-primary"
	}
	return "text-text-muted3"
}
