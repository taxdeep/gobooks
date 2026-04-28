// 遵循project_guide.md
package pages

import (
	"strings"

	"github.com/shopspring/decimal"

	"balanciz/internal/services"
)

// purchaseTxTypeLabel returns the display label for a canonical AP
// transaction type. Mirrors salesTxTypeLabel on the AR side so the two
// surfaces stay visually consistent.
func purchaseTxTypeLabel(t string) string {
	switch t {
	case services.PurchaseTxTypeBill:
		return "Bill"
	case services.PurchaseTxTypeExpense:
		return "Expense"
	case services.PurchaseTxTypeVendorCreditNote:
		return "Vendor credit"
	case services.PurchaseTxTypeVendorRefund:
		return "Vendor refund"
	default:
		return t
	}
}

// purchaseTxStatusKind maps a row's (type, status) pair to a badge
// colour class. Small state machine by type — draws from the native
// status strings on Bill / Expense / VendorCreditNote / VendorRefund.
func purchaseTxStatusKind(r services.PurchaseTxRow) string {
	s := strings.ToLower(r.Status)
	switch r.Type {
	case services.PurchaseTxTypeBill:
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
			return "info" // posted / unknown
		}
	case services.PurchaseTxTypeExpense:
		switch s {
		case "posted":
			return "success"
		case "draft":
			return "neutral"
		case "voided":
			return "danger"
		default:
			return "neutral"
		}
	case services.PurchaseTxTypeVendorCreditNote:
		switch s {
		case "posted":
			return "info"
		case "partially_applied":
			return "info"
		case "fully_applied":
			return "success"
		case "voided":
			return "danger"
		default:
			return "neutral"
		}
	case services.PurchaseTxTypeVendorRefund:
		switch s {
		case "posted":
			return "success"
		case "reversed":
			return "danger"
		case "voided":
			return "danger"
		default:
			return "neutral"
		}
	}
	return "neutral"
}

func purchaseTxStatusBadgeClass(r services.PurchaseTxRow) string {
	base := "inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium"
	switch purchaseTxStatusKind(r) {
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

// purchaseTxStatusLabel Title-cases the native status string,
// replacing underscores with spaces. Empty status renders as em-dash
// so the column never looks blank.
func purchaseTxStatusLabel(r services.PurchaseTxRow) string {
	s := strings.ToLower(r.Status)
	if s == "" {
		return "—"
	}
	return strings.ToUpper(s[:1]) + strings.ReplaceAll(s[1:], "_", " ")
}

// purchaseTxRowsTotal sums the rendered rows' amounts for the table
// footer. Matches salesTxRowsTotal on the AR side.
func purchaseTxRowsTotal(rows []services.PurchaseTxRow) string {
	sum := decimal.Zero
	for _, r := range rows {
		sum = sum.Add(r.Amount)
	}
	return sum.StringFixed(2)
}
