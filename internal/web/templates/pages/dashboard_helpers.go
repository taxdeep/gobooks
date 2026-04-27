// 遵循project_guide.md
package pages

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// dashboardBankCountVM turns the bank-account list into a MoneyVM-shaped
// value for the KPI card. The Dashboard doesn't aggregate balances yet
// (would need per-account balance queries); the card shows the count
// as the "big number" and flags IsPositive=true so it always renders
// in the neutral text color rather than the red loss color.
func dashboardBankCountVM(vm DashboardVM) MoneyVM {
	n := len(vm.BankAccounts)
	label := fmt.Sprintf("%d", n)
	return MoneyVM{
		Value:      label,
		IsPositive: true,
	}
}

func dashboardIncomeStatementURL(vm DashboardVM, anchor string) string {
	href := "/reports/income-statement?from=" + vm.ReportFrom + "&to=" + vm.ReportTo
	if anchor != "" {
		href += "#" + anchor
	}
	return href
}

// dashboardRevenueMax returns (formattedMaxLabel, numericMax) over the
// revenue trend series. Used both for scaling the bar heights and for
// the "Peak" hint shown below the chart.
func dashboardRevenueMax(points []RevenueTrendPointVM) (string, float64) {
	if len(points) == 0 {
		return "", 0
	}
	maxVal := 0.0
	maxLabel := ""
	for _, p := range points {
		v := dashboardRevenueAsFloat(p.Revenue.Value)
		if v > maxVal {
			maxVal = v
			maxLabel = p.Label + " — " + p.Revenue.Value
		}
	}
	return maxLabel, maxVal
}

// dashboardBarStyle returns an inline style string with a percentage
// height for one bar in the revenue-trend chart. A bar's height is the
// fraction of the series max (min 4% so zero bars stay visible as a
// thin baseline rather than disappearing entirely).
func dashboardBarStyle(value string, maxVal float64) string {
	if maxVal <= 0 {
		return "height:4%"
	}
	v := dashboardRevenueAsFloat(value)
	pct := (v / maxVal) * 100
	if pct < 4 {
		pct = 4
	}
	if pct > 100 {
		pct = 100
	}
	return fmt.Sprintf("height:%.0f%%", pct)
}

// dashboardRevenueAsFloat reads the formatted MoneyVM string back into
// a float for chart-scaling calculations. Tolerates thousands separators,
// currency symbols, and parentheses-negative syntax (e.g. "(1,234.50)").
// This is a display-only path — nothing downstream relies on exact
// precision — so float is acceptable here.
func dashboardRevenueAsFloat(s string) float64 {
	s = strings.TrimSpace(s)
	negative := false
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		negative = true
		s = strings.TrimPrefix(strings.TrimSuffix(s, ")"), "(")
	}
	// Strip everything that isn't a digit, dot, comma, or minus sign.
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		}
	}
	cleaned := b.String()
	if cleaned == "" {
		return 0
	}
	d, err := decimal.NewFromString(cleaned)
	if err != nil {
		return 0
	}
	f, _ := d.Float64()
	if negative {
		f = -f
	}
	if f < 0 {
		return -f
	}
	return f
}
