// 遵循project_guide.md
package services

// exception_resolution_hook_service.go — Batch 21: Resolution hook policy and execution.
//
// Provides:
//   - AvailableHooksForException: compute available hooks for one exception
//   - ExecuteResolutionHook:      execute an execution hook and record attempt
//   - ListRecentResolutionAttempts: load recent attempts for an exception
//
// ─── Design ───────────────────────────────────────────────────────────────────
//
// This service owns the hook policy — the rules that determine which hooks are
// available for a given exception based on its type, status, and source linkage.
// The policy is computed here and passed to the UI via the handler/VM layer.
// No hook policy logic lives in templates or handlers.
//
// ─── Hook taxonomy ────────────────────────────────────────────────────────────
//
//   Execution hooks (server action + attempt record):
//     retry_match  — calls MatchGatewayPayoutToBankEntry with the exception's
//                    linked payout and bank entry.  On success, atomically
//                    records the attempt and auto-resolves the exception.
//                    On failure, records a rejected attempt and returns the
//                    domain error so the UI can display it to the operator.
//
//   Navigation hooks (link only, NO attempt record):
//     open_payout_components — generates a URL to the payout detail / component
//                              editor.  The link is passed to the UI; no server
//                              route is called.
//
// ─── Hook availability rules ──────────────────────────────────────────────────
//
//   retry_match
//     eligible types : ExceptionAmountMismatch only
//     requires       : GatewayPayoutID ≠ nil, BankEntryID ≠ nil
//     requires       : exception status ∈ {open, reviewed}
//     requires       : payout NOT already matched
//     requires       : bank entry NOT already matched
//
//   open_payout_components
//     eligible types : ExceptionAmountMismatch, ExceptionUnknownComponentPattern
//     requires       : GatewayPayoutID ≠ nil
//     requires       : exception status ∈ {open, reviewed}
//     requires       : payout NOT already matched
//
// ─── Attempt recording ────────────────────────────────────────────────────────
//
//   On retry_match success:
//     One transaction contains:
//       1. PayoutReconciliation domain truth creation
//       2. attempt row (status=succeeded)
//       3. exception status update to resolved
//
//   On retry_match rejection:
//     The rejection attempt row (status=rejected) is inserted in the same
//     transaction that re-validates the exception's current state. If attempt
//     persistence fails, the whole operation fails so audit truth is never lost.
//
// ─── Not in Batch 21 ──────────────────────────────────────────────────────────
//   - Manual override / forced match
//   - Multi-to-multi resolution hooks
//   - SLA / assignment / notification workflows
//   - Hook execution chains / orchestration

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── Sentinel errors ───────────────────────────────────────────────────────────

var (
	ErrHookTypeUnsupported     = errors.New("unsupported resolution hook type")
	ErrHookNotAvailable        = errors.New("resolution hook is not available for this exception in its current state")
	ErrHookExceptionClosed     = errors.New("resolution hook cannot be executed on a terminal exception")
	ErrHookMissingSourcePayout = errors.New("hook requires a linked gateway payout on the exception")
	ErrHookMissingSourceEntry  = errors.New("hook requires a linked bank entry on the exception")
)

// ── ResolutionHook ────────────────────────────────────────────────────────────

// ResolutionHook is the computed availability and metadata for one hook
// on a specific exception instance.  It is ephemeral (never persisted).
type ResolutionHook struct {
	Type              models.ResolutionHookType
	Label             string
	Description       string
	Available         bool
	UnavailableReason string
	// NavigateURL is non-empty for navigation-only hooks.
	// Empty for execution hooks (which use a POST route).
	NavigateURL string
}

// ── Hook policy ───────────────────────────────────────────────────────────────

