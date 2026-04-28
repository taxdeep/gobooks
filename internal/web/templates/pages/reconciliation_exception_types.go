// 遵循project_guide.md
package pages

import "balanciz/internal/models"

// ── Reconciliation exception list VM ─────────────────────────────────────────

// ReconciliationExceptionListVM is the view model for the exception list page.
type ReconciliationExceptionListVM struct {
	HasCompany bool
	Exceptions []models.ReconciliationException

	// Feedback.
	JustFiled    bool   // manual exception just created
	JustActioned bool   // status transition just completed
	FormError    string // inline form validation message
}

// ── Resolution hook VM ────────────────────────────────────────────────────────

// ExceptionResolutionHookVM is the display representation of one resolution
// hook computed by the service layer.  It is derived from services.ResolutionHook
// and converted in the handler so the pages package stays free of service imports.
type ExceptionResolutionHookVM struct {
	Type              models.ResolutionHookType
	Label             string
	Description       string
	Available         bool
	UnavailableReason string
	// NavigateURL is non-empty for navigation-only hooks (open in new tab / follow link).
	// Empty for execution hooks rendered as POST forms.
	NavigateURL string
}

// ── Reconciliation exception detail VM ───────────────────────────────────────

// ReconciliationExceptionDetailVM is the view model for the exception detail page.
type ReconciliationExceptionDetailVM struct {
	HasCompany bool
	Exception  *models.ReconciliationException

	// Linked source summaries loaded read-only for investigation context.
	LinkedPayout               *models.GatewayPayout
	LinkedPayoutExpectedNet    string
	LinkedPayoutReconciliation *models.PayoutReconciliation
	LinkedBankEntry            *models.BankEntry
	LinkedBankEntryMatch       *models.PayoutReconciliation

	// Resolution hooks — computed by the service layer and converted in the handler.
	AvailableHooks []ExceptionResolutionHookVM
	// RecentAttempts is the last N execution-hook attempt records, newest first.
	RecentAttempts []models.ReconciliationResolutionAttempt

	// Feedback.
	JustActioned bool   // status transition just completed (review/dismiss/resolve)
	ActionError  string // status transition error message
	HookSuccess  bool   // execution hook just succeeded
	HookError    string // execution hook failed
}
