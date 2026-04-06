// 遵循project_guide.md
package pages

import "github.com/shopspring/decimal"

// Money formats a decimal with 2 decimal places.
// This avoids float64 formatting in templates.
func Money(d decimal.Decimal) string {
	return d.StringFixed(2)
}

// MoneyBlankZero returns Money(d) unless d is zero, in which case it returns
// an empty string. Used in aging tables where empty cells are cleaner than "0.00".
func MoneyBlankZero(d decimal.Decimal) string {
	if d.IsZero() {
		return ""
	}
	return d.StringFixed(2)
}

