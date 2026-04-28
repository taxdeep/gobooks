// 遵循project_guide.md
package services

// sales_order_line_qty_adjust.go — S2 (2026-04-25): allow operators to
// raise / lower an SO line's Qty after the SO has been partially invoiced,
// subject to:
//
//   newQty >= line.InvoicedQty                 (can't drop below shipped)
//   newQty <= OriginalQuantity + buffer        (over-shipment cap from S3)
//
// Stock-item lines are additionally constrained to whole-unit qty (S1).
//
// The line's OriginalQuantity stays untouched — it anchors the buffer cap
// regardless of how many adjustments happen.  Only the live Quantity moves.
//
// Recompute pipeline:
//   1. Update line.Quantity → recompute LineNet/TaxAmount/LineTotal.
//   2. Re-aggregate the SO's Subtotal/TaxTotal/Total from all current lines.
//   3. Audit row carries before/after qty + the active over-ship policy
//      source so reviewers see whether the cap came from company default
//      or warehouse override.

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ErrSalesOrderLineNotFound is returned when the (so_id, line_id) pair
// doesn't resolve to a line on a SO owned by this company.
var ErrSalesOrderLineNotFound = errors.New("sales order line not found")

// AdjustSalesOrderLineQty raises / lowers Qty on a single SO line on a
// partially-invoiced SO. Returns the updated SO so the caller can re-render.
//
// actor + actorUserID feed the audit log; pass actorUserID=nil for system
// callers that don't have a user context.
func AdjustSalesOrderLineQty(
	db *gorm.DB,
	companyID, soID, lineID uint,
	newQty decimal.Decimal,
	actor string,
	actorUserID *uuid.UUID,
) (*models.SalesOrder, error) {
	if !newQty.IsPositive() {
		return nil, errors.New("quantity must be greater than 0")
	}

	var so models.SalesOrder
	if err := db.Where("id = ? AND company_id = ?", soID, companyID).First(&so).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSalesOrderNotFound
		}
		return nil, err
	}
	if so.Status != models.SalesOrderStatusPartiallyInvoiced {
		return nil, fmt.Errorf("%w: per-line Qty adjust is only allowed on partially-invoiced sales orders (status: %s)",
			ErrSalesOrderInvalidStatus, so.Status)
	}

	var line models.SalesOrderLine
	if err := db.Where("id = ? AND sales_order_id = ?", lineID, soID).First(&line).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSalesOrderLineNotFound
		}
		return nil, err
	}

	// Floor: can't drop below what's already invoiced — those invoices
	// have already shipped goods / charged the customer for that qty.
	if newQty.LessThan(line.InvoicedQty) {
		return nil, fmt.Errorf("new qty %s is less than already-invoiced qty %s — partial-invoice rows cannot be reversed from this form",
			newQty.String(), line.InvoicedQty.String())
	}

	// Ceiling: original-contract + over-shipment buffer.  When the line
	// has no OriginalQuantity captured (legacy row pre-S2), fall back to
	// the current Quantity so the cap is still well-defined.
	originalQty := line.OriginalQuantity
	if !originalQty.IsPositive() {
		originalQty = line.Quantity
	}
	// SO line has no per-line warehouse — the destination warehouse is
	// chosen at Shipment time (Phase I, future).  Company default applies
	// here; warehouse-level overrides will fire from the Shipment/Receipt
	// post paths once those slices land. See ResolveOverShipmentPolicy
	// doc for the full layering story.
	policy, err := ResolveOverShipmentPolicy(db, companyID, 0)
	if err != nil {
		return nil, fmt.Errorf("resolve over-shipment policy: %w", err)
	}
	maxQty := policy.MaxAllowedQty(originalQty)
	if newQty.GreaterThan(maxQty) {
		return nil, fmt.Errorf("new qty %s exceeds the over-shipment cap %s (original %s + buffer %s — see Settings ▸ Company ▸ Profile or this warehouse's profile)",
			newQty.String(), maxQty.String(), originalQty.String(), maxQty.Sub(originalQty).String())
	}

	// Stock-item integer rule (S1).
	if err := validateStockItemQty(db, companyID, line.ProductServiceID, newQty, 1); err != nil {
		return nil, err
	}

	// Recompute line totals + the stock-UOM equivalent qty so the
	// inventory-side cached value stays in sync with the new line qty.
	beforeQty := line.Quantity
	rate := loadTaxRate(db, line.TaxCodeID)
	line.Quantity = newQty
	if line.LineUOMFactor.IsPositive() {
		line.QtyInStockUOM = newQty.Mul(line.LineUOMFactor).Round(4)
	} else {
		line.QtyInStockUOM = newQty.Round(4)
	}
	calcSalesOrderLine(&line, rate)

	if err := db.Transaction(func(tx *gorm.DB) error {
		// Persist line first so the SO-total recompute below sees the
		// new amounts via a fresh Find rather than racing the in-mem copy.
		if err := tx.Save(&line).Error; err != nil {
			return fmt.Errorf("save adjusted line: %w", err)
		}

		var allLines []models.SalesOrderLine
		if err := tx.Where("sales_order_id = ?", soID).Find(&allLines).Error; err != nil {
			return fmt.Errorf("reload lines: %w", err)
		}
		var subtotal, taxTotal decimal.Decimal
		for _, l := range allLines {
			subtotal = subtotal.Add(l.LineNet)
			taxTotal = taxTotal.Add(l.TaxAmount)
		}
		so.Subtotal = subtotal.Round(4)
		so.TaxTotal = taxTotal.Round(4)
		so.Total = subtotal.Add(taxTotal).Round(4)
		if err := tx.Model(&so).Updates(map[string]any{
			"subtotal":  so.Subtotal,
			"tax_total": so.TaxTotal,
			"total":     so.Total,
		}).Error; err != nil {
			return fmt.Errorf("update SO totals: %w", err)
		}

		cid := companyID
		TryWriteAuditLogWithContextDetails(tx,
			"sales_order.line.qty_adjusted",
			"sales_order_line",
			line.ID,
			actor,
			map[string]any{
				"sales_order_id": soID,
				"company_id":     companyID,
			},
			&cid,
			actorUserID,
			map[string]any{"qty": beforeQty.String()},
			map[string]any{
				"qty":             newQty.String(),
				"original_qty":    originalQty.String(),
				"max_allowed":     maxQty.String(),
				"buffer_source":   policy.Source, // "company" / "warehouse" / ""
				"buffer_enabled":  policy.Enabled,
				"buffer_mode":     string(policy.Mode),
				"buffer_value":    policy.Value.String(),
				"invoiced_qty":    line.InvoicedQty.String(),
			},
		)
		return nil
	}); err != nil {
		return nil, err
	}

	return &so, nil
}
