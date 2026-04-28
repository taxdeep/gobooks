// 遵循project_guide.md
package services

// product_service_uom.go — Phase U1 (2026-04-25): UOM (Unit of Measure)
// management on ProductService.
//
// See UOM_DESIGN.md.  This file ships:
//   - ChangeStockUOM — guarded transition of the stock unit (parallel
//     to ChangeTrackingMode).
//   - SaveProductUOMs — composite save of the three UOMs + factors,
//     used by the edit page when the operator updates UOM config without
//     touching the stock unit (which is the common case).

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── Errors ───────────────────────────────────────────────────────────────────

var (
	// ErrStockUOMHasStock — attempted to change stock_uom while
	// inventory_balances.qty > 0 or any layer remaining > 0.
	// Operators must drain stock first.
	ErrStockUOMHasStock = errors.New("product_service: cannot change stock_uom while item has on-hand or layer remaining")
	// ErrStockUOMNotStockItem — attempted to set a non-default stock UOM
	// on a non-stock item.  Service / non-inventory items don't track
	// inventory and have nothing to count in.
	ErrStockUOMNotStockItem = errors.New("product_service: stock UOM customisation only applies to stock-tracked items")
)

// ── ChangeStockUOM ──────────────────────────────────────────────────────────

// ChangeStockUOMInput drives the guarded stock-UOM transition.
type ChangeStockUOMInput struct {
	CompanyID   uint
	ItemID      uint
	NewStockUOM string
	Actor       string     // email or "system"
	ActorUserID *uuid.UUID // optional
}

// ChangeStockUOM transitions the item's StockUOM after verifying:
//   - item is stock-tracked
//   - no on-hand qty across any warehouse
//   - no remaining FIFO layer
//
// Sell/Purchase UOM and factors are NOT touched here — the operator must
// re-set them via SaveProductUOMs after the stock unit changes (because
// the old factors are now meaningless against the new stock unit).
//
// Audit row written: action=product_service.stock_uom.changed,
// before/after carry the old + new stock UOM strings.
func ChangeStockUOM(db *gorm.DB, in ChangeStockUOMInput) error {
	in.NewStockUOM = models.NormalizeUOM(in.NewStockUOM)
	if in.NewStockUOM == "" {
		return fmt.Errorf("new stock UOM is required")
	}

	return db.Transaction(func(tx *gorm.DB) error {
		var item models.ProductService
		if err := tx.Where("id = ? AND company_id = ?", in.ItemID, in.CompanyID).
			First(&item).Error; err != nil {
			return fmt.Errorf("load product_service: %w", err)
		}
		if !item.IsStockItem {
			return ErrStockUOMNotStockItem
		}
		if item.StockUOM == in.NewStockUOM {
			return nil // no-op
		}

		// On-hand guard (parallel to ChangeTrackingMode).
		var onHand struct{ Total float64 }
		if err := tx.Model(&models.InventoryBalance{}).
			Select("COALESCE(SUM(quantity_on_hand), 0) AS total").
			Where("company_id = ? AND item_id = ?", in.CompanyID, in.ItemID).
			Scan(&onHand).Error; err != nil {
			return fmt.Errorf("sum on-hand: %w", err)
		}
		if onHand.Total != 0 {
			return fmt.Errorf("%w: sum(on-hand)=%v", ErrStockUOMHasStock, onHand.Total)
		}

		var layerRem struct{ Total float64 }
		if err := tx.Model(&models.InventoryCostLayer{}).
			Select("COALESCE(SUM(remaining_quantity), 0) AS total").
			Where("company_id = ? AND item_id = ?", in.CompanyID, in.ItemID).
			Scan(&layerRem).Error; err != nil {
			return fmt.Errorf("sum layer remaining: %w", err)
		}
		if layerRem.Total != 0 {
			return fmt.Errorf("%w: sum(layer remaining)=%v", ErrStockUOMHasStock, layerRem.Total)
		}

		oldUOM := item.StockUOM
		// Reset Sell/Purchase UOM + factors to track the new stock unit.
		// Factors against the OLD stock unit are nonsense once the stock
		// unit changes; safer to default to 1:1 and let the operator re-
		// configure than to silently keep stale factors.
		updates := map[string]any{
			"stock_uom":           in.NewStockUOM,
			"sell_uom":            in.NewStockUOM,
			"sell_uom_factor":     decimal.NewFromInt(1),
			"purchase_uom":        in.NewStockUOM,
			"purchase_uom_factor": decimal.NewFromInt(1),
		}
		if err := tx.Model(&models.ProductService{}).
			Where("id = ? AND company_id = ?", in.ItemID, in.CompanyID).
			Updates(updates).Error; err != nil {
			return fmt.Errorf("update stock UOM: %w", err)
		}

		cid := in.CompanyID
		TryWriteAuditLogWithContextDetails(tx,
			"product_service.stock_uom.changed",
			"product_service",
			in.ItemID,
			in.Actor,
			map[string]any{"company_id": in.CompanyID, "item_name": item.Name},
			&cid,
			in.ActorUserID,
			map[string]any{"stock_uom": oldUOM},
			map[string]any{"stock_uom": in.NewStockUOM},
		)
		return nil
	})
}

