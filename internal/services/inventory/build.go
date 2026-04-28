// 遵循project_guide.md
package inventory

// build.go — PostInventoryBuild (assembly / manufacturing build) — Phase D.2.
//
// A build is a value-transforming event: N component items are consumed at
// their current weighted-average cost, optional labor and overhead are
// added in base currency, and 1 finished-good item is produced at the
// blended unit cost. The semantics differ from TransferStock in that the
// outflow leg unit costs vary per component and the inflow leg cost is the
// computed blend (not a snapshot).
//
// Design choice — no header table: the IN/OUT contract treats a build as a
// pair of movements (build_consume × N + build_produce × 1) sharing the
// same SourceID. This keeps the bounded context narrow; if a richer Build
// document is needed for reporting, it lives at the business-document
// layer like WarehouseTransfer does (it would call PostInventoryBuild
// internally).
//
// Flow:
//  1. Validate input (parent must be inventory-tracked, qty > 0, etc.)
//  2. Idempotency short-circuit on the produce leg
//  3. Resolve component list (caller override OR parent's BOM rows)
//  4. Issue each component (negative deltas; cost = current avg)
//  5. Sum component costs; add labor + overhead; compute finished unit cost
//  6. Receive the parent at the blended unit cost (positive delta)
//  7. Return both legs so caller (GL) can post the JE
//
// Idempotency keys derived per leg from the caller's build-level key:
//   "<key>:produce" for the finished-good receipt
//   "<key>:consume:<componentItemID>" for each component issue

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// PostInventoryBuild executes a single build event. See INVENTORY_MODULE_API.md
// §3 (BOM-driven IN verbs) for the contract.
//
// Atomicity: this function does NOT open its own transaction. Each component
// consumption is a separate IssueStock call; if the 2nd of N component legs
// fails (insufficient stock, DB error), the 1st is already persisted.
// Callers MUST wrap PostInventoryBuild in db.Transaction(func(tx *gorm.DB)…)
// so a mid-build failure rolls the entire build back. The behavior mirrors
// TransferStock.
func PostInventoryBuild(db *gorm.DB, in PostInventoryBuildInput) (*PostInventoryBuildResult, error) {
	if err := validateBuildInput(in); err != nil {
		return nil, err
	}

	// Idempotency short-circuit: if the produce leg already landed for this
	// build key, reconstruct the result from persisted movements rather than
	// re-issuing components. The consume legs are guaranteed to share the
	// same SourceID by construction below.
	if in.IdempotencyKey != "" {
		cached, err := readBuildByIdempotency(db, in.CompanyID, produceKey(in.IdempotencyKey))
		if err != nil {
			return nil, err
		}
		if cached != nil {
			return cached, nil
		}
	}

	if err := verifyInventoryItem(db, in.CompanyID, in.ParentItemID); err != nil {
		return nil, err
	}
	if in.WarehouseID != 0 {
		if err := verifyWarehouseBelongsToCompany(db, in.CompanyID, in.WarehouseID); err != nil {
			return nil, err
		}
	}

	components, err := resolveBuildComponents(db, in)
	if err != nil {
		return nil, err
	}
	if len(components) == 0 {
		return nil, fmt.Errorf("inventory.PostInventoryBuild: parent %d has no components and no overrides supplied", in.ParentItemID)
	}

	// ── Consume legs ────────────────────────────────────────────────────────
	consumed := make([]BuildComponentConsumed, 0, len(components))
	componentCostBase := decimal.Zero

	memo := in.Memo
	if memo == "" {
		memo = fmt.Sprintf("Assembly build #%d: produce %s × item %d",
			in.BuildRef, in.Quantity.String(), in.ParentItemID)
	}

	for _, comp := range components {
		totalQty := comp.PerUnitQuantity.Mul(in.Quantity)
		issueKey := ""
		if in.IdempotencyKey != "" {
			issueKey = consumeKey(in.IdempotencyKey, comp.ItemID)
		}
		issue, err := IssueStock(db, IssueStockInput{
			CompanyID:      in.CompanyID,
			ItemID:         comp.ItemID,
			WarehouseID:    in.WarehouseID,
			Quantity:       totalQty,
			MovementDate:   in.BuildDate,
			SourceType:     "build_consume",
			SourceID:       in.BuildRef,
			IdempotencyKey: issueKey,
			ActorUserID:    in.ActorUserID,
			Memo:           memo,
		})
		if err != nil {
			return nil, fmt.Errorf("inventory.PostInventoryBuild: consume component %d: %w", comp.ItemID, err)
		}
		consumed = append(consumed, BuildComponentConsumed{
			ItemID:          comp.ItemID,
			IssueMovementID: issue.MovementID,
			Quantity:        totalQty,
			UnitCostBase:    issue.UnitCostBase,
			TotalCostBase:   issue.CostOfIssueBase,
		})
		componentCostBase = componentCostBase.Add(issue.CostOfIssueBase)
	}

	// ── Produce leg ─────────────────────────────────────────────────────────
	totalCostBase := componentCostBase.Add(in.LaborCostBase).Add(in.OverheadCostBase)
	unitCostBase := decimal.Zero
	if in.Quantity.IsPositive() {
		unitCostBase = totalCostBase.Div(in.Quantity).Round(4)
	}

	receiveKey := ""
	if in.IdempotencyKey != "" {
		receiveKey = produceKey(in.IdempotencyKey)
	}
	receive, err := ReceiveStock(db, ReceiveStockInput{
		CompanyID:      in.CompanyID,
		ItemID:         in.ParentItemID,
		WarehouseID:    in.WarehouseID,
		Quantity:       in.Quantity,
		MovementDate:   in.BuildDate,
		UnitCost:       unitCostBase,
		ExchangeRate:   decimal.NewFromInt(1),
		SourceType:     "build_produce",
		SourceID:       in.BuildRef,
		IdempotencyKey: receiveKey,
		ActorUserID:    in.ActorUserID,
		Memo:           memo,
	})
	if err != nil {
		return nil, fmt.Errorf("inventory.PostInventoryBuild: produce: %w", err)
	}

	return &PostInventoryBuildResult{
		ProduceMovementID: receive.MovementID,
		UnitCostBase:      unitCostBase,
		FinishedValueBase: in.Quantity.Mul(unitCostBase).RoundBank(2),
		ComponentCostBase: componentCostBase,
		LaborCostBase:     in.LaborCostBase,
		OverheadCostBase:  in.OverheadCostBase,
		Components:        consumed,
	}, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

func validateBuildInput(in PostInventoryBuildInput) error {
	if in.CompanyID == 0 {
		return fmt.Errorf("inventory.PostInventoryBuild: CompanyID required")
	}
	if in.ParentItemID == 0 {
		return fmt.Errorf("inventory.PostInventoryBuild: ParentItemID required")
	}
	if !in.Quantity.IsPositive() {
		return ErrNegativeQuantity
	}
	if in.BuildDate.IsZero() {
		return fmt.Errorf("inventory.PostInventoryBuild: BuildDate required")
	}
	if in.LaborCostBase.IsNegative() || in.OverheadCostBase.IsNegative() {
		return fmt.Errorf("inventory.PostInventoryBuild: labor / overhead cannot be negative")
	}
	for _, c := range in.ComponentOverrides {
		if c.ItemID == 0 {
			return fmt.Errorf("inventory.PostInventoryBuild: override item id required")
		}
		if !c.PerUnitQuantity.IsPositive() {
			return fmt.Errorf("inventory.PostInventoryBuild: override per-unit qty for item %d must be positive", c.ItemID)
		}
	}
	return nil
}

// resolveBuildComponents picks the BOM source: explicit overrides if set,
// otherwise the parent's item_components rows. Override mode does NOT
// inherit from the BOM — callers spell out the full list when they
// override (matches the "rework / substitute" use case).
func resolveBuildComponents(db *gorm.DB, in PostInventoryBuildInput) ([]BuildComponentInput, error) {
	if len(in.ComponentOverrides) > 0 {
		return in.ComponentOverrides, nil
	}
	var rows []models.ItemComponent
	err := db.Where("company_id = ? AND parent_item_id = ?", in.CompanyID, in.ParentItemID).
		Order("sort_order asc, id asc").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("inventory.PostInventoryBuild: load components: %w", err)
	}
	out := make([]BuildComponentInput, 0, len(rows))
	for _, r := range rows {
		out = append(out, BuildComponentInput{
			ItemID:          r.ComponentItemID,
			PerUnitQuantity: r.Quantity,
		})
	}
	return out, nil
}

