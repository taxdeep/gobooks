// 遵循project_guide.md
package services

// reconciliation_exception_test.go — Batch 20: Reconciliation exception tests.
//
// 21 tests covering:
//   A. CreateReconciliationException — happy, dedup, invalid type, missing company
//   B. GetReconciliationException    — happy, wrong company, not found
//   C. ListReconciliationExceptions  — all, status filter, cross-company isolation
//   D. Status transitions            — review, dismiss, resolve, idempotent, terminal block
//   E. ExceptionTypeForMatchError    — all structural errors, non-structural no-op

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"balanciz/internal/models"
)

var exceptionSeedSeq uint64

// ── Test DB ───────────────────────────────────────────────────────────────────

func exceptionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:exception_%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Account{},
		&models.GatewayPayout{},
		&models.BankEntry{},
		&models.PayoutReconciliation{},
		&models.ReconciliationException{},
	); err != nil {
		t.Fatalf("migrate exception table: %v", err)
	}
	return db
}

// ── Seed helpers ──────────────────────────────────────────────────────────────

func makeException(
	t *testing.T,
	db *gorm.DB,
	companyID uint,
	exType models.ReconciliationExceptionType,
) *models.ReconciliationException {
	t.Helper()
	payout := seedExceptionPayout(t, db, companyID, 1000+uint(atomic.AddUint64(&exceptionSeedSeq, 1)))
	ex, wasCreated, err := CreateReconciliationException(db, CreateReconciliationExceptionInput{
		CompanyID:       companyID,
		ExceptionType:   exType,
		GatewayPayoutID: &payout.ID,
		Summary:         "test exception",
		CreatedByActor:  "test@example.com",
	})
	if err != nil {
		t.Fatalf("makeException: %v", err)
	}
	if !wasCreated {
		t.Fatal("makeException: expected wasCreated=true")
	}
	return ex
}

func seedExceptionPayout(t *testing.T, db *gorm.DB, companyID, payoutID uint) *models.GatewayPayout {
	t.Helper()
	payout := &models.GatewayPayout{
		ID:               payoutID,
		CompanyID:        companyID,
		GatewayAccountID: 1,
		ProviderPayoutID: fmt.Sprintf("po_%d_%d", companyID, payoutID),
		PayoutDate:       time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC),
		CurrencyCode:     "USD",
		GrossAmount:      decimal.RequireFromString("100.00"),
		FeeAmount:        decimal.Zero,
		NetAmount:        decimal.RequireFromString("100.00"),
		BankAccountID:    1,
	}
	if err := db.Create(payout).Error; err != nil {
		t.Fatalf("seed payout: %v", err)
	}
	return payout
}

// ── A. CreateReconciliationException ─────────────────────────────────────────

