// 遵循project_guide.md
package pages

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
	"gobooks/internal/models"
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

