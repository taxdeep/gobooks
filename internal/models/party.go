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

// CustomerCurrencyPolicy governs which transaction currencies are permitted on
// documents created for a customer. AR.9 / § 12.3 of project_guide.md.
type CustomerCurrencyPolicy string

const (
	// CustomerCurrencyPolicySingle — only the customer's default currency is allowed.
	// Any attempt to create a document in a different currency is rejected.
	CustomerCurrencyPolicySingle CustomerCurrencyPolicy = "single"

	// CustomerCurrencyPolicyMultiAllowed — multiple currencies are allowed.
	// The default currency is pre-filled but can be overridden per document.
	CustomerCurrencyPolicyMultiAllowed CustomerCurrencyPolicy = "multi_allowed"
)

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
	// CurrencyCode is the customer's default invoice currency (ISO 4217, e.g. "USD").
	// Empty string means the company's base currency is used.
	CurrencyCode string `gorm:"type:text;not null;default:''"`
	// CurrencyPolicy controls whether documents may use currencies other than
	// CurrencyCode. Defaults to "single" (backward-compatible).
	CurrencyPolicy CustomerCurrencyPolicy `gorm:"type:text;not null;default:'single'"`
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

// VendorCurrencyPolicy mirrors CustomerCurrencyPolicy for the AP side.
type VendorCurrencyPolicy string

const (
	VendorCurrencyPolicySingle       VendorCurrencyPolicy = "single"
	VendorCurrencyPolicyMultiAllowed VendorCurrencyPolicy = "multi_allowed"
)

// Vendor is a company-scoped record for purchase-side party selection (bills, journal, etc.).
type Vendor struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`
	Name      string `gorm:"not null"`
	// Email is the vendor's contact email address.
	Email string `gorm:"type:text;not null;default:''"`
	// Phone is the vendor's contact phone number.
	Phone   string `gorm:"type:text;not null;default:''"`
	Address string `gorm:"type:text"` // optional mailing / remittance address
	// CurrencyCode is the vendor's default billing currency (ISO 4217, e.g. "USD").
	// Empty string means the company's base currency is used.
	CurrencyCode string `gorm:"type:text;not null;default:''"`
	// CurrencyPolicy controls whether documents may use currencies other than
	// CurrencyCode. Defaults to "single" (backward-compatible).
	CurrencyPolicy VendorCurrencyPolicy `gorm:"type:text;not null;default:'single'"`
	// Notes is a free-form internal note about this vendor.
	Notes string `gorm:"type:text;not null;default:''"`
	// DefaultPaymentTermCode references a PaymentTerm.Code for this company.
	DefaultPaymentTermCode string `gorm:"type:text;not null;default:''"`
	CreatedAt              time.Time
}

