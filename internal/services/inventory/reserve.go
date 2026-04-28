// 遵循project_guide.md
package inventory

// reserve.go — ReserveStock / ReleaseStock (Phase E1).
//
// Design shape
// ------------
// A reservation is a live counter on inventory_balances.quantity_reserved.
// Reserving commits units against a document (e.g. confirmed SO) so that
// parallel posts cannot issue those units to someone else. Shipping the
// reservation is IssueStock (which consumes on-hand and, in a future slice,
// also decrements the reservation); cancelling it is ReleaseStock.
//
// Reserved NEVER produces a movement row. The movement ledger tracks stock
// truth (on-hand); the reservation counter tracks *commitments* which are
// not yet stock events. Reports expose:
//
//   Available = OnHand − Reserved
//
// Bounds:
//   - Reserved ≥ 0 at all times. ReleaseStock past zero returns
//     ErrReservationUnderflow.
//   - Reserved ≤ OnHand at reserve time, by default. ReserveStock past
//     OnHand returns ErrInsufficientAvailable. (Allowing negative-available
//     is a future flag if we need backorder-style reservations.)
//
// Idempotency (E1 gap)
// --------------------
// The Input types carry an IdempotencyKey field for API-surface stability,
// but this slice does NOT persist reservation events, so it has nothing to
// dedupe against. Callers are responsible for not invoking ReserveStock
// twice with the same logical intent. A later slice can add an
// inventory_reservations ledger table and enforce the key uniquely.

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ReserveStock increments the reserved counter for (company, item,
// warehouse) by the requested quantity, provided the available quantity
// (on-hand − already-reserved) can cover it. See INVENTORY_MODULE_API.md
// §3.7.
func ReserveStock(db *gorm.DB, in ReserveStockInput) (*ReserveStockResult, error) {
	if err := validateReserveInput(in, "ReserveStock"); err != nil {
		return nil, err
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

	bal, err := readOrCreateBalance(db, in.CompanyID, in.ItemID, warehouseID, true)
	if err != nil {
		return nil, err
	}

	available := bal.QuantityOnHand.Sub(bal.QuantityReserved)
	if in.Quantity.GreaterThan(available) {
		return nil, fmt.Errorf("%w: need %s, available %s (on_hand=%s reserved=%s)",
			ErrInsufficientAvailable, in.Quantity.String(), available.String(),
			bal.QuantityOnHand.String(), bal.QuantityReserved.String())
	}

	bal.QuantityReserved = bal.QuantityReserved.Add(in.Quantity)
	if err := db.Save(bal).Error; err != nil {
		return nil, fmt.Errorf("inventory.ReserveStock: persist balance: %w", err)
	}
	return &ReserveStockResult{QuantityReserved: bal.QuantityReserved}, nil
}

// ReleaseStock decrements the reserved counter. Reserves can be released
// because the upstream document was cancelled, edited, or shipped (in which
// case IssueStock is also called — the two events are independent). The
// quantity must not exceed the currently-reserved amount.
func ReleaseStock(db *gorm.DB, in ReleaseStockInput) error {
	if err := validateReserveInput(in, "ReleaseStock"); err != nil {
		return err
	}
	if err := verifyInventoryItem(db, in.CompanyID, in.ItemID); err != nil {
		return err
	}

	warehouseID := ptrUintIfNonZero(in.WarehouseID)
	if warehouseID != nil {
		if err := verifyWarehouseBelongsToCompany(db, in.CompanyID, *warehouseID); err != nil {
			return err
		}
	}

	bal, err := readOrCreateBalance(db, in.CompanyID, in.ItemID, warehouseID, true)
	if err != nil {
		return err
	}
	if in.Quantity.GreaterThan(bal.QuantityReserved) {
		return fmt.Errorf("%w: release %s, currently reserved %s",
			ErrReservationUnderflow, in.Quantity.String(), bal.QuantityReserved.String())
	}
	bal.QuantityReserved = bal.QuantityReserved.Sub(in.Quantity)
	if err := db.Save(bal).Error; err != nil {
		return fmt.Errorf("inventory.ReleaseStock: persist balance: %w", err)
	}
	return nil
}

// validateReserveInput is shared between Reserve and Release since
// ReleaseStockInput is a type alias for ReserveStockInput.
func validateReserveInput(in ReserveStockInput, verb string) error {
	if in.CompanyID == 0 {
		return fmt.Errorf("inventory.%s: CompanyID required", verb)
	}
	if in.ItemID == 0 {
		return fmt.Errorf("inventory.%s: ItemID required", verb)
	}
	if !in.Quantity.IsPositive() {
		return ErrNegativeQuantity
	}
	if in.SourceType == "" {
		return ErrInvalidSource
	}
	return nil
}

// Compile-time guard: the two input types are aliased, so this import-only
// reference prevents the linter from flagging decimal/errors as unused when
// one of the functions shrinks in the future.
var _ = errors.New
var _ = decimal.Zero
var _ = models.InventoryBalance{}
