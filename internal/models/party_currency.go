// 遵循project_guide.md
package models

import "time"

// CustomerAllowedCurrency records each transaction currency that a Customer is
// explicitly permitted to use on documents. Only enforced when
// Customer.CurrencyPolicy = "multi_allowed".
//
// If CurrencyPolicy = "multi_allowed" and this table has no rows for the
// customer, any currency enabled for the company is accepted (open policy).
// When rows are present, only the listed currencies are accepted.
type CustomerAllowedCurrency struct {
	ID           uint   `gorm:"primaryKey"`
	CompanyID    uint   `gorm:"not null;index"`
	CustomerID   uint   `gorm:"not null;index"`
	CurrencyCode string `gorm:"type:varchar(3);not null"`
	CreatedAt    time.Time
}

// VendorAllowedCurrency records each transaction currency that a Vendor is
// explicitly permitted to use on documents. Only enforced when
// Vendor.CurrencyPolicy = "multi_allowed".
type VendorAllowedCurrency struct {
	ID           uint   `gorm:"primaryKey"`
	CompanyID    uint   `gorm:"not null;index"`
	VendorID     uint   `gorm:"not null;index"`
	CurrencyCode string `gorm:"type:varchar(3);not null"`
	CreatedAt    time.Time
}
