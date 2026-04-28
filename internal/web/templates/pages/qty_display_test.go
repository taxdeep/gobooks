// 遵循project_guide.md
package pages

import (
	"testing"

	"github.com/shopspring/decimal"

	"balanciz/internal/models"
)

// TestQtyDisplay locks the integer-only rule for stock-tracked inventory
// items: 8 watermelons render as "8", not "8.00". Service / non-inventory /
// other-charge items keep the 2-decimal display so 1.5 hours of consulting
// still works.
func TestQtyDisplay(t *testing.T) {
	cases := []struct {
		name        string
		qty         string
		isStockItem bool
		want        string
	}{
		{"stock item whole qty", "8", true, "8"},
		{"stock item with .00 input", "8.00", true, "8"},
		{"stock item zero", "0", true, "0"},
		{"non-stock item whole qty", "2", false, "2.00"},
		{"non-stock item fractional", "1.5", false, "1.50"},
		{"non-stock item small fraction", "0.25", false, "0.25"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := QtyDisplay(decimal.RequireFromString(tc.qty), tc.isStockItem)
			if got != tc.want {
				t.Errorf("QtyDisplay(%s, isStock=%v) = %q, want %q", tc.qty, tc.isStockItem, got, tc.want)
			}
		})
	}
}

// TestQtyDisplayForLineProduct covers the templ-friendly variant that pulls
// IsStockItem off a *ProductService pointer.  Free-text lines (nil ProductService)
// fall through to the 2-decimal form.
func TestQtyDisplayForLineProduct(t *testing.T) {
	stockItem := &models.ProductService{IsStockItem: true, Name: "Watermelon"}
	serviceItem := &models.ProductService{IsStockItem: false, Name: "Consulting"}

	if got := QtyDisplayForLineProduct(decimal.NewFromInt(8), stockItem); got != "8" {
		t.Errorf("stock item: got %q, want %q", got, "8")
	}
	if got := QtyDisplayForLineProduct(decimal.RequireFromString("1.5"), serviceItem); got != "1.50" {
		t.Errorf("service item: got %q, want %q", got, "1.50")
	}
	// Free-text line: nil ProductService must fall back safely (we don't
	// know the unit semantics; over-truncating would surprise the operator).
	if got := QtyDisplayForLineProduct(decimal.RequireFromString("3.25"), nil); got != "3.25" {
		t.Errorf("nil product: got %q, want %q", got, "3.25")
	}
}
