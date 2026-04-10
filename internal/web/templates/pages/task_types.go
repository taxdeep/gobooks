// 遵循project_guide.md
package pages

import "github.com/shopspring/decimal"
import "gobooks/internal/models"
import "gobooks/internal/services"

type TasksVM struct {
	HasCompany bool

	FormError string
	Created   bool
	Updated   bool
	Completed bool
	Cancelled bool

	CanCreate bool
	CanUpdate bool

	Customers []models.Customer
	Tasks     []models.Task

	FilterCustomerID string
	FilterStatus     string
	FilterFrom       string
	FilterTo         string
}

type TaskFormVM struct {
	HasCompany bool
	IsEdit     bool
	EditingID  uint

	Status       models.TaskStatus
	ReadOnlyCore bool
	CanCancel    bool

	CustomerID       string
	Title            string
	TaskDate         string
	Quantity         string
	UnitType         string
	Rate             string
	CurrencyCode     string
	IsBillable       bool
	Notes            string
	// ServiceItemID is the string form of ProductServiceID (empty = none selected).
	ServiceItemID string

	CustomerError     string
	TitleError        string
	TaskDateError     string
	QuantityError     string
	UnitTypeError     string
	RateError         string
	CurrencyError     string
	ServiceItemError  string
	FormError         string

	BaseCurrencyCode string
	MultiCurrency    bool
	CurrencyOptions  []string
	Customers        []models.Customer
	// ServiceItems holds the active service-type items for the company, used to
	// populate the Service Item dropdown in the New / Edit Task form.
	ServiceItems []models.ProductService
}

type TaskDetailVM struct {
	HasCompany bool

	Task models.Task

	LinkedExpenses  []models.Expense
	LinkedBillLines []models.BillLine

	BillableExpenseAmount  decimal.Decimal
	NonBillableExpenseCost decimal.Decimal

	BillingTrace services.TaskBillingTrace

	FormError   string
	Created     bool
	Updated     bool
	Completed   bool
	Cancelled   bool
	CanEdit     bool
	CanComplete bool
	CanCancel   bool
}

type BillableWorkVM struct {
	HasCompany bool

	FormError string

	CanGenerate bool

	Customers          []models.Customer
	SelectedCustomerID string

	Tasks     []models.Task
	Expenses  []models.Expense
	BillLines []models.BillLine

	SelectedTaskIDs     []string
	SelectedExpenseIDs  []string
	SelectedBillLineIDs []string
}

type BillableWorkReportVM struct {
	HasCompany bool

	FormError string

	Customers          []models.Customer
	SelectedCustomerID string

	Tasks     []models.Task
	Expenses  []models.Expense
	BillLines []models.BillLine

	TaskLaborTotals       []services.CurrencyTotal
	BillableExpenseTotals []services.CurrencyTotal
	TotalUnbilledTotals   []services.CurrencyTotal

	CustomerSummaries map[uint]services.CustomerBillableSummary
}
