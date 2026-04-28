// 遵循project_guide.md
package services

// payment_reverse_hook_service.go — Batch 26/27: Payment reverse hook policy and execution.
//
// Provides:
//   - AvailablePaymentReverseHooks:   compute available hooks for one exception
//   - ExecutePaymentReverseHook:      execute an execution hook and record attempt
//   - ListRecentPRAttempts:           load recent attempts for an exception
//
// ─── Design ───────────────────────────────────────────────────────────────────
//
// This service owns the hook policy for the payment reverse domain — the rules
// that determine which hooks are available for a given exception based on its
// type, status, and source linkage.
//
// The policy is computed here and passed to the UI via the handler/VM layer.
// No hook policy logic lives in templates or handlers.
//
// ─── Hook taxonomy ────────────────────────────────────────────────────────────
//
//   Navigation hooks (link only, NO attempt record):
//     open_original_charge       — link to the original charge/capture transaction
//     open_reverse_transaction   — link to the reverse (refund/chargeback) transaction
//     open_forward_allocations   — link to the transactions page for the original charge
//
//   Execution hooks (server action + attempt record):
//     retry_safe_reverse_check   — calls the existing reverse-allocation eligibility
//                                  validator.  Read-only: does NOT apply the reversal.
//                                  On success: records attempted + transitions to reviewed.
//                                  On rejection: records rejected attempt + reason.
//
// ─── Eligibility rules ───────────────────────────────────────────────────────
//
//   open_original_charge
//     requires : OriginalTxnID != nil
//     always available for non-terminal exceptions (navigation only)
//
//   open_reverse_transaction
//     requires : ReverseTxnID != nil
//     always available for non-terminal exceptions (navigation only)
//
//   open_forward_allocations
//     requires : OriginalTxnID != nil
//     requires : original charge has multi-invoice PaymentAllocation rows
//     not shown when strategy is single or none
//
//   retry_safe_reverse_check
//     requires : ReverseTxnID != nil AND OriginalTxnID != nil
//     requires : exception status ∈ {open, reviewed}
//     requires : no prior succeeded retry_safe_reverse_check attempt
//     requires : original charge has multi-alloc records (multi-path only)
//     requires : reverse txn is posted (has PostedJournalEntryID)
//     unavailable when terminal or when structural pre-conditions fail
//
// ─── Attempt recording ───────────────────────────────────────────────────────
//
//   On retry_safe_reverse_check success:
//     - Validation passed: records PRAttemptSucceeded attempt
//     - Exception status transitions to "reviewed" (idempotent if already reviewed)
//
//   On retry_safe_reverse_check rejection:
//     - Records PRAttemptRejected attempt with the domain error as Detail
//     - Exception status does NOT change
//
// ─── Concurrency hardening (Batch 27) ────────────────────────────────────────
//
//   executePRRetryCheckHook runs entirely inside a single db.Transaction with
//   SELECT FOR UPDATE on the exception row.  This serialises concurrent hook
//   POST requests (e.g. double-click) so that:
//     - At most one attempt is ever recorded as "succeeded" per distinct
//       execution window.
//     - Rejection attempt rows are committed atomically with the eligibility
//       re-check — they are never lost to a rollback.
//     - The status transition (open → reviewed) is atomic with the attempt row.
//
//   Business-rejection errors are propagated via a closed-over outerErr that
//   is set inside the transaction func (which returns nil to commit), and then
//   returned by the outer function after the transaction commits.  This ensures
//   both the attempt row and the caller error are always consistent.
//
// ─── Not in Batch 26/27 ──────────────────────────────────────────────────────
//   - Auto-apply after validation success (Batch 28+)
//   - Manual invoice restore editor
//   - Full guarded reverse execution engine
//   - Assignment / SLA / notification workflows

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── Sentinel errors (execution) ───────────────────────────────────────────────

// ── Sentinel errors ───────────────────────────────────────────────────────────

