// 遵循project_guide.md
package services

// product_service_qty_guard.go — shared integer-quantity rule for stock-
// tracked inventory line items (S1 — 2026-04-25, generalised for S4).
//
// The rule: when a line points at a ProductService with IsStockItem=true,
// the qty must be a whole-unit integer.  Slicing one watermelon into
// pieces is a BOM concern (or a future UOM concern), not a line-item
// concern.  Service / non-inventory / other-charge items keep fractional
// qty so 1.5h of consulting still works.  Free-text lines (no
// ProductServiceID) are unrestricted — we don't know the unit semantics.

import (
	"fmt"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// validateStockItemQty enforces the integer-only rule for a single line.
//
// Used by SO + Quote + PO + CN + VCN line write paths. Looks the product up
// by ID once per call so callers can't bypass by passing a stale IsStockItem
// flag in their input struct.  lineNum is 1-based for the error message.
//
// Returns nil for free-text lines, service / non-inventory items, and
// when the qty is already a whole number.
func validateStockItemQty(db *gorm.DB, companyID uint, productServiceID *uint, qty decimal.Decimal, lineNum int) error {
	if productServiceID == nil || *productServiceID == 0 {
		return nil
	}
	var ps models.ProductService
	if err := db.Select("id", "name", "is_stock_item").
		Where("id = ? AND company_id = ?", *productServiceID, companyID).
		First(&ps).Error; err != nil {
		// Existence check belongs elsewhere; if the product doesn't exist
		// the calling code's FK / existence checks will fail. Don't double-
		// surface the error here.
		return nil
	}
	if !ps.IsStockItem {
		return nil
	}
	if !qty.Equal(qty.Truncate(0)) {
		return fmt.Errorf("line %d (%s): stock items must use whole-unit quantities (got %s)",
			lineNum, ps.Name, qty.String())
	}
	return nil
}

// StockItemQtyRowError is the form-friendly variant. Returns an empty
// string when the qty is acceptable; otherwise a short message suitable
// for inline display next to the offending row (no "line N" prefix —
// the row position is already obvious in the form).
//
// Used by handler-side editors (Invoice, Bill, etc.) that surface line
// errors inline rather than as a single batch error returned by a service.
func StockItemQtyRowError(db *gorm.DB, companyID uint, productServiceID *uint, qty decimal.Decimal) string {
	if productServiceID == nil || *productServiceID == 0 {
		return ""
	}
	var ps models.ProductService
	if err := db.Select("id", "is_stock_item").
		Where("id = ? AND company_id = ?", *productServiceID, companyID).
		First(&ps).Error; err != nil {
		return ""
	}
	if !ps.IsStockItem {
		return ""
	}
	if !qty.Equal(qty.Truncate(0)) {
		return "Stock items must use whole-unit quantities."
	}
	return ""
}
