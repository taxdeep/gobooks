// 遵循project_guide.md
package services

// product_service_tracking.go — Phase F1 service surface for
// ProductService.TrackingMode.
//
// The mutation path for tracking_mode is NOT a plain field update — a
// wrong-direction flip (e.g. switching an item from 'none' to 'lot'
// while it has 10 units on hand) would orphan those units outside the
// tracking truth. This file centralises the guards so any handler or
// admin tool that changes tracking_mode goes through the same policy.
//
// Hard rules
// ----------
//  1. Non-stock items may only be on 'none'. Attempting to set 'lot' or
//     'serial' on a service / non-inventory / other-charge item returns
//     an error.
//  2. tracking_mode may only change when the item has ZERO on-hand
//     across every balance row AND zero layer remaining across every
//     cost layer. Phase F1 does NOT ship a historical conversion tool
//     — operators must drain stock first, or wait for a future slice.
//  3. Every successful change writes an audit log entry
//     (action = "product_service.tracking_mode.changed") recording the
//     before/after value. This is the minimum viable audit trail for
//     mutation traceability — avoids adding a dedicated
//     tracking_mode_changed_at column until operational need proves it.

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

var (
	// ErrTrackingModeInvalidForItem — the requested tracking_mode value
	// is not valid for the item's type/stock classification.
	ErrTrackingModeInvalidForItem = errors.New("product_service: tracking_mode invalid for this item")

	// ErrTrackingModeHasStock — attempted to change tracking_mode while
	// the item still has non-zero on-hand or layer-remaining. Block the
	// change; operators must drain stock first.
	ErrTrackingModeHasStock = errors.New("product_service: cannot change tracking_mode while item has on-hand or layer remaining")

	// ErrTrackingCapabilityNotEnabled — attempted to move an item out
	// of tracking_mode='none' while the company-level capability gate
	// (companies.tracking_enabled) is still FALSE. The caller must
	// first flip the gate via ChangeCompanyTrackingCapability, which is
	// an audited admin action documented in INVENTORY_MODULE_API.md §F.7.
	ErrTrackingCapabilityNotEnabled = errors.New("product_service: company-level tracking capability is not enabled; enable it before flipping any item's tracking_mode")

	// ErrTrackingCapabilityHasTrackedItems — attempted to DISABLE the
	// company-level tracking capability while at least one item still
	// has tracking_mode != 'none'. Refuses the flip rather than
	// silently zeroing out tracked items' modes — that would orphan
	// live tracking data (lots / serials / consumption anchors).
	ErrTrackingCapabilityHasTrackedItems = errors.New("product_service: cannot disable company tracking capability while any item still has tracking_mode != none")
)

// ChangeTrackingModeInput is the input to ChangeTrackingMode.
type ChangeTrackingModeInput struct {
	CompanyID   uint
	ItemID      uint
	NewMode     string // "none" | "lot" | "serial"
	Actor       string
	ActorUserID *uuid.UUID
}

// ChangeTrackingMode flips ProductService.TrackingMode after validating:
//   - the new mode is compatible with the item's stock-item status;
//   - no on-hand exists for the item across any balance row;
//   - no remaining layer quantity exists across any cost layer.
//
// On success it persists the change AND writes an audit log entry
// recording the before/after mode. Runs inside a single transaction so
// a concurrent post cannot slip stock in between the stock check and
// the mode update.
func ChangeTrackingMode(db *gorm.DB, in ChangeTrackingModeInput) error {
	if in.CompanyID == 0 || in.ItemID == 0 {
		return fmt.Errorf("product_service.ChangeTrackingMode: CompanyID and ItemID required")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var item models.ProductService
		if err := tx.Where("id = ? AND company_id = ?", in.ItemID, in.CompanyID).
			First(&item).Error; err != nil {
			return fmt.Errorf("load item: %w", err)
		}
		oldMode := item.TrackingMode
		if oldMode == in.NewMode {
			return nil // no-op
		}

		// Phase G gate: moving an item OUT OF tracking_mode='none'
		// requires the company-level tracking capability to be ON.
		// Flipping BACK to 'none' is always allowed regardless of the
		// gate — it reduces tracking footprint and never introduces
		// tracking truth. Also layered above item-level validation so
		// callers get the capability error before they discover
		// item-level errors; clearer remediation path.
		if in.NewMode != models.TrackingNone {
			var company models.Company
			if err := tx.Select("id", "tracking_enabled").
				Where("id = ?", in.CompanyID).First(&company).Error; err != nil {
				return fmt.Errorf("load company: %w", err)
			}
			if !company.TrackingEnabled {
				return ErrTrackingCapabilityNotEnabled
			}
		}

		// Validate new mode against item classification.
		probe := item
		probe.TrackingMode = in.NewMode
		if err := probe.ValidateTrackingMode(); err != nil {
			return fmt.Errorf("%w: %s", ErrTrackingModeInvalidForItem, err.Error())
		}

		// Block when any on-hand or layer-remaining exists.
		var onHand struct {
			Total float64
		}
		if err := tx.Model(&models.InventoryBalance{}).
			Select("COALESCE(SUM(quantity_on_hand), 0) AS total").
			Where("company_id = ? AND item_id = ?", in.CompanyID, in.ItemID).
			Scan(&onHand).Error; err != nil {
			return fmt.Errorf("sum on-hand: %w", err)
		}
		if onHand.Total != 0 {
			return fmt.Errorf("%w: sum(on-hand)=%v", ErrTrackingModeHasStock, onHand.Total)
		}

		var layerRem struct {
			Total float64
		}
		if err := tx.Model(&models.InventoryCostLayer{}).
			Select("COALESCE(SUM(remaining_quantity), 0) AS total").
			Where("company_id = ? AND item_id = ?", in.CompanyID, in.ItemID).
			Scan(&layerRem).Error; err != nil {
			return fmt.Errorf("sum layer remaining: %w", err)
		}
		if layerRem.Total != 0 {
			return fmt.Errorf("%w: sum(layer remaining)=%v", ErrTrackingModeHasStock, layerRem.Total)
		}

		// Persist.
		if err := tx.Model(&models.ProductService{}).
			Where("id = ? AND company_id = ?", item.ID, in.CompanyID).
			Update("tracking_mode", in.NewMode).Error; err != nil {
			return fmt.Errorf("persist tracking_mode: %w", err)
		}

		// Audit.
		cid := in.CompanyID
		TryWriteAuditLogWithContextDetails(
			tx,
			"product_service.tracking_mode.changed",
			"product_service",
			item.ID,
			actorOrSystem(in.Actor),
			map[string]any{
				"item_name": item.Name,
				"sku":       item.SKU,
			},
			&cid,
			in.ActorUserID,
			map[string]any{"tracking_mode": oldMode},
			map[string]any{"tracking_mode": in.NewMode},
		)
		return nil
	})
}

