// 遵循project_guide.md
package inventory

// adjust.go — AdjustStock (signed gain/loss) — third IN verb, Phase D.0
// slice 4.
//
// AdjustStock is a thin dispatcher over ReceiveStock (for positive deltas)
// and IssueStock (for negative deltas). Delegating keeps validation,
// idempotency, balance-lock semantics, and movement-row writing in a single
// place; AdjustStock only massages inputs, resolves the gain-side unit cost
// when the caller doesn't supply one, and normalises the result's sign.
//
// Zero-delta adjustments are recorded as audit-only marker rows (no balance
// change) to preserve the legacy CreateAdjustment behaviour some existing
// callers rely on.

import (
	"fmt"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// AdjustStock applies a signed inventory adjustment. See
// INVENTORY_MODULE_API.md §3.3.
func AdjustStock(db *gorm.DB, in AdjustStockInput) (*AdjustStockResult, error) {
	if err := validateAdjustInput(in); err != nil {
		return nil, err
	}

	// Zero-delta: write a marker movement for audit and return. The balance
	// is untouched, so we bypass Receive/Issue entirely.
	if in.QuantityDelta.IsZero() {
		return writeAdjustmentMarker(db, in)
	}

	memo := buildAdjustMemo(in)

	if in.QuantityDelta.IsPositive() {
		return adjustGain(db, in, memo)
	}
	return adjustLoss(db, in, memo)
}

// ── Gain (positive delta) ────────────────────────────────────────────────────

// adjustGain delegates to ReceiveStock. When the caller hasn't provided a
// unit cost, we fall back to the current weighted-average cost at the
// target balance so a found-stock adjustment doesn't distort valuation.
func adjustGain(db *gorm.DB, in AdjustStockInput, memo string) (*AdjustStockResult, error) {
	warehouseID := ptrUintIfNonZero(in.WarehouseID)

	unitCost := decimal.Zero
	if in.UnitCost != nil {
		unitCost = *in.UnitCost
	}
	if unitCost.IsZero() {
		fallback, err := currentAverageCost(db, in.CompanyID, in.ItemID, warehouseID)
		if err != nil {
			return nil, err
		}
		unitCost = fallback
	}

	exchangeRate := decimal.NewFromInt(1)
	if in.ExchangeRate != nil && !in.ExchangeRate.IsZero() {
		exchangeRate = *in.ExchangeRate
	}

	rs, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID:      in.CompanyID,
		ItemID:         in.ItemID,
		WarehouseID:    in.WarehouseID,
		Quantity:       in.QuantityDelta,
		MovementDate:   in.MovementDate,
		UnitCost:       unitCost,
		CurrencyCode:   in.CurrencyCode,
		ExchangeRate:   exchangeRate,
		SourceType:     normaliseAdjustSourceType(in.SourceType),
		SourceID:       in.SourceID,
		Memo:           memo,
		IdempotencyKey: in.IdempotencyKey,
		ActorUserID:    in.ActorUserID,
	})
	if err != nil {
		return nil, err
	}
	return &AdjustStockResult{
		MovementID:          rs.MovementID,
		UnitCostBase:        rs.UnitCostBase,
		AdjustmentValueBase: rs.InventoryValueBase, // positive (gain)
	}, nil
}

// ── Loss (negative delta) ───────────────────────────────────────────────────

// adjustLoss delegates to IssueStock. Unit cost is always the current
// weighted average — IssueStock handles that; the caller's UnitCost field
// is ignored per the API spec.
func adjustLoss(db *gorm.DB, in AdjustStockInput, memo string) (*AdjustStockResult, error) {
	isr, err := IssueStock(db, IssueStockInput{
		CompanyID:      in.CompanyID,
		ItemID:         in.ItemID,
		WarehouseID:    in.WarehouseID,
		Quantity:       in.QuantityDelta.Abs(),
		MovementDate:   in.MovementDate,
		SourceType:     normaliseAdjustSourceType(in.SourceType),
		SourceID:       in.SourceID,
		Memo:           memo,
		IdempotencyKey: in.IdempotencyKey,
		ActorUserID:    in.ActorUserID,
	})
	if err != nil {
		return nil, err
	}
	return &AdjustStockResult{
		MovementID:          isr.MovementID,
		UnitCostBase:        isr.UnitCostBase,
		AdjustmentValueBase: isr.CostOfIssueBase.Neg(), // negative (loss)
	}, nil
}

// ── Zero-delta marker ────────────────────────────────────────────────────────

