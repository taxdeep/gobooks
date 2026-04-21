// 遵循project_guide.md
package inventory

// issue_vendor_return.go — Phase I slice I.6b.2a: the dedicated
// narrow-semantic outflow verb for return-to-vendor at the ORIGINAL
// receipt cost. The charter Q3 keystone for the AP-side Return
// Shipment path.
//
// Why this is a new verb and NOT an extension of IssueStock
// ---------------------------------------------------------
// IssueStock's contract: "callers never pass a cost; the module
// returns the cost." That contract is load-bearing for the cost-
// authority principle — AR/AP layers never own inventory cost truth,
// so the inventory engine stays the single source of truth for what
// a unit costs when it leaves.
//
// AP returns need a DIFFERENT cost semantic: the exact
// snapshot cost of the ORIGINAL receipt, not today's running average
// or a FIFO peel. Partial-qty returns demand rate × qty, not the
// full-match shape that `ReverseMovement` offers.
//
// The naive shortcut — "extend IssueStock to accept an optional
// UnitCostOverride gated by a SourceType allow-list" — was proposed
// and **explicitly rejected** by charter Q3. Even gated, it makes
// the cost-authority contract softer, and the allow-list is a
// living hole that expands under feature pressure. A dedicated
// narrow verb keeps the contract tight by construction:
//
//   - Name signals intent: "vendor return".
//   - No UnitCost / UnitCostOverride field ever exists in
//     IssueVendorReturnInput. The cost is READ from the source
//     movement, never supplied.
//   - Scope is bounded — if scrap-at-historical-cost or manual
//     tracked-lot reversal need the same shape, they open their
//     own narrow verb, each with its own name and its own scope.
//
// This file is under 250 lines on purpose. If it grows much past
// that, the likely cause is scope creep and someone should push
// back.

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// IssueVendorReturn books an outflow movement at the traced ORIGINAL
// receipt cost. See INVENTORY_MODULE_API.md §7 Phase I.6 / Q3.
//
// Behavior:
//
//  1. Load + validate the source movement (company scope, not a
//     reversal row, positive cost). The source movement is the
//     cost anchor — its `unit_cost_base` becomes this outflow's
//     unit cost.
//  2. Resolve the outflow WarehouseID (caller-supplied, or
//     inherited from the source movement).
//  3. Row-lock the target balance; reject if insufficient.
//  4. Decrement balance on-hand; `average_cost` is unchanged
//     (weighted-avg outbound convention — preserves cost-authority
//     for subsequent issues).
//  5. Persist an outflow inventory_movements row with
//     `movement_type='vendor_return'`, the caller's SourceType /
//     SourceID / SourceLineID (e.g. `'vendor_return_shipment'`),
//     and the traced UnitCostBase.
//  6. Return MovementID + UnitCostBase + OutflowValueBase.
//
// Deliberately NOT done:
//   - FIFO layer peel (see file-level doc).
//   - Tracked-lot consumption. If the item is lot/serial-tracked,
//     the caller must supply selections via a future slice; for
//     I.6b.2a scope, tracked items on the return path are out-of-
//     scope and fail fast at the balance check (insufficient stock
//     reads the aggregate, which is fine for untracked items).
//   - PPV / price-variance leg. Charter explicit: "Writes no PPV
//     leg." The outflow value matches the source cost exactly.
//   - Landed-cost allocation. No concept — outflow doesn't carry
//     landed cost, it mirrors the source movement's cost.
func IssueVendorReturn(db *gorm.DB, in IssueVendorReturnInput) (*IssueVendorReturnResult, error) {
	if err := validateIssueVendorReturnInput(in); err != nil {
		return nil, err
	}

	// Idempotency short-circuit.
	if in.IdempotencyKey != "" {
		if cached, err := issueVendorReturnByKey(db, in.CompanyID, in.IdempotencyKey); err != nil {
			return nil, err
		} else if cached != nil {
			return cached, nil
		}
	}

	// Load + validate the source movement (the cost anchor).
	var orig models.InventoryMovement
	err := db.Where("id = ? AND company_id = ?", in.OriginalMovementID, in.CompanyID).
		First(&orig).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("inventory.IssueVendorReturn: source movement %d not found", in.OriginalMovementID)
	}
	if err != nil {
		return nil, fmt.Errorf("inventory.IssueVendorReturn: load source movement: %w", err)
	}
	// Source must not itself be a reversal row — lineage-chain clarity.
	if orig.ReversalOfMovementID != nil {
		return nil, fmt.Errorf(
			"inventory.IssueVendorReturn: source movement %d is a reversal row; trace the original receipt instead",
			orig.ID,
		)
	}
	// Source must be a positive-delta row (a receipt / inflow). Tracing
	// an outflow as the cost source is almost always a caller bug and
	// would produce wrong economics.
	if !orig.QuantityDelta.IsPositive() {
		return nil, fmt.Errorf(
			"inventory.IssueVendorReturn: source movement %d has non-positive delta (%s); cost trace must point at an inflow",
			orig.ID, orig.QuantityDelta.String(),
		)
	}
	// Extract the snapshot cost. Prefer UnitCostBase (canonical since
	// Phase D.0); fall back to UnitCost for legacy rows.
	snapshotCost := decimal.Zero
	switch {
	case orig.UnitCostBase != nil && !orig.UnitCostBase.IsZero():
		snapshotCost = *orig.UnitCostBase
	case orig.UnitCost != nil && !orig.UnitCost.IsZero():
		snapshotCost = *orig.UnitCost
	}
	if !snapshotCost.IsPositive() {
		return nil, fmt.Errorf(
			"inventory.IssueVendorReturn: source movement %d has no traceable cost (unit_cost_base=0); cannot book a return at zero cost",
			orig.ID,
		)
	}

	// Resolve the outflow warehouse.
	var warehouseID *uint
	if in.WarehouseID != 0 {
		warehouseID = ptrUintIfNonZero(in.WarehouseID)
		if err := verifyWarehouseBelongsToCompany(db, in.CompanyID, in.WarehouseID); err != nil {
			return nil, err
		}
	} else {
		// Inherit the source movement's warehouse (happy path —
		// goods leave from wherever they arrived).
		warehouseID = orig.WarehouseID
	}

	// Row-lock balance; enforce sufficient stock. Returns never
	// AllowNegative — if the caller says "we shipped 10 back" but
	// only 7 are on-hand, either the count is wrong or the goods
	// already left via another path. Fail loud.
	bal, err := readOrCreateBalance(db, in.CompanyID, orig.ItemID, warehouseID, true)
	if err != nil {
		return nil, err
	}
	if bal.QuantityOnHand.LessThan(in.Quantity) {
		return nil, fmt.Errorf(
			"%w: vendor return needs %s, have %s (item=%d)",
			ErrInsufficientStock, in.Quantity.String(), bal.QuantityOnHand.String(), orig.ItemID,
		)
	}

	// Decrement balance on-hand. Average cost unchanged (outbound
	// convention — preserves authoritative weighted-avg for future
	// issues that WILL use IssueStock's avg-reading path).
	bal.QuantityOnHand = bal.QuantityOnHand.Sub(in.Quantity)
	if err := db.Save(bal).Error; err != nil {
		return nil, fmt.Errorf("inventory.IssueVendorReturn: save balance: %w", err)
	}

	// Persist the outflow movement row at the TRACED cost.
	outflowValueBase := in.Quantity.Mul(snapshotCost).RoundBank(2)
	unitCostBase := snapshotCost
	totalDoc := outflowValueBase // doc == base for the traced-cost verb
	rate := decimal.NewFromInt(1)

	mov := models.InventoryMovement{
		CompanyID:      in.CompanyID,
		ItemID:         orig.ItemID,
		MovementType:   models.MovementTypeVendorReturn,
		QuantityDelta:  in.Quantity.Neg(), // outflow
		UnitCost:       &snapshotCost,
		TotalCost:      &totalDoc,
		CurrencyCode:   orig.CurrencyCode,
		ExchangeRate:   &rate,
		UnitCostBase:   &unitCostBase,
		SourceType:     in.SourceType,
		SourceID:       ptrUintIfNonZero(in.SourceID),
		SourceLineID:   in.SourceLineID,
		ReferenceNote:  buildIssueVendorReturnReferenceNote(in, orig),
		Memo:           in.Memo,
		IdempotencyKey: in.IdempotencyKey,
		ActorUserID:    in.ActorUserID,
		WarehouseID:    warehouseID,
		MovementDate:   in.MovementDate,
	}
	if err := db.Create(&mov).Error; err != nil {
		return nil, fmt.Errorf("inventory.IssueVendorReturn: create outflow movement: %w", err)
	}

	return &IssueVendorReturnResult{
		MovementID:       mov.ID,
		UnitCostBase:     snapshotCost,
		OutflowValueBase: outflowValueBase,
	}, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

