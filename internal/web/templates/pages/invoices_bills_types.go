// 遵循project_guide.md
package pages

import "gobooks/internal/models"

type InvoicesVM struct {
	HasCompany bool

	Customers []models.Customer
	Invoices  []models.Invoice

	// Legacy inline-create fields (kept for backward compat; no longer rendered).
	InvoiceNumber string
	CustomerID    string
	InvoiceDate   string
	Amount        string
	Memo          string

	InvoiceNumberError string
	CustomerError      string
	DateError          string
	AmountError        string
	FormError          string

	DuplicateWarning bool
	DuplicateMessage string

	Created bool
	// Saved is set after a save-draft redirect (?saved=1).
	Saved bool
	// Posted is set after a successful post (?posted=1).
	Posted bool
	// Deleted is set after a draft delete redirect (?deleted=1).
	Deleted bool

	FilterQ             string
	FilterCustomerID    string
	FilterCustomerLabel string // resolved customer name for SmartPicker echo display
	FilterStatus        string
	FilterFrom          string
	FilterTo            string
}

type BillsVM struct {
	HasCompany bool

	Vendors []models.Vendor
	Bills   []models.Bill

	FormError string

	// Saved is set after a save-draft redirect (?saved=1).
	Saved bool
	// Posted is set after a successful submit/post redirect (?posted=1).
	Posted bool
	// Voided is set after a successful void redirect (?voided=1).
	Voided bool

	FilterQ           string
	FilterVendorID    string
	FilterVendorLabel string // resolved vendor name for SmartPicker echo display
	FilterFrom        string
	FilterTo          string
}