var (
	ErrPRHookTypeUnsupported   = errors.New("unsupported payment reverse hook type")
	ErrPRHookNotAvailable      = errors.New("payment reverse hook is not available for this exception in its current state")
	ErrPRHookExceptionClosed   = errors.New("payment reverse hook cannot be executed on a terminal exception")
	ErrPRHookMissingReverseTxn = errors.New("hook requires a linked reverse transaction on the exception")
	ErrPRHookDuplicate         = errors.New("payment reverse hook already succeeded for this exception")
)

// ── PRHook ────────────────────────────────────────────────────────────────────

// PRHook is the computed availability and metadata for one hook on a specific
// payment reverse exception instance.  It is ephemeral (never persisted).
type PRHook struct {
	Type              models.PRHookType
	Label             string
	Description       string
	Available         bool
	UnavailableReason string
	// NavigateURL is non-empty for navigation-only hooks.
	// Empty for execution hooks (which use a POST route).
	NavigateURL string
}

// ── Hook policy ───────────────────────────────────────────────────────────────

// AvailablePaymentReverseHooks computes the set of resolution hooks relevant to
// the given payment reverse exception.
//
// Hook availability is computed by reading current DB state (allocation strategy,
// txn posted state).  If a hook is structurally applicable but currently
// unavailable, it is included with Available=false and a reason.
//
// This function is the single authoritative source of hook policy for the
// payment reverse domain.  UI templates must not implement their own policy logic.
func AvailablePaymentReverseHooks(
	db *gorm.DB,
	companyID uint,
	ex *models.PaymentReverseException,
) []PRHook {
	if ex == nil || companyID == 0 {
		return nil
	}
	if ex.CompanyID != companyID {
		return nil
	}

	hooks := make([]PRHook, 0, 4)
	isTerminal := models.IsTerminalPRExceptionStatus(ex.Status)
	var (
		reverseTxn     *models.PaymentTransaction
		reverseTxnOK   bool
		originalTxn    *models.PaymentTransaction
		originalTxnOK  bool
		sourceLinkOK   bool
		sourceLinkNote string
	)
	if ex.ReverseTxnID != nil {
		reverseTxn, reverseTxnOK = prLoadTransaction(db, companyID, *ex.ReverseTxnID)
	}
	if ex.OriginalTxnID != nil {
		originalTxn, originalTxnOK = prLoadTransaction(db, companyID, *ex.OriginalTxnID)
	}
	if reverseTxnOK && originalTxnOK {
		sourceLinkOK, sourceLinkNote = prReverseTxnLinksToOriginal(db, companyID, *reverseTxn, originalTxn.ID)
	}

	// ── open_reverse_transaction (navigation) ─────────────────────────────────
	if ex.ReverseTxnID != nil {
		h := PRHook{
			Type:        models.PRHookOpenReverseTransaction,
			Label:       "View Reverse Transaction",
			Description: "Navigate to the refund or chargeback transaction that triggered this exception.",
		}
		if isTerminal {
			h.Available = false
			h.UnavailableReason = "Exception is already closed."
		} else if !reverseTxnOK {
			h.Available = false
			h.UnavailableReason = "Linked reverse transaction was not found for this company."
		} else if !isPaymentReverseTxnType(reverseTxn.TransactionType) {
			h.Available = false
			h.UnavailableReason = "Linked reverse transaction is not a refund or chargeback."
		} else {
			h.Available = true
			h.NavigateURL = prTransactionURL(*ex.ReverseTxnID)
		}
		hooks = append(hooks, h)
	}

	// ── open_original_charge (navigation) ────────────────────────────────────
	if ex.OriginalTxnID != nil {
		h := PRHook{
			Type:        models.PRHookOpenOriginalCharge,
			Label:       "View Original Charge",
			Description: "Navigate to the original charge or capture transaction whose allocation is being reversed.",
		}
		if isTerminal {
			h.Available = false
			h.UnavailableReason = "Exception is already closed."
		} else if !originalTxnOK {
			h.Available = false
			h.UnavailableReason = "Linked original charge was not found for this company."
		} else if !isChargeOrCaptureTxnType(originalTxn.TransactionType) {
			h.Available = false
			h.UnavailableReason = "Linked original transaction is not a charge or capture."
		} else {
			h.Available = true
			h.NavigateURL = prTransactionURL(*ex.OriginalTxnID)
		}
		hooks = append(hooks, h)
	}

	// ── open_forward_allocations (navigation) ─────────────────────────────────
	if ex.OriginalTxnID != nil {
		h := PRHook{
			Type:        models.PRHookOpenForwardAllocations,
			Label:       "View Forward Allocations",
			Description: "Navigate to the transactions page to inspect how the original charge was allocated across invoices.",
		}
		switch {
		case isTerminal:
			h.Available = false
			h.UnavailableReason = "Exception is already closed."
		case !originalTxnOK:
			h.Available = false
			h.UnavailableReason = "Linked original charge was not found for this company."
		case !isChargeOrCaptureTxnType(originalTxn.TransactionType):
			h.Available = false
			h.UnavailableReason = "Linked original transaction is not a charge or capture."
		case !prOriginalChargeHasMultiAlloc(db, companyID, *ex.OriginalTxnID):
			h.Available = false
			h.UnavailableReason = "Original charge did not use multi-invoice allocation."
		default:
			h.Available = true
			h.NavigateURL = prTransactionURL(*ex.OriginalTxnID)
		}
		hooks = append(hooks, h)
	}

	// ── retry_safe_reverse_check (execution) ─────────────────────────────────
	if ex.ReverseTxnID != nil && ex.OriginalTxnID != nil {
		h := PRHook{
			Type:  models.PRHookRetryCheck,
			Label: "Run Reverse Eligibility Check",
			Description: "Re-validate whether the reverse transaction is still eligible for " +
				"multi-invoice reverse allocation. This check is read-only — it does not apply the reversal.",
		}
		switch {
		case isTerminal:
			h.Available = false
			h.UnavailableReason = "Exception is already closed."
		case prRetryCheckAlreadySucceeded(db, companyID, ex.ID):
			h.Available = false
			h.UnavailableReason = "Eligibility check already passed for this exception; duplicate execution is not needed."
		case !reverseTxnOK:
			h.Available = false
			h.UnavailableReason = "Linked reverse transaction was not found for this company."
		case !originalTxnOK:
			h.Available = false
			h.UnavailableReason = "Linked original charge was not found for this company."
		case !isPaymentReverseTxnType(reverseTxn.TransactionType):
			h.Available = false
			h.UnavailableReason = "Linked reverse transaction is not a refund or chargeback."
		case !isChargeOrCaptureTxnType(originalTxn.TransactionType):
			h.Available = false
			h.UnavailableReason = "Linked original transaction is not a charge or capture."
		case !sourceLinkOK:
			h.Available = false
			if sourceLinkNote != "" {
				h.UnavailableReason = sourceLinkNote
			} else {
				h.UnavailableReason = "Reverse transaction does not currently resolve to the linked original charge."
			}
		case !prOriginalChargeHasMultiAlloc(db, companyID, *ex.OriginalTxnID):
			h.Available = false
			h.UnavailableReason = "Original charge did not use multi-invoice allocation; eligibility check is not applicable."
		case !prReverseTxnIsPosted(db, companyID, *ex.ReverseTxnID):
			h.Available = false
			h.UnavailableReason = "Reverse transaction has not been posted to the ledger yet."
		default:
			h.Available = true
		}
		hooks = append(hooks, h)
	}

	return hooks
}

