// 遵循project_guide.md
package pages

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
	AddrStreet2    string
	AddrCity       string
	AddrProvince   string
	AddrPostalCode string
	AddrCountry    string

	NameError string

	Customers    []models.Customer
	PaymentTerms []models.PaymentTerm
}

type CustomerNewVM struct {
	HasCompany bool

	Name                   string
	Email                  string
	DefaultPaymentTermCode string
	AddrStreet1            string
	AddrStreet2    string
	AddrCity       string
	AddrProvince   string
	AddrPostalCode string
	AddrCountry    string

	NameError string
	FormError string

	PaymentTerms []models.PaymentTerm
}

type VendorsVM struct {
	HasCompany bool

	Name                   string
	Address                string
	DefaultPaymentTermCode string
	NameError              string
	FormError   string
	Created     bool

	Vendors      []models.Vendor
	PaymentTerms []models.PaymentTerm
}
