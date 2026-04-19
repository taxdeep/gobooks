// 遵循project_guide.md
package inventory

// tracking.go — Phase F inbound / outbound helpers for lot / serial /
// expiry capture. Called from ReceiveStock (F2 inbound). Outbound and
// reversal paths live alongside IssueStock / ReverseMovement (F3).
//
// Orthogonality contract
// ----------------------
// Nothing in this file participates in costing. Lots and serials are
// tracking truth only — they record WHICH physical units moved, WHEN,
// and their EXPIRY. Cost still flows via inventory_balances (moving-avg)
// or inventory_cost_layers + inventory_layer_consumption (FIFO). Don't
// reach into cost layers from here; don't let callers use lot/serial
// identity to override costing.
//
// Guarded cases
// -------------
//   - Untracked item (tracking_mode="none") with lot/serial data:
//     reject with ErrTrackingDataOnUntrackedItem.
//   - Tracked item missing required data:
//     reject with ErrTrackingDataMissing.
//   - Cross-mode data mismatch (lot data for serial item, etc):
//     reject with ErrTrackingModeMismatch.
//   - Duplicate live serial:
//     reject with ErrDuplicateSerialInbound. Also backstopped by the
//     partial unique index in migration 064 so a racy concurrent insert
//     can't slip through.

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// loadTrackingMode fetches the product's TrackingMode. Missing items
// return ErrInvalidItem via verifyInventoryItem in the caller; this
// helper assumes the verification already passed.
func loadTrackingMode(db *gorm.DB, companyID, itemID uint) (string, error) {
	var row struct {
		TrackingMode string
	}
	err := db.Model(&models.ProductService{}).
		Select("tracking_mode").
		Where("id = ? AND company_id = ?", itemID, companyID).
		Scan(&row).Error
	if err != nil {
		return "", fmt.Errorf("load tracking_mode: %w", err)
	}
	if row.TrackingMode == "" {
		return models.TrackingNone, nil
	}
	return row.TrackingMode, nil
}

// validateInboundTracking checks the lot/serial shape against the
// item's tracking_mode. Does NOT persist anything — just gates. Called
// at the top of ReceiveStock before any mutation.
func validateInboundTracking(mode string, in ReceiveStockInput) error {
	hasLot := in.LotNumber != ""
	hasSerials := len(in.SerialNumbers) > 0
	hasSerialExpiries := len(in.SerialExpiryDates) > 0

	switch mode {
	case models.TrackingNone:
		if hasLot || hasSerials || hasSerialExpiries || in.ExpiryDate != nil {
			return ErrTrackingDataOnUntrackedItem
		}
		return nil

	case models.TrackingLot:
		if hasSerials || hasSerialExpiries {
			return fmt.Errorf("%w: lot-tracked item received with serial data", ErrTrackingModeMismatch)
		}
		if !hasLot {
			return fmt.Errorf("%w: lot_number required for lot-tracked item", ErrTrackingDataMissing)
		}
		return nil

	case models.TrackingSerial:
		if hasLot || in.ExpiryDate != nil {
			return fmt.Errorf("%w: serial-tracked item received with lot/expiry at header", ErrTrackingModeMismatch)
		}
		if !hasSerials {
			return fmt.Errorf("%w: serial_numbers required for serial-tracked item", ErrTrackingDataMissing)
		}
		expected := int(in.Quantity.IntPart())
		if !in.Quantity.IsInteger() || in.Quantity.Sign() <= 0 {
			return fmt.Errorf("%w: serial-tracked quantity must be a positive integer (got %s)",
				ErrSerialCountMismatch, in.Quantity.String())
		}
		if len(in.SerialNumbers) != expected {
			return fmt.Errorf("%w: quantity=%s but %d serial_numbers provided",
				ErrSerialCountMismatch, in.Quantity.String(), len(in.SerialNumbers))
		}
		if hasSerialExpiries && len(in.SerialExpiryDates) != len(in.SerialNumbers) {
			return fmt.Errorf("%w: serial_expiry_dates length (%d) must match serial_numbers (%d)",
				ErrSerialCountMismatch, len(in.SerialExpiryDates), len(in.SerialNumbers))
		}
		return nil

	default:
		return fmt.Errorf("inventory: unknown tracking_mode %q on item %d", mode, in.ItemID)
	}
}

