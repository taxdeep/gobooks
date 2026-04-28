// 遵循project_guide.md
package services

// payment_reverse_exception_service.go — Batch 23: Payment-side reverse exception truth.
//
// Provides:
//   - CreatePaymentReverseException:     record a named anomaly (with dedup)
//   - GetPaymentReverseException:        load one exception by ID
//   - ListPaymentReverseExceptions:      list exceptions for a company
//   - ReviewPaymentReverseException:     open → reviewed
//   - DismissPaymentReverseException:    open/reviewed → dismissed (terminal)
//   - ResolvePaymentReverseException:    open/reviewed → resolved (terminal)
//   - FindActiveReverseExceptionForTxn:  check if a txn has an open/reviewed exception
//   - PaymentReverseExceptionTypeForReverseAllocError: map service error → exception type
//
// ─── Design ───────────────────────────────────────────────────────────────────
//
// Payment reverse exceptions are a named, auditable truth layer — they record
// "why this reverse allocation attempt could not succeed automatically."  They
// do NOT:
//   - create or modify any Journal Entry
//   - change the application state of any transaction or invoice
//   - serve as an override or force-apply path
//
// This service is intentionally separate from reconciliation_exception_service.go.
// Reconciliation exceptions relate to payout ↔ bank-entry matching anomalies.
// Payment reverse exceptions relate to payment-side reversal structural failures.
//
// ─── Dedup ────────────────────────────────────────────────────────────────────
//
// Before creating an exception, the service checks for an existing open or
// reviewed exception with the same (company_id, type, reverse_txn_id,
// original_txn_id).  If found, the existing record is returned and wasCreated
// is false.  This prevents exception storms when the operator repeatedly
// attempts the same failed reversal.
//
// ─── Auto-creation triggers ───────────────────────────────────────────────────
//
// Exceptions are created at the handler layer (not inside the reverse service)
// when ApplyRefundReverseAllocations / ApplyChargebackReverseAllocations
// returns a structural error.  This keeps the reverse allocation service
// focused on application truth.
//
//   ErrReverseAllocNoOriginalTxn            → PRExceptionReverseAllocationAmbiguous
//   ErrReverseAllocExceedsReversibleTotal   → PRExceptionAmountExceedsStrategy
//   ErrReverseAllocWouldExceedInvoiceTotal  → PRExceptionOverCreditBoundary
//   ErrReverseAllocInvoiceNotRestoreable    → PRExceptionRequiresManualSplit
//
// Ordinary input errors (not-found, wrong type, not posted) do NOT create
// exceptions because they indicate operator data-entry mistakes, not
// structural anomalies.
//
// ─── Status machine ───────────────────────────────────────────────────────────
//
//   open     → reviewed, dismissed, resolved
//   reviewed → dismissed, resolved
//   dismissed → terminal (no further transitions)
//   resolved  → terminal (no further transitions)

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── Sentinel errors ───────────────────────────────────────────────────────────

var (
	ErrPRExceptionNotFound          = errors.New("payment reverse exception not found")
	ErrPRExceptionTypeInvalid       = errors.New("unsupported payment reverse exception type")
	ErrPRExceptionTransitionInvalid = errors.New("invalid payment reverse exception status transition")
	ErrPRExceptionAlreadyClosed     = errors.New("payment reverse exception is already in a terminal state")
	ErrPRExceptionSourceRequired    = errors.New("at least one transaction reference is required")
	ErrPRExceptionSourceInvalid     = errors.New("payment reverse exception source transaction is invalid")
	ErrPRExceptionSourceMismatch    = errors.New("payment reverse exception source linkage is inconsistent")
	ErrPRExceptionDismissNote       = errors.New("dismissal note is required")
	ErrPRExceptionResolveNote       = errors.New("resolution note is required")
)

// ── Input types ───────────────────────────────────────────────────────────────

