// 遵循project_guide.md
package services

// reconciliation_exception_service.go — Batch 20: Reconciliation exception truth.
//
// Provides:
//   - CreateReconciliationException:     record a named anomaly (with dedup)
//   - GetReconciliationException:        load one exception by ID
//   - ListReconciliationExceptions:      list exceptions for a company
//   - ReviewReconciliationException:     open → reviewed
//   - DismissReconciliationException:    open/reviewed → dismissed (terminal)
//   - ResolveReconciliationException:    open/reviewed → resolved (terminal)
//
// ─── Design ───────────────────────────────────────────────────────────────────
//
// Exceptions are a named, auditable truth layer — they record "why this
// reconciliation attempt could not succeed automatically."  They do NOT:
//   - create or modify any Journal Entry
//   - change the matched/unmatched state of payouts or bank entries
//   - serve as an override or force-match path
//
// ─── Dedup ────────────────────────────────────────────────────────────────────
//
// Before creating an exception, the service checks for an existing open or
// reviewed exception with the same (company_id, type, gateway_payout_id,
// bank_entry_id).  If found, the existing record is returned and wasCreated is
// false.  This prevents exception storms when operators repeatedly attempt the
// same failed match.
//
// ─── Auto-creation triggers ───────────────────────────────────────────────────
//
// Exceptions are created at the handler layer (not inside the matching service)
// when MatchGatewayPayoutToBankEntry returns a structural error.  This keeps
// the reconciliation service focused on matching truth.
//
//   ErrReconAmountMismatch          → ExceptionAmountMismatch
//   ErrReconAccountMismatch         → ExceptionAccountMismatch
//   ErrReconCurrencyMismatch        → ExceptionCurrencyMismatch
//   ErrReconPayoutAlreadyMatched    → ExceptionPayoutConflict
//   ErrReconBankEntryAlreadyMatched → ExceptionBankEntryConflict
//
// Ordinary input errors (not-found, invalid amounts) do NOT create exceptions.
//
// ─── Status machine ───────────────────────────────────────────────────────────
//
//   open → reviewed, dismissed, resolved
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
	ErrExceptionNotFound              = errors.New("reconciliation exception not found")
	ErrExceptionTypeInvalid           = errors.New("unsupported exception type")
	ErrExceptionTransitionInvalid     = errors.New("invalid exception status transition")
	ErrExceptionAlreadyClosed         = errors.New("exception is already in a terminal state")
	ErrExceptionSourceRequired        = errors.New("at least one source reference is required")
	ErrExceptionPayoutNotFound        = errors.New("gateway payout not found or does not belong to this company")
	ErrExceptionBankEntryNotFound     = errors.New("bank entry not found or does not belong to this company")
	ErrExceptionReconciliationMissing = errors.New("payout reconciliation not found or does not belong to this company")
	ErrExceptionSourceMismatch        = errors.New("exception source references do not belong to the same reconciliation context")
	ErrExceptionDismissNoteRequired   = errors.New("dismissal note is required")
)

// ── Input types ───────────────────────────────────────────────────────────────

// CreateReconciliationExceptionInput carries all fields needed to record an exception.
type CreateReconciliationExceptionInput struct {
	CompanyID uint

	// ExceptionType must be one of the supported types.
	ExceptionType models.ReconciliationExceptionType

	// Optional references. At least one source reference must be set.
	GatewayPayoutID        *uint
	BankEntryID            *uint
	PayoutReconciliationID *uint

	// Summary is a one-line human-readable description.
	Summary string

	// Detail is optional additional context (amounts, IDs, reasons, JSON).
	Detail string

	// CreatedByActor is the user email or "system".
	CreatedByActor string
}

// ── Core service ──────────────────────────────────────────────────────────────