// AvailableHooksForException computes the set of resolution hooks relevant to
// the given exception.  Only hooks appropriate for the exception type are
// included; hooks for irrelevant types are omitted entirely.
//
// Hook availability is computed by reading current DB state (matched status of
// linked payout / bank entry).  If a hook is structurally applicable but
// currently unavailable, it is included with Available=false and a reason.
//
// This function is the single authoritative source of hook policy.
// UI templates must not implement their own policy logic.
func AvailableHooksForException(db *gorm.DB, companyID uint, ex *models.ReconciliationException) []ResolutionHook {
	hooks := make([]ResolutionHook, 0, 2)

	isTerminal := models.IsTerminalExceptionStatus(ex.Status)

	// ── open_payout_components (navigation) ───────────────────────────────────
	if isTypeEligibleForComponentsHook(ex.ExceptionType) && ex.GatewayPayoutID != nil {
		h := ResolutionHook{
			Type:  models.HookTypeOpenPayoutComponents,
			Label: "Add / Edit Payout Components",
			Description: "Adjust fee, reserve, or adjustment components to correct " +
				"the payout's expected bank deposit, then retry the match.",
		}
		switch {
		case isTerminal:
			h.Available = false
			h.UnavailableReason = "Exception is already closed."
		case isPayoutMatched(db, companyID, *ex.GatewayPayoutID):
			h.Available = false
			h.UnavailableReason = "Payout is already matched; components cannot be modified."
		default:
			h.Available = true
			h.NavigateURL = fmt.Sprintf("/settings/payment-gateways/payouts/%d", *ex.GatewayPayoutID)
		}
		hooks = append(hooks, h)
	}

	// ── retry_match (execution) ───────────────────────────────────────────────
	if isTypeEligibleForRetryMatch(ex.ExceptionType) && ex.GatewayPayoutID != nil && ex.BankEntryID != nil {
		h := ResolutionHook{
			Type:  models.HookTypeRetryMatch,
			Label: "Retry Match",
			Description: "Attempt to match this payout to the linked bank entry using " +
				"the current expected net (payout net ± components).",
		}
		switch {
		case isTerminal:
			h.Available = false
			h.UnavailableReason = "Exception is already closed."
		case isPayoutMatched(db, companyID, *ex.GatewayPayoutID):
			h.Available = false
			h.UnavailableReason = "Payout is already matched to a bank entry."
		case isBankEntryMatched(db, companyID, *ex.BankEntryID):
			h.Available = false
			h.UnavailableReason = "Bank entry is already matched to a payout."
		default:
			h.Available = true
		}
		hooks = append(hooks, h)
	}

	return hooks
}

// ── Hook execution ────────────────────────────────────────────────────────────

// ExecuteResolutionHook executes an execution hook for the given exception.
//
// Navigation hooks (IsExecutionHook = false) return ErrHookTypeUnsupported.
// All eligibility checks are re-validated inside the service before execution.
//
// An attempt row is always recorded regardless of outcome:
//   - Rejection (eligibility or domain failure): attempt status = rejected
//   - Success: attempt status = succeeded; exception auto-resolved atomically
func ExecuteResolutionHook(
	db *gorm.DB,
	companyID, exceptionID uint,
	hookType models.ResolutionHookType,
	actor string,
) error {
	if !models.IsExecutionHook(hookType) {
		return ErrHookTypeUnsupported
	}

	actor = normalizeActor(actor)

	switch hookType {
	case models.HookTypeRetryMatch:
		return executeRetryMatchHook(db, companyID, exceptionID, actor)
	default:
		return ErrHookTypeUnsupported
	}
}

// ListRecentResolutionAttempts returns the most recent attempts for an
// exception, newest first.  At most `limit` rows are returned.
func ListRecentResolutionAttempts(
	db *gorm.DB,
	companyID, exceptionID uint,
	limit int,
) ([]models.ReconciliationResolutionAttempt, error) {
	var attempts []models.ReconciliationResolutionAttempt
	err := db.
		Where("company_id = ? AND reconciliation_exception_id = ?", companyID, exceptionID).
		Order("created_at DESC, id DESC").
		Limit(limit).
		Find(&attempts).Error
	return attempts, err
}

// ── Internal: retry_match execution ──────────────────────────────────────────

