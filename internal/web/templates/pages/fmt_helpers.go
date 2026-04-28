// 遵循project_guide.md
package pages

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"balanciz/internal/services"
)

// Uitoa formats a uint as a string (handy in templates).
func Uitoa(id uint) string {
	return fmt.Sprintf("%d", id)
}

// Itoa formats an int as a string (handy in templates).
func Itoa(i int) string {
	return strconv.Itoa(i)
}

// QtyDisplay formats a line-item quantity according to the item's stock
// nature.  Stock-tracked inventory items are always whole units (you sell
// 8 watermelons, not 8.00 watermelons — slicing one into pieces is a BOM
// concern, not a line-item concern).  Service / non-inventory / other-charge
// keep the existing 2-decimal display so partial-unit pricing still works
// (e.g. 1.5 hours of consulting).
//
// Pass isStockItem from ProductService.IsStockItem; when no product is
// linked (free-text line), fall back to the 2-decimal form — we don't know
// the unit semantics, and over-truncating a 1.5 free-text qty would be
// surprising.
func QtyDisplay(qty decimal.Decimal, isStockItem bool) string {
	if isStockItem {
		return qty.Truncate(0).String()
	}
	return qty.StringFixed(2)
}

// QtyDisplayForLineProduct is the templ-friendly call site that pulls
// IsStockItem off a *ProductService pointer (nil = free-text line, falls
// back to the 2-decimal form).
func QtyDisplayForLineProduct(qty decimal.Decimal, ps *models.ProductService) string {
	if ps == nil {
		return qty.StringFixed(2)
	}
	return QtyDisplay(qty, ps.IsStockItem)
}

// QtyWithUOM renders "8 CASE" — the user-facing line qty paired with its
// snapshotted UOM. When the line UOM is the boring default ("EA") the UOM
// suffix is omitted to keep the columns tight; non-default UOMs surface
// inline so reviewers see the unit at a glance.
//
// stockUOM may be empty / unknown — the function still renders the line
// UOM. When non-empty AND different from lineUOM, a parenthetical
// "(192 BOTTLE in stock)" hint is appended so reviewers can follow the
// conversion without opening the catalog.
func QtyWithUOM(qty decimal.Decimal, isStockItem bool, lineUOM string, factor decimal.Decimal, stockUOM string) string {
	display := QtyDisplay(qty, isStockItem)
	uom := lineUOM
	if uom == "" {
		uom = "EA"
	}
	if uom == "EA" {
		// Default UOM — keep the cell terse.
		return display
	}
	out := display + " " + uom
	if stockUOM != "" && stockUOM != uom && factor.IsPositive() && !factor.Equal(decimal.NewFromInt(1)) {
		stockQty := qty.Mul(factor).Round(4)
		out += " (" + QtyDisplay(stockQty, true) + " " + stockUOM + " in stock)"
	}
	return out
}

// AccountRowClass styles inactive chart rows without changing overall table layout.
func AccountRowClass(a models.Account) string {
	if !a.IsActive {
		return "border-b border-border-subtle bg-background text-text-muted2"
	}
	return "border-b border-border-subtle"
}

// AccountClassificationLabel formats root · detail for tables.
func AccountClassificationLabel(a models.Account) string {
	return models.ClassificationDisplay(a.RootAccountType, a.DetailAccountType)
}

// FiscalYearEndMonth extracts the MM part from a "MM-DD" fiscal year end value.
func FiscalYearEndMonth(value string) string {
	if len(value) == 5 && value[2] == '-' {
		return value[:2]
	}
	return ""
}

// FiscalYearEndDay extracts the DD part from a "MM-DD" fiscal year end value.
func FiscalYearEndDay(value string) string {
	if len(value) == 5 && value[2] == '-' {
		return value[3:]
	}
	return ""
}

// invoiceBalanceDue returns the outstanding balance for an invoice.
// Uses BalanceDue if positive, otherwise falls back to the full Amount.
func invoiceBalanceDue(inv models.Invoice) decimal.Decimal {
	if inv.BalanceDue.GreaterThan(decimal.Zero) {
		return inv.BalanceDue
	}
	return inv.Amount
}

