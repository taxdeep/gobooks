// 遵循project_guide.md
package services

// over_shipment_service.go — Resolution + helpers for the SO-line over-
// shipment buffer policy added 2026-04-25 (S3 of the SO-line UX cleanup).
//
// Precedence
//   1. Warehouse override — when warehouseID is non-zero AND that warehouse
//      has OverShipmentEnabled=true, its (mode, value) wins.
//   2. Company default    — otherwise the company's settings apply.
//   3. None               — when neither layer is enabled, returns a
//      Disabled policy (Enabled=false). Callers compute MaxAllowedQty on
//      the policy directly; the helper short-circuits to the original qty.
//
// The resolver does one cheap query per layer needed (warehouse first,
// company second) and stops early. Callers are expected to call this once
// per validation pass, not per line.

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ResolveOverShipmentPolicy returns the effective over-shipment policy for
// a given (company, warehouse) pair. Pass warehouseID=0 to skip the
// override lookup (typical for SO-line writes that don't carry a per-line
// warehouse — the company default applies).
func ResolveOverShipmentPolicy(db *gorm.DB, companyID, warehouseID uint) (models.OverShipmentPolicy, error) {
	if companyID == 0 {
		return models.OverShipmentPolicy{}, errors.New("companyID is required")
	}

	if warehouseID > 0 {
		var w models.Warehouse
		err := db.Select("id", "company_id", "over_shipment_enabled", "over_shipment_mode", "over_shipment_value").
			Where("id = ? AND company_id = ?", warehouseID, companyID).
			First(&w).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return models.OverShipmentPolicy{}, fmt.Errorf("load warehouse for over-shipment policy: %w", err)
		}
		if err == nil && w.OverShipmentEnabled {
			return models.OverShipmentPolicy{
				Enabled: true,
				Mode:    w.OverShipmentMode,
				Value:   w.OverShipmentValue,
				Source:  "warehouse",
			}, nil
		}
	}

	var c models.Company
	if err := db.Select("id", "over_shipment_enabled", "over_shipment_mode", "over_shipment_value").
		Where("id = ?", companyID).
		First(&c).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.OverShipmentPolicy{}, nil
		}
		return models.OverShipmentPolicy{}, fmt.Errorf("load company for over-shipment policy: %w", err)
	}
	if !c.OverShipmentEnabled {
		return models.OverShipmentPolicy{}, nil
	}
	return models.OverShipmentPolicy{
		Enabled: true,
		Mode:    c.OverShipmentMode,
		Value:   c.OverShipmentValue,
		Source:  "company",
	}, nil
}

// NormalizeOverShipmentMode coerces an empty / unknown mode string to the
// safe default ("percent"). Used by save handlers so legacy form posts and
// future-added mode strings can't write a garbage column value.
func NormalizeOverShipmentMode(m models.OverShipmentMode) models.OverShipmentMode {
	switch m {
	case models.OverShipmentModePercent, models.OverShipmentModeQty:
		return m
	default:
		return models.OverShipmentModePercent
	}
}