// writeAdjustmentMarker handles the zero-delta case. No balance change, no
// Receive/Issue delegation; we just emit a single audit movement so the
// act of running a zero-variance cycle count (for instance) is still
// recorded.
func writeAdjustmentMarker(db *gorm.DB, in AdjustStockInput) (*AdjustStockResult, error) {
	if in.IdempotencyKey != "" {
		if cached, err := cachedAdjustmentByKey(db, in.CompanyID, in.IdempotencyKey); err != nil {
			return nil, err
		} else if cached != nil {
			return cached, nil
		}
	}
	if err := verifyInventoryItem(db, in.CompanyID, in.ItemID); err != nil {
		return nil, err
	}
	warehouseID := ptrUintIfNonZero(in.WarehouseID)
	if warehouseID != nil {
		if err := verifyWarehouseBelongsToCompany(db, in.CompanyID, *warehouseID); err != nil {
			return nil, err
		}
	}

	// Use current avg for cost so reports show the contemporaneous valuation
	// even on a zero-delta marker.
	unitCost, err := currentAverageCost(db, in.CompanyID, in.ItemID, warehouseID)
	if err != nil {
		return nil, err
	}
	zero := decimal.Zero
	rate := decimal.NewFromInt(1)
	mov := models.InventoryMovement{
		CompanyID:      in.CompanyID,
		ItemID:         in.ItemID,
		MovementType:   models.MovementTypeAdjustment,
		QuantityDelta:  zero,
		UnitCost:       &unitCost,
		TotalCost:      &zero,
		ExchangeRate:   &rate,
		UnitCostBase:   &unitCost,
		SourceType:     normaliseAdjustSourceType(in.SourceType),
		SourceID:       ptrUintIfNonZero(in.SourceID),
		ReferenceNote:  buildAdjustMemo(in),
		Memo:           in.Memo,
		IdempotencyKey: in.IdempotencyKey,
		ActorUserID:    in.ActorUserID,
		WarehouseID:    warehouseID,
		MovementDate:   in.MovementDate,
	}
	if err := db.Create(&mov).Error; err != nil {
		return nil, fmt.Errorf("inventory.AdjustStock: create marker movement: %w", err)
	}
	return &AdjustStockResult{
		MovementID:          mov.ID,
		UnitCostBase:        unitCost,
		AdjustmentValueBase: decimal.Zero,
	}, nil
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func validateAdjustInput(in AdjustStockInput) error {
	if in.CompanyID == 0 {
		return fmt.Errorf("inventory.AdjustStock: CompanyID required")
	}
	if in.ItemID == 0 {
		return fmt.Errorf("inventory.AdjustStock: ItemID required")
	}
	if in.MovementDate.IsZero() {
		return fmt.Errorf("inventory.AdjustStock: MovementDate required")
	}
	if in.UnitCost != nil && in.UnitCost.IsNegative() {
		return fmt.Errorf("inventory.AdjustStock: UnitCost cannot be negative")
	}
	return nil
}

// currentAverageCost returns the weighted-average cost at the target balance,
// or zero when no balance row exists yet. Used both as the fallback for
// gain-side cost resolution and as the cost recorded on zero-delta markers.
func currentAverageCost(db *gorm.DB, companyID, itemID uint, warehouseID *uint) (decimal.Decimal, error) {
	bal, err := readOrCreateBalance(db, companyID, itemID, warehouseID, false)
	if err != nil {
		return decimal.Zero, err
	}
	return bal.AverageCost, nil
}

// normaliseAdjustSourceType keeps callers that pass an empty SourceType
// working — defaulting to "adjustment" matches the legacy CreateAdjustment
// behaviour. Explicit values like "stock_count" or "write_off" flow
// through unchanged so downstream reporting can distinguish them.
func normaliseAdjustSourceType(sourceType string) string {
	if sourceType == "" {
		return "adjustment"
	}
	return sourceType
}

// buildAdjustMemo prepends the reason tag to the free-form memo so audit
// reports can scan for reason patterns without a dedicated column. Keeps
// the schema open to new reasons without migration.
func buildAdjustMemo(in AdjustStockInput) string {
	switch {
	case in.Reason != "" && in.Memo != "":
		return fmt.Sprintf("[%s] %s", in.Reason, in.Memo)
	case in.Reason != "":
		return fmt.Sprintf("[%s]", in.Reason)
	default:
		return in.Memo
	}
}

// cachedAdjustmentByKey returns a prior AdjustStock result when the idempotency
// key maps to a recorded movement. For zero-delta markers only — the Receive
// and Issue delegations handle their own caching.
func cachedAdjustmentByKey(db *gorm.DB, companyID uint, key string) (*AdjustStockResult, error) {
	var mov models.InventoryMovement
	err := db.Where("company_id = ? AND idempotency_key = ?", companyID, key).First(&mov).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("inventory.AdjustStock: idempotency lookup: %w", err)
	}
	unitCost := decimal.Zero
	if mov.UnitCostBase != nil {
		unitCost = *mov.UnitCostBase
	}
	total := decimal.Zero
	if mov.UnitCostBase != nil && !mov.QuantityDelta.IsZero() {
		total = mov.QuantityDelta.Mul(unitCost).RoundBank(2)
	}
	return &AdjustStockResult{
		MovementID:          mov.ID,
		UnitCostBase:        unitCost,
		AdjustmentValueBase: total,
	}, nil
}