func actorOrSystem(a string) string {
	if a == "" {
		return "system"
	}
	return a
}

// ── Company-level tracking capability gate (Phase G slice G.1) ───────────────
//
// The gate is a single boolean on companies.tracking_enabled. The semantic
// contract (see INVENTORY_MODULE_API.md §F.7):
//
//   - Default FALSE. Existing companies and new companies are both
//     off-by-default. Tracking is not something you stumble into; it's
//     something you deliberately turn on.
//   - Enabling is an audited admin action. The audit row captures who
//     flipped it and when, so compliance can reconstruct the path from
//     "company had no tracked items" → "company has tracked items."
//   - Disabling while any item still has tracking_mode != 'none' is
//     refused. The system does not silently zero-out tracked items —
//     that would orphan lots / serials / consumption anchors, the very
//     tracking truth the gate exists to protect.

// ChangeCompanyTrackingCapabilityInput is the input to the
// enable/disable flip. Enabled=true activates the gate; Enabled=false
// requires a fully-untracked item catalog.
type ChangeCompanyTrackingCapabilityInput struct {
	CompanyID   uint
	Enabled     bool
	Actor       string
	ActorUserID *uuid.UUID
}

// ChangeCompanyTrackingCapability flips companies.tracking_enabled.
// Enabling is unconditional (beyond the company existing). Disabling
// rejects if any product_service in the company still has
// tracking_mode != 'none'. Audit row written on every effective flip;
// no-op flips (already in target state) produce neither side-effect
// nor audit row.
func ChangeCompanyTrackingCapability(db *gorm.DB, in ChangeCompanyTrackingCapabilityInput) error {
	if in.CompanyID == 0 {
		return fmt.Errorf("product_service.ChangeCompanyTrackingCapability: CompanyID required")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var company models.Company
		if err := tx.Where("id = ?", in.CompanyID).First(&company).Error; err != nil {
			return fmt.Errorf("load company: %w", err)
		}
		if company.TrackingEnabled == in.Enabled {
			return nil // no-op
		}

		// Guard: disabling requires a fully-untracked item catalog.
		// Does not consider on-hand — that's a per-item concern, and
		// a company with zero tracked items (regardless of their
		// stock levels) is safe to flip off.
		if !in.Enabled {
			var trackedCount int64
			if err := tx.Model(&models.ProductService{}).
				Where("company_id = ? AND tracking_mode <> ?",
					in.CompanyID, models.TrackingNone).
				Count(&trackedCount).Error; err != nil {
				return fmt.Errorf("count tracked items: %w", err)
			}
			if trackedCount > 0 {
				return fmt.Errorf("%w: %d item(s) still tracked", ErrTrackingCapabilityHasTrackedItems, trackedCount)
			}
		}

		if err := tx.Model(&models.Company{}).
			Where("id = ?", in.CompanyID).
			Update("tracking_enabled", in.Enabled).Error; err != nil {
			return fmt.Errorf("persist tracking_enabled: %w", err)
		}

		cid := in.CompanyID
		action := "company.tracking_capability.enabled"
		if !in.Enabled {
			action = "company.tracking_capability.disabled"
		}
		TryWriteAuditLogWithContextDetails(
			tx,
			action,
			"company",
			company.ID,
			actorOrSystem(in.Actor),
			map[string]any{"company_name": company.Name},
			&cid,
			in.ActorUserID,
			map[string]any{"tracking_enabled": company.TrackingEnabled},
			map[string]any{"tracking_enabled": in.Enabled},
		)
		return nil
	})
}