func validateIssueVendorReturnInput(in IssueVendorReturnInput) error {
	if in.CompanyID == 0 {
		return fmt.Errorf("inventory.IssueVendorReturn: CompanyID required")
	}
	if in.OriginalMovementID == 0 {
		return fmt.Errorf("inventory.IssueVendorReturn: OriginalMovementID required — the cost anchor must be named explicitly (Q3: lineage in, not cost in)")
	}
	if !in.Quantity.IsPositive() {
		return ErrNegativeQuantity
	}
	if in.MovementDate.IsZero() {
		return fmt.Errorf("inventory.IssueVendorReturn: MovementDate required")
	}
	if in.SourceType == "" {
		return ErrInvalidSource
	}
	return nil
}

// issueVendorReturnByKey returns the cached result for a prior
// matching IssueVendorReturn call. Separate from issueByIdempotencyKey
// because the signature differs (OutflowValueBase vs CostOfIssueBase).
func issueVendorReturnByKey(db *gorm.DB, companyID uint, key string) (*IssueVendorReturnResult, error) {
	var mov models.InventoryMovement
	err := db.Where("company_id = ? AND idempotency_key = ?", companyID, key).First(&mov).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inventory.IssueVendorReturn: idempotency lookup: %w", err)
	}
	// A cached key that points at a non-outflow row signals a
	// caller-side key collision across event types — reject loudly.
	if !mov.QuantityDelta.IsNegative() {
		return nil, ErrDuplicateIdempotency
	}
	unitCostBase := decimal.Zero
	if mov.UnitCostBase != nil {
		unitCostBase = *mov.UnitCostBase
	}
	absQty := mov.QuantityDelta.Neg()
	return &IssueVendorReturnResult{
		MovementID:       mov.ID,
		UnitCostBase:     unitCostBase,
		OutflowValueBase: absQty.Mul(unitCostBase).RoundBank(2),
	}, nil
}

func buildIssueVendorReturnReferenceNote(in IssueVendorReturnInput, orig models.InventoryMovement) string {
	if in.Memo != "" {
		return fmt.Sprintf("[return-to-vendor] %s", in.Memo)
	}
	return fmt.Sprintf("[return-to-vendor] traced cost from movement #%d", orig.ID)
}