// CreatePaymentReverseExceptionInput carries all fields needed to record an exception.
type CreatePaymentReverseExceptionInput struct {
	CompanyID uint

	// ExceptionType must be one of the supported types.
	ExceptionType models.PaymentReverseExceptionType

	// ReverseTxnID anchors the exception to the failed reversal attempt and is required.
	ReverseTxnID *uint

	// OriginalTxnID is optional on input; when omitted, the service derives it
	// from ReverseTxnID when a charge/capture can be resolved safely.
	OriginalTxnID *uint

	// Summary is a one-line human-readable description.
	Summary string

	// Detail is optional additional context (amounts, IDs, reasons).
	Detail string

	// CreatedByActor is the user email or "system".
	CreatedByActor string
}

// ── Core service ──────────────────────────────────────────────────────────────

// CreatePaymentReverseException records a new exception.
//
// Returns (exception, wasCreated, error).
// wasCreated=false when a matching open/reviewed exception already exists
// (dedup path) — the existing record is returned instead of creating a duplicate.
func CreatePaymentReverseException(
	db *gorm.DB,
	input CreatePaymentReverseExceptionInput,
) (*models.PaymentReverseException, bool, error) {
	if !isSupportedPRExceptionType(input.ExceptionType) {
		return nil, false, ErrPRExceptionTypeInvalid
	}
	if input.CompanyID == 0 {
		return nil, false, fmt.Errorf("company_id is required")
	}
	if input.ReverseTxnID == nil {
		return nil, false, ErrPRExceptionSourceRequired
	}

	actor := normalizeActor(input.CreatedByActor)

	var (
		ex         *models.PaymentReverseException
		wasCreated bool
	)
	if err := db.Transaction(func(tx *gorm.DB) error {
		reverseTxnID, originalTxnID, err := normalizePRExceptionSources(
			tx, input.CompanyID, input.ReverseTxnID, input.OriginalTxnID,
		)
		if err != nil {
			return err
		}
		dedupKey := buildPRExceptionDedupKey(input.ExceptionType, reverseTxnID, originalTxnID)

		existing, err := findActivePRException(tx, input.CompanyID, dedupKey)
		if err != nil {
			return fmt.Errorf("dedup check: %w", err)
		}
		if existing != nil {
			ex = existing
			wasCreated = false
			return nil
		}

		ex = &models.PaymentReverseException{
			CompanyID:      input.CompanyID,
			ExceptionType:  input.ExceptionType,
			Status:         models.PRExceptionStatusOpen,
			ReverseTxnID:   reverseTxnID,
			OriginalTxnID:  originalTxnID,
			DedupKey:       dedupKey,
			Summary:        input.Summary,
			Detail:         input.Detail,
			CreatedByActor: actor,
		}
		if err := tx.Create(ex).Error; err != nil {
			if isUniqueConstraintError(err) {
				existing, lookupErr := findActivePRException(tx, input.CompanyID, dedupKey)
				if lookupErr != nil {
					return fmt.Errorf("load existing payment reverse exception after unique conflict: %w", lookupErr)
				}
				if existing != nil {
					ex = existing
					wasCreated = false
					return nil
				}
			}
			return fmt.Errorf("create payment reverse exception: %w", err)
		}
		wasCreated = true
		return nil
	}); err != nil {
		return nil, false, err
	}

	if wasCreated {
		slog.Info("payment reverse exception created",
			"exception_id", ex.ID,
			"type", ex.ExceptionType,
			"company_id", ex.CompanyID,
			"reverse_txn_id", ex.ReverseTxnID,
			"original_txn_id", ex.OriginalTxnID,
		)
	}
	return ex, wasCreated, nil
}

// GetPaymentReverseException loads an exception by ID scoped to a company.
func GetPaymentReverseException(db *gorm.DB, companyID, exceptionID uint) (*models.PaymentReverseException, error) {
	var ex models.PaymentReverseException
	if err := db.Where("id = ? AND company_id = ?", exceptionID, companyID).First(&ex).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPRExceptionNotFound
		}
		return nil, err
	}
	return &ex, nil
}