func executeRetryMatchHook(
	db *gorm.DB,
	companyID uint,
	exceptionID uint,
	actor string,
) error {
	var resultErr error
	txErr := db.Transaction(func(tx *gorm.DB) error {
		ex, err := loadLockedReconciliationException(tx, companyID, exceptionID)
		if err != nil {
			return err
		}

		if models.IsTerminalExceptionStatus(ex.Status) {
			resultErr = fmt.Errorf("%w: current status is %s", ErrHookExceptionClosed, ex.Status)
			return createResolutionAttempt(tx, companyID, ex.ID, models.HookTypeRetryMatch,
				models.AttemptStatusRejected, actor,
				"Hook rejected: exception is closed",
				fmt.Sprintf("current status: %s", ex.Status))
		}
		if ex.GatewayPayoutID == nil {
			resultErr = ErrHookMissingSourcePayout
			return createResolutionAttempt(tx, companyID, ex.ID, models.HookTypeRetryMatch,
				models.AttemptStatusRejected, actor,
				"Hook rejected: no payout linked", "")
		}
		if ex.BankEntryID == nil {
			resultErr = ErrHookMissingSourceEntry
			return createResolutionAttempt(tx, companyID, ex.ID, models.HookTypeRetryMatch,
				models.AttemptStatusRejected, actor,
				"Hook rejected: no bank entry linked", "")
		}
		if !isTypeEligibleForRetryMatch(ex.ExceptionType) {
			msg := fmt.Sprintf("exception type %s is not eligible for retry_match", ex.ExceptionType)
			resultErr = fmt.Errorf("%w: %s", ErrHookNotAvailable, msg)
			return createResolutionAttempt(tx, companyID, ex.ID, models.HookTypeRetryMatch,
				models.AttemptStatusRejected, actor,
				"Hook rejected: ineligible exception type", msg)
		}

		matchErr := matchGatewayPayoutToBankEntryTx(tx, companyID, *ex.GatewayPayoutID, *ex.BankEntryID, actor)
		if matchErr != nil {
			resultErr = matchErr
			return createResolutionAttempt(tx, companyID, ex.ID, models.HookTypeRetryMatch,
				models.AttemptStatusRejected, actor,
				"Retry match failed", matchErr.Error())
		}

		if err := createResolutionAttempt(tx, companyID, ex.ID, models.HookTypeRetryMatch,
			models.AttemptStatusSucceeded, actor,
			"Retry match succeeded; payout reconciled to bank entry",
			fmt.Sprintf("payout_id=%d bank_entry_id=%d", *ex.GatewayPayoutID, *ex.BankEntryID),
		); err != nil {
			return fmt.Errorf("record succeeded attempt: %w", err)
		}
		if err := updateLockedExceptionStatus(tx, ex,
			models.ExceptionStatusResolved, actor,
			"Auto-resolved: retry_match hook succeeded",
		); err != nil {
			return fmt.Errorf("auto-resolve exception: %w", err)
		}
		return nil
	})
	if txErr != nil {
		return txErr
	}
	return resultErr
}

// ── Internal: hook policy helpers ────────────────────────────────────────────

// isTypeEligibleForRetryMatch returns true when retry_match is a meaningful
// action for the given exception type.  Only amount_mismatch is eligible:
// it is the only type where adding payout components can resolve the condition.
// account_mismatch and currency_mismatch cannot be fixed by component changes.
// Conflict types (payout_conflict, bank_entry_conflict) mean the object is
// already matched, so retrying will always fail immediately.
func isTypeEligibleForRetryMatch(t models.ReconciliationExceptionType) bool {
	return t == models.ExceptionAmountMismatch
}

// isTypeEligibleForComponentsHook returns true when adding/editing payout
// components is a sensible next action for the given exception type.
func isTypeEligibleForComponentsHook(t models.ReconciliationExceptionType) bool {
	switch t {
	case models.ExceptionAmountMismatch,
		models.ExceptionUnknownComponentPattern:
		return true
	}
	return false
}

// isBankEntryMatched returns true when a PayoutReconciliation record exists
// for the given bank entry.
func isBankEntryMatched(db *gorm.DB, companyID, bankEntryID uint) bool {
	var count int64
	db.Model(&models.PayoutReconciliation{}).
		Where("company_id = ? AND bank_entry_id = ?", companyID, bankEntryID).
		Count(&count)
	return count > 0
}

// ── Internal: attempt recording ───────────────────────────────────────────────

// createResolutionAttempt inserts one immutable attempt row for an execution hook.
func createResolutionAttempt(
	db *gorm.DB,
	companyID, exceptionID uint,
	hookType models.ResolutionHookType,
	status models.ResolutionAttemptStatus,
	actor, summary, detail string,
) error {
	attempt := &models.ReconciliationResolutionAttempt{
		CompanyID:                 companyID,
		ReconciliationExceptionID: exceptionID,
		HookType:                  hookType,
		Status:                    status,
		Summary:                   summary,
		Detail:                    detail,
		Actor:                     actor,
	}
	return db.Create(attempt).Error
}
