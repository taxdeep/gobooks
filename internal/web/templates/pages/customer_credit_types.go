// 遵循project_guide.md
package pages

import (
	"balanciz/internal/models"

	"github.com/shopspring/decimal"
)

// CustomerCreditsVM is the view model for the customer credits page.
// It shows all credits for a customer and provides an apply form.
type CustomerCreditsVM struct {
	HasCompany bool

	Customer models.Customer

	Credits      []models.CustomerCredit
	Applications []models.CustomerCreditApplication // for the selected credit

	// ActiveCredits are credits with status = active (non-zero remaining).
	ActiveCredits []models.CustomerCredit

	// OutstandingInvoices are the invoices eligible to receive a credit apply.
	OutstandingInvoices []models.Invoice

	// TotalRemaining is the sum of remaining_amount across all active credits.
	TotalRemaining decimal.Decimal

	// Refunds issued to this customer (all statuses, newest first) — surfaces
	// the outcome of "Convert to refund" flows next to the credit history.
	Refunds []models.ARRefund

	// Apply form state.
	SelectedCreditID uint
	SelectedInvoiceID uint
	ApplyAmount       string // raw form value (repopulated on error)

	// Feedback.
	JustApplied bool
	FormError   string
}

// CreditApplicationsVM is used when viewing a single credit's history.
type CreditApplicationsVM struct {
	HasCompany   bool
	Customer     models.Customer
	Credit       models.CustomerCredit
	Applications []models.CustomerCreditApplication
	FormError    string
}