// ── Hook execution ────────────────────────────────────────────────────────────

// ExecutePaymentReverseHook executes an execution hook for the given exception.
//
// Navigation hooks (IsPRExecutionHook = false) return ErrPRHookTypeUnsupported.
// All eligibility checks are re-validated inside the service before execution.
//
// An attempt row is always recorded regardless of outcome:
//   - Rejection (eligibility or domain failure): attempt status = rejected
//   - Success: attempt status = succeeded; exception may auto-transition to reviewed
func ExecutePaymentReverseHook(
	db *gorm.DB,
	companyID, exceptionID uint,
	hookType models.PRHookType,
	actor string,
) error {
	if !models.IsPRExecutionHook(hookType) {
		return ErrPRHookTypeUnsupported
	}

	actor = normalizeActor(actor)

	switch hookType {
	case models.PRHookRetryCheck:
		return executePRRetryCheckHook(db, companyID, exceptionID, actor)
	default:
		return ErrPRHookTypeUnsupported
	}
}

// ListRecentPRAttempts returns the most recent attempts for an exception,
// newest first.  At most `limit` rows are returned.
func ListRecentPRAttempts(
	db *gorm.DB,
	companyID, exceptionID uint,
	limit int,
) ([]models.PaymentReverseResolutionAttempt, error) {
	var attempts []models.PaymentReverseResolutionAttempt
	err := db.
		Where("company_id = ? AND payment_reverse_exception_id = ?", companyID, exceptionID).
		Order("created_at DESC, id DESC").
		Limit(limit).
		Find(&attempts).Error
	return attempts, err
}

