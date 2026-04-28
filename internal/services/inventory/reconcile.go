// 遵循project_guide.md
package inventory

// reconcile.go — FIFO layer drift detection & bounded repair (Phase E2.3).
//
// Invariant this module polices
// -----------------------------
//   SUM(inventory_cost_layers.remaining_quantity) per (item, warehouse)
//   == inventory_balances.quantity_on_hand
//
// E2.1 makes this hold for every post/void cycle on fresh data. But two
// historical populations can still drift:
//
// 1. Pre-E2.1 FIFO issues that were later reversed.
//    The original issue drained layers + on-hand (E2 did both). The
//    reversal, lacking a consumption log, could only restore on-hand.
//    Result: on-hand > SUM(remaining) — "positive drift".
//
// 2. Pre-migration-059 inventory.
//    The layer table didn't exist. When a company flips to FIFO, its
//    existing on-hand has no layer cover at all. Result: on-hand > 0 and
//    SUM(remaining) == 0 (often zero rows exist).
//
// Scope of this slice
// -------------------
// Inspect: full. Returns every (company, item, warehouse) cell whose
// remainings don't agree with on-hand, classifying each by drift sign
// and whether any layer rows exist.
//
// Repair: bounded — handles ONLY the genesis case (#2 above). For every
// other drift class, the report's Notes field spells out exactly why
// the slice won't auto-repair, so operators can choose to investigate
// or schedule a bespoke fix. Rationale: restoring layer remainings for
// case #1 requires knowing which layer the reversed issue originally
// drew from, which is precisely the history that got lost. A future
// slice can introduce a policy (restore to youngest layer, or synthesize
// a new layer at period avg cost) — that's a deliberate design choice,
// not a reconcile-job call.

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"balanciz/internal/models"
)

// ReconcileReport describes one (company, item, warehouse) cell's FIFO
// layer health. Drift > 0 → on-hand exceeds layer coverage. Drift < 0 →
// layers exceed on-hand (not expected; surfaced for investigation).
type ReconcileReport struct {
	CompanyID         uint
	ItemID            uint
	WarehouseID       *uint
	QuantityOnHand    decimal.Decimal
	SumLayerRemaining decimal.Decimal
	Drift             decimal.Decimal // on-hand − SUM(remaining)
	LayerRowCount     int             // number of inventory_cost_layers rows for the cell
	Repaired          bool            // true iff Repair applied a fix
	Notes             string          // explanation when not repaired or on error
}

// DriftClassification names the drift class so callers can branch on
// "genesis migration" vs "needs investigation" without parsing the Notes.
type DriftClassification string

const (
	// DriftGenesisNoLayers — on-hand > 0 and zero layer rows exist.
	// Repairable by synthesizing a single genesis layer at the balance's
	// current average cost. Happens when a company flips to FIFO with
	// existing moving-average stock.
	DriftGenesisNoLayers DriftClassification = "genesis_no_layers"

	// DriftPositiveWithLayers — on-hand > SUM(remaining) but layer rows
	// exist. Usually caused by post-E2.1 reversal of a pre-E2.1 issue.
	// Not auto-repaired — we can't determine which layer to restore to
	// without the original consumption history.
	DriftPositiveWithLayers DriftClassification = "positive_needs_investigation"

	// DriftNegative — SUM(remaining) > on-hand. Not auto-repaired;
	// typically indicates a double-reversal or hand-edit.
	DriftNegative DriftClassification = "negative_needs_investigation"
)

// InspectFIFOLayerDrift scans every InventoryBalance row for the company
// and reports cells whose layer remainings disagree with on-hand.
// Read-only; no state changes. Guarded to FIFO companies — see file
// header for why.
func InspectFIFOLayerDrift(db *gorm.DB, companyID uint) ([]ReconcileReport, error) {
	return scanFIFODrift(db, companyID, false)
}

