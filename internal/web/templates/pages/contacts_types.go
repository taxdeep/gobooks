// 遵循project_guide.md
package pages

import "gobooks/internal/services"
import "gobooks/internal/models"

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
	DefaultPaymentTermCode string
	AddrStreet1            string
	AddrStreet2            string
	AddrCity               string
	AddrProvince           string
	AddrPostalCode         string
	AddrCountry            string

	NameError string

	Customers    []models.Customer
	PaymentTerms []models.PaymentTerm

	BillableSummaries map[uint]services.CustomerBillableSummary
}

type CustomerNewVM struct {
	HasCompany bool

	Name                   string
	Email                  string
	DefaultPaymentTermCode string
	AddrStreet1            string
	AddrStreet2            string
	AddrCity               string
	AddrProvince           string
	AddrPostalCode         string
	AddrCountry            string

	NameError string
	FormError string

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