// ── Internal: retry_safe_reverse_check execution ─────────────────────────────

// executePRRetryCheckHook runs the retry_safe_reverse_check hook inside a
// single atomic transaction with SELECT FOR UPDATE on the exception row.
//
// Concurrency contract:
//   - Concurrent calls serialize on the row lock (PostgreSQL SELECT FOR UPDATE).
//   - Every rejection path commits its attempt row within the same transaction.
//   - Business-rejection errors are signalled via outerErr (set inside the tx
//     func which returns nil to commit, then returned after tx commits).
//   - At most one "succeeded" attempt can be committed per eligibility window;
//     a second concurrent call that passes eligibility will see the updated
//     status after the first commits and record a rejected attempt instead.
//
// The status transition (open → reviewed) is inlined inside the same
// transaction as the attempt row to keep the two writes atomic.
func executePRRetryCheckHook(
	db *gorm.DB,
	companyID uint,
	exceptionID uint,
	actor string,
) error {
	// outerErr carries a business-rejection error back to the caller.
	// It is set inside the transaction func (which returns nil to commit the
	// attempt), then returned after the transaction commits successfully.
	var outerErr error

	txErr := db.Transaction(func(tx *gorm.DB) error {
		// 1. Lock the exception row — serialises concurrent hook POST calls.
		var ex models.PaymentReverseException
		if err := applyLockForUpdate(
			tx.Where("id = ? AND company_id = ?", exceptionID, companyID),
		).First(&ex).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPRExceptionNotFound
			}
			return err
		}

		if prRetryCheckAlreadySucceeded(tx, companyID, exceptionID) {
			reason := "retry_safe_reverse_check already succeeded for this exception; duplicate execution rejected"
			if err := tx.Create(&models.PaymentReverseResolutionAttempt{
				CompanyID:                 companyID,
				PaymentReverseExceptionID: exceptionID,
				HookType:                  models.PRHookRetryCheck,
				Status:                    models.PRAttemptRejected,
				Summary:                   "Duplicate eligibility check rejected",
				Detail:                    reason,
				Actor:                     actor,
				CreatedAt:                 time.Now().UTC(),
			}).Error; err != nil {
				return fmt.Errorf("record %s attempt: %w", models.PRAttemptRejected, err)
			}
			outerErr = fmt.Errorf("%w: %s", ErrPRHookDuplicate, reason)
			return nil
		}

		// 2. Re-validate eligibility inside the lock using the authoritative
		//    hook policy.  AvailablePaymentReverseHooks uses tx for all reads,
		//    so it sees the locked state of the exception.
		hooks := AvailablePaymentReverseHooks(tx, companyID, &ex)
		eligible := false
		for _, h := range hooks {
			if h.Type == models.PRHookRetryCheck && h.Available {
				eligible = true
				break
			}
		}
		if !eligible {
			var reason string
			for _, h := range hooks {
				if h.Type == models.PRHookRetryCheck {
					reason = h.UnavailableReason
					break
				}
			}
			if reason == "" {
				reason = "eligibility check not available for this exception"
			}
			if err := tx.Create(&models.PaymentReverseResolutionAttempt{
				CompanyID:                 companyID,
				PaymentReverseExceptionID: exceptionID,
				HookType:                  models.PRHookRetryCheck,
				Status:                    models.PRAttemptRejected,
				Summary:                   "Eligibility check rejected: preconditions not met",
				Detail:                    reason,
				Actor:                     actor,
				CreatedAt:                 time.Now().UTC(),
			}).Error; err != nil {
				return fmt.Errorf("record %s attempt: %w", models.PRAttemptRejected, err)
			}
			// Commit the rejection attempt; propagate the error to the caller
			// after the transaction completes.
			outerErr = fmt.Errorf("%w: %s", ErrPRHookNotAvailable, reason)
			return nil
		}

		// 3. Load the reverse txn type inside the tx (consistent with lock).
		var revTxn models.PaymentTransaction
		if err := tx.Where("id = ? AND company_id = ?", *ex.ReverseTxnID, companyID).
			First(&revTxn).Error; err != nil {
			if err2 := tx.Create(&models.PaymentReverseResolutionAttempt{
				CompanyID:                 companyID,
				PaymentReverseExceptionID: exceptionID,
				HookType:                  models.PRHookRetryCheck,
				Status:                    models.PRAttemptRejected,
				Summary:                   "Eligibility check rejected: reverse transaction not found",
				Detail:                    err.Error(),
				Actor:                     actor,
				CreatedAt:                 time.Now().UTC(),
			}).Error; err2 != nil {
				return fmt.Errorf("record %s attempt: %w", models.PRAttemptRejected, err2)
			}
			outerErr = fmt.Errorf("%w: load reverse transaction: %w", ErrPRHookMissingReverseTxn, err)
			return nil
		}

		// 4. Call the read-only reverse-allocation eligibility validator.
		//    Validator reads use tx — consistent snapshot within the transaction.
		var validationErr error
		switch revTxn.TransactionType {
		case models.TxnTypeRefund:
			validationErr = ValidateRefundReverseAllocatable(tx, companyID, revTxn.ID)
		case models.TxnTypeChargeback:
			validationErr = ValidateChargebackReverseAllocatable(tx, companyID, revTxn.ID)
		default:
			validationErr = fmt.Errorf("reverse transaction type %s is not refund or chargeback", revTxn.TransactionType)
		}

		if validationErr != nil {
			if err := tx.Create(&models.PaymentReverseResolutionAttempt{
				CompanyID:                 companyID,
				PaymentReverseExceptionID: exceptionID,
				HookType:                  models.PRHookRetryCheck,
				Status:                    models.PRAttemptRejected,
				Summary:                   "Eligibility check: reverse allocation is not yet eligible",
				Detail:                    validationErr.Error(),
				Actor:                     actor,
				CreatedAt:                 time.Now().UTC(),
			}).Error; err != nil {
				return fmt.Errorf("record %s attempt: %w", models.PRAttemptRejected, err)
			}
			// outerErr stays nil — validator rejection is a normal domain outcome,
			// not a caller error.
			return nil
		}

		// 5. Validation passed — record success attempt.
		if err := tx.Create(&models.PaymentReverseResolutionAttempt{
			CompanyID:                 companyID,
			PaymentReverseExceptionID: exceptionID,
			HookType:                  models.PRHookRetryCheck,
			Status:                    models.PRAttemptSucceeded,
			Summary:                   "Eligibility check passed: reverse allocation is eligible",
			Detail:                    fmt.Sprintf("reverse_txn_id=%d type=%s", revTxn.ID, revTxn.TransactionType),
			Actor:                     actor,
			CreatedAt:                 time.Now().UTC(),
		}).Error; err != nil {
			return fmt.Errorf("record succeeded attempt: %w", err)
		}

		// 6. Idempotent: transition to reviewed only if currently open.
		//    Inlined directly (no nested transaction) because we already hold
		//    the row lock and have verified the current status above.
		if ex.Status == models.PRExceptionStatusOpen {
			if err := tx.Model(&ex).Updates(map[string]any{
				"status":     models.PRExceptionStatusReviewed,
				"updated_at": time.Now().UTC(),
			}).Error; err != nil {
				return fmt.Errorf("transition exception to reviewed: %w", err)
			}
		}
		return nil
	})

	if txErr != nil {
		return txErr
	}
	return outerErr
}

