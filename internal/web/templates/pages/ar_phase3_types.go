// 遵循project_guide.md
package pages

import "gobooks/internal/models"

// ── CustomerDeposit VMs ───────────────────────────────────────────────────────

// DepositsVM is the view model for the Customer Deposits list page.
type DepositsVM struct {
	HasCompany     bool
	Deposits       []models.CustomerDeposit
	Customers      []models.Customer
	FilterStatus   string
	FilterCustomer string
	Created        bool
	Saved          bool
}

// DepositDetailVM is the view model for a single CustomerDeposit detail / edit page.
type DepositDetailVM struct {
	HasCompany bool
	Deposit    models.CustomerDeposit
	Customers  []models.Customer
	Accounts   []models.Account // bank + liability accounts for pickers
	Invoices   []models.Invoice // posted/sent invoices for the same customer
	FormError  string
	Saved      bool
	Posted     bool
	Voided     bool
	Applied    bool
}
