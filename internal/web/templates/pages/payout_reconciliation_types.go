// 遵循project_guide.md
package pages

import (
	"github.com/shopspring/decimal"
	"balanciz/internal/models"
)

// ── Payout reconciliation overview VM ────────────────────────────────────────

// PayoutReconciliationOverviewVM is the view model for the payout reconciliation
// overview page — shows unmatched payouts, unmatched bank entries, and completed
// matches side by side.
type PayoutReconciliationOverviewVM struct {
	HasCompany bool

	UnmatchedPayouts  []models.GatewayPayout
	UnmatchedEntries  []models.BankEntry
	MatchedRecords    []models.PayoutReconciliation

	// Feedback.
	JustMatched  bool
	JustCreated  bool // bank entry created
	FormError    string
}

// ── Bank entry creation VM ────────────────────────────────────────────────────

// BankEntryCreateVM is the view model for the inline bank entry creation form.
type BankEntryCreateVM struct {
	HasCompany   bool
	BankAccounts []models.Account
	FormError    string
}

// ── Payout match form VM ──────────────────────────────────────────────────────

// PayoutMatchFormVM is the view model for the per-payout match form.
type PayoutMatchFormVM struct {
	HasCompany bool

	Payout           *models.GatewayPayout
	Reconciliation   *models.PayoutReconciliation // non-nil = already matched
	CandidateEntries []models.BankEntry            // bank entries with same account + expected net + currency

	// Components and ExpectedNet are loaded from payout_component_service.
	// ExpectedNet = Payout.NetAmount ± component adjustments.
	// When no components exist, ExpectedNet == Payout.NetAmount.
	Components  []models.GatewayPayoutComponent
	ExpectedNet decimal.Decimal

	FormError string
}
