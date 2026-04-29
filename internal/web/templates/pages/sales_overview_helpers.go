// 遵循project_guide.md
package pages

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"

	"balanciz/internal/services"
)

func salesOverviewMoney(amount decimal.Decimal, currency string) string {
	if currency == "" {
		return Money(amount)
	}
	return currency + " " + Money(amount)
}

func salesOverviewDeltaLabel(delta decimal.Decimal, currency string, previousLabel string) string {
	switch {
	case delta.IsZero():
		return salesOverviewMoney(decimal.Zero, currency) + " change from " + previousLabel
	case delta.IsPositive():
		return salesOverviewMoney(delta, currency) + " more than " + previousLabel
	default:
		return salesOverviewMoney(delta.Abs(), currency) + " less than " + previousLabel
	}
}

func salesOverviewDeltaClass(delta decimal.Decimal) string {
	if delta.IsNegative() {
		return "text-danger"
	}
	if delta.IsPositive() {
		return "text-success"
	}
	return "text-text-muted2"
}

func salesOverviewCashFlowHeaderClass(kind string) string {
	base := "px-3 py-2 text-right font-semibold "
	switch kind {
	case "current":
		return base + "bg-primary-soft text-primary"
	case "forecast":
		return base + "bg-success-soft text-success"
	default:
		return base + "bg-background text-text-muted"
	}
}

func salesOverviewPointHeight(amount, max decimal.Decimal) string {
	if !max.IsPositive() || !amount.IsPositive() {
		return "height: 2px;"
	}
	pct := amount.Div(max).Mul(decimal.NewFromInt(100))
	if pct.LessThan(decimal.NewFromInt(8)) {
		pct = decimal.NewFromInt(8)
	}
	return "height: " + pct.StringFixed(2) + "%;"
}

func salesOverviewPointTitle(point services.SalesOverviewIncomePoint, currency string) string {
	return point.Label + ": " + salesOverviewMoney(point.Amount, currency)
}

func salesOverviewWorkflowNodes() []salesOverviewWorkflowNode {
	return []salesOverviewWorkflowNode{
		{Key: "customers", Label: "Customers", Href: "/customers", X: 8, Y: 8},
		{Key: "quotes", Label: "Quotes", Href: "/quotes", X: 38, Y: 8},
		{Key: "orders", Label: "Sales Orders", Href: "/sales-orders", X: 8, Y: 44},
		{Key: "tasks", Label: "Billable Work", Href: "/tasks", X: 8, Y: 78},
		{Key: "invoices", Label: "Invoices", Href: "/invoices", X: 46, Y: 78},
		{Key: "receipts", Label: "Receipts", Href: "/receipts", X: 78, Y: 78},
	}
}

type salesOverviewWorkflowNode struct {
	Key   string
	Label string
	Href  string
	X     int
	Y     int
}

func salesOverviewNodeStyle(node salesOverviewWorkflowNode) string {
	return fmt.Sprintf("left:%d%%;top:%d%%;", node.X, node.Y)
}

func salesOverviewNodeClass(node salesOverviewWorkflowNode) string {
	base := "absolute flex w-[7.25rem] -translate-x-1/2 -translate-y-1/2 flex-col items-center gap-1 rounded-md px-2 py-1 text-center text-small font-semibold text-text hover:bg-background"
	if strings.Contains(node.Key, "invoices") || strings.Contains(node.Key, "receipts") {
		return base + " text-primary"
	}
	return base
}
