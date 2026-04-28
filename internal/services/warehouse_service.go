// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/gorm"
)

// ── Errors ───────────────────────────────────────────────────────────────────

var (
	ErrWarehouseNotFound    = errors.New("warehouse not found")
	ErrWarehouseCodeExists  = errors.New("warehouse code already in use for this company")
	ErrCannotDeactivateMain = errors.New("cannot deactivate the default warehouse")
)

// ── Input types ───────────────────────────────────────────────────────────────

type WarehouseInput struct {
	Code         string
	Name         string
	Description  string
	IsDefault    bool
	IsActive     bool
	AddressLine1 string
	City         string
	Country      string

	// Over-shipment override (Phase S3 — 2026-04-25). When Enabled is
	// true, this warehouse uses its own (Mode, Value) instead of the
	// company default. Operators leave Enabled=false on warehouses that
	// should follow company policy.
	OverShipmentEnabled bool
	OverShipmentMode    models.OverShipmentMode
	OverShipmentValue   decimal.Decimal
}

// ── CRUD ──────────────────────────────────────────────────────────────────────

// ListWarehouses returns all warehouses for a company, ordered by name.
func ListWarehouses(db *gorm.DB, companyID uint) ([]models.Warehouse, error) {
	var ws []models.Warehouse
	err := db.Where("company_id = ?", companyID).Order("name asc").Find(&ws).Error
	return ws, err
}

// GetWarehouse returns a single warehouse by ID.
func GetWarehouse(db *gorm.DB, companyID, id uint) (*models.Warehouse, error) {
	var w models.Warehouse
	if err := db.Where("id = ? AND company_id = ?", id, companyID).First(&w).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrWarehouseNotFound
		}
		return nil, err
	}
	return &w, nil
}

// CreateWarehouse creates a new warehouse. If IsDefault is true, all other
// warehouses for the company are unset from default first.
func CreateWarehouse(db *gorm.DB, companyID uint, in WarehouseInput) (*models.Warehouse, error) {
	if err := validateWarehouseCode(db, companyID, 0, in.Code); err != nil {
		return nil, err
	}

	var w models.Warehouse
	err := db.Transaction(func(tx *gorm.DB) error {
		if in.IsDefault {
			if err := clearDefaultFlag(tx, companyID, 0); err != nil {
				return err
			}
		}
		w = models.Warehouse{
			CompanyID:           companyID,
			Code:                in.Code,
			Name:                in.Name,
			Description:         in.Description,
			IsDefault:           in.IsDefault,
			IsActive:            in.IsActive,
			AddressLine1:        in.AddressLine1,
			City:                in.City,
			Country:             in.Country,
			OverShipmentEnabled: in.OverShipmentEnabled,
			OverShipmentMode:    NormalizeOverShipmentMode(in.OverShipmentMode),
			OverShipmentValue:   in.OverShipmentValue,
		}
		return tx.Create(&w).Error
	})
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// UpdateWarehouse updates an existing warehouse.
func UpdateWarehouse(db *gorm.DB, companyID, id uint, in WarehouseInput) (*models.Warehouse, error) {
	w, err := GetWarehouse(db, companyID, id)
	if err != nil {
		return nil, err
	}

	if err := validateWarehouseCode(db, companyID, id, in.Code); err != nil {
		return nil, err
	}

	// Cannot deactivate the default warehouse.
	if w.IsDefault && !in.IsActive {
		return nil, ErrCannotDeactivateMain
	}

	err = db.Transaction(func(tx *gorm.DB) error {
		if in.IsDefault && !w.IsDefault {
			if err := clearDefaultFlag(tx, companyID, id); err != nil {
				return err
			}
		}
		w.Code = in.Code
		w.Name = in.Name
		w.Description = in.Description
		w.IsDefault = in.IsDefault
		w.IsActive = in.IsActive
		w.AddressLine1 = in.AddressLine1
		w.City = in.City
		w.Country = in.Country
		w.OverShipmentEnabled = in.OverShipmentEnabled
		w.OverShipmentMode = NormalizeOverShipmentMode(in.OverShipmentMode)
		w.OverShipmentValue = in.OverShipmentValue
		return tx.Save(w).Error
	})
	if err != nil {
		return nil, err
	}
	return w, nil
}

// ── Default warehouse resolution ─────────────────────────────────────────────

// DefaultWarehouseID returns the ID of the company's default warehouse.
// Returns (0, ErrWarehouseNotFound) if no default exists.
func DefaultWarehouseID(db *gorm.DB, companyID uint) (uint, error) {
	var w models.Warehouse
	err := db.Select("id").
		Where("company_id = ? AND is_default = true", companyID).
		First(&w).Error
	if err == gorm.ErrRecordNotFound {
		return 0, ErrWarehouseNotFound
	}
	if err != nil {
		return 0, err
	}
	return w.ID, nil
}

// EnsureDefaultWarehouse guarantees that a default warehouse named "MAIN" exists
// for the company. Idempotent — no-op if one already exists.
// Returns the default warehouse ID.
func EnsureDefaultWarehouse(db *gorm.DB, companyID uint) (uint, error) {
	id, err := DefaultWarehouseID(db, companyID)
	if err == nil {
		return id, nil // already exists
	}
	if !errors.Is(err, ErrWarehouseNotFound) {
		return 0, err
	}

	// Create default.
	w, err := CreateWarehouse(db, companyID, WarehouseInput{
		Code:      "MAIN",
		Name:      "Main Warehouse",
		IsDefault: true,
		IsActive:  true,
	})
	if err != nil {
		// Race condition: another request may have created it first.
		if errors.Is(err, ErrWarehouseCodeExists) {
			return DefaultWarehouseID(db, companyID)
		}
		return 0, fmt.Errorf("create default warehouse: %w", err)
	}
	return w.ID, nil
}

// ResolveInventoryWarehouse returns the warehouse ID to use for inventory
// operations on a document. Resolution order:
//
//  1. docWarehouseID — if not nil, use it directly.
//  2. Company default warehouse — if one exists, use it.
//  3. nil — fall back to the legacy LocationType/LocationRef path.
func ResolveInventoryWarehouse(db *gorm.DB, companyID uint, docWarehouseID *uint) *uint {
	if docWarehouseID != nil {
		return docWarehouseID
	}
	id, err := DefaultWarehouseID(db, companyID)
	if err != nil || id == 0 {
		return nil
	}
	return &id
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// validateWarehouseCode ensures the code is unique within the company.
// excludeID = 0 means no exclusion (create path); non-zero means update path.
func validateWarehouseCode(db *gorm.DB, companyID, excludeID uint, code string) error {
	q := db.Model(&models.Warehouse{}).
		Where("company_id = ? AND code = ?", companyID, code)
	if excludeID > 0 {
		q = q.Where("id <> ?", excludeID)
	}
	var count int64
	q.Count(&count)
	if count > 0 {
		return ErrWarehouseCodeExists
	}
	return nil
}

// clearDefaultFlag unsets is_default on all warehouses except the given ID.
func clearDefaultFlag(tx *gorm.DB, companyID, excludeID uint) error {
	q := tx.Model(&models.Warehouse{}).
		Where("company_id = ? AND is_default = true", companyID)
	if excludeID > 0 {
		q = q.Where("id <> ?", excludeID)
	}
	return q.Update("is_default", false).Error
}