// persistLotInbound creates or tops up the inventory_lots row for this
// receipt. Uses the (company, item, lot_number) unique index to decide
// create-vs-top-up. Returns the lot row ID in case callers ever need
// it (not surfaced on ReceiveStockResult in F2 — would bloat the
// result type when only the lot-tracked callers care).
func persistLotInbound(db *gorm.DB, in ReceiveStockInput) error {
	var lot models.InventoryLot
	err := db.Where("company_id = ? AND item_id = ? AND lot_number = ?",
		in.CompanyID, in.ItemID, in.LotNumber).First(&lot).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("load lot: %w", err)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// First receipt of this lot — create.
		lot = models.InventoryLot{
			CompanyID:         in.CompanyID,
			ItemID:            in.ItemID,
			LotNumber:         in.LotNumber,
			ExpiryDate:        in.ExpiryDate,
			ReceivedDate:      in.MovementDate,
			OriginalQuantity:  in.Quantity,
			RemainingQuantity: in.Quantity,
		}
		if err := db.Create(&lot).Error; err != nil {
			return fmt.Errorf("create lot: %w", err)
		}
		return nil
	}

	// Top-up: existing lot. Increment original + remaining. If the
	// caller supplies an expiry on the top-up and the existing row has
	// none, adopt it; if both have a value and they disagree, we reject
	// — reuse of a lot_number for units with a different shelf-life is
	// a data-integrity problem, not a legitimate top-up.
	if in.ExpiryDate != nil {
		switch {
		case lot.ExpiryDate == nil:
			lot.ExpiryDate = in.ExpiryDate
		case !lot.ExpiryDate.Equal(*in.ExpiryDate):
			return fmt.Errorf("inventory: lot %q expiry mismatch on top-up (existing=%s, incoming=%s) — lot numbers must be reused only for identical shelf-life",
				in.LotNumber, lot.ExpiryDate.Format("2006-01-02"), in.ExpiryDate.Format("2006-01-02"))
		}
	}
	lot.OriginalQuantity = lot.OriginalQuantity.Add(in.Quantity)
	lot.RemainingQuantity = lot.RemainingQuantity.Add(in.Quantity)
	if err := db.Save(&lot).Error; err != nil {
		return fmt.Errorf("top-up lot: %w", err)
	}
	return nil
}

// persistSerialInbound creates one inventory_serial_units row per
// serial with state=on_hand. A duplicate-live serial is rejected;
// expired/void-archived rows from a previous lifecycle are allowed to
// coexist alongside the new on_hand row for audit continuity.
func persistSerialInbound(db *gorm.DB, in ReceiveStockInput) error {
	for i, sn := range in.SerialNumbers {
		// Explicit pre-check for a better error message; the DB unique
		// index also backstops if a race slips by.
		var liveCount int64
		if err := db.Model(&models.InventorySerialUnit{}).
			Where("company_id = ? AND item_id = ? AND serial_number = ? AND current_state IN ?",
				in.CompanyID, in.ItemID, sn,
				[]models.SerialState{models.SerialStateOnHand, models.SerialStateReserved},
			).Count(&liveCount).Error; err != nil {
			return fmt.Errorf("pre-check serial %q: %w", sn, err)
		}
		if liveCount > 0 {
			return fmt.Errorf("%w: %q", ErrDuplicateSerialInbound, sn)
		}

		row := models.InventorySerialUnit{
			CompanyID:    in.CompanyID,
			ItemID:       in.ItemID,
			SerialNumber: sn,
			CurrentState: models.SerialStateOnHand,
			ReceivedDate: in.MovementDate,
		}
		if i < len(in.SerialExpiryDates) && in.SerialExpiryDates[i] != nil {
			row.ExpiryDate = in.SerialExpiryDates[i]
		}
		if err := db.Create(&row).Error; err != nil {
			return fmt.Errorf("create serial unit %q: %w", sn, err)
		}
	}
	return nil
}

// ── Outbound tracking (Phase F3) ─────────────────────────────────────────────