// RepairFIFOLayerDrift runs Inspect first, then attempts to fix the
// genesis-no-layers class by synthesizing a layer. All other drift
// classes are reported but not altered.
func RepairFIFOLayerDrift(db *gorm.DB, companyID uint) ([]ReconcileReport, error) {
	return scanFIFODrift(db, companyID, true)
}

func scanFIFODrift(db *gorm.DB, companyID uint, doRepair bool) ([]ReconcileReport, error) {
	if companyID == 0 {
		return nil, fmt.Errorf("inventory.reconcile: CompanyID required")
	}
	method := companyCostingMethod(db, companyID)
	if method != models.InventoryCostingFIFO {
		return nil, fmt.Errorf("inventory.reconcile: only applies to FIFO companies (company %d is on %q)",
			companyID, method)
	}

	var balances []models.InventoryBalance
	if err := db.Where("company_id = ?", companyID).Find(&balances).Error; err != nil {
		return nil, fmt.Errorf("inventory.reconcile: load balances: %w", err)
	}

	reports := make([]ReconcileReport, 0)
	for _, bal := range balances {
		sum, layerCount, err := sumAndCountLayers(db, companyID, bal.ItemID, bal.WarehouseID)
		if err != nil {
			return nil, err
		}
		drift := bal.QuantityOnHand.Sub(sum)
		if drift.IsZero() {
			continue
		}

		r := ReconcileReport{
			CompanyID:         companyID,
			ItemID:            bal.ItemID,
			WarehouseID:       bal.WarehouseID,
			QuantityOnHand:    bal.QuantityOnHand,
			SumLayerRemaining: sum,
			Drift:             drift,
			LayerRowCount:     layerCount,
		}
		r.Notes = classifyNotes(r)

		if doRepair {
			if classify(r) == DriftGenesisNoLayers {
				if err := repairGenesisNoLayers(db, companyID, bal); err != nil {
					r.Notes = "genesis repair failed: " + err.Error()
				} else {
					r.Repaired = true
					r.Notes = "synthesized genesis layer at current average cost"
				}
			}
		}

		reports = append(reports, r)
	}
	return reports, nil
}

// classify picks the DriftClassification from the report's numbers.
func classify(r ReconcileReport) DriftClassification {
	switch {
	case r.Drift.IsPositive() && r.LayerRowCount == 0:
		return DriftGenesisNoLayers
	case r.Drift.IsPositive():
		return DriftPositiveWithLayers
	default:
		return DriftNegative
	}
}

// classifyNotes returns a one-line explanation suitable for Notes, based
// on the drift class.
func classifyNotes(r ReconcileReport) string {
	switch classify(r) {
	case DriftGenesisNoLayers:
		return "genesis state: on-hand exists but no layer rows — eligible for auto-repair"
	case DriftPositiveWithLayers:
		return "positive drift with existing layers — likely reversed pre-E2.1 issue; cannot auto-restore without consumption history"
	default:
		return "negative drift: layers exceed on-hand — investigate (possible double-reversal or hand-edit)"
	}
}

