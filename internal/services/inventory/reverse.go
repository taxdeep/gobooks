// 遵循project_guide.md
package inventory

// reverse.go — ReverseMovement — fifth IN verb, Phase D.0 slice 5.
//
// Reverses a prior movement using the ORIGINAL movement's snapshot cost.
// This is the correctness keystone: a December return of a March sale
// must reverse March's COGS at March's cost, not December's current
// weighted average. Same for purchase voids — the cost removed from
// inventory is the cost at the moment of purchase, not the drifted avg.
//
// Reversal is expressed as a NEW movement row with QuantityDelta of the
// opposite sign, linked bidirectionally to the original via
// reversal_of_movement_id + reversed_by_movement_id. History is append-
// only; the original row is never modified except for that one FK.

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ReverseMovement books a reversal entry against an existing movement.
// See INVENTORY_MODULE_API.md §3.5.
func ReverseMovement(db *gorm.DB, in ReverseMovementInput) (*ReverseMovementResult, error) {
	if err := validateReverseInput(in); err != nil {
		return nil, err
	}

	// Idempotency short-circuit.
	if in.IdempotencyKey != "" {
		if cached, err := cachedReversalByKey(db, in.CompanyID, in.IdempotencyKey); err != nil {
			return nil, err
		} else if cached != nil {
			return cached, nil
		}
	}

	// Load the original and verify it belongs to this company and hasn't
	// already been reversed.
	var orig models.InventoryMovement
	err := db.Where("id = ? AND company_id = ?", in.OriginalMovementID, in.CompanyID).
		First(&orig).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("inventory.ReverseMovement: original movement not found")
	}
	if err != nil {
		return nil, fmt.Errorf("inventory.ReverseMovement: load original: %w", err)
	}
	if orig.ReversedByMovementID != nil {
		return nil, ErrReversalAlreadyApplied
	}
	// A reversal row itself cannot be reversed (produce a new, forward
	// movement instead). Keeps the chain unambiguous.
	if orig.ReversalOfMovementID != nil {
		return nil, fmt.Errorf("inventory.ReverseMovement: cannot reverse a reversal row (id=%d)", orig.ID)
	}

	// Determine the snapshot cost. Prefer UnitCostBase (populated by D.0
	// code paths); fall back to the document-currency UnitCost for legacy
	// rows that pre-date the expanded schema.
	snapshotCost := decimal.Zero
	switch {
	case orig.UnitCostBase != nil && !orig.UnitCostBase.IsZero():
		snapshotCost = *orig.UnitCostBase
	case orig.UnitCost != nil:
		snapshotCost = *orig.UnitCost
	}

	// Reversal delta has the opposite sign of the original.
	reverseDelta := orig.QuantityDelta.Neg()

	// Lock + read target balance.
	bal, err := readOrCreateBalance(db, in.CompanyID, orig.ItemID, orig.WarehouseID, true)
	if err != nil {
		return nil, err
	}

	// When the reversal is an outflow (undoing a prior receipt), insufficient
	// stock is blocked — the original receipt's units may have been partially
	// consumed and cannot be plucked out of thin air.
	if reverseDelta.IsNegative() {
		removeQty := reverseDelta.Abs()
		if bal.QuantityOnHand.LessThan(removeQty) {
			return nil, fmt.Errorf("%w: reversing movement %d needs %s, have %s",
				ErrInsufficientStock, orig.ID, removeQty.String(), bal.QuantityOnHand.String())
		}
	}

	if err := applyReversalAtSnapshotCost(db, bal, reverseDelta, snapshotCost); err != nil {
		return nil, err
	}

	// Persist the reversal movement. Movement type mirrors the original so
	// reports that filter by type stay coherent; direction is conveyed by
	// QuantityDelta sign and the source_type/reversal_of linkage.
	absQty := reverseDelta.Abs()
	reversalValueBase := reverseDelta.Mul(snapshotCost).RoundBank(2)
	origMovID := orig.ID
	costSnapshot := costSnapshotForQuantity(orig, absQty, snapshotCost)

	reversal := models.InventoryMovement{
		CompanyID:            in.CompanyID,
		ItemID:               orig.ItemID,
		MovementType:         orig.MovementType,
		QuantityDelta:        reverseDelta,
		UnitCost:             &costSnapshot.UnitCostDoc,
		TotalCost:            &costSnapshot.TotalCostDoc,
		CurrencyCode:         costSnapshot.CurrencyCode,
		ExchangeRate:         &costSnapshot.ExchangeRate,
		UnitCostBase:         &costSnapshot.UnitCostBase,
		SourceType:           in.SourceType,
		SourceID:             ptrUintIfNonZero(in.SourceID),
		ReferenceNote:        buildReversalReferenceNote(in, orig),
		Memo:                 in.Memo,
		IdempotencyKey:       in.IdempotencyKey,
		ActorUserID:          in.ActorUserID,
		ReversalOfMovementID: &origMovID,
		WarehouseID:          orig.WarehouseID,
		MovementDate:         in.MovementDate,
	}
	if err := db.Create(&reversal).Error; err != nil {
		return nil, fmt.Errorf("inventory.ReverseMovement: create reversal movement: %w", err)
	}

	// Phase E2.1: if the original was a FIFO-costed issue, restore the
	// consumed layers' RemainingQuantity. We detect FIFO by the existence
	// of consumption rows — weighted-avg issues never wrote any. Legacy
	// FIFO issues (pre-E2.1, consumption log absent) skip this step; on-
	// hand restoration via snapshot cost is accounting-correct but the
	// FIFO layer counters stay stale until a reconcile job reseats them.
	if orig.QuantityDelta.IsNegative() {
		if err := restoreConsumedLayers(db, in.CompanyID, orig.ID, reversal.ID); err != nil {
			return nil, err
		}
	}

	// Phase F3: if the original was a tracked outbound, unwind the
	// lot/serial consumption anchors. Untracked items skip this.
	// Tracked items WITHOUT anchors (data anomaly or legacy) return
	// ErrTrackingAnchorMissing — we don't guess which lots/serials to
	// restore. Same stance as E2.1's missing-layer-consumption case.
	if orig.QuantityDelta.IsNegative() {
		trackingMode, err := loadTrackingMode(db, in.CompanyID, orig.ItemID)
		if err != nil {
			return nil, err
		}
		if trackingMode == models.TrackingLot || trackingMode == models.TrackingSerial {
			if err := restoreTrackedConsumption(db, in.CompanyID, orig.ItemID, orig.ID, reversal.ID); err != nil {
				return nil, err
			}
		}
	}

	// Link the original back to the reversal — closes the bidirectional
	// chain. Uses UpdateColumn to avoid GORM's default zero-value skipping
	// when the field is still nil on `orig`.
	if err := db.Model(&models.InventoryMovement{}).
		Where("id = ?", orig.ID).
		Update("reversed_by_movement_id", reversal.ID).Error; err != nil {
		return nil, fmt.Errorf("inventory.ReverseMovement: link original: %w", err)
	}

	return &ReverseMovementResult{
		ReversalMovementID: reversal.ID,
		UnitCostBase:       snapshotCost,
		ReversalValueBase:  reversalValueBase,
	}, nil
}