// ListPaymentReverseExceptions returns all exceptions for a company, newest first.
// If statusFilter is provided, only exceptions with those statuses are returned.
func ListPaymentReverseExceptions(
	db *gorm.DB,
	companyID uint,
	statusFilter ...models.PaymentReverseExceptionStatus,
) ([]models.PaymentReverseException, error) {
	q := db.Where("company_id = ?", companyID)
	if len(statusFilter) > 0 {
		q = q.Where("status IN ?", statusFilter)
	}
	var exceptions []models.PaymentReverseException
	err := q.Order("created_at DESC, id DESC").Find(&exceptions).Error
	return exceptions, err
}

// FindActiveReverseExceptionForTxn returns the first open or reviewed exception
// for a given reverse transaction.  Returns nil when no active exception exists.
func FindActiveReverseExceptionForTxn(
	db *gorm.DB,
	companyID, reverseTxnID uint,
) (*models.PaymentReverseException, error) {
	var ex models.PaymentReverseException
	err := db.Where(
		"company_id = ? AND reverse_txn_id = ? AND status IN ?",
		companyID, reverseTxnID,
		[]string{
			string(models.PRExceptionStatusOpen),
			string(models.PRExceptionStatusReviewed),
		},
	).Order("created_at DESC").First(&ex).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ex, nil
}

// ── Status transitions ────────────────────────────────────────────────────────

// ReviewPaymentReverseException transitions an exception from open → reviewed.
// If the exception is already reviewed, the call is a no-op (idempotent).
func ReviewPaymentReverseException(db *gorm.DB, companyID, exceptionID uint, actor string) error {
	return updatePRExceptionStatus(db, companyID, exceptionID, models.PRExceptionStatusReviewed, normalizeActor(actor), "")
}

// DismissPaymentReverseException transitions an exception to dismissed (terminal).
// A dismissal note is required.
func DismissPaymentReverseException(db *gorm.DB, companyID, exceptionID uint, actor, note string) error {
	return updatePRExceptionStatus(db, companyID, exceptionID, models.PRExceptionStatusDismissed, normalizeActor(actor), note)
}

// ResolvePaymentReverseException transitions an exception to resolved (terminal).
func ResolvePaymentReverseException(db *gorm.DB, companyID, exceptionID uint, actor, note string) error {
	return updatePRExceptionStatus(db, companyID, exceptionID, models.PRExceptionStatusResolved, normalizeActor(actor), note)
}

// ── Error → exception type mapping ───────────────────────────────────────────