// validateOutboundTracking gates tracked IssueStock calls the same way
// validateInboundTracking gates ReceiveStock. It does NOT decrement any
// state; that happens in consumeLotSelections / consumeSerialSelections
// after the movement row has been created (so consumption rows can
// reference movement.ID).
func validateOutboundTracking(mode string, in IssueStockInput) error {
	hasLots := len(in.LotSelections) > 0
	hasSerials := len(in.SerialSelections) > 0

	switch mode {
	case models.TrackingNone:
		if hasLots || hasSerials {
			return ErrTrackingDataOnUntrackedItem
		}
		return nil

	case models.TrackingLot:
		if hasSerials {
			return fmt.Errorf("%w: lot-tracked item issued with serial selections", ErrTrackingModeMismatch)
		}
		if !hasLots {
			return fmt.Errorf("%w: LotSelections required", ErrLotSelectionMissing)
		}
		// SUM(LotSelections.Quantity) must equal the event quantity.
		var sum = decimal.Zero
		for _, sel := range in.LotSelections {
			if sel.LotID == 0 {
				return fmt.Errorf("%w: LotID must be non-zero", ErrLotSelectionMissing)
			}
			if !sel.Quantity.IsPositive() {
				return fmt.Errorf("%w: selection qty must be positive (got %s)",
					ErrLotSelectionMissing, sel.Quantity.String())
			}
			sum = sum.Add(sel.Quantity)
		}
		if !sum.Equal(in.Quantity) {
			return fmt.Errorf("%w: selection sum %s != issue quantity %s",
				ErrLotSelectionMissing, sum.String(), in.Quantity.String())
		}
		return nil

	case models.TrackingSerial:
		if hasLots {
			return fmt.Errorf("%w: serial-tracked item issued with lot selections", ErrTrackingModeMismatch)
		}
		expected := int(in.Quantity.IntPart())
		if !in.Quantity.IsInteger() || in.Quantity.Sign() <= 0 {
			return fmt.Errorf("%w: serial-tracked quantity must be a positive integer (got %s)",
				ErrSerialSelectionMissing, in.Quantity.String())
		}
		if len(in.SerialSelections) != expected {
			return fmt.Errorf("%w: got %d serial selections for quantity %s",
				ErrSerialSelectionMissing, len(in.SerialSelections), in.Quantity.String())
		}
		return nil

	default:
		return fmt.Errorf("inventory: unknown tracking_mode %q on item %d", mode, in.ItemID)
	}
}

// consumeLotSelections decrements each selected lot's RemainingQuantity
// and writes an inventory_tracking_consumption row per (movement, lot)
// pair so ReverseMovement can undo the draw precisely.
//
// movementID is the inventory_movements row ID of the issue event —
// passed in because IssueStock creates the movement before calling
// this. Company/item isolation is enforced on every lot lookup.
func consumeLotSelections(db *gorm.DB, in IssueStockInput, movementID uint) error {
	for _, sel := range in.LotSelections {
		var lot models.InventoryLot
		if err := db.Where("id = ? AND company_id = ? AND item_id = ?",
			sel.LotID, in.CompanyID, in.ItemID).First(&lot).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: lot_id=%d", ErrLotNotFound, sel.LotID)
			}
			return fmt.Errorf("load lot %d: %w", sel.LotID, err)
		}
		if sel.Quantity.GreaterThan(lot.RemainingQuantity) {
			return fmt.Errorf("%w: lot_id=%d need %s have %s",
				ErrLotSelectionExceedsRemaining, lot.ID,
				sel.Quantity.String(), lot.RemainingQuantity.String())
		}
		lot.RemainingQuantity = lot.RemainingQuantity.Sub(sel.Quantity)
		if err := db.Save(&lot).Error; err != nil {
			return fmt.Errorf("decrement lot %d: %w", lot.ID, err)
		}
		lotID := lot.ID
		consumption := models.InventoryTrackingConsumption{
			CompanyID:       in.CompanyID,
			IssueMovementID: movementID,
			ItemID:          in.ItemID,
			LotID:           &lotID,
			QuantityDrawn:   sel.Quantity,
		}
		if err := db.Create(&consumption).Error; err != nil {
			return fmt.Errorf("tracking consumption (lot %d): %w", lot.ID, err)
		}
	}
	return nil
}