// restoreConsumedLayers loads the live layer-consumption rows for an
// original issue movement and adds each drawn quantity back to its layer's
// RemainingQuantity. The consumption rows are then stamped with
// reversed_by_movement_id so a second reversal attempt cannot double-
// restore. If no rows exist this function is a no-op (the issue was
// weighted-avg OR legacy pre-E2.1 FIFO).
func restoreConsumedLayers(db *gorm.DB, companyID, originalMovementID, reversalMovementID uint) error {
	var rows []models.InventoryLayerConsumption
	if err := db.Where("company_id = ? AND issue_movement_id = ? AND reversed_by_movement_id IS NULL",
		companyID, originalMovementID).Find(&rows).Error; err != nil {
		return fmt.Errorf("inventory.ReverseMovement: load layer consumption: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}

	for i := range rows {
		row := &rows[i]
		var layer models.InventoryCostLayer
		if err := db.First(&layer, row.LayerID).Error; err != nil {
			return fmt.Errorf("inventory.ReverseMovement: reload layer %d: %w", row.LayerID, err)
		}
		layer.RemainingQuantity = layer.RemainingQuantity.Add(row.QuantityDrawn)
		if err := db.Save(&layer).Error; err != nil {
			return fmt.Errorf("inventory.ReverseMovement: restore layer %d: %w", layer.ID, err)
		}
		row.ReversedByMovementID = &reversalMovementID
		if err := db.Save(row).Error; err != nil {
			return fmt.Errorf("inventory.ReverseMovement: mark consumption %d reversed: %w", row.ID, err)
		}
	}
	return nil
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func validateReverseInput(in ReverseMovementInput) error {
	if in.CompanyID == 0 {
		return fmt.Errorf("inventory.ReverseMovement: CompanyID required")
	}
	if in.OriginalMovementID == 0 {
		return fmt.Errorf("inventory.ReverseMovement: OriginalMovementID required")
	}
	if in.MovementDate.IsZero() {
		return fmt.Errorf("inventory.ReverseMovement: MovementDate required")
	}
	if in.SourceType == "" {
		return ErrInvalidSource
	}
	return nil
}

// cachedReversalByKey returns the cached result for a prior reversal that
// used the same idempotency key.
func cachedReversalByKey(db *gorm.DB, companyID uint, key string) (*ReverseMovementResult, error) {
	var mov models.InventoryMovement
	err := db.Where("company_id = ? AND idempotency_key = ?", companyID, key).First(&mov).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inventory.ReverseMovement: idempotency lookup: %w", err)
	}
	// A cached key that doesn't map to a reversal row (ReversalOfMovementID
	// unset) signals a caller-side collision — reject loudly.
	if mov.ReversalOfMovementID == nil {
		return nil, ErrDuplicateIdempotency
	}
	unitCost := decimal.Zero
	if mov.UnitCostBase != nil {
		unitCost = *mov.UnitCostBase
	}
	return &ReverseMovementResult{
		ReversalMovementID: mov.ID,
		UnitCostBase:       unitCost,
		ReversalValueBase:  mov.QuantityDelta.Mul(unitCost).RoundBank(2),
	}, nil
}

func buildReversalReferenceNote(in ReverseMovementInput, orig models.InventoryMovement) string {
	if in.Memo != "" {
		return fmt.Sprintf("[reversal:%s] %s", in.Reason, in.Memo)
	}
	return fmt.Sprintf("[reversal:%s] reverses movement #%d", in.Reason, orig.ID)
}