// CreateReconciliationException records a new exception.
//
// Returns (exception, wasCreated, error).
// wasCreated=false when a matching open/reviewed exception already exists
// (dedup path) — the existing record is returned instead of creating a duplicate.
//
// No JE is created.  The exception does not affect matched/unmatched state.
func CreateReconciliationException(
	db *gorm.DB,
	input CreateReconciliationExceptionInput,
) (*models.ReconciliationException, bool, error) {
	if !isSupportedExceptionType(input.ExceptionType) {
		return nil, false, ErrExceptionTypeInvalid
	}
	if input.CompanyID == 0 {
		return nil, false, fmt.Errorf("company_id is required")
	}
	if input.GatewayPayoutID == nil && input.BankEntryID == nil && input.PayoutReconciliationID == nil {
		return nil, false, ErrExceptionSourceRequired
	}

	actor := normalizeActor(input.CreatedByActor)
	dedupKey := buildExceptionDedupKey(
		input.ExceptionType,
		input.GatewayPayoutID,
		input.BankEntryID,
		input.PayoutReconciliationID,
	)

	var (
		ex         *models.ReconciliationException
		wasCreated bool
	)
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := validateAndLockExceptionSources(tx, input.CompanyID, input); err != nil {
			return err
		}

		existing, err := findActiveException(tx, input.CompanyID, dedupKey)
		if err != nil {
			return fmt.Errorf("dedup check: %w", err)
		}
		if existing != nil {
			ex = existing
			wasCreated = false
			return nil
		}

		ex = &models.ReconciliationException{
			CompanyID:              input.CompanyID,
			ExceptionType:          input.ExceptionType,
			Status:                 models.ExceptionStatusOpen,
			GatewayPayoutID:        input.GatewayPayoutID,
			BankEntryID:            input.BankEntryID,
			PayoutReconciliationID: input.PayoutReconciliationID,
			DedupKey:               dedupKey,
			Summary:                input.Summary,
			Detail:                 input.Detail,
			CreatedByActor:         actor,
		}
		if err := tx.Create(ex).Error; err != nil {
			if isUniqueConstraintError(err) {
				existing, lookupErr := findActiveException(tx, input.CompanyID, dedupKey)
				if lookupErr != nil {
					return fmt.Errorf("load existing reconciliation exception after unique conflict: %w", lookupErr)
				}
				if existing != nil {
					ex = existing
					wasCreated = false
					return nil
				}
			}
			return fmt.Errorf("create reconciliation exception: %w", err)
		}
		wasCreated = true
		return nil
	}); err != nil {
		return nil, false, err
	}

	if wasCreated {
		slog.Info("reconciliation exception created",
			"exception_id", ex.ID,
			"type", ex.ExceptionType,
			"company_id", ex.CompanyID,
			"payout_id", ex.GatewayPayoutID,
			"bank_entry_id", ex.BankEntryID,
		)
	}
	return ex, wasCreated, nil
}

// GetReconciliationException loads an exception by ID scoped to a company.
func GetReconciliationException(db *gorm.DB, companyID, exceptionID uint) (*models.ReconciliationException, error) {
	var ex models.ReconciliationException
	if err := db.Where("id = ? AND company_id = ?", exceptionID, companyID).First(&ex).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrExceptionNotFound
		}
		return nil, err
	}
	return &ex, nil
}

// ListReconciliationExceptions returns all exceptions for a company, newest first.
// If statusFilter is provided, only exceptions with those statuses are returned.
func ListReconciliationExceptions(
	db *gorm.DB,
	companyID uint,
	statusFilter ...models.ReconciliationExceptionStatus,
) ([]models.ReconciliationException, error) {
	q := db.Where("company_id = ?", companyID)
	if len(statusFilter) > 0 {
		q = q.Where("status IN ?", statusFilter)
	}
	var exceptions []models.ReconciliationException
	err := q.Order("created_at DESC, id DESC").Find(&exceptions).Error
	return exceptions, err
}

// ── Status transitions ────────────────────────────────────────────────────────

// ReviewReconciliationException transitions an exception from open → reviewed.
// If the exception is already reviewed, the call is a no-op (idempotent).
func ReviewReconciliationException(db *gorm.DB, companyID, exceptionID uint, actor string) error {
	return updateExceptionStatus(db, companyID, exceptionID, models.ExceptionStatusReviewed, normalizeActor(actor), "")
}