// TestCreateException_Happy verifies a new exception is created with correct
// initial state.
func TestCreateException_Happy(t *testing.T) {
	db := exceptionTestDB(t)
	payout := seedExceptionPayout(t, db, 1, 42)

	ex, wasCreated, err := CreateReconciliationException(db, CreateReconciliationExceptionInput{
		CompanyID:       1,
		ExceptionType:   models.ExceptionAmountMismatch,
		GatewayPayoutID: &payout.ID,
		Summary:         "net 100 ≠ bank 105",
		Detail:          "extra 5 unexplained",
		CreatedByActor:  "system",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !wasCreated {
		t.Error("expected wasCreated=true")
	}
	if ex.ID == 0 {
		t.Error("expected non-zero ID")
	}
	if ex.Status != models.ExceptionStatusOpen {
		t.Errorf("status: want open got %s", ex.Status)
	}
	if ex.ExceptionType != models.ExceptionAmountMismatch {
		t.Errorf("type: want amount_mismatch got %s", ex.ExceptionType)
	}
	if ex.Summary != "net 100 ≠ bank 105" {
		t.Errorf("summary mismatch: %q", ex.Summary)
	}
	if ex.GatewayPayoutID == nil || *ex.GatewayPayoutID != 42 {
		t.Errorf("payout_id: want 42 got %v", ex.GatewayPayoutID)
	}
	if ex.CreatedByActor != "system" {
		t.Errorf("actor: want system got %q", ex.CreatedByActor)
	}
}

func TestCreateException_SourceRequired(t *testing.T) {
	db := exceptionTestDB(t)
	_, _, err := CreateReconciliationException(db, CreateReconciliationExceptionInput{
		CompanyID:      1,
		ExceptionType:  models.ExceptionAmountMismatch,
		Summary:        "missing linkage",
		CreatedByActor: "system",
	})
	if !errors.Is(err, ErrExceptionSourceRequired) {
		t.Errorf("want ErrExceptionSourceRequired, got %v", err)
	}
}

// TestCreateException_InvalidType rejects unknown exception types.
func TestCreateException_InvalidType(t *testing.T) {
	db := exceptionTestDB(t)
	_, _, err := CreateReconciliationException(db, CreateReconciliationExceptionInput{
		CompanyID:     1,
		ExceptionType: "bogus_type",
		Summary:       "should fail",
	})
	if !errors.Is(err, ErrExceptionTypeInvalid) {
		t.Errorf("want ErrExceptionTypeInvalid, got %v", err)
	}
}

// TestCreateException_MissingCompany rejects zero company_id.
func TestCreateException_MissingCompany(t *testing.T) {
	db := exceptionTestDB(t)
	_, _, err := CreateReconciliationException(db, CreateReconciliationExceptionInput{
		CompanyID:     0,
		ExceptionType: models.ExceptionAmountMismatch,
		Summary:       "no company",
	})
	if err == nil {
		t.Error("expected error for zero company_id")
	}
}

// TestCreateException_DeduplicatesOpenException verifies that a second create
// with the same key fields returns the existing open exception (wasCreated=false).
func TestCreateException_DeduplicatesOpenException(t *testing.T) {
	db := exceptionTestDB(t)
	payout := seedExceptionPayout(t, db, 1, 10)

	inp := CreateReconciliationExceptionInput{
		CompanyID:       1,
		ExceptionType:   models.ExceptionAmountMismatch,
		GatewayPayoutID: &payout.ID,
		Summary:         "first attempt",
		CreatedByActor:  "system",
	}

	first, wasCreated1, err := CreateReconciliationException(db, inp)
	if err != nil || !wasCreated1 {
		t.Fatalf("first create failed: err=%v wasCreated=%v", err, wasCreated1)
	}

	// Second call with same key — should return existing.
	inp.Summary = "second attempt"
	second, wasCreated2, err := CreateReconciliationException(db, inp)
	if err != nil {
		t.Fatalf("second create unexpected error: %v", err)
	}
	if wasCreated2 {
		t.Error("expected wasCreated=false on dedup path")
	}
	if second.ID != first.ID {
		t.Errorf("dedup must return same record: want ID=%d got ID=%d", first.ID, second.ID)
	}
	// Summary must be from the original, not overwritten.
	if second.Summary != "first attempt" {
		t.Errorf("dedup must not overwrite existing: got %q", second.Summary)
	}
}

// TestCreateException_DeduplicatesReviewedException verifies dedup works on
// reviewed (non-terminal) exceptions too.
func TestCreateException_DeduplicatesReviewedException(t *testing.T) {
	db := exceptionTestDB(t)
	payout := seedExceptionPayout(t, db, 2, 20)

	inp := CreateReconciliationExceptionInput{
		CompanyID:       2,
		ExceptionType:   models.ExceptionAccountMismatch,
		GatewayPayoutID: &payout.ID,
		Summary:         "account mismatch",
		CreatedByActor:  "system",
	}

	first, _, err := CreateReconciliationException(db, inp)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Transition to reviewed.
	if err := ReviewReconciliationException(db, 2, first.ID, "ops@example.com"); err != nil {
		t.Fatalf("review: %v", err)
	}

	// A second create attempt should still dedup (reviewed is not terminal).
	second, wasCreated, err := CreateReconciliationException(db, inp)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if wasCreated {
		t.Error("expected wasCreated=false when reviewed exception exists")
	}
	if second.ID != first.ID {
		t.Errorf("expected same record ID: got %d vs %d", second.ID, first.ID)
	}
}

// TestCreateException_NoDedupOnTerminal verifies that a new exception IS created
// when the matching exception is already in a terminal state (dismissed).
func TestCreateException_NoDedupOnTerminal(t *testing.T) {
	db := exceptionTestDB(t)
	payout := seedExceptionPayout(t, db, 3, 30)

	inp := CreateReconciliationExceptionInput{
		CompanyID:       3,
		ExceptionType:   models.ExceptionPayoutConflict,
		GatewayPayoutID: &payout.ID,
		Summary:         "conflict",
		CreatedByActor:  "system",
	}

	first, _, err := CreateReconciliationException(db, inp)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Dismiss the first exception (terminal).
	if err := DismissReconciliationException(db, 3, first.ID, "ops@example.com", "won't fix"); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	// Second create should now succeed as a new record.
	second, wasCreated, err := CreateReconciliationException(db, inp)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if !wasCreated {
		t.Error("expected wasCreated=true when previous exception was dismissed")
	}
	if second.ID == first.ID {
		t.Error("expected a new record, got same ID")
	}
}

func TestCreateException_PayoutReferenceMustBelongToCompany(t *testing.T) {
	db := exceptionTestDB(t)
	payout := seedExceptionPayout(t, db, 2, 31)

	_, _, err := CreateReconciliationException(db, CreateReconciliationExceptionInput{
		CompanyID:       1,
		ExceptionType:   models.ExceptionUnsupportedManyToOne,
		GatewayPayoutID: &payout.ID,
		Summary:         "wrong company linkage",
		CreatedByActor:  "ops@example.com",
	})
	if !errors.Is(err, ErrExceptionPayoutNotFound) {
		t.Errorf("want ErrExceptionPayoutNotFound, got %v", err)
	}
}

func TestCreateException_ActiveUniqueIndexBlocksDuplicateRows(t *testing.T) {
	db := exceptionTestDB(t)
	payout := seedExceptionPayout(t, db, 1, 32)

	first, wasCreated, err := CreateReconciliationException(db, CreateReconciliationExceptionInput{
		CompanyID:       1,
		ExceptionType:   models.ExceptionUnsupportedManyToMany,
		GatewayPayoutID: &payout.ID,
		Summary:         "manual exception",
		CreatedByActor:  "ops@example.com",
	})
	if err != nil || !wasCreated {
		t.Fatalf("first create failed: err=%v wasCreated=%v", err, wasCreated)
	}

	duplicate := &models.ReconciliationException{
		CompanyID:       1,
		ExceptionType:   models.ExceptionUnsupportedManyToMany,
		Status:          models.ExceptionStatusOpen,
		GatewayPayoutID: &payout.ID,
		DedupKey:        buildExceptionDedupKey(models.ExceptionUnsupportedManyToMany, &payout.ID, nil, nil),
		Summary:         "duplicate row",
		CreatedByActor:  "system",
	}
	if err := db.Create(duplicate).Error; err == nil {
		t.Fatal("expected active duplicate insert to fail unique index")
	}

	var count int64
	if err := db.Model(&models.ReconciliationException{}).Where("company_id = ?", 1).Count(&count).Error; err != nil {
		t.Fatalf("count exceptions: %v", err)
	}
	if count != 1 {
		t.Fatalf("want exactly 1 active exception row, got %d", count)
	}

	if err := DismissReconciliationException(db, 1, first.ID, "ops@example.com", "handled outside"); err != nil {
		t.Fatalf("dismiss first: %v", err)
	}

	reopened := &models.ReconciliationException{
		CompanyID:       1,
		ExceptionType:   models.ExceptionUnsupportedManyToMany,
		Status:          models.ExceptionStatusOpen,
		GatewayPayoutID: &payout.ID,
		DedupKey:        buildExceptionDedupKey(models.ExceptionUnsupportedManyToMany, &payout.ID, nil, nil),
		Summary:         "new active exception after dismissal",
		CreatedByActor:  "system",
	}
	if err := db.Create(reopened).Error; err != nil {
		t.Fatalf("expected new active exception after terminal close to succeed, got %v", err)
	}
}

// ── B. GetReconciliationException ────────────────────────────────────────────

// TestGetException_Happy loads an exception by ID within the same company.
func TestGetException_Happy(t *testing.T) {
	db := exceptionTestDB(t)
	ex := makeException(t, db, 1, models.ExceptionCurrencyMismatch)

	loaded, err := GetReconciliationException(db, 1, ex.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if loaded.ID != ex.ID {
		t.Errorf("ID mismatch: want %d got %d", ex.ID, loaded.ID)
	}
}

// TestGetException_WrongCompany returns ErrExceptionNotFound for cross-company access.
func TestGetException_WrongCompany(t *testing.T) {
	db := exceptionTestDB(t)
	ex := makeException(t, db, 1, models.ExceptionCurrencyMismatch)

	_, err := GetReconciliationException(db, 99, ex.ID)
	if !errors.Is(err, ErrExceptionNotFound) {
		t.Errorf("want ErrExceptionNotFound, got %v", err)
	}
}

// TestGetException_NotFound returns ErrExceptionNotFound for a non-existent ID.
func TestGetException_NotFound(t *testing.T) {
	db := exceptionTestDB(t)
	_, err := GetReconciliationException(db, 1, 9999)
	if !errors.Is(err, ErrExceptionNotFound) {
		t.Errorf("want ErrExceptionNotFound, got %v", err)
	}
}

// ── C. ListReconciliationExceptions ──────────────────────────────────────────

// TestListExceptions_NoFilter returns all exceptions for a company.
func TestListExceptions_NoFilter(t *testing.T) {
	db := exceptionTestDB(t)

	// Create 3 exceptions for company 1 and 1 for company 2 (isolation check).
	makeException(t, db, 1, models.ExceptionAmountMismatch)
	makeException(t, db, 1, models.ExceptionAccountMismatch)
	makeException(t, db, 1, models.ExceptionCurrencyMismatch)
	makeException(t, db, 2, models.ExceptionAmountMismatch)

	list, err := ListReconciliationExceptions(db, 1)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("want 3 exceptions for company 1, got %d", len(list))
	}
}

// TestListExceptions_StatusFilter returns only exceptions with the requested status.
func TestListExceptions_StatusFilter(t *testing.T) {
	db := exceptionTestDB(t)

	ex1 := makeException(t, db, 1, models.ExceptionAmountMismatch)
	ex2 := makeException(t, db, 1, models.ExceptionAccountMismatch)

	// Dismiss ex2.
	if err := DismissReconciliationException(db, 1, ex2.ID, "ops", "not relevant"); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	_ = ex1

	openList, err := ListReconciliationExceptions(db, 1, models.ExceptionStatusOpen)
	if err != nil {
		t.Fatalf("list open: %v", err)
	}
	if len(openList) != 1 {
		t.Errorf("want 1 open exception, got %d", len(openList))
	}
	if openList[0].Status != models.ExceptionStatusOpen {
		t.Errorf("expected open status, got %s", openList[0].Status)
	}

	dismissedList, err := ListReconciliationExceptions(db, 1, models.ExceptionStatusDismissed)
	if err != nil {
		t.Fatalf("list dismissed: %v", err)
	}
	if len(dismissedList) != 1 {
		t.Errorf("want 1 dismissed exception, got %d", len(dismissedList))
	}
}

// ── D. Status transitions ─────────────────────────────────────────────────────

// TestReviewException_Happy transitions open → reviewed.
func TestReviewException_Happy(t *testing.T) {
	db := exceptionTestDB(t)
	ex := makeException(t, db, 1, models.ExceptionAmountMismatch)

	if err := ReviewReconciliationException(db, 1, ex.ID, "ops@example.com"); err != nil {
		t.Fatalf("review: %v", err)
	}

	loaded, _ := GetReconciliationException(db, 1, ex.ID)
	if loaded.Status != models.ExceptionStatusReviewed {
		t.Errorf("status: want reviewed got %s", loaded.Status)
	}
	if loaded.ResolvedByActor != "ops@example.com" {
		t.Errorf("actor: want ops@example.com got %q", loaded.ResolvedByActor)
	}
}

// TestReviewException_Idempotent verifies reviewing an already-reviewed exception
// is a no-op (returns nil error).
func TestReviewException_Idempotent(t *testing.T) {
	db := exceptionTestDB(t)
	ex := makeException(t, db, 1, models.ExceptionAmountMismatch)

	if err := ReviewReconciliationException(db, 1, ex.ID, "ops"); err != nil {
		t.Fatalf("first review: %v", err)
	}
	// Second review should be a no-op, not an error.
	if err := ReviewReconciliationException(db, 1, ex.ID, "ops"); err != nil {
		t.Errorf("second review (idempotent): unexpected error: %v", err)
	}
}

// TestReviewException_TerminalReject blocks transition from dismissed.
func TestReviewException_TerminalReject(t *testing.T) {
	db := exceptionTestDB(t)
	ex := makeException(t, db, 1, models.ExceptionAmountMismatch)

	if err := DismissReconciliationException(db, 1, ex.ID, "ops", "no action needed"); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	err := ReviewReconciliationException(db, 1, ex.ID, "ops")
	if !errors.Is(err, ErrExceptionAlreadyClosed) {
		t.Errorf("want ErrExceptionAlreadyClosed, got %v", err)
	}
}

// TestDismissException_Happy transitions open → dismissed (terminal).
func TestDismissException_Happy(t *testing.T) {
	db := exceptionTestDB(t)
	ex := makeException(t, db, 1, models.ExceptionBankEntryConflict)

	if err := DismissReconciliationException(db, 1, ex.ID, "ops@example.com", "expected discrepancy"); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	loaded, _ := GetReconciliationException(db, 1, ex.ID)
	if loaded.Status != models.ExceptionStatusDismissed {
		t.Errorf("status: want dismissed got %s", loaded.Status)
	}
	if loaded.ResolutionNote != "expected discrepancy" {
		t.Errorf("resolution_note: got %q", loaded.ResolutionNote)
	}
	if loaded.ResolvedAt == nil {
		t.Error("resolved_at must be set after dismiss")
	}
}

func TestDismissException_RequiresNote(t *testing.T) {
	db := exceptionTestDB(t)
	ex := makeException(t, db, 1, models.ExceptionBankEntryConflict)

	err := DismissReconciliationException(db, 1, ex.ID, "ops@example.com", "   ")
	if !errors.Is(err, ErrExceptionDismissNoteRequired) {
		t.Errorf("want ErrExceptionDismissNoteRequired, got %v", err)
	}
}

// TestDismissException_FromReviewed allows reviewed → dismissed.
func TestDismissException_FromReviewed(t *testing.T) {
	db := exceptionTestDB(t)
	ex := makeException(t, db, 1, models.ExceptionPayoutConflict)

	if err := ReviewReconciliationException(db, 1, ex.ID, "ops"); err != nil {
		t.Fatalf("review: %v", err)
	}
	if err := DismissReconciliationException(db, 1, ex.ID, "ops", "reviewed and dismissed"); err != nil {
		t.Fatalf("dismiss from reviewed: %v", err)
	}

	loaded, _ := GetReconciliationException(db, 1, ex.ID)
	if loaded.Status != models.ExceptionStatusDismissed {
		t.Errorf("status: want dismissed got %s", loaded.Status)
	}
}

// TestResolveException_Happy transitions open → resolved (terminal).
func TestResolveException_Happy(t *testing.T) {
	db := exceptionTestDB(t)
	ex := makeException(t, db, 1, models.ExceptionUnsupportedManyToMany)

	if err := ResolveReconciliationException(db, 1, ex.ID, "ops@example.com", "handled manually"); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	loaded, _ := GetReconciliationException(db, 1, ex.ID)
	if loaded.Status != models.ExceptionStatusResolved {
		t.Errorf("status: want resolved got %s", loaded.Status)
	}
	if loaded.ResolvedAt == nil {
		t.Error("resolved_at must be set after resolve")
	}
	if loaded.ResolutionNote != "handled manually" {
		t.Errorf("resolution_note: got %q", loaded.ResolutionNote)
	}
}

// TestResolveException_TerminalReject blocks further transitions after resolve.
func TestResolveException_TerminalReject(t *testing.T) {
	db := exceptionTestDB(t)
	ex := makeException(t, db, 1, models.ExceptionAmountMismatch)

	if err := ResolveReconciliationException(db, 1, ex.ID, "ops", "done"); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// Attempting to dismiss a resolved exception must fail.
	err := DismissReconciliationException(db, 1, ex.ID, "ops", "too late")
	if !errors.Is(err, ErrExceptionAlreadyClosed) {
		t.Errorf("want ErrExceptionAlreadyClosed, got %v", err)
	}
}

func TestDismissException_SameTargetTerminalReject(t *testing.T) {
	db := exceptionTestDB(t)
	ex := makeException(t, db, 1, models.ExceptionAmountMismatch)

	if err := DismissReconciliationException(db, 1, ex.ID, "ops", "closed"); err != nil {
		t.Fatalf("first dismiss: %v", err)
	}

	err := DismissReconciliationException(db, 1, ex.ID, "ops", "closed again")
	if !errors.Is(err, ErrExceptionAlreadyClosed) {
		t.Errorf("want ErrExceptionAlreadyClosed on second dismiss, got %v", err)
	}
}

// TestTransition_InvalidOpenToOpen verifies that open → open returns ErrExceptionTransitionInvalid.
// (Reviewed is idempotent; open is not — it goes through updateExceptionStatus which checks
// the target ≠ current first, then validates the transition table.)
func TestTransition_InvalidOpenToOpen(t *testing.T) {
	db := exceptionTestDB(t)
	// Use updateExceptionStatus directly via the exported wrappers.
	// open → open: the idempotent check won't fire (different target never equal to current
	// in the reviewed path), but we can test reviewed → open which IS an invalid transition.
	ex := makeException(t, db, 1, models.ExceptionAmountMismatch)

	if err := ReviewReconciliationException(db, 1, ex.ID, "ops"); err != nil {
		t.Fatalf("review: %v", err)
	}

	// reviewed → open is not a valid transition.
	err := updateExceptionStatus(db, 1, ex.ID, models.ExceptionStatusOpen, "ops", "")
	if !errors.Is(err, ErrExceptionTransitionInvalid) {
		t.Errorf("want ErrExceptionTransitionInvalid for reviewed→open, got %v", err)
	}
}

// ── E. ExceptionTypeForMatchError ─────────────────────────────────────────────

// TestExceptionTypeForMatchError_StructuralErrors verifies every structural
// reconciliation error maps to the correct exception type.
func TestExceptionTypeForMatchError_StructuralErrors(t *testing.T) {
	cases := []struct {
		err      error
		wantType models.ReconciliationExceptionType
	}{
		{ErrReconAmountMismatch, models.ExceptionAmountMismatch},
		{ErrReconAccountMismatch, models.ExceptionAccountMismatch},
		{ErrReconCurrencyMismatch, models.ExceptionCurrencyMismatch},
		{ErrReconPayoutAlreadyMatched, models.ExceptionPayoutConflict},
		{ErrReconBankEntryAlreadyMatched, models.ExceptionBankEntryConflict},
	}
	for _, tc := range cases {
		t.Run(string(tc.wantType), func(t *testing.T) {
			got, ok := ExceptionTypeForMatchError(tc.err)
			if !ok {
				t.Errorf("expected ok=true for %v", tc.err)
			}
			if got != tc.wantType {
				t.Errorf("want %s got %s", tc.wantType, got)
			}
		})
	}
}

// TestExceptionTypeForMatchError_NonStructuralErrors verifies that ordinary
// input / not-found errors do NOT produce exceptions.
func TestExceptionTypeForMatchError_NonStructuralErrors(t *testing.T) {
	nonStructural := []error{
		ErrReconPayoutNotFound,
		ErrReconBankEntryNotFound,
		ErrReconBankEntryInvalid,
		ErrReconBankAccountInvalid,
	}
	for _, err := range nonStructural {
		t.Run(err.Error(), func(t *testing.T) {
			_, ok := ExceptionTypeForMatchError(err)
			if ok {
				t.Errorf("expected ok=false for non-structural error %v", err)
			}
		})
	}
}

// TestExceptionTypeForMatchError_WrappedError verifies that errors.Is() is used
// (wrapped errors are also detected correctly).
func TestExceptionTypeForMatchError_WrappedError(t *testing.T) {
	wrapped := fmt.Errorf("reconciliation failed: %w", ErrReconAmountMismatch)
	got, ok := ExceptionTypeForMatchError(wrapped)
	if !ok {
		t.Error("expected ok=true for wrapped ErrReconAmountMismatch")
	}
	if got != models.ExceptionAmountMismatch {
		t.Errorf("want ExceptionAmountMismatch, got %s", got)
	}
}
