// 遵循project_guide.md
package services

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"balanciz/internal/models"
)

// pdf_qty_uom_test.go — locks the PDF qty-display helper. The customer-
// facing PDF intentionally OMITS the "(N BOTTLE in stock)" parenthetical
// that the operator UI shows; this test guards against accidentally
// pulling the operator-facing internal hint into customer documents.

func TestPDFQtyWithUOM(t *testing.T) {
	stockItem := &models.ProductService{IsStockItem: true, Name: "Bottle"}
	serviceItem := &models.ProductService{IsStockItem: false, Name: "Consulting"}

	cases := []struct {
		name    string
		qty     string
		ps      *models.ProductService
		lineUOM string
		want    string
	}{
		{"plain stock item EA → no suffix", "8", stockItem, "EA", "8"},
		{"stock item empty UOM → no suffix (defaults EA)", "8", stockItem, "", "8"},
		{"stock item CASE → suffix", "10", stockItem, "CASE", "10 CASE"},
		{"stock item BOTTLE → suffix", "240", stockItem, "BOTTLE", "240 BOTTLE"},
		{"service item EA → no suffix, 2-decimal", "1.5", serviceItem, "EA", "1.50"},
		{"service item HOUR → suffix, 2-decimal", "1.5", serviceItem, "HOUR", "1.50 HOUR"},
		{"free-text line (nil product) → 2-decimal, no suffix", "3.25", nil, "", "3.25"},
		{"free-text with override UOM → 2-decimal + suffix", "3", nil, "BUNDLE", "3.00 BUNDLE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PDFQtyWithUOM(decimal.RequireFromString(tc.qty), tc.ps, tc.lineUOM)
			if got != tc.want {
				t.Errorf("PDFQtyWithUOM(%s, %v, %q) = %q, want %q",
					tc.qty, tc.ps != nil && tc.ps.IsStockItem, tc.lineUOM, got, tc.want)
			}
		})
	}
}

// TestPDFQtyWithUOM_NoStockEquivalentInPDF — explicit guard: the
// "(N BOTTLE in stock)" hint we surface in the operator UI must NOT
// appear on PDFs.  Customer-facing documents only carry the unit they
// were billed in.
func TestPDFQtyWithUOM_NoStockEquivalentInPDF(t *testing.T) {
	stockItem := &models.ProductService{IsStockItem: true, Name: "Bottle"}
	got := PDFQtyWithUOM(decimal.NewFromInt(10), stockItem, "CASE")
	if got != "10 CASE" {
		t.Errorf("expected just '10 CASE', got %q (PDF must not include the stock-equivalent hint)", got)
	}
	for _, banned := range []string{"in stock", "BOTTLE", "(", ")"} {
		if strings.Contains(got, banned) {
			t.Errorf("PDF output must not contain %q, got %q", banned, got)
		}
	}
}
