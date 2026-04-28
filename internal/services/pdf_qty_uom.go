// 遵循project_guide.md
package services

// pdf_qty_uom.go — small shared helper used by every doc-type PDF adapter
// (Invoice / Bill / PO / SO / Quote) to render the Qty cell with its
// snapshotted UOM. Keeps the formatting consistent across customer-facing
// PDFs without each adapter re-implementing the rule.
//
// Format: "8" for plain EA stock items / non-stock items; "8 CASE" when
// the line UOM differs from the boring default. The (N BOTTLE in stock)
// hint we surface in the operator UI is intentionally OMITTED on the
// printed PDF — it's an internal reviewer aid, not customer information.
//
// Stock-vs-non-stock toggle is the same as the operator-facing display:
// stock items render whole units (8); non-stock items keep 2 decimals
// (1.50). Free-text lines (no product) fall back to 2 decimals.

import (
	"github.com/shopspring/decimal"

	"balanciz/internal/models"
)

// PDFQtyWithUOM formats a line qty + UOM snapshot for printed output.
// Pass nil ProductService for free-text lines; "" / zero LineUOM /
// zero LineUOMFactor are tolerated and degrade safely.
func PDFQtyWithUOM(qty decimal.Decimal, ps *models.ProductService, lineUOM string) string {
	display := pdfQtyString(qty, ps)
	uom := lineUOM
	if uom == "" {
		uom = "EA"
	}
	if uom == "EA" {
		return display
	}
	return display + " " + uom
}

// pdfQtyString formats just the numeric portion based on item type.
// Mirrors templates/pages/QtyDisplay so PDF + on-screen UI stay aligned.
func pdfQtyString(qty decimal.Decimal, ps *models.ProductService) string {
	if ps != nil && ps.IsStockItem {
		return qty.Truncate(0).String()
	}
	return qty.StringFixed(2)
}
