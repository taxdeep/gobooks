// 遵循project_guide.md
package services

// line_uom_snapshot.go — Phase U2 (2026-04-25). Resolves the UOM defaults
// for a doc-line at save time and computes the stock-UOM equivalent qty.
//
// Both AR (Invoice/Quote/SO/CN) and AP (Bill/PO/VCN) document services call
// this just before persisting each line. Centralised so the snapshot
// behaviour stays consistent across the eight write paths.

import (
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// LineUOMSnapshot holds the three values to write onto a doc line:
// LineUOM (the snapshotted unit), LineUOMFactor (StockUOM per LineUOM),
// QtyInStockUOM (Qty × factor, rounded to 4dp).
type LineUOMSnapshot struct {
	LineUOM       string
	LineUOMFactor decimal.Decimal
	QtyInStockUOM decimal.Decimal
}

// LineUOMSide picks which side's UOM to default to.
type LineUOMSide int

const (
	// LineUOMSell — use ProductService.SellUOM (Invoice / Quote / SO / CN line writes).
	LineUOMSell LineUOMSide = iota
	// LineUOMPurchase — use ProductService.PurchaseUOM (Bill / PO / VCN line writes).
	LineUOMPurchase
)

// SnapshotLineUOM resolves the UOM defaults for a single line:
//   - When productServiceID is nil (free-text line) or the product can't be
//     loaded: return EA / 1 / qty (1:1 fallback).
//   - When ProductService is non-stock: same fallback (UOM is meaningless).
//   - When stock-tracked: return SellUOM/PurchaseUOM + factor + qty×factor.
//
// Pass overrideLineUOM/overrideLineUOMFactor when the operator's form
// included an explicit UOM override (per design §9.3 — per-line override
// is allowed).  Empty / zero values fall through to the product defaults.
func SnapshotLineUOM(
	db *gorm.DB,
	companyID uint,
	productServiceID *uint,
	side LineUOMSide,
	qty decimal.Decimal,
	overrideLineUOM string,
	overrideLineUOMFactor decimal.Decimal,
) LineUOMSnapshot {
	one := decimal.NewFromInt(1)

	// Free-text line: no product, no UOM semantics.
	if productServiceID == nil || *productServiceID == 0 {
		return LineUOMSnapshot{
			LineUOM:       "EA",
			LineUOMFactor: one,
			QtyInStockUOM: qty.Round(4),
		}
	}

	var ps models.ProductService
	if err := db.Select("id", "is_stock_item", "stock_uom", "sell_uom", "sell_uom_factor", "purchase_uom", "purchase_uom_factor").
		Where("id = ? AND company_id = ?", *productServiceID, companyID).
		First(&ps).Error; err != nil {
		// Existence check belongs elsewhere; fall through to safe defaults.
		return LineUOMSnapshot{
			LineUOM:       "EA",
			LineUOMFactor: one,
			QtyInStockUOM: qty.Round(4),
		}
	}

	if !ps.IsStockItem {
		return LineUOMSnapshot{
			LineUOM:       "EA",
			LineUOMFactor: one,
			QtyInStockUOM: qty.Round(4),
		}
	}

	// Default to the product's side UOM.
	defaultUOM := ps.SellUOM
	defaultFactor := ps.SellUOMFactor
	if side == LineUOMPurchase {
		defaultUOM = ps.PurchaseUOM
		defaultFactor = ps.PurchaseUOMFactor
	}
	if defaultUOM == "" {
		defaultUOM = "EA"
	}
	if !defaultFactor.IsPositive() {
		defaultFactor = one
	}

	// Operator override beats default — but only when both fields are
	// supplied. Half-completed override (UOM but no factor) falls back
	// to the product default, which avoids a silent factor-1 surprise.
	lineUOM := defaultUOM
	lineFactor := defaultFactor
	if overrideLineUOM != "" && overrideLineUOMFactor.IsPositive() {
		lineUOM = models.NormalizeUOM(overrideLineUOM)
		lineFactor = overrideLineUOMFactor
	}

	return LineUOMSnapshot{
		LineUOM:       lineUOM,
		LineUOMFactor: lineFactor,
		QtyInStockUOM: qty.Mul(lineFactor).Round(4),
	}
}
