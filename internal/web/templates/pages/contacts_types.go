// 遵循产品需求 v1.0
package pages

import "gobooks/internal/models"

type CustomersVM struct {
	HasCompany bool

	Name          string
	Address       string
	PaymentTerm   string
	NameError     string
	FormError     string
	Created       bool

	Customers []models.Customer
}

type VendorsVM struct {
	HasCompany bool

	Name          string
	Address       string
	PaymentTerm   string
	NameError     string
	FormError     string
	Created       bool

	Vendors []models.Vendor
}