func invoiceDisplayStatus(inv models.Invoice) models.InvoiceStatus {
	return services.EffectiveInvoiceStatus(inv)
}

func invoicePaymentVisibility(inv models.Invoice) services.InvoicePaymentVisibility {
	return services.BuildInvoicePaymentVisibility(inv)
}

func invoicePaymentBadgeClass(v services.InvoicePaymentVisibility) string {
	base := "inline-block rounded px-2 py-0.5 text-small font-medium "
	switch v.State {
	case services.InvoicePaymentStatePaid:
		return base + "bg-success-soft text-success-hover border border-success-border"
	case services.InvoicePaymentStatePartiallyPaid:
		return base + "bg-warning-soft text-warning border border-warning-soft"
	default:
		return base + "bg-background text-text-muted border border-border"
	}
}

func paymentRequestDisplayLabel(status models.PaymentRequestStatus) string {
	return services.PaymentRequestStatusLabel(status)
}

func currencyTotalsLabel(totals []services.CurrencyTotal) string {
	if len(totals) == 0 {
		return Money(decimal.Zero)
	}
	parts := make([]string, 0, len(totals))
	for _, total := range totals {
		parts = append(parts, taskMoney(total.Amount, total.CurrencyCode))
	}
	return strings.Join(parts, " + ")
}

func traceRowLifecycleLabel(row services.TaskInvoiceTraceRow) string {
	switch {
	case row.IsActive:
		return "Active"
	case row.InvoiceID == nil:
		return "Released"
	default:
		return "Historical"
	}
}

func traceRowLifecycleBadgeClass(row services.TaskInvoiceTraceRow) string {
	base := "inline-block rounded px-2 py-0.5 text-small font-medium "
	switch {
	case row.IsActive:
		return base + "bg-success-soft text-success-hover border border-success-border"
	case row.InvoiceID == nil:
		return base + "bg-background text-text-muted2 border border-border"
	default:
		return base + "bg-warning-soft text-warning border border-warning-soft"
	}
}

func traceInvoiceHref(row services.TaskInvoiceTraceRow) string {
	if row.InvoiceID == nil || *row.InvoiceID == 0 {
		return ""
	}
	return "/invoices/" + Uitoa(*row.InvoiceID)
}

func customerBillableSummary(m map[uint]services.CustomerBillableSummary, customerID uint) services.CustomerBillableSummary {
	return services.CustomerSummaryOrZero(m, customerID)
}

// emailSendStatusBadgeClass returns the Tailwind badge class for an email send status.
func emailSendStatusBadgeClass(status models.EmailSendStatus) string {
	base := "inline-block rounded px-2 py-0.5 text-small font-medium "
	switch status {
	case models.EmailSendStatusSent:
		return base + "bg-success-soft text-success-hover border border-success-border"
	case models.EmailSendStatusFailed:
		return base + "bg-danger-soft text-danger border border-border-danger"
	default:
		return base + "bg-warning-soft text-warning border border-warning-soft"
	}
}

// templateSourceLabel returns a human-readable label for the template source field.
func templateSourceLabel(source string) string {
	switch source {
	case "pinned":
		return "pinned"
	case "company_default":
		return "company default"
	default:
		return "system fallback"
	}
}

// billBalanceDue returns the outstanding balance for a bill.
// Uses BalanceDue if positive, otherwise falls back to the full Amount.
func billBalanceDue(b models.Bill) decimal.Decimal {
	if b.BalanceDue.GreaterThan(decimal.Zero) {
		return b.BalanceDue
	}
	return b.Amount
}

