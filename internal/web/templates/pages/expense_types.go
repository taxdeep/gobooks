package pages

import "gobooks/internal/models"

type ExpenseListVM struct {
	HasCompany bool

	FormError string
	Created   bool
	Updated   bool

	CanCreate bool
	CanUpdate bool

	Expenses []models.Expense
}

type ExpenseFormVM struct {
	HasCompany bool
	IsEdit     bool
	EditingID  uint

	ExpenseDate      string
	Description      string
	Amount           string
	CurrencyCode     string
	VendorID         string
	ExpenseAccountID string
	TaskID           string
	IsBillable       bool
	Notes            string

	ExpenseDateError      string
	DescriptionError      string
	AmountError           string
	CurrencyError         string
	VendorError           string
	ExpenseAccountError   string
	TaskError             string
	BillableCustomerError string
	FormError             string

	BaseCurrencyCode string
	MultiCurrency    bool
	CurrencyOptions  []string
	Vendors          []models.Vendor
	ExpenseAccounts  []models.Account
	SelectableTasks  []models.Task
}