// PaymentReverseExceptionTypeForReverseAllocError maps a reverse allocation
// service error to the appropriate PaymentReverseExceptionType.
//
// Returns (type, true) for structural errors that should be recorded as exceptions.
// Returns ("", false) for ordinary input / idempotency errors that should NOT
// create exceptions (e.g. wrong transaction type, not yet posted, already applied).
func PaymentReverseExceptionTypeForReverseAllocError(err error) (models.PaymentReverseExceptionType, bool) {
	switch {
	case errors.Is(err, ErrReverseAllocNoOriginalTxn):
		return models.PRExceptionReverseAllocationAmbiguous, true
	case errors.Is(err, ErrReverseAllocUnsupportedMultiLayerReversal):
		return models.PRExceptionUnsupportedMultiLayerReversal, true
	case errors.Is(err, ErrReverseAllocExceedsReversibleTotal):
		return models.PRExceptionAmountExceedsStrategy, true
	case errors.Is(err, ErrReverseAllocWouldExceedInvoiceTotal):
		return models.PRExceptionOverCreditBoundary, true
	case errors.Is(err, ErrReverseAllocInvoiceNotRestoreable):
		return models.PRExceptionRequiresManualSplit, true
	default:
		// ErrReverseAllocAlreadyApplied, ErrReverseAllocTxnNotReversible,
		// ErrReverseAllocTxnNotPosted, ErrReverseAllocNoAllocations —
		// these are ordinary input/idempotency errors, not structural anomalies.
		return "", false
	}
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func updatePRExceptionStatus(
	db *gorm.DB,
	companyID, exceptionID uint,
	newStatus models.PaymentReverseExceptionStatus,
	actor, note string,
) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var ex models.PaymentReverseException
		if err := applyLockForUpdate(
			tx.Where("id = ? AND company_id = ?", exceptionID, companyID),
		).First(&ex).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPRExceptionNotFound
			}
			return err
		}

		if models.IsTerminalPRExceptionStatus(ex.Status) {
			return fmt.Errorf("%w: current status is %s", ErrPRExceptionAlreadyClosed, ex.Status)
		}
		if ex.Status == newStatus {
			return nil // idempotent no-op (reviewed → reviewed)
		}
		if newStatus == models.PRExceptionStatusDismissed && strings.TrimSpace(note) == "" {
			return ErrPRExceptionDismissNote
		}
		if newStatus == models.PRExceptionStatusResolved && strings.TrimSpace(note) == "" {
			return ErrPRExceptionResolveNote
		}
		if err := validatePRExceptionTransition(ex.Status, newStatus); err != nil {
			return err
		}

		updates := map[string]any{
			"status":            newStatus,
			"resolved_by_actor": actor,
			"resolution_note":   note,
			"updated_at":        time.Now(),
		}
		if models.IsTerminalPRExceptionStatus(newStatus) {
			now := time.Now()
			updates["resolved_at"] = &now
		}

		if err := tx.Model(&ex).Updates(updates).Error; err != nil {
			return fmt.Errorf("update payment reverse exception status: %w", err)
		}

		slog.Info("payment reverse exception status updated",
			"exception_id", ex.ID,
			"old_status", ex.Status,
			"new_status", newStatus,
			"actor", actor,
		)
		return nil
	})
}

func validatePRExceptionTransition(
	current models.PaymentReverseExceptionStatus,
	next models.PaymentReverseExceptionStatus,
) error {
	switch current {
	case models.PRExceptionStatusOpen:
		switch next {
		case models.PRExceptionStatusReviewed,
			models.PRExceptionStatusDismissed,
			models.PRExceptionStatusResolved:
			return nil
		}
	case models.PRExceptionStatusReviewed:
		switch next {
		case models.PRExceptionStatusDismissed,
			models.PRExceptionStatusResolved:
			return nil
		}
	}
	return fmt.Errorf("%w: %s → %s", ErrPRExceptionTransitionInvalid, current, next)
}

func findActivePRException(
	db *gorm.DB,
	companyID uint,
	dedupKey string,
) (*models.PaymentReverseException, error) {
	q := db.Where("company_id = ? AND dedup_key = ? AND status IN ?",
		companyID, dedupKey,
		[]string{
			string(models.PRExceptionStatusOpen),
			string(models.PRExceptionStatusReviewed),
		})

	var ex models.PaymentReverseException
	err := q.First(&ex).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ex, nil
}

func isSupportedPRExceptionType(t models.PaymentReverseExceptionType) bool {
	for _, supported := range models.AllPaymentReverseExceptionTypes() {
		if t == supported {
			return true
		}
	}
	return false
}

func buildPRExceptionDedupKey(
	exType models.PaymentReverseExceptionType,
	reverseTxnID, originalTxnID *uint,
) string {
	return fmt.Sprintf("%s:%d:%d",
		exType,
		normalizeOptionalUint(reverseTxnID),
		normalizeOptionalUint(originalTxnID),
	)
}

