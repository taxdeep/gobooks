// 遵循project_guide.md
package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// ── Task status ───────────────────────────────────────────────────────────────

// TaskStatus tracks the lifecycle of a task.
//
// State machine:
//
//	open → completed → invoiced
//	open → cancelled
//	completed → cancelled   (only before invoiced)
//	invoiced → (no transitions; immutable once billed)
type TaskStatus string

const (
	// TaskStatusOpen is the initial state. Work is in progress.
	TaskStatusOpen TaskStatus = "open"
	// TaskStatusCompleted means work is done and the task is ready for billing.
	// Only completed tasks appear in the "Generate Invoice Draft" candidate list.
	TaskStatusCompleted TaskStatus = "completed"
	// TaskStatusInvoiced means the task has been included in an Invoice.
	// The task is immutable while in this state; it reverts to completed if the
	// invoice is voided (service layer responsibility).
	TaskStatusInvoiced TaskStatus = "invoiced"
	// TaskStatusCancelled means the task was abandoned; never billed.
	TaskStatusCancelled TaskStatus = "cancelled"
)

// AllTaskStatuses returns statuses in stable UI order.
func AllTaskStatuses() []TaskStatus {
	return []TaskStatus{
		TaskStatusOpen,
		TaskStatusCompleted,
		TaskStatusInvoiced,
		TaskStatusCancelled,
	}
}

// TaskStatusLabel returns a human-readable label for a task status.
func TaskStatusLabel(s TaskStatus) string {
	switch s {
	case TaskStatusOpen:
		return "Open"
	case TaskStatusCompleted:
		return "Completed"
	case TaskStatusInvoiced:
		return "Invoiced"
	case TaskStatusCancelled:
		return "Cancelled"
	default:
		return string(s)
	}
}

const (
	TaskUnitTypeHour  = "hour"
	TaskUnitTypeDay   = "day"
	TaskUnitTypeUnit  = "unit"
	TaskUnitTypeFixed = "fixed"
)

// AllTaskUnitTypes returns supported task unit types in stable UI order.
func AllTaskUnitTypes() []string {
	return []string{
		TaskUnitTypeHour,
		TaskUnitTypeDay,
		TaskUnitTypeUnit,
		TaskUnitTypeFixed,
	}
}

// IsValidTaskUnitType reports whether the unit type is supported.
func IsValidTaskUnitType(v string) bool {
	switch v {
	case TaskUnitTypeHour, TaskUnitTypeDay, TaskUnitTypeUnit, TaskUnitTypeFixed:
		return true
	default:
		return false
	}
}

// TaskUnitTypeLabel returns a human-readable label for a task unit type.
func TaskUnitTypeLabel(v string) string {
	switch v {
	case TaskUnitTypeHour:
		return "Hour"
	case TaskUnitTypeDay:
		return "Day"
	case TaskUnitTypeUnit:
		return "Unit"
	case TaskUnitTypeFixed:
		return "Fixed Fee"
	default:
		return v
	}
}

// ── ReinvoiceStatus — shared by Task-linked expenses and bill lines ───────────

// ReinvoiceStatus tracks the billable-expense invoice state for expenses and
// bill lines that are linked to a task and marked is_billable = true.
type ReinvoiceStatus string

const (
	// ReinvoiceStatusNone is the zero value — used for non-billable or
	// non-task-linked records; these never enter the invoicing flow.
	ReinvoiceStatusNone ReinvoiceStatus = ""
	// ReinvoiceStatusUninvoiced marks a billable task-linked cost that is
	// pending inclusion in an Invoice Draft.
	ReinvoiceStatusUninvoiced ReinvoiceStatus = "uninvoiced"
	// ReinvoiceStatusInvoiced marks a cost that has been included in an Invoice.
	// Reverts to uninvoiced if the invoice is voided.
	ReinvoiceStatusInvoiced ReinvoiceStatus = "invoiced"
	// ReinvoiceStatusExcluded marks a cost that the user has explicitly decided
	// not to pass through to the customer (preserves the record; not an error).
	ReinvoiceStatusExcluded ReinvoiceStatus = "excluded"
)

// TaskInvoiceSourceType identifies which source table produced an invoice line.
type TaskInvoiceSourceType string

const (
	TaskInvoiceSourceTask     TaskInvoiceSourceType = "task"
	TaskInvoiceSourceExpense  TaskInvoiceSourceType = "expense"
	TaskInvoiceSourceBillLine TaskInvoiceSourceType = "bill_line"
)