// prOriginalChargeHasMultiAlloc returns true when the original charge txn
// has at least one PaymentAllocation record (multi-invoice path).
func prOriginalChargeHasMultiAlloc(db *gorm.DB, companyID, origTxnID uint) bool {
	var count int64
	db.Model(&models.PaymentAllocation{}).
		Where("company_id = ? AND payment_transaction_id = ?", companyID, origTxnID).
		Count(&count)
	return count > 0
}

func prLoadTransaction(db *gorm.DB, companyID, txnID uint) (*models.PaymentTransaction, bool) {
	var txn models.PaymentTransaction
	if err := db.Where("id = ? AND company_id = ?", txnID, companyID).First(&txn).Error; err != nil {
		return nil, false
	}
	return &txn, true
}

func prReverseTxnLinksToOriginal(db *gorm.DB, companyID uint, revTxn models.PaymentTransaction, originalTxnID uint) (bool, string) {
	orig, err := resolveOriginalChargeForReverseAllocation(db, companyID, revTxn)
	if err != nil {
		return false, "Reverse transaction does not currently resolve to the linked original charge: " + err.Error()
	}
	if orig == nil {
		return false, "Reverse transaction does not currently resolve to the linked original charge."
	}
	if orig.ID != originalTxnID {
		return false, fmt.Sprintf("Reverse transaction resolves to original charge #%d, not #%d.", orig.ID, originalTxnID)
	}
	return true, ""
}

// prReverseTxnIsPosted returns true when the reverse transaction has been
// posted to the ledger (PostedJournalEntryID is not nil).
func prReverseTxnIsPosted(db *gorm.DB, companyID, reverseTxnID uint) bool {
	var txn models.PaymentTransaction
	if err := db.Select("posted_journal_entry_id").
		Where("id = ? AND company_id = ?", reverseTxnID, companyID).
		First(&txn).Error; err != nil {
		return false
	}
	return txn.PostedJournalEntryID != nil
}

func prRetryCheckAlreadySucceeded(db *gorm.DB, companyID, exceptionID uint) bool {
	if companyID == 0 || exceptionID == 0 {
		return false
	}
	var count int64
	db.Model(&models.PaymentReverseResolutionAttempt{}).
		Where("company_id = ? AND payment_reverse_exception_id = ? AND hook_type = ? AND status = ?",
			companyID, exceptionID, models.PRHookRetryCheck, models.PRAttemptSucceeded).
		Count(&count)
	return count > 0
}

// prTransactionURL builds the navigation URL to a specific transaction row
// on the transactions page (anchor #txn-{id}).
func prTransactionURL(txnID uint) string {
	return fmt.Sprintf("/settings/payment-gateways/transactions#txn-%d", txnID)
}