func normalizePRExceptionSources(
	tx *gorm.DB,
	companyID uint,
	reverseTxnID, originalTxnID *uint,
) (*uint, *uint, error) {
	var normalizedReverseTxnID *uint
	var normalizedOriginalTxnID *uint
	var reverseTxn *models.PaymentTransaction

	if reverseTxnID != nil {
		var txn models.PaymentTransaction
		if err := tx.Where("id = ? AND company_id = ?", *reverseTxnID, companyID).First(&txn).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil, fmt.Errorf("%w: reverse transaction not found", ErrPRExceptionSourceInvalid)
			}
			return nil, nil, fmt.Errorf("load reverse transaction: %w", err)
		}
		if !isPaymentReverseTxnType(txn.TransactionType) {
			return nil, nil, fmt.Errorf("%w: reverse transaction must be refund/chargeback, got %s", ErrPRExceptionSourceInvalid, txn.TransactionType)
		}
		id := txn.ID
		normalizedReverseTxnID = &id
		reverseTxn = &txn
	}

	if originalTxnID != nil {
		var txn models.PaymentTransaction
		if err := tx.Where("id = ? AND company_id = ?", *originalTxnID, companyID).First(&txn).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil, fmt.Errorf("%w: original transaction not found", ErrPRExceptionSourceInvalid)
			}
			return nil, nil, fmt.Errorf("load original transaction: %w", err)
		}
		if !isChargeOrCaptureTxnType(txn.TransactionType) {
			return nil, nil, fmt.Errorf("%w: original transaction must be charge/capture, got %s", ErrPRExceptionSourceInvalid, txn.TransactionType)
		}
		id := txn.ID
		normalizedOriginalTxnID = &id
	}

	if reverseTxn != nil {
		derivedOriginalTxnID, err := derivePRExceptionOriginalTxnID(tx, companyID, *reverseTxn)
		if err != nil {
			return nil, nil, err
		}
		if derivedOriginalTxnID != nil {
			if normalizedOriginalTxnID != nil && *normalizedOriginalTxnID != *derivedOriginalTxnID {
				return nil, nil, fmt.Errorf(
					"%w: reverse transaction %d resolves to original %d, not %d",
					ErrPRExceptionSourceMismatch,
					reverseTxn.ID,
					*derivedOriginalTxnID,
					*normalizedOriginalTxnID,
				)
			}
			normalizedOriginalTxnID = derivedOriginalTxnID
		}
	}

	if normalizedReverseTxnID == nil {
		return nil, nil, ErrPRExceptionSourceRequired
	}
	return normalizedReverseTxnID, normalizedOriginalTxnID, nil
}

func derivePRExceptionOriginalTxnID(
	tx *gorm.DB,
	companyID uint,
	reverseTxn models.PaymentTransaction,
) (*uint, error) {
	if reverseTxn.OriginalTransactionID != nil {
		var directOrig models.PaymentTransaction
		if err := tx.Where("id = ? AND company_id = ?", *reverseTxn.OriginalTransactionID, companyID).
			First(&directOrig).Error; err == nil {
			if isChargeOrCaptureTxnType(directOrig.TransactionType) {
				id := directOrig.ID
				return &id, nil
			}
			if isPaymentReverseTxnType(directOrig.TransactionType) && directOrig.OriginalTransactionID != nil {
				var upstreamOrig models.PaymentTransaction
				if err := tx.Where("id = ? AND company_id = ?", *directOrig.OriginalTransactionID, companyID).
					First(&upstreamOrig).Error; err == nil && isChargeOrCaptureTxnType(upstreamOrig.TransactionType) {
					id := upstreamOrig.ID
					return &id, nil
				} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return nil, fmt.Errorf("load upstream original transaction: %w", err)
				}
			}
		} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("load direct original transaction: %w", err)
		}
	}

	if orig := resolveOriginalCharge(tx, companyID, reverseTxn); orig != nil {
		id := orig.ID
		return &id, nil
	}
	return nil, nil
}

func isChargeOrCaptureTxnType(txnType models.PaymentTransactionType) bool {
	return txnType == models.TxnTypeCharge || txnType == models.TxnTypeCapture
}

func isPaymentReverseTxnType(txnType models.PaymentTransactionType) bool {
	return txnType == models.TxnTypeRefund || txnType == models.TxnTypeChargeback
}
