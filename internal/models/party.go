// 遵循产品需求 v1.0
package models

import "time"

// PartyType is used in Journal Lines to reference either a customer or a vendor.
// Keep this minimal for now (MVP).
type PartyType string

const (
	PartyTypeNone     PartyType = ""
	PartyTypeCustomer PartyType = "customer"
	PartyTypeVendor   PartyType = "vendor"
)

func (t PartyType) Valid() bool {
	switch t {
	case PartyTypeNone, PartyTypeCustomer, PartyTypeVendor:
		return true
	default:
		return false
	}
}

// Customer is a company-scoped record for sales-side party selection (invoices, journal, etc.).
type Customer struct {
	ID          uint   `gorm:"primaryKey"`
	CompanyID   uint   `gorm:"not null;index"`
	Name        string `gorm:"not null"`
	Address     string `gorm:"type:text"` // optional mailing / billing address
	PaymentTerm string `gorm:"type:text"` // optional, e.g. Net 30, Due on receipt
	CreatedAt   time.Time
}

// Vendor is a company-scoped record for purchase-side party selection (bills, journal, etc.).
type Vendor struct {
	ID          uint   `gorm:"primaryKey"`
	CompanyID   uint   `gorm:"not null;index"`
	Name        string `gorm:"not null"`
	Address     string `gorm:"type:text"` // optional mailing / remittance address
	PaymentTerm string `gorm:"type:text"` // optional, e.g. Net 30, Due on receipt
	CreatedAt   time.Time
}

