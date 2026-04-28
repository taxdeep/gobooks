// 遵循project_guide.md
package pages

import (
	"balanciz/internal/models"

	"github.com/shopspring/decimal"
)

// VendorCreditsVM is the view model for the vendor credits page at
// /vendors/:id/credits. Mirrors CustomerCreditsVM on the AR side, but backed
// by VendorCreditNote directly (there is no VendorCredit aggregator model —
// VCNs are the unit of vendor-side credit).
type VendorCreditsVM struct {
	HasCompany bool

	Vendor models.Vendor

	// CreditNotes is every VCN for the vendor (all statuses), newest first.
	// The template splits active vs historical based on Status + RemainingAmount.
	CreditNotes []models.VendorCreditNote

	// OpenBills are bills eligible to receive a credit apply (posted /
	// partially_paid with balance_due > 0). Used by the Apply-to-Bill link
	// — each VCN's detail page has the actual apply form.
	OpenBills []models.Bill

	// TotalRemaining is the sum of RemainingAmount across active VCNs
	// (status = posted or partially_applied).
	TotalRemaining decimal.Decimal

	// Refunds is every VendorRefund we've received from this vendor, newest
	// first. Surfaces the outcome of "Convert to refund" flows next to the
	// credit history.
	Refunds []models.VendorRefund
}