// DismissReconciliationException transitions an exception to dismissed (terminal).
// A dismissal note is required.
func DismissReconciliationException(db *gorm.DB, companyID, exceptionID uint, actor, note string) error {
	return updateExceptionStatus(db, companyID, exceptionID, models.ExceptionStatusDismissed, normalizeActor(actor), note)
}

// ResolveReconciliationException transitions an exception to resolved (terminal).
func ResolveReconciliationException(db *gorm.DB, companyID, exceptionID uint, actor, note string) error {
	return updateExceptionStatus(db, companyID, exceptionID, models.ExceptionStatusResolved, normalizeActor(actor), note)
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// updateExceptionStatus is the shared transition logic.
func updateExceptionStatus(
	db *gorm.DB,
	companyID, exceptionID uint,
	newStatus models.ReconciliationExceptionStatus,
	actor, note string,
) error {
	return db.Transaction(func(tx *gorm.DB) error {
		ex, err := loadLockedReconciliationException(tx, companyID, exceptionID)
		if err != nil {
			return err
		}
		return updateLockedExceptionStatus(tx, ex, newStatus, actor, note)
	})
}

// validateExceptionTransition checks that the transition from current → new is legal.
//
//	open     → reviewed, dismissed, resolved
//	reviewed → dismissed, resolved
//	terminal → nothing (handled by caller)
func validateExceptionTransition(
	current models.ReconciliationExceptionStatus,
	next models.ReconciliationExceptionStatus,
) error {
	switch current {
	case models.ExceptionStatusOpen:
		switch next {
		case models.ExceptionStatusReviewed,
			models.ExceptionStatusDismissed,
			models.ExceptionStatusResolved:
			return nil
		}
	case models.ExceptionStatusReviewed:
		switch next {
		case models.ExceptionStatusDismissed,
			models.ExceptionStatusResolved:
			return nil
		}
	}
	return fmt.Errorf("%w: %s → %s", ErrExceptionTransitionInvalid, current, next)
}

// findActiveException searches for an existing open or reviewed exception with the
// same normalized dedup key. Returns nil when no such exception exists.
func findActiveException(
	db *gorm.DB,
	companyID uint,
	dedupKey string,
) (*models.ReconciliationException, error) {
	q := db.Where("company_id = ? AND dedup_key = ? AND status IN ?",
		companyID, dedupKey,
		[]string{
			string(models.ExceptionStatusOpen),
			string(models.ExceptionStatusReviewed),
		})

	var ex models.ReconciliationException
	err := q.First(&ex).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ex, nil
}

// isSupportedExceptionType returns true when the type is in the supported set.
func isSupportedExceptionType(t models.ReconciliationExceptionType) bool {
	for _, supported := range models.AllReconciliationExceptionTypes() {
		if t == supported {
			return true
		}
	}
	return false
}

// normalizeActor trims whitespace and falls back to "system".
func normalizeActor(actor string) string {
	trimmed := strings.TrimSpace(actor)
	if trimmed != "" {
		return trimmed
	}
	return "system"
}

func loadLockedReconciliationException(
	tx *gorm.DB,
	companyID, exceptionID uint,
) (*models.ReconciliationException, error) {
	var ex models.ReconciliationException
	if err := applyLockForUpdate(tx.Where("id = ? AND company_id = ?", exceptionID, companyID)).
		First(&ex).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrExceptionNotFound
		}
		return nil, err
	}
	return &ex, nil
}