// ── SaveProductUOMs ─────────────────────────────────────────────────────────

// SaveProductUOMsInput updates the Sell/Purchase UOMs + factors without
// touching the stock unit.  The common edit case: operator sets up
// "stocks in BOTTLE, sells in BOTTLE, buys in CASE" (factor 24) on an
// existing item.
type SaveProductUOMsInput struct {
	CompanyID         uint
	ItemID            uint
	SellUOM           string
	SellUOMFactor     decimal.Decimal
	PurchaseUOM       string
	PurchaseUOMFactor decimal.Decimal
	Actor             string
	ActorUserID       *uuid.UUID
}

// SaveProductUOMs validates + persists the per-side UOMs and factors.
// Returns the model-layer validation error if the tuple is inconsistent
// (e.g. SellUOM == StockUOM but factor != 1).
//
// Stock UOM stays untouched here — use ChangeStockUOM for that path
// (which has its own on-hand guard and audit semantics).  Audit row:
// product_service.uom.saved with before/after of the four fields.
func SaveProductUOMs(db *gorm.DB, in SaveProductUOMsInput) error {
	in.SellUOM = models.NormalizeUOM(in.SellUOM)
	in.PurchaseUOM = models.NormalizeUOM(in.PurchaseUOM)
	if !in.SellUOMFactor.IsPositive() {
		in.SellUOMFactor = decimal.NewFromInt(1)
	}
	if !in.PurchaseUOMFactor.IsPositive() {
		in.PurchaseUOMFactor = decimal.NewFromInt(1)
	}

	return db.Transaction(func(tx *gorm.DB) error {
		var item models.ProductService
		if err := tx.Where("id = ? AND company_id = ?", in.ItemID, in.CompanyID).
			First(&item).Error; err != nil {
			return fmt.Errorf("load product_service: %w", err)
		}

		// Build a probe with the proposed values + the item's existing
		// stock unit, so model validator catches "sell == stock but
		// factor != 1" inconsistencies under the actual stock UOM.
		probe := item
		probe.SellUOM = in.SellUOM
		probe.SellUOMFactor = in.SellUOMFactor
		probe.PurchaseUOM = in.PurchaseUOM
		probe.PurchaseUOMFactor = in.PurchaseUOMFactor
		if err := probe.ValidateUOMs(); err != nil {
			return err
		}

		before := map[string]any{
			"sell_uom":            item.SellUOM,
			"sell_uom_factor":     item.SellUOMFactor.String(),
			"purchase_uom":        item.PurchaseUOM,
			"purchase_uom_factor": item.PurchaseUOMFactor.String(),
		}
		updates := map[string]any{
			"sell_uom":            in.SellUOM,
			"sell_uom_factor":     in.SellUOMFactor,
			"purchase_uom":        in.PurchaseUOM,
			"purchase_uom_factor": in.PurchaseUOMFactor,
		}
		if err := tx.Model(&models.ProductService{}).
			Where("id = ? AND company_id = ?", in.ItemID, in.CompanyID).
			Updates(updates).Error; err != nil {
			return fmt.Errorf("save UOMs: %w", err)
		}

		cid := in.CompanyID
		TryWriteAuditLogWithContextDetails(tx,
			"product_service.uom.saved",
			"product_service",
			in.ItemID,
			in.Actor,
			map[string]any{"company_id": in.CompanyID, "item_name": item.Name},
			&cid,
			in.ActorUserID,
			before,
			map[string]any{
				"sell_uom":            in.SellUOM,
				"sell_uom_factor":     in.SellUOMFactor.String(),
				"purchase_uom":        in.PurchaseUOM,
				"purchase_uom_factor": in.PurchaseUOMFactor.String(),
			},
		)
		return nil
	})
}
