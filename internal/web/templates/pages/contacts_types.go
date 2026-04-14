// 遵循project_guide.md
package pages

import (
	"github.com/shopspring/decimal"
	"gobooks/internal/models"
	"gobooks/internal/services"
)

type CustomersVM struct {
	HasCompany bool

	FormError string
	Created   bool
	Updated   bool

	// Drawer state
	DrawerOpen bool
	EditingID  uint

	// Editable form fields (repopulated on validation failure)
	Name                   string
	Email                  string
	CurrencyCode           string
	DefaultPaymentTermCode string
	AddrStreet1            string
	AddrStreet2            string
	AddrCity               string
	AddrProvince           string
	AddrPostalCode         string
	AddrCountry            string

	NameError     string
	CurrencyError string

	// MultiCurrency indicates whether the company has multi-currency enabled.
	MultiCurrency    bool
	BaseCurrencyCode string
	Currencies       []models.Currency

	Customers    []models.Customer
	PaymentTerms []models.PaymentTerm

	BillableSummaries map[uint]services.CustomerBillableSummary
}

type CustomerNewVM struct {
	HasCompany bool

	Name                   string
	Email                  string
	CurrencyCode           string
	DefaultPaymentTermCode string
	AddrStreet1            string
	AddrStreet2            string
	AddrCity               string
	AddrProvince           string
	AddrPostalCode         string
	AddrCountry            string

	NameError     string
	CurrencyError string
	FormError     string

	// MultiCurrency indicates whether the company has multi-currency enabled.
	MultiCurrency    bool
	BaseCurrencyCode string
	Currencies       []models.Currency

	PaymentTerms []models.PaymentTerm
}

type CustomerDetailVM struct {
	HasCompany bool

	Customer                models.Customer
	DefaultPaymentTermLabel string
	BillableSummary         services.CustomerBillableSummary
	ARSummary               services.CustomerARSummary
	OutstandingInvoices     []models.Invoice
	RecentInvoices          []models.Invoice
	MostRecentInvoice       *models.Invoice

	// Batch 16: credit balance visibility
	CreditCount     int
	CreditRemaining decimal.Decimal

	// Phase 12: currency policy management
	AllowedCurrencies []models.CustomerAllowedCurrency
	BaseCurrencyCode  string
	CurrencyPolicySaved bool
	CurrencyPolicyError string
}

type VendorsVM struct {
	HasCompany bool

	Name                   string
	Email                  string
	Phone                  string
	Address                string
	CurrencyCode           string
	Notes                  string
	DefaultPaymentTermCode string
	NameError              string
	FormError              string
	Created                bool

	// MultiCurrency indicates whether the company has multi-currency enabled.
	// When false the currency selector is hidden and BaseCurrencyCode is shown read-only.
	MultiCurrency    bool
	BaseCurrencyCode string

	Vendors      []models.Vendor
	PaymentTerms []models.PaymentTerm
	Currencies   []models.Currency // enabled currencies (base + foreign); only used when MultiCurrency == true
}