// payBillsInitData generates the Alpine x-data attribute value for the Pay Bills page.
// It returns a JS function call: payBillsData(bills, accountCurrencies, baseCurrency)
func payBillsInitData(vm PayBillsVM) string {
	// Build bill array with pre-populated amounts for error-repopulation.
	var billsSB strings.Builder
	billsSB.WriteString("[")
	for i, b := range vm.OpenBills {
		if i > 0 {
			billsSB.WriteString(",")
		}
		bal := billBalanceDue(b)
		curr := b.CurrencyCode
		if curr == "" {
			curr = vm.BaseCurrency
		}
		preAmt := "0.00"
		if vm.BillAmounts != nil {
			if a, ok := vm.BillAmounts[fmt.Sprintf("%d", b.ID)]; ok && a != "" {
				preAmt = a
			}
		}
		billsSB.WriteString(fmt.Sprintf(
			`{"id":"%d","balance":"%s","currency":"%s","amount":"%s"}`,
			b.ID, bal.StringFixed(2), curr, preAmt))
	}
	billsSB.WriteString("]")

	// Build accountCurrencies object: {"1":"USD","2":"EUR"}
	var accSB strings.Builder
	accSB.WriteString("{")
	first := true
	for id, code := range vm.AccountCurrencies {
		if !first {
			accSB.WriteString(",")
		}
		accSB.WriteString(fmt.Sprintf(`"%d":"%s"`, id, code))
		first = false
	}
	accSB.WriteString("}")

	baseCurr := vm.BaseCurrency
	if baseCurr == "" {
		baseCurr = "CAD"
	}
	return fmt.Sprintf("payBillsData(%s,%s,%q)", billsSB.String(), accSB.String(), baseCurr)
}

// currencyFlag returns the Unicode flag emoji for a well-known ISO 4217 currency code,
// or an empty string when the currency has no obvious single-country flag.
func currencyFlag(code string) string {
	switch code {
	case "CAD":
		return "🇨🇦"
	case "USD":
		return "🇺🇸"
	case "EUR":
		return "🇪🇺"
	case "GBP":
		return "🇬🇧"
	case "AUD":
		return "🇦🇺"
	case "NZD":
		return "🇳🇿"
	case "JPY":
		return "🇯🇵"
	case "CNY":
		return "🇨🇳"
	case "HKD":
		return "🇭🇰"
	case "SGD":
		return "🇸🇬"
	case "CHF":
		return "🇨🇭"
	case "SEK":
		return "🇸🇪"
	case "NOK":
		return "🇳🇴"
	case "DKK":
		return "🇩🇰"
	case "MXN":
		return "🇲🇽"
	case "BRL":
		return "🇧🇷"
	case "INR":
		return "🇮🇳"
	case "KRW":
		return "🇰🇷"
	default:
		return ""
	}
}

// billDueStatusFromBill combines billDaysFromToday + billDueStatus for use in templates.
func billDueStatusFromBill(b models.Bill) string {
	return billDueStatus(b, billDaysFromToday(b))
}

// billDueStatus returns "overdue", "today", "soon" (≤7 days), or "ok".
func billDueStatus(b models.Bill, daysFromToday int) string {
	if daysFromToday < 0 {
		return "overdue"
	}
	if daysFromToday == 0 {
		return "today"
	}
	if daysFromToday <= 7 {
		return "soon"
	}
	return "ok"
}

// fieldBorderClass returns the appropriate Tailwind border class based on whether
// the field has a validation error.
func fieldBorderClass(hasError bool) string {
	if hasError {
		return "border-border-danger"
	}
	return "border-border-input"
}

// totalBillBalanceDue sums the balance due across all bills (for the footer total).
func totalBillBalanceDue(bills []models.Bill) decimal.Decimal {
	var total decimal.Decimal
	for _, b := range bills {
		total = total.Add(billBalanceDue(b))
	}
	return total
}

// customerDefaultCurrLabel returns a display label for the customer's default currency.
// Falls back to the company base currency when the customer has no explicit currency set.
func customerDefaultCurrLabel(currencyCode, baseCurrency string) string {
	if currencyCode != "" {
		return currencyCode
	}
	return baseCurrency
}

// billDaysFromToday returns the number of calendar days until the bill's due date
// (negative = already overdue). Returns 9999 when DueDate is nil (no due date set).
func billDaysFromToday(b models.Bill) int {
	if b.DueDate == nil {
		return 9999
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	due := b.DueDate.UTC().Truncate(24 * time.Hour)
	return int(due.Sub(today).Hours() / 24)
}