// ── Task model ────────────────────────────────────────────────────────────────

// Task is a company-scoped work record within the Work Execution Layer.
//
// Design constraints (Batch 1):
//   - customer_id is required (no customer = no task in v1).
//   - rate, unit_type, and currency_code are snapshot fields; they are captured
//     at creation time and are never updated by downstream item/price-list changes.
//   - invoice_id and invoice_line_id are quick-lookup cache columns that point to
//     the current active Invoice linkage. They are cleared to NULL when the linked
//     invoice is voided. The authoritative audit trail lives in task_invoice_sources.
//   - Task does not touch JournalEntries or LedgerEntries directly.
type Task struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	// CustomerID is required; Tasks must always belong to a customer.
	CustomerID uint     `gorm:"not null;index"`
	Customer   Customer `gorm:"foreignKey:CustomerID"`

	// Title is the human-readable work description.
	// It becomes the invoice line Description when the task is billed.
	Title string `gorm:"type:text;not null;default:''"`

	TaskDate time.Time `gorm:"not null"`

	// Snapshot fields — fixed at creation, not affected by later item/rate changes.
	Quantity     decimal.Decimal `gorm:"type:numeric(18,6);not null;default:1"`
	UnitType     string          `gorm:"type:text;not null;default:'hour'"` // hour|day|unit|fixed
	Rate         decimal.Decimal `gorm:"type:numeric(18,6);not null;default:0"`
	CurrencyCode string          `gorm:"type:text;not null;default:''"`

	IsBillable bool       `gorm:"not null;default:true"`
	Status     TaskStatus `gorm:"type:text;not null;default:'open'"`
	Notes      string     `gorm:"type:text;not null;default:''"`

	// Quick-lookup cache for current invoice linkage.
	// Authoritative source: task_invoice_sources.
	// Cleared to NULL by the service layer when the linked invoice is voided.
	InvoiceID     *uint        `gorm:"index"`
	Invoice       *Invoice     `gorm:"foreignKey:InvoiceID"`
	InvoiceLineID *uint        `gorm:"index"`
	InvoiceLine   *InvoiceLine `gorm:"foreignKey:InvoiceLineID"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// BillableAmount returns quantity × rate as the computed labor billing amount.
// This is the amount that will appear on the invoice line when the task is billed.
func (t Task) BillableAmount() decimal.Decimal {
	return t.Quantity.Mul(t.Rate)
}

// ── TaskInvoiceSource bridge table ────────────────────────────────────────────

// TaskInvoiceSource is the authoritative audit record linking a billing source
// (task, expense, or bill_line) to the invoice line it produced.
//
// Records are NEVER deleted — even after an invoice is voided — preserving the
// full generation history. The quick-lookup cache columns on the source tables
// (tasks.invoice_id, expenses.invoice_id, etc.) are cleared by the service layer
// on void, while this table retains the historical linkage row.
//
// Active/current linkage semantics:
//   - A source may appear in multiple bridge rows over its lifetime.
//   - At most one row may be active for a given (source_type, source_id).
//   - Historical rows are marked voided_at != NULL when that linkage is no
//     longer active (for example after an invoice is voided).
type TaskInvoiceSource struct {
	ID        uint `gorm:"primaryKey"`
	CompanyID uint `gorm:"not null;index"`

	// Target invoice and line.
	//
	// These pointers are NULL only after a draft invoice is deleted and the
	// linkage is released. We preserve the bridge row for audit history while
	// clearing the current draft reference so the source may be re-billed later.
	InvoiceID     *uint        `gorm:"index"`
	Invoice       *Invoice     `gorm:"foreignKey:InvoiceID"`
	InvoiceLineID *uint        `gorm:"index"`
	InvoiceLine   *InvoiceLine `gorm:"foreignKey:InvoiceLineID"`

	// SourceType identifies which table source_id refers to.
	// Values: "task" | "expense" | "bill_line"
	SourceType TaskInvoiceSourceType `gorm:"type:text;not null"`
	// SourceID is the primary key of the source row in its respective table.
	SourceID uint `gorm:"not null"`

	// AmountSnapshot is the billing amount captured at generation time.
	// Immutable; not affected by later edits to the source row.
	AmountSnapshot decimal.Decimal `gorm:"type:numeric(18,2);not null;default:0"`

	// VoidedAt marks when this linkage became historical/inactive.
	// NULL = current active linkage; non-NULL = preserved history row.
	VoidedAt *time.Time `gorm:"index"`

	CreatedAt time.Time
}
