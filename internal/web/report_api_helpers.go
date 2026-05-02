package web

import "github.com/shopspring/decimal"

func reportDecimalString(v decimal.Decimal) string {
	return v.StringFixed(2)
}
