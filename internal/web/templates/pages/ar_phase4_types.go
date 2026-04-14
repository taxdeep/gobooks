// 遵循project_guide.md
package pages

import "gobooks/internal/models"

// ── CustomerReceipt VMs ───────────────────────────────────────────────────────

// ReceiptsVM is the view model for the Customer Receipts list page.
type ReceiptsVM struct {
	HasCompany     bool
	Receipts       []models.CustomerReceipt
	Customers      []models.Customer
	FilterStatus   string
	FilterCustomer string
	Created        bool
	Saved          bool
}

// ReceiptDetailVM is the view model for a single CustomerReceipt detail / edit page.
type ReceiptDetailVM struct {
	HasCompany   bool
	Receipt      models.CustomerReceipt
	Customers    []models.Customer
	Accounts     []models.Account // bank accounts for picker
	Invoices     []models.Invoice // open invoices for same customer (apply form)
	Applications []models.PaymentApplication
	FormError    string
	Saved        bool
	Confirmed    bool
	Reversed     bool
	Voided       bool
	Applied      bool
	Unapplied    bool
}
