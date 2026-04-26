// 遵循project_guide.md
package pages

import (
	"testing"

	"github.com/shopspring/decimal"
)

// qty_with_uom_test.go — locks the U4 display helper that pairs a line
// qty with its snapshotted UOM, omitting the suffix for the boring
// EA default and appending a stock-equivalent hint when the UOMs differ.

func TestQtyWithUOM(t *testing.T) {
	cases := []struct {
		name        string
		qty         string
		isStock     bool
		lineUOM     string
		factor      string
		stockUOM    string
		want        string
	}{
		{"plain stock item EA", "8", true, "EA", "1", "EA", "8"},
		{"empty UOM defaults EA", "8", true, "", "1", "EA", "8"},
		{"non-EA UOM with stock match (factor 1)", "8", true, "BOTTLE", "1", "BOTTLE", "8 BOTTLE"},
		{"non-EA UOM with conversion to stock", "10", true, "CASE", "24", "BOTTLE", "10 CASE (240 BOTTLE in stock)"},
		{"service item line", "1.5", false, "EA", "1", "", "1.50"},
		{"service item with UOM override", "1.5", false, "HOUR", "1", "", "1.50 HOUR"},
		{"factor=1 same UOM no parenthetical", "5", true, "BOX", "1", "BOX", "5 BOX"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := QtyWithUOM(
				decimal.RequireFromString(tc.qty),
				tc.isStock,
				tc.lineUOM,
				decimal.RequireFromString(tc.factor),
				tc.stockUOM,
			)
			if got != tc.want {
				t.Errorf("QtyWithUOM(...) = %q, want %q", got, tc.want)
			}
		})
	}
}