// readBuildByIdempotency reconstructs a prior build's result from the
// produce-leg movement plus all consume-leg movements that share the same
// SourceID. Returns (nil, nil) if no produce-leg exists for this key.
func readBuildByIdempotency(db *gorm.DB, companyID uint, produceLegKey string) (*PostInventoryBuildResult, error) {
	var produce models.InventoryMovement
	err := db.Where("company_id = ? AND idempotency_key = ?", companyID, produceLegKey).First(&produce).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inventory.PostInventoryBuild: idempotency lookup: %w", err)
	}
	if produce.SourceType != "build_produce" || produce.SourceID == nil {
		return nil, ErrDuplicateIdempotency
	}

	// Reconstruct figures from the persisted produce row.
	unitCostBase := decimal.Zero
	if produce.UnitCostBase != nil {
		unitCostBase = *produce.UnitCostBase
	}
	finishedValue := produce.QuantityDelta.Mul(unitCostBase).RoundBank(2)

	// Pull all consume legs for the same build.
	var consumes []models.InventoryMovement
	if err := db.Where("company_id = ? AND source_type = ? AND source_id = ?",
		companyID, "build_consume", *produce.SourceID).
		Find(&consumes).Error; err != nil {
		return nil, fmt.Errorf("inventory.PostInventoryBuild: load consume legs: %w", err)
	}

	consumed := make([]BuildComponentConsumed, 0, len(consumes))
	componentCost := decimal.Zero
	for _, m := range consumes {
		uc := decimal.Zero
		if m.UnitCostBase != nil {
			uc = *m.UnitCostBase
		}
		absQty := m.QuantityDelta.Neg()
		total := absQty.Mul(uc).RoundBank(2)
		consumed = append(consumed, BuildComponentConsumed{
			ItemID:          m.ItemID,
			IssueMovementID: m.ID,
			Quantity:        absQty,
			UnitCostBase:    uc,
			TotalCostBase:   total,
		})
		componentCost = componentCost.Add(total)
	}

	return &PostInventoryBuildResult{
		ProduceMovementID: produce.ID,
		UnitCostBase:      unitCostBase,
		FinishedValueBase: finishedValue,
		ComponentCostBase: componentCost,
		// Labor/overhead are not stored separately; they're folded into
		// UnitCostBase. Replays return the persisted unit cost as truth.
		Components: consumed,
	}, nil
}

func produceKey(base string) string  { return base + ":produce" }
func consumeKey(base string, itemID uint) string {
	return fmt.Sprintf("%s:consume:%d", base, itemID)
}
