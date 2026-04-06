// 遵循project_guide.md
package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// Expense is a company-scoped standalone cost record, distinct from vendor Bills.
//
// It represents a direct expense entry (e.g. out-of-pocket spend, credit card
// charge, or any cost not tied to a formal vendor invoice) that needs to be
// tracked and, optionally, billed through to a customer.
//
// Task linkage rules (enforced by the service layer, not the DB schema):
//   - When task_id IS NULL:   a plain internal expense; task linkage fields are ignored.
//   - When task_id IS NOT NULL: the expense enters the Task body:
//   - billable_customer_id becomes required and must equal Task.customer_id.
//   - is_billable determines whether the expense is passed through to the customer.
//   - If is_billable = true:  reinvoice_status is set to "uninvoiced"; the expense
//     can be included in a billable Invoice Draft via the Draft Generator.
//   - If is_billable = false: reinvoice_status stays ""; the expense counts toward
//     the task's non-billable cost for margin analysis only.
//
// invoice_id / invoice_line_id are quick-lookup cache columns.
// The authoritative linkage record lives in task_invoice_sources.
type Expense struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	// Task linkage (optional).
	TaskID   *uint `gorm:"index"`
	Task     *Task `gorm:"foreignKey:TaskID"`

	// BillableCustomerID identifies who this expense is billed to.
	// When TaskID is set, must equal Task.CustomerID (service-layer rule).
	BillableCustomerID *uint     `gorm:"index"`
	BillableCustomer   *Customer `gorm:"foreignKey:BillableCustomerID"`

	// IsBillable marks whether this expense should be passed through to the customer.
	// Only meaningful when TaskID is set.
	IsBillable bool `gorm:"not null;default:false"`

	// ReinvoiceStatus tracks the invoice lifecycle of this billable expense.
	// '' (none) | uninvoiced | invoiced | excluded
	// Managed by the service layer; not set directly by handlers.
	ReinvoiceStatus ReinvoiceStatus `gorm:"type:text;not null;default:''"`

	// Quick-lookup cache for current invoice linkage.
	// Authoritative source: task_invoice_sources.
	// Cleared to NULL by the service layer when the linked invoice is voided.
	InvoiceID     *uint        `gorm:"index"`
	Invoice       *Invoice     `gorm:"foreignKey:InvoiceID"`
	InvoiceLineID *uint        `gorm:"index"`
	InvoiceLine   *InvoiceLine `gorm:"foreignKey:InvoiceLineID"`

	// Core expense details.
	ExpenseDate  time.Time       `gorm:"not null"`
	Description  string          `gorm:"type:text;not null;default:''"`
	Amount       decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`
	CurrencyCode string          `gorm:"type:text;not null;default:''"`

	// Optional vendor and GL account references.
	VendorID *uint    `gorm:"index"`
	Vendor   *Vendor  `gorm:"foreignKey:VendorID"`

	ExpenseAccountID *uint    `gorm:"index"`
	ExpenseAccount   *Account `gorm:"foreignKey:ExpenseAccountID"`

	// MarkupPercent is reserved for future pass-through pricing support.
	// v1: always 0; UI does not expose this field.
	MarkupPercent decimal.Decimal `gorm:"type:numeric(8,4);not null;default:0"`

	Notes string `gorm:"type:text;not null;default:''"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
