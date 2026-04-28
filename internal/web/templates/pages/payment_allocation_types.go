// 遵循project_guide.md
package pages

import (
	"github.com/shopspring/decimal"
	"balanciz/internal/models"
)

// ── Payment multi-allocation VM ───────────────────────────────────────────────

// AllocatableInvoiceRow represents one candidate invoice on the allocation form.
type AllocatableInvoiceRow struct {
	Invoice    models.Invoice
	AmountStr  string // pre-filled amount from form re-display (empty on first load)
}

// PaymentAllocationVM is the view model for the payment multi-allocation form.
type PaymentAllocationVM struct {
	HasCompany bool

	// Source transaction.
	Txn              models.PaymentTransaction
	AlreadyAllocated decimal.Decimal
	Remaining        decimal.Decimal

	// Candidate invoices (same customer, open statuses).
	Invoices []AllocatableInvoiceRow

	// Post-success / error feedback.
	Success   bool
	FormError string

	// Allocations already applied (shown after success or on re-load).
	ExistingAllocations []models.PaymentAllocation
}

// ── Customer credit multi-allocation VM ──────────────────────────────────────

// CreditAllocationVM is the view model for the credit multi-allocation form.
type CreditAllocationVM struct {
	HasCompany bool

	// Source credit.
	Credit    models.CustomerCredit
	CustomerID uint

	// Candidate invoices (same customer, open statuses).
	Invoices []AllocatableInvoiceRow

	// Post-success / error feedback.
	Success   bool
	FormError string

	// Applications already recorded for this credit.
	ExistingApplications []models.CustomerCreditApplication
}
