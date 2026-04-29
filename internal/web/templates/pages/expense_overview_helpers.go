// 遵循project_guide.md
package pages

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"

	"balanciz/internal/services"
)

func expenseOverviewMoney(amount decimal.Decimal, currency string) string {
	if currency == "" {
		return Money(amount)
	}
	return currency + " " + Money(amount)
}

func expenseOverviewDeltaLabel(delta decimal.Decimal, currency string, previousLabel string) string {
	switch {
	case delta.IsZero():
		return expenseOverviewMoney(decimal.Zero, currency) + " change from " + previousLabel
	case delta.IsPositive():
		return expenseOverviewMoney(delta, currency) + " more than " + previousLabel
	default:
		return expenseOverviewMoney(delta.Abs(), currency) + " less than " + previousLabel
	}
}

func expenseOverviewDeltaClass(delta decimal.Decimal) string {
	if delta.IsNegative() {
		return "text-success"
	}
	if delta.IsPositive() {
		return "text-warning"
	}
	return "text-text-muted2"
}

func expenseOverviewCashOutHeaderClass(kind string) string {
	base := "px-3 py-2 text-right font-semibold "
	switch kind {
	case "current":
		return base + "bg-primary-soft text-primary"
	case "forecast":
		return base + "bg-warning-soft text-warning"
	default:
		return base + "bg-background text-text-muted"
	}
}

func expenseOverviewPointHeight(amount, max decimal.Decimal) string {
	if !max.IsPositive() || !amount.IsPositive() {
		return "height: 2px;"
	}
	pct := amount.Div(max).Mul(decimal.NewFromInt(100))
	if pct.LessThan(decimal.NewFromInt(8)) {
		pct = decimal.NewFromInt(8)
	}
	return "height: " + pct.StringFixed(2) + "%;"
}

func expenseOverviewPointTitle(point services.ExpenseOverviewPoint, currency string) string {
	return point.Label + ": " + expenseOverviewMoney(point.Amount, currency)
}

func expenseOverviewWorkflowNodes() []expenseOverviewWorkflowNode {
	return []expenseOverviewWorkflowNode{
		{Key: "vendors", Label: "Vendors", Href: "/vendors", X: 11, Y: 16},
		{Key: "purchase_orders", Label: "Purchase Orders", Href: "/purchase-orders", X: 39, Y: 16},
		{Key: "bills", Label: "Bills", Href: "/bills", X: 11, Y: 43},
		{Key: "expenses", Label: "Expenses", Href: "/expenses", X: 11, Y: 80},
		{Key: "pay_bills", Label: "Pay Bills", Href: "/banking/pay-bills", X: 50, Y: 80},
		{Key: "credits", Label: "Vendor Credits", Href: "/vendor-credit-notes", X: 78, Y: 80},
	}
}

type expenseOverviewWorkflowNode struct {
	Key   string
	Label string
	Href  string
	X     int
	Y     int
}

func expenseOverviewNodeStyle(node expenseOverviewWorkflowNode) string {
	return fmt.Sprintf("left:%d%%;top:%d%%;", node.X, node.Y)
}

func expenseOverviewNodeClass(node expenseOverviewWorkflowNode) string {
	base := "expense-workflow-node"
	if strings.Contains(node.Key, "pay") || strings.Contains(node.Key, "credits") {
		return base + " text-primary"
	}
	return base
}
