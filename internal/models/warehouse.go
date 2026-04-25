// 遵循project_guide.md
package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// Warehouse represents a physical or logical stock location owned by a company.
// Each company starts with one default warehouse (Code="MAIN").
// Future phases may add bin/shelf tracking within a warehouse.
type Warehouse struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	Code        string `gorm:"type:text;not null"`
	Name        string `gorm:"type:text;not null"`
	Description string `gorm:"type:text;not null;default:''"`

	IsDefault bool `gorm:"not null;default:false"`
	IsActive  bool `gorm:"not null;default:true"`

	// Optional address fields
	AddressLine1 string `gorm:"type:text;not null;default:''"`
	City         string `gorm:"type:text;not null;default:''"`
	Country      string `gorm:"type:text;not null;default:''"`

	// Over-shipment buffer override (Phase 2026-04-25). When
	// OverShipmentEnabled is set on a warehouse it wins over the company
	// default; when false, the company-level policy applies. Mode + Value
	// are only consulted when Enabled=true. See models.OverShipmentPolicy
	// + services.ResolveOverShipmentPolicy for the precedence rules.
	OverShipmentEnabled bool             `gorm:"not null;default:false"`
	OverShipmentMode    OverShipmentMode `gorm:"type:text;not null;default:'percent'"`
	OverShipmentValue   decimal.Decimal  `gorm:"type:numeric(10,4);not null;default:0"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
