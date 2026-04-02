// 遵循project_guide.md
package models

import (
	"strings"
	"time"
)

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
	ID             uint   `gorm:"primaryKey"`
	CompanyID      uint   `gorm:"not null;index"`
	Name           string `gorm:"not null"`
	Email          string `gorm:"type:text;not null;default:''"` // optional customer email for invoicing
	AddrStreet1    string `gorm:"type:text;not null;default:''"`
	AddrStreet2    string `gorm:"type:text;not null;default:''"`
	AddrCity       string `gorm:"type:text;not null;default:''"`
	AddrProvince   string `gorm:"type:text;not null;default:''"`
	AddrPostalCode string `gorm:"type:text;not null;default:''"`
	AddrCountry    string `gorm:"type:text;not null;default:''"`
	// DefaultPaymentTermCode references a PaymentTerm.Code for this company.
	// Empty string means "use company default at document creation time".
	DefaultPaymentTermCode string `gorm:"type:text;not null;default:''"`
	CreatedAt              time.Time
}

// FormattedAddress returns a newline-separated address string composed from
// structured fields, matching the format used in invoice snapshots and PDF rendering.
func (c Customer) FormattedAddress() string {
	parts := make([]string, 0, 4)
	if c.AddrStreet1 != "" {
		parts = append(parts, c.AddrStreet1)
	}
	if c.AddrStreet2 != "" {
		parts = append(parts, c.AddrStreet2)
	}
	cityProv := ""
	if c.AddrCity != "" {
		cityProv = c.AddrCity
	}
	if c.AddrProvince != "" {
		if cityProv != "" {
			cityProv += ", "
		}
		cityProv += c.AddrProvince
	}
	if c.AddrPostalCode != "" {
		if cityProv != "" {
			cityProv += " "
		}
		cityProv += c.AddrPostalCode
	}
	if cityProv != "" {
		parts = append(parts, cityProv)
	}
	if c.AddrCountry != "" {
		parts = append(parts, c.AddrCountry)
	}
	return strings.Join(parts, "\n")
}

// Vendor is a company-scoped record for purchase-side party selection (bills, journal, etc.).
type Vendor struct {
	ID          uint   `gorm:"primaryKey"`
	CompanyID   uint   `gorm:"not null;index"`
	Name        string `gorm:"not null"`
	Address string `gorm:"type:text"` // optional mailing / remittance address
	// DefaultPaymentTermCode references a PaymentTerm.Code for this company.
	DefaultPaymentTermCode string `gorm:"type:text;not null;default:''"`
	CreatedAt              time.Time
}

