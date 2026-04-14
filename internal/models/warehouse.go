// 遵循project_guide.md
package models

import "time"

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

	CreatedAt time.Time
	UpdatedAt time.Time
}