// repairGenesisNoLayers synthesizes a single cost layer that covers the
// full on-hand quantity at the balance's current average cost. The
// synthesized layer uses a dedicated source_movement_id sentinel (the
// oldest non-reversed inbound movement for the cell, if any; otherwise
// any inbound movement for the cell) to preserve the NOT NULL constraint
// without inventing a fake movement. Runs inside a per-cell transaction.
func repairGenesisNoLayers(db *gorm.DB, companyID uint, bal models.InventoryBalance) error {
	return db.Transaction(func(tx *gorm.DB) error {
		// Lock the balance cell so nothing moves while we synthesize.
		var locked models.InventoryBalance
		bq := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", bal.ID)
		if err := bq.First(&locked).Error; err != nil {
			return fmt.Errorf("lock balance: %w", err)
		}
		// Re-confirm under lock: no layer rows, positive on-hand.
		var layerCount int64
		lq := tx.Model(&models.InventoryCostLayer{}).
			Where("company_id = ? AND item_id = ?", companyID, locked.ItemID)
		if locked.WarehouseID != nil {
			lq = lq.Where("warehouse_id = ?", *locked.WarehouseID)
		} else {
			lq = lq.Where("warehouse_id IS NULL")
		}
		if err := lq.Count(&layerCount).Error; err != nil {
			return fmt.Errorf("recount layers under lock: %w", err)
		}
		if layerCount != 0 {
			return errors.New("layer rows appeared under lock; aborting genesis repair")
		}
		if !locked.QuantityOnHand.IsPositive() {
			return errors.New("on-hand no longer positive under lock")
		}

		// Find an inbound movement to point at for the required
		// source_movement_id FK. This is purely for referential
		// hygiene — the synthesized layer carries the current avg
		// cost, not the pointed-at movement's cost.
		sourceMovID, err := pickGenesisSourceMovement(tx, companyID, locked.ItemID, locked.WarehouseID)
		if err != nil {
			return err
		}

		// Use the earliest plausible date for the layer so FIFO ordering
		// puts it at the bottom of the stack (it represents "everything
		// that was here before the layer system existed").
		//
		// Provenance (H2): this row is NOT a real receipt. SourceMovementID
		// is a pure FK anchor; the authoritative attribution marker is
		// IsSynthetic / ProvenanceType. Downstream readers must branch on
		// those, not on SourceMovementID, to avoid misattributing
		// synthesized opening stock to the anchor movement.
		genesisDate := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
		layer := models.InventoryCostLayer{
			CompanyID:         companyID,
			ItemID:            locked.ItemID,
			WarehouseID:       locked.WarehouseID,
			SourceMovementID:  sourceMovID,
			IsSynthetic:       true,
			ProvenanceType:    models.ProvenanceSyntheticGenesis,
			OriginalQuantity:  locked.QuantityOnHand,
			RemainingQuantity: locked.QuantityOnHand,
			UnitCostBase:      locked.AverageCost,
			ReceivedDate:      genesisDate,
		}
		if err := tx.Create(&layer).Error; err != nil {
			return fmt.Errorf("create genesis layer: %w", err)
		}
		return nil
	})
}

// pickGenesisSourceMovement finds the oldest inbound InventoryMovement
// for the cell so we have a valid FK target for the synthesized layer.
// Returns an error if no such movement exists — that means the balance
// materialized without any backing movement history, a data anomaly
// the reconcile job shouldn't paper over.
func pickGenesisSourceMovement(db *gorm.DB, companyID, itemID uint, warehouseID *uint) (uint, error) {
	var m models.InventoryMovement
	q := db.Where("company_id = ? AND item_id = ? AND quantity_delta > 0", companyID, itemID)
	if warehouseID != nil {
		q = q.Where("warehouse_id = ?", *warehouseID)
	} else {
		q = q.Where("warehouse_id IS NULL")
	}
	err := q.Order("movement_date asc, id asc").First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, fmt.Errorf("no inbound movement found for cell — cannot synthesize genesis layer without an FK target")
	}
	if err != nil {
		return 0, fmt.Errorf("pick genesis source movement: %w", err)
	}
	return m.ID, nil
}

// sumAndCountLayers returns SUM(remaining_quantity) and the row count
// of cost layers for (company, item, warehouse).
func sumAndCountLayers(db *gorm.DB, companyID, itemID uint, warehouseID *uint) (decimal.Decimal, int, error) {
	q := db.Model(&models.InventoryCostLayer{}).
		Where("company_id = ? AND item_id = ?", companyID, itemID)
	if warehouseID != nil {
		q = q.Where("warehouse_id = ?", *warehouseID)
	} else {
		q = q.Where("warehouse_id IS NULL")
	}
	var row struct {
		Total decimal.Decimal
		Cnt   int64
	}
	if err := q.Select("COALESCE(SUM(remaining_quantity), 0) AS total, COUNT(*) AS cnt").Scan(&row).Error; err != nil {
		return decimal.Zero, 0, fmt.Errorf("inventory.reconcile: sum+count layers: %w", err)
	}
	return row.Total, int(row.Cnt), nil
}