func updateLockedExceptionStatus(
	tx *gorm.DB,
	ex *models.ReconciliationException,
	newStatus models.ReconciliationExceptionStatus,
	actor, note string,
) error {
	// Terminal state: no further transitions allowed.
	if models.IsTerminalExceptionStatus(ex.Status) {
		return fmt.Errorf("%w: current status is %s", ErrExceptionAlreadyClosed, ex.Status)
	}

	// Idempotent no-op: reviewed -> reviewed only.
	if ex.Status == newStatus {
		return nil
	}

	if newStatus == models.ExceptionStatusDismissed && strings.TrimSpace(note) == "" {
		return ErrExceptionDismissNoteRequired
	}

	// Validate transition.
	if err := validateExceptionTransition(ex.Status, newStatus); err != nil {
		return err
	}

	updates := map[string]any{
		"status":            newStatus,
		"resolved_by_actor": actor,
		"resolution_note":   note,
		"updated_at":        time.Now(),
	}
	if models.IsTerminalExceptionStatus(newStatus) {
		now := time.Now()
		updates["resolved_at"] = &now
	}

	if err := tx.Model(ex).Updates(updates).Error; err != nil {
		return fmt.Errorf("update exception status: %w", err)
	}

	slog.Info("reconciliation exception status updated",
		"exception_id", ex.ID,
		"old_status", ex.Status,
		"new_status", newStatus,
		"actor", actor,
	)
	return nil
}

func validateAndLockExceptionSources(
	tx *gorm.DB,
	companyID uint,
	input CreateReconciliationExceptionInput,
) error {
	var payout models.GatewayPayout
	if input.GatewayPayoutID != nil {
		if err := applyLockForUpdate(
			tx.Where("id = ? AND company_id = ?", *input.GatewayPayoutID, companyID),
		).First(&payout).Error; err != nil {
			return ErrExceptionPayoutNotFound
		}
	}

	var entry models.BankEntry
	if input.BankEntryID != nil {
		if err := applyLockForUpdate(
			tx.Where("id = ? AND company_id = ?", *input.BankEntryID, companyID),
		).First(&entry).Error; err != nil {
			return ErrExceptionBankEntryNotFound
		}
	}

	if input.PayoutReconciliationID != nil {
		var rec models.PayoutReconciliation
		if err := applyLockForUpdate(
			tx.Where("id = ? AND company_id = ?", *input.PayoutReconciliationID, companyID),
		).First(&rec).Error; err != nil {
			return ErrExceptionReconciliationMissing
		}
		if input.GatewayPayoutID != nil && *input.GatewayPayoutID != rec.GatewayPayoutID {
			return ErrExceptionSourceMismatch
		}
		if input.BankEntryID != nil && *input.BankEntryID != rec.BankEntryID {
			return ErrExceptionSourceMismatch
		}
	}

	return nil
}

func buildExceptionDedupKey(
	exType models.ReconciliationExceptionType,
	payoutID, bankEntryID, reconciliationID *uint,
) string {
	return fmt.Sprintf("%s:%d:%d:%d",
		exType,
		normalizeOptionalUint(payoutID),
		normalizeOptionalUint(bankEntryID),
		normalizeOptionalUint(reconciliationID),
	)
}

func normalizeOptionalUint(v *uint) uint {
	if v == nil {
		return 0
	}
	return *v
}

// ── Error-type mapping (used by handlers) ─────────────────────────────────────

// ExceptionTypeForMatchError maps a reconciliation service error to the
// appropriate exception type.  Returns (type, true) for structural errors
// that should be recorded as exceptions.  Returns ("", false) for ordinary
// input / not-found errors that should NOT create exceptions.
func ExceptionTypeForMatchError(err error) (models.ReconciliationExceptionType, bool) {
	switch {
	case errors.Is(err, ErrReconAmountMismatch):
		return models.ExceptionAmountMismatch, true
	case errors.Is(err, ErrReconAccountMismatch):
		return models.ExceptionAccountMismatch, true
	case errors.Is(err, ErrReconCurrencyMismatch):
		return models.ExceptionCurrencyMismatch, true
	case errors.Is(err, ErrReconPayoutAlreadyMatched):
		return models.ExceptionPayoutConflict, true
	case errors.Is(err, ErrReconBankEntryAlreadyMatched):
		return models.ExceptionBankEntryConflict, true
	default:
		// ErrReconPayoutNotFound, ErrReconBankEntryNotFound, etc. are not exceptions.
		return "", false
	}
}
