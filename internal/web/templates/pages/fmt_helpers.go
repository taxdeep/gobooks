// 遵循project_guide.md
package pages

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
	"gobooks/internal/models"
	"gobooks/internal/services"
)

// Uitoa formats a uint as a string (handy in templates).
func Uitoa(id uint) string {
	return fmt.Sprintf("%d", id)
}

// Itoa formats an int as a string (handy in templates).
func Itoa(i int) string {
	return strconv.Itoa(i)
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

// billBalanceDue returns the outstanding balance for a bill.
// Uses BalanceDue if positive, otherwise falls back to the full Amount.
func billBalanceDue(b models.Bill) decimal.Decimal {
	if b.BalanceDue.GreaterThan(decimal.Zero) {
		return b.BalanceDue
	}
	return b.Amount
}

// payBillsInitData generates the Alpine x-data attribute value for the Pay Bills page.
// It returns a JS function call: payBillsData([{id:"1",balance:"123.45"}, ...])
func payBillsInitData(bills []models.Bill) string {
	var sb strings.Builder
	sb.WriteString("payBillsData([")
	for i, b := range bills {
		if i > 0 {
			sb.WriteString(",")
		}
		bal := billBalanceDue(b)
		sb.WriteString(fmt.Sprintf(`{"id":"%d","balance":"%s"}`, b.ID, bal.StringFixed(2)))
	}
	sb.WriteString("])")
	return sb.String()
}