// consumeSerialSelections flips each selected serial from on_hand to
// issued and writes an inventory_tracking_consumption row per serial.
// Rejects if any serial is not found or not currently on_hand.
func consumeSerialSelections(db *gorm.DB, in IssueStockInput, movementID uint) error {
	for _, sn := range in.SerialSelections {
		var unit models.InventorySerialUnit
		err := db.Where("company_id = ? AND item_id = ? AND serial_number = ? AND current_state = ?",
			in.CompanyID, in.ItemID, sn, models.SerialStateOnHand).
			First(&unit).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// Distinguish "wrong state" from "doesn't exist" —
				// better operator signal.
				var anyState int64
				db.Model(&models.InventorySerialUnit{}).
					Where("company_id = ? AND item_id = ? AND serial_number = ?",
						in.CompanyID, in.ItemID, sn).Count(&anyState)
				if anyState > 0 {
					return fmt.Errorf("%w: %q", ErrSerialNotOnHand, sn)
				}
				return fmt.Errorf("%w: %q", ErrSerialNotFound, sn)
			}
			return fmt.Errorf("load serial %q: %w", sn, err)
		}
		unit.CurrentState = models.SerialStateIssued
		if err := db.Save(&unit).Error; err != nil {
			return fmt.Errorf("flip serial %q to issued: %w", sn, err)
		}
		unitID := unit.ID
		one := decimal.NewFromInt(1)
		consumption := models.InventoryTrackingConsumption{
			CompanyID:       in.CompanyID,
			IssueMovementID: movementID,
			ItemID:          in.ItemID,
			SerialUnitID:    &unitID,
			QuantityDrawn:   one,
		}
		if err := db.Create(&consumption).Error; err != nil {
			return fmt.Errorf("tracking consumption (serial %q): %w", sn, err)
		}
	}
	return nil
}

// restoreTrackedConsumption unwinds the lot/serial consumption anchored
// to the given original movement. Called from ReverseMovement after
// loading the tracking mode from the item.
//
// Lot rows: re-add QuantityDrawn to the lot's RemainingQuantity.
// Serial rows: flip the serial unit's CurrentState back to on_hand.
//
// Each processed row is stamped with ReversedByMovementID so a repeat
// reversal can't double-restore.
//
// If the movement is tracked but the anchor log is empty, the caller
// must treat it as ErrTrackingAnchorMissing — we do NOT guess. This
// matches the E2.1 stance for cost-layer consumption: no anchor, no
// unwind.
func restoreTrackedConsumption(db *gorm.DB, companyID, itemID, originalMovementID, reversalMovementID uint) error {
	var rows []models.InventoryTrackingConsumption
	if err := db.Where("company_id = ? AND item_id = ? AND issue_movement_id = ? AND reversed_by_movement_id IS NULL",
		companyID, itemID, originalMovementID).Find(&rows).Error; err != nil {
		return fmt.Errorf("load tracking consumption: %w", err)
	}
	if len(rows) == 0 {
		return ErrTrackingAnchorMissing
	}

	for i := range rows {
		row := &rows[i]
		switch {
		case row.LotID != nil:
			var lot models.InventoryLot
			if err := db.First(&lot, *row.LotID).Error; err != nil {
				return fmt.Errorf("reload lot %d: %w", *row.LotID, err)
			}
			lot.RemainingQuantity = lot.RemainingQuantity.Add(row.QuantityDrawn)
			if err := db.Save(&lot).Error; err != nil {
				return fmt.Errorf("restore lot %d remaining: %w", *row.LotID, err)
			}
		case row.SerialUnitID != nil:
			var unit models.InventorySerialUnit
			if err := db.First(&unit, *row.SerialUnitID).Error; err != nil {
				return fmt.Errorf("reload serial unit %d: %w", *row.SerialUnitID, err)
			}
			unit.CurrentState = models.SerialStateOnHand
			if err := db.Save(&unit).Error; err != nil {
				return fmt.Errorf("restore serial %d: %w", *row.SerialUnitID, err)
			}
		}
		row.ReversedByMovementID = &reversalMovementID
		if err := db.Save(row).Error; err != nil {
			return fmt.Errorf("mark consumption %d reversed: %w", row.ID, err)
		}
	}
	return nil
}
