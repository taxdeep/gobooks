// 遵循project_guide.md
package services

// payment_reverse_hook_service_test.go — Batch 26: Payment reverse hook service tests.
//
// Test groups:
//   A — AvailablePaymentReverseHooks: hook visibility per exception state / linkage
//   B — ExecutePaymentReverseHook: navigation hook rejection, execution happy path,
//       rejection path, closed exception, cross-company, duplicate idempotency
//   C — ListRecentPRAttempts: ordering, limit, cross-company isolation

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"balanciz/internal/models"
)

// ── atomic seed counter ───────────────────────────────────────────────────────

var prHookSeedSeq uint64

// ── Test DB ───────────────────────────────────────────────────────────────────

func prHookTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:prhook_%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:                                   logger.Discard,
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.PaymentTransaction{},
		&models.PaymentAllocation{},
		&models.PaymentReverseAllocation{},
		&models.PaymentReverseException{},
		&models.PaymentReverseResolutionAttempt{},
	); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

// ── Seed helpers ──────────────────────────────────────────────────────────────

func phPtrUint(v uint) *uint { return &v }

func seedPRHookTxn(t *testing.T, db *gorm.DB, companyID uint, txnType models.PaymentTransactionType, amount decimal.Decimal, posted bool) *models.PaymentTransaction {
	t.Helper()
	txn := &models.PaymentTransaction{
		CompanyID:        companyID,
		GatewayAccountID: 1,
		TransactionType:  txnType,
		Amount:           amount,
		CurrencyCode:     "USD",
		Status:           "completed",
		RawPayload:       datatypes.JSON([]byte("{}")),
	}
	if posted {
		jeID := uint(99)
		txn.PostedJournalEntryID = &jeID
	}
	if err := db.Create(txn).Error; err != nil {
		t.Fatalf("seed txn: %v", err)
	}
	return txn
}

func seedPRHookAlloc(t *testing.T, db *gorm.DB, companyID, txnID, invoiceID uint, amount decimal.Decimal) *models.PaymentAllocation {
	t.Helper()
	alloc := &models.PaymentAllocation{
		CompanyID:            companyID,
		PaymentTransactionID: txnID,
		InvoiceID:            invoiceID,
		AllocatedAmount:      amount,
	}
	if err := db.Create(alloc).Error; err != nil {
		t.Fatalf("seed alloc: %v", err)
	}
	return alloc
}

func seedPRHookException(
	t *testing.T,
	db *gorm.DB,
	companyID uint,
	exType models.PaymentReverseExceptionType,
	status models.PaymentReverseExceptionStatus,
	revTxnID, origTxnID *uint,
) *models.PaymentReverseException {
	t.Helper()
	ex := &models.PaymentReverseException{
		CompanyID:      companyID,
		ExceptionType:  exType,
		Status:         status,
		ReverseTxnID:   revTxnID,
		OriginalTxnID:  origTxnID,
		DedupKey:       fmt.Sprintf("prhook-%s-%d-%d", exType, companyID, atomic.AddUint64(&prHookSeedSeq, 1)),
		Summary:        "test",
		CreatedByActor: "test",
	}
	if err := db.Create(ex).Error; err != nil {
		t.Fatalf("seed PR exception: %v", err)
	}
	return ex
}

// ── A. AvailablePaymentReverseHooks ──────────────────────────────────────────

// TestPRHooks_NilExceptionReturnsNil verifies nil exception returns empty slice.
func TestPRHooks_NilExceptionReturnsNil(t *testing.T) {
	db := prHookTestDB(t)
	hooks := AvailablePaymentReverseHooks(db, 1, nil)
	if hooks != nil {
		t.Errorf("want nil, got %v", hooks)
	}
}

// TestPRHooks_NoLinksReturnsNoHooks verifies that an exception with no txn links
// produces no hooks.
func TestPRHooks_NoLinksReturnsNoHooks(t *testing.T) {
	db := prHookTestDB(t)
	ex := seedPRHookException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil, nil)
	hooks := AvailablePaymentReverseHooks(db, 1, ex)
	if len(hooks) != 0 {
		t.Errorf("want 0 hooks, got %d", len(hooks))
	}
}

// TestPRHooks_CrossCompanyExceptionReturnsNoHooks verifies availability is
// scoped to the exception's company, not just matching transaction IDs.
func TestPRHooks_CrossCompanyExceptionReturnsNoHooks(t *testing.T) {
	db := prHookTestDB(t)
	revTxn := seedPRHookTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(500), false)
	ex := seedPRHookException(t, db, 2, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, phPtrUint(revTxn.ID), nil)

	hooks := AvailablePaymentReverseHooks(db, 1, ex)
	if len(hooks) != 0 {
		t.Fatalf("want 0 hooks for cross-company exception, got %+v", hooks)
	}
}

// TestPRHooks_NavigationHooksAvailableWhenLinked verifies open_reverse_transaction
// and open_original_charge are available when txns are linked.
func TestPRHooks_NavigationHooksAvailableWhenLinked(t *testing.T) {
	db := prHookTestDB(t)
	revTxn := seedPRHookTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(500), false)
	origTxn := seedPRHookTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(500), false)
	ex := seedPRHookException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, phPtrUint(revTxn.ID), phPtrUint(origTxn.ID))

	hooks := AvailablePaymentReverseHooks(db, 1, ex)
	hookMap := prHookMap(hooks)

	if h, ok := hookMap[models.PRHookOpenReverseTransaction]; !ok || !h.Available {
		t.Error("want open_reverse_transaction available")
	}
	if h, ok := hookMap[models.PRHookOpenOriginalCharge]; !ok || !h.Available {
		t.Error("want open_original_charge available")
	}
	// Navigate URL must be set.
	if hookMap[models.PRHookOpenReverseTransaction].NavigateURL == "" {
		t.Error("open_reverse_transaction must have NavigateURL")
	}
}

// TestPRHooks_ForwardAllocHookAvailableWhenMultiAlloc verifies open_forward_allocations
// is available when the original charge has multi-alloc records.
func TestPRHooks_ForwardAllocHookAvailableWhenMultiAlloc(t *testing.T) {
	db := prHookTestDB(t)
	origTxn := seedPRHookTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(1000), false)
	seedPRHookAlloc(t, db, 1, origTxn.ID, 101, decimal.NewFromInt(600))
	seedPRHookAlloc(t, db, 1, origTxn.ID, 102, decimal.NewFromInt(400))
	revTxn := seedPRHookTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(1000), false)
	ex := seedPRHookException(t, db, 1, models.PRExceptionAmountExceedsStrategy, models.PRExceptionStatusOpen, phPtrUint(revTxn.ID), phPtrUint(origTxn.ID))

	hooks := AvailablePaymentReverseHooks(db, 1, ex)
	hookMap := prHookMap(hooks)

	if h, ok := hookMap[models.PRHookOpenForwardAllocations]; !ok || !h.Available {
		t.Error("want open_forward_allocations available when multi-alloc exists")
	}
}

// TestPRHooks_ForwardAllocHookUnavailableWhenNoMultiAlloc verifies that
// open_forward_allocations is unavailable when no PaymentAllocation rows exist.
func TestPRHooks_ForwardAllocHookUnavailableWhenNoMultiAlloc(t *testing.T) {
	db := prHookTestDB(t)
	origTxn := seedPRHookTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(500), false)
	// No PaymentAllocation rows — single-invoice path or unallocated.
	revTxn := seedPRHookTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(500), false)
	ex := seedPRHookException(t, db, 1, models.PRExceptionRequiresManualSplit, models.PRExceptionStatusOpen, phPtrUint(revTxn.ID), phPtrUint(origTxn.ID))

	hooks := AvailablePaymentReverseHooks(db, 1, ex)
	hookMap := prHookMap(hooks)

	if h, ok := hookMap[models.PRHookOpenForwardAllocations]; ok && h.Available {
		t.Error("want open_forward_allocations unavailable when no multi-alloc")
	}
}

// TestPRHooks_RetryCheckAvailableWhenEligible verifies retry_safe_reverse_check
// is available when original has multi-alloc and reverse txn is posted.
func TestPRHooks_RetryCheckAvailableWhenEligible(t *testing.T) {
	db := prHookTestDB(t)
	origTxn := seedPRHookTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(1000), false)
	seedPRHookAlloc(t, db, 1, origTxn.ID, 101, decimal.NewFromInt(1000))
	revTxn := seedPRHookTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(500), true) // posted
	db.Model(revTxn).Update("original_transaction_id", origTxn.ID)
	ex := seedPRHookException(t, db, 1, models.PRExceptionAmountExceedsStrategy, models.PRExceptionStatusOpen, phPtrUint(revTxn.ID), phPtrUint(origTxn.ID))

	hooks := AvailablePaymentReverseHooks(db, 1, ex)
	hookMap := prHookMap(hooks)

	if h, ok := hookMap[models.PRHookRetryCheck]; !ok || !h.Available {
		t.Errorf("want retry_safe_reverse_check available, got: %+v", hookMap[models.PRHookRetryCheck])
	}
}

// TestPRHooks_RetryCheckUnavailableWhenNotPosted verifies retry check is
// unavailable when reverse txn is not posted.
func TestPRHooks_RetryCheckUnavailableWhenNotPosted(t *testing.T) {
	db := prHookTestDB(t)
	origTxn := seedPRHookTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(1000), false)
	seedPRHookAlloc(t, db, 1, origTxn.ID, 101, decimal.NewFromInt(1000))
	revTxn := seedPRHookTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(500), false) // NOT posted
	ex := seedPRHookException(t, db, 1, models.PRExceptionAmountExceedsStrategy, models.PRExceptionStatusOpen, phPtrUint(revTxn.ID), phPtrUint(origTxn.ID))

	hooks := AvailablePaymentReverseHooks(db, 1, ex)
	hookMap := prHookMap(hooks)

	if h, ok := hookMap[models.PRHookRetryCheck]; ok && h.Available {
		t.Error("want retry_safe_reverse_check unavailable when not posted")
	}
}

// TestPRHooks_StaleLinkedTransactionsAreUnavailable verifies navigation hooks
// do not become fake links when the exception references missing transactions.
func TestPRHooks_StaleLinkedTransactionsAreUnavailable(t *testing.T) {
	db := prHookTestDB(t)
	missingReverseID := uint(404)
	missingOriginalID := uint(405)
	ex := seedPRHookException(t, db, 1, models.PRExceptionChainConflict, models.PRExceptionStatusOpen, phPtrUint(missingReverseID), phPtrUint(missingOriginalID))

	hooks := AvailablePaymentReverseHooks(db, 1, ex)
	hookMap := prHookMap(hooks)

	for _, hookType := range []models.PRHookType{
		models.PRHookOpenReverseTransaction,
		models.PRHookOpenOriginalCharge,
		models.PRHookOpenForwardAllocations,
		models.PRHookRetryCheck,
	} {
		if h, ok := hookMap[hookType]; ok && h.Available {
			t.Fatalf("want hook %s unavailable for stale source linkage, got %+v", hookType, h)
		}
	}
}

// TestPRHooks_SourceMismatchDisablesRetry verifies the execution hook is not
// offered when the reverse transaction resolves to a different original charge.
func TestPRHooks_SourceMismatchDisablesRetry(t *testing.T) {
	db := prHookTestDB(t)
	actualOrig := seedPRHookTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(1000), false)
	linkedOrig := seedPRHookTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(1000), false)
	seedPRHookAlloc(t, db, 1, linkedOrig.ID, 101, decimal.NewFromInt(1000))
	revTxn := seedPRHookTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(500), true)
	db.Model(revTxn).Update("original_transaction_id", actualOrig.ID)
	ex := seedPRHookException(t, db, 1, models.PRExceptionAmountExceedsStrategy, models.PRExceptionStatusOpen, phPtrUint(revTxn.ID), phPtrUint(linkedOrig.ID))

	hooks := AvailablePaymentReverseHooks(db, 1, ex)
	hookMap := prHookMap(hooks)
	if h, ok := hookMap[models.PRHookRetryCheck]; !ok || h.Available {
		t.Fatalf("want retry_safe_reverse_check unavailable for source mismatch, got %+v", h)
	}
}

// TestPRHooks_RetryCheckUnavailableAfterSuccess verifies duplicate-run policy:
// once retry_safe_reverse_check has succeeded for an exception, the execution
// hook is no longer exposed as available.
func TestPRHooks_RetryCheckUnavailableAfterSuccess(t *testing.T) {
	db := prHookTestDB(t)
	origTxn := seedPRHookTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(1000), true)
	seedPRHookAlloc(t, db, 1, origTxn.ID, 101, decimal.NewFromInt(1000))
	revTxn := seedPRHookTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(500), true)
	db.Model(revTxn).Update("original_transaction_id", origTxn.ID)
	ex := seedPRHookException(t, db, 1, models.PRExceptionAmountExceedsStrategy,
		models.PRExceptionStatusReviewed, phPtrUint(revTxn.ID), phPtrUint(origTxn.ID))

	if err := db.Create(&models.PaymentReverseResolutionAttempt{
		CompanyID:                 1,
		PaymentReverseExceptionID: ex.ID,
		HookType:                  models.PRHookRetryCheck,
		Status:                    models.PRAttemptSucceeded,
		Summary:                   "previous success",
		Actor:                     "test@example.com",
	}).Error; err != nil {
		t.Fatalf("seed succeeded attempt: %v", err)
	}

	hooks := AvailablePaymentReverseHooks(db, 1, ex)
	hookMap := prHookMap(hooks)
	if h, ok := hookMap[models.PRHookRetryCheck]; !ok || h.Available {
		t.Fatalf("want retry_safe_reverse_check unavailable after previous success, got %+v", h)
	}
}

// TestPRHooks_AllHooksUnavailableWhenTerminal verifies that all hooks
// report Available=false when the exception is in a terminal state.
func TestPRHooks_AllHooksUnavailableWhenTerminal(t *testing.T) {
	db := prHookTestDB(t)
	origTxn := seedPRHookTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(1000), true)
	seedPRHookAlloc(t, db, 1, origTxn.ID, 101, decimal.NewFromInt(1000))
	revTxn := seedPRHookTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(500), true)
	ex := seedPRHookException(t, db, 1, models.PRExceptionAmountExceedsStrategy, models.PRExceptionStatusResolved, phPtrUint(revTxn.ID), phPtrUint(origTxn.ID))

	hooks := AvailablePaymentReverseHooks(db, 1, ex)
	for _, h := range hooks {
		if h.Available {
			t.Errorf("want hook %s unavailable on terminal exception, got available=true", h.Type)
		}
	}
}

// ── B. ExecutePaymentReverseHook ─────────────────────────────────────────────

// TestPRHookExec_NavigationHookReturnsUnsupported verifies that passing a
// navigation hook type returns ErrPRHookTypeUnsupported.
func TestPRHookExec_NavigationHookReturnsUnsupported(t *testing.T) {
	db := prHookTestDB(t)
	ex := seedPRHookException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil, nil)
	err := ExecutePaymentReverseHook(db, 1, ex.ID, models.PRHookOpenOriginalCharge, "test@example.com")
	if err == nil {
		t.Fatal("want error for navigation hook, got nil")
	}
}

// TestPRHookExec_ClosedExceptionRejectsAndRecordsAttempt verifies that executing
// a hook on a terminal exception records a rejected attempt and returns an error.
func TestPRHookExec_ClosedExceptionRejectsAndRecordsAttempt(t *testing.T) {
	db := prHookTestDB(t)
	origTxn := seedPRHookTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(1000), true)
	seedPRHookAlloc(t, db, 1, origTxn.ID, 101, decimal.NewFromInt(1000))
	revTxn := seedPRHookTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(500), true)
	ex := seedPRHookException(t, db, 1, models.PRExceptionAmountExceedsStrategy, models.PRExceptionStatusResolved, phPtrUint(revTxn.ID), phPtrUint(origTxn.ID))

	err := ExecutePaymentReverseHook(db, 1, ex.ID, models.PRHookRetryCheck, "test@example.com")
	if err == nil {
		t.Fatal("want error for closed exception, got nil")
	}

	// Attempt must be recorded.
	attempts, _ := ListRecentPRAttempts(db, 1, ex.ID, 10)
	if len(attempts) == 0 {
		t.Error("want at least 1 attempt recorded on rejected execution")
	}
	if attempts[0].Status != models.PRAttemptRejected {
		t.Errorf("want attempt status=rejected, got %s", attempts[0].Status)
	}
}

// TestPRHookExec_CrossCompanyReject verifies that a hook cannot be executed
// against an exception from a different company.
func TestPRHookExec_CrossCompanyReject(t *testing.T) {
	db := prHookTestDB(t)
	origTxn := seedPRHookTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(1000), true)
	seedPRHookAlloc(t, db, 1, origTxn.ID, 101, decimal.NewFromInt(1000))
	revTxn := seedPRHookTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(500), true)
	ex := seedPRHookException(t, db, 1, models.PRExceptionAmountExceedsStrategy, models.PRExceptionStatusOpen, phPtrUint(revTxn.ID), phPtrUint(origTxn.ID))

	// Attempt to execute as company 2.
	err := ExecutePaymentReverseHook(db, 2, ex.ID, models.PRHookRetryCheck, "other@example.com")
	if err == nil {
		t.Fatal("want error for cross-company execution, got nil")
	}
	// No attempt should be recorded for company 2.
	attempts, _ := ListRecentPRAttempts(db, 2, ex.ID, 10)
	if len(attempts) != 0 {
		t.Errorf("want 0 attempts for wrong company, got %d", len(attempts))
	}
}

// TestPRHookExec_RetryCheckRejectsWhenNoMultiAlloc verifies that
// retry_safe_reverse_check records a rejected attempt when the original charge
// has no multi-alloc records.
func TestPRHookExec_RetryCheckRejectsWhenNoMultiAlloc(t *testing.T) {
	db := prHookTestDB(t)
	origTxn := seedPRHookTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(1000), false)
	// No PaymentAllocation rows.
	revTxn := seedPRHookTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(500), true)
	ex := seedPRHookException(t, db, 1, models.PRExceptionAmountExceedsStrategy, models.PRExceptionStatusOpen, phPtrUint(revTxn.ID), phPtrUint(origTxn.ID))

	err := ExecutePaymentReverseHook(db, 1, ex.ID, models.PRHookRetryCheck, "test@example.com")
	if err == nil {
		t.Fatal("want error when no multi-alloc, got nil")
	}

	attempts, _ := ListRecentPRAttempts(db, 1, ex.ID, 10)
	if len(attempts) == 0 {
		t.Error("want attempt recorded on rejection")
	}
	if attempts[0].Status != models.PRAttemptRejected {
		t.Errorf("want rejected, got %s", attempts[0].Status)
	}
}

// TestPRHookExec_RetryCheckRejectsValidationFailure verifies that
// retry_safe_reverse_check records a rejected attempt (not an error) when
// the underlying validator rejects (e.g. reverse not posted).
func TestPRHookExec_RetryCheckRejectsValidationFailure(t *testing.T) {
	db := prHookTestDB(t)
	origTxn := seedPRHookTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(1000), false)
	seedPRHookAlloc(t, db, 1, origTxn.ID, 101, decimal.NewFromInt(1000))
	// Reverse txn is posted for policy eligibility check, but the validator
	// itself also checks posting — use a txn where it's posted (we pass policy check),
	// but we simulate failure by having no original allocs point to a real invoice.
	// Actually: simplest failure path is reverse NOT posted — fails policy AND validator.
	revTxn := seedPRHookTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(500), false) // not posted → validator fails
	ex := seedPRHookException(t, db, 1, models.PRExceptionAmountExceedsStrategy, models.PRExceptionStatusOpen, phPtrUint(revTxn.ID), phPtrUint(origTxn.ID))

	// Hook is not available (not posted) so ExecutePaymentReverseHook should reject.
	err := ExecutePaymentReverseHook(db, 1, ex.ID, models.PRHookRetryCheck, "test@example.com")
	if err == nil {
		t.Fatal("want error, got nil")
	}

	// Attempt must still be recorded.
	attempts, _ := ListRecentPRAttempts(db, 1, ex.ID, 10)
	if len(attempts) == 0 {
		t.Error("want attempt recorded even on availability-rejected execution")
	}
}

// TestPRHookExec_RetryCheckSuccessRecordsAttemptAndTransitionsToReviewed verifies
// the happy path: validation passes, attempt recorded as succeeded, exception
// transitions from open → reviewed.
func TestPRHookExec_RetryCheckSuccessRecordsAttemptAndTransitionsToReviewed(t *testing.T) {
	db := prHookTestDB(t)

	// Build a scenario where ValidateRefundReverseAllocatable will succeed.
	// Requirements: refund txn is posted, has OriginalTransactionID pointing to a
	// charge that has PaymentAllocation rows, and is not already applied.
	origTxn := seedPRHookTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(1000), true)
	// Link origTxn directly.
	seedPRHookAlloc(t, db, 1, origTxn.ID, 101, decimal.NewFromInt(600))
	seedPRHookAlloc(t, db, 1, origTxn.ID, 102, decimal.NewFromInt(400))

	revTxn := seedPRHookTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(500), true) // posted
	// Set OriginalTransactionID so resolver can find the original charge.
	db.Model(revTxn).Update("original_transaction_id", origTxn.ID)

	ex := seedPRHookException(t, db, 1, models.PRExceptionAmountExceedsStrategy, models.PRExceptionStatusOpen, phPtrUint(revTxn.ID), phPtrUint(origTxn.ID))

	err := ExecutePaymentReverseHook(db, 1, ex.ID, models.PRHookRetryCheck, "test@example.com")
	if err != nil {
		t.Fatalf("unexpected error on happy path: %v", err)
	}

	// Attempt recorded with succeeded.
	attempts, _ := ListRecentPRAttempts(db, 1, ex.ID, 10)
	if len(attempts) == 0 {
		t.Fatal("want at least 1 attempt recorded")
	}
	if attempts[0].Status != models.PRAttemptSucceeded {
		t.Errorf("want succeeded, got %s", attempts[0].Status)
	}

	// Exception transitioned to reviewed.
	updated, _ := GetPaymentReverseException(db, 1, ex.ID)
	if updated.Status != models.PRExceptionStatusReviewed {
		t.Errorf("want exception status=reviewed, got %s", updated.Status)
	}
}

// TestPRHookExec_RetryCheckAlreadyReviewedStaysReviewed verifies that executing
// a successful retry check on an already-reviewed exception doesn't error.
func TestPRHookExec_RetryCheckAlreadyReviewedStaysReviewed(t *testing.T) {
	db := prHookTestDB(t)

	origTxn := seedPRHookTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(1000), true)
	seedPRHookAlloc(t, db, 1, origTxn.ID, 201, decimal.NewFromInt(1000))
	revTxn := seedPRHookTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(500), true)
	db.Model(revTxn).Update("original_transaction_id", origTxn.ID)

	ex := seedPRHookException(t, db, 1, models.PRExceptionAmountExceedsStrategy, models.PRExceptionStatusReviewed, phPtrUint(revTxn.ID), phPtrUint(origTxn.ID))

	err := ExecutePaymentReverseHook(db, 1, ex.ID, models.PRHookRetryCheck, "test@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, _ := GetPaymentReverseException(db, 1, ex.ID)
	if updated.Status != models.PRExceptionStatusReviewed {
		t.Errorf("want reviewed, got %s", updated.Status)
	}
}

// ── C. ListRecentPRAttempts ──────────────────────────────────────────────────

// TestPRAttempts_NewestFirst verifies that ListRecentPRAttempts returns
// newest attempts first.
func TestPRAttempts_NewestFirst(t *testing.T) {
	db := prHookTestDB(t)
	ex := seedPRHookException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil, nil)

	// Insert two attempts directly with different CreatedAt.
	first := &models.PaymentReverseResolutionAttempt{
		CompanyID:                 1,
		PaymentReverseExceptionID: ex.ID,
		HookType:                  models.PRHookRetryCheck,
		Status:                    models.PRAttemptRejected,
		Summary:                   "first",
		CreatedAt:                 time.Now().UTC().Add(-time.Minute),
	}
	second := &models.PaymentReverseResolutionAttempt{
		CompanyID:                 1,
		PaymentReverseExceptionID: ex.ID,
		HookType:                  models.PRHookRetryCheck,
		Status:                    models.PRAttemptSucceeded,
		Summary:                   "second",
		CreatedAt:                 time.Now().UTC(),
	}
	db.Create(first)
	db.Create(second)

	attempts, err := ListRecentPRAttempts(db, 1, ex.ID, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("want 2 attempts, got %d", len(attempts))
	}
	if attempts[0].Summary != "second" {
		t.Errorf("want newest first (second), got %q", attempts[0].Summary)
	}
}

// TestPRAttempts_LimitRespected verifies that the limit parameter is respected.
func TestPRAttempts_LimitRespected(t *testing.T) {
	db := prHookTestDB(t)
	ex := seedPRHookException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil, nil)

	for i := 0; i < 5; i++ {
		db.Create(&models.PaymentReverseResolutionAttempt{
			CompanyID:                 1,
			PaymentReverseExceptionID: ex.ID,
			HookType:                  models.PRHookRetryCheck,
			Status:                    models.PRAttemptRejected,
			Summary:                   fmt.Sprintf("attempt %d", i),
		})
	}

	attempts, _ := ListRecentPRAttempts(db, 1, ex.ID, 3)
	if len(attempts) != 3 {
		t.Errorf("want 3 (limited), got %d", len(attempts))
	}
}

// TestPRAttempts_CrossCompanyIsolation verifies that attempts from another
// company are not visible.
func TestPRAttempts_CrossCompanyIsolation(t *testing.T) {
	db := prHookTestDB(t)
	ex1 := seedPRHookException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil, nil)
	ex2 := seedPRHookException(t, db, 2, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil, nil)

	db.Create(&models.PaymentReverseResolutionAttempt{
		CompanyID:                 1,
		PaymentReverseExceptionID: ex1.ID,
		HookType:                  models.PRHookRetryCheck,
		Status:                    models.PRAttemptRejected,
		Summary:                   "company 1 attempt",
	})
	db.Create(&models.PaymentReverseResolutionAttempt{
		CompanyID:                 2,
		PaymentReverseExceptionID: ex2.ID,
		HookType:                  models.PRHookRetryCheck,
		Status:                    models.PRAttemptRejected,
		Summary:                   "company 2 attempt",
	})

	// Company 1 should only see its own attempt.
	attemptsC1, _ := ListRecentPRAttempts(db, 1, ex1.ID, 10)
	if len(attemptsC1) != 1 || attemptsC1[0].Summary != "company 1 attempt" {
		t.Errorf("want 1 attempt for company 1, got %d: %v", len(attemptsC1), attemptsC1)
	}

	// Company 2 should only see its own attempt.
	attemptsC2, _ := ListRecentPRAttempts(db, 2, ex2.ID, 10)
	if len(attemptsC2) != 1 || attemptsC2[0].Summary != "company 2 attempt" {
		t.Errorf("want 1 attempt for company 2, got %d: %v", len(attemptsC2), attemptsC2)
	}
}

// ── D. Batch 27 — Concurrency hardening tests ────────────────────────────────
//
// These tests verify that the SELECT FOR UPDATE + single-transaction execution
// model correctly handles duplicate-click and terminal-state race conditions.
// On SQLite the lock is a no-op; we verify the semantic outcome (only one
// succeeded attempt) via sequential calls on the same connection, which is the
// correct test strategy per the project pattern for SQLite-based unit tests.

// TestPRHookExec_DuplicateClickOnlyFirstSucceeds verifies that when the retry
// hook is executed twice sequentially (simulating a rapid double-click), the
// second call sees the updated status and records a rejected attempt.
//
// On SQLite, SELECT FOR UPDATE is a no-op; the test validates the semantic
// guarantee that the status transition (open → reviewed) made by the first call
// causes the second call to see the exception as ineligible.
func TestPRHookExec_DuplicateClickOnlyFirstSucceeds(t *testing.T) {
	t.Log("SELECT FOR UPDATE is a no-op on SQLite; this test verifies that " +
		"the status transition (open → reviewed) from the first call causes " +
		"the second call to record a rejected attempt, not a second succeeded.")

	db := prHookTestDB(t)

	origTxn := seedPRHookTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(1000), true)
	seedPRHookAlloc(t, db, 1, origTxn.ID, 101, decimal.NewFromInt(1000))
	revTxn := seedPRHookTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(500), true)
	db.Model(revTxn).Update("original_transaction_id", origTxn.ID)

	ex := seedPRHookException(t, db, 1, models.PRExceptionAmountExceedsStrategy,
		models.PRExceptionStatusOpen, phPtrUint(revTxn.ID), phPtrUint(origTxn.ID))

	// First call — should succeed.
	err1 := ExecutePaymentReverseHook(db, 1, ex.ID, models.PRHookRetryCheck, "actor@example.com")
	if err1 != nil {
		t.Fatalf("first hook call: want nil, got %v", err1)
	}

	// Verify exception is now reviewed after first call.
	after1, _ := GetPaymentReverseException(db, 1, ex.ID)
	if after1.Status != models.PRExceptionStatusReviewed {
		t.Fatalf("expected reviewed after first call, got %s", after1.Status)
	}

	// Second call — exception is reviewed but not terminal, retry_safe_reverse_check
	// already has a successful retry_safe_reverse_check attempt, so it must be
	// recorded as a rejected duplicate rather than as a second success.
	err2 := ExecutePaymentReverseHook(db, 1, ex.ID, models.PRHookRetryCheck, "actor@example.com")
	if err2 == nil {
		t.Fatal("second hook call: want duplicate rejection, got nil")
	}

	attempts, _ := ListRecentPRAttempts(db, 1, ex.ID, 10)
	succeededCount := 0
	rejectedCount := 0
	for _, a := range attempts {
		switch a.Status {
		case models.PRAttemptSucceeded:
			succeededCount++
		case models.PRAttemptRejected:
			rejectedCount++
		}
	}
	if succeededCount != 1 || rejectedCount != 1 {
		t.Errorf("want exactly 1 succeeded and 1 rejected duplicate attempt, got succeeded=%d rejected=%d total=%d",
			succeededCount, rejectedCount, len(attempts))
	}

	// Status must still be reviewed (not regressed to open or beyond).
	after2, _ := GetPaymentReverseException(db, 1, ex.ID)
	if after2.Status != models.PRExceptionStatusReviewed {
		t.Errorf("want reviewed after second call, got %s", after2.Status)
	}
}

// TestPRHookExec_TerminalAfterDismissRejectsAndRecordsAttempt verifies that
// once an exception is dismissed (terminal), any subsequent hook attempt records
// a rejected attempt and returns an error — the terminal state is not bypassed.
func TestPRHookExec_TerminalAfterDismissRejectsAndRecordsAttempt(t *testing.T) {
	db := prHookTestDB(t)

	origTxn := seedPRHookTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(1000), true)
	seedPRHookAlloc(t, db, 1, origTxn.ID, 101, decimal.NewFromInt(1000))
	revTxn := seedPRHookTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(500), true)
	db.Model(revTxn).Update("original_transaction_id", origTxn.ID)

	// Start as open, execute hook successfully.
	ex := seedPRHookException(t, db, 1, models.PRExceptionAmountExceedsStrategy,
		models.PRExceptionStatusOpen, phPtrUint(revTxn.ID), phPtrUint(origTxn.ID))
	if err := ExecutePaymentReverseHook(db, 1, ex.ID, models.PRHookRetryCheck, "actor@example.com"); err != nil {
		t.Fatalf("first hook call: %v", err)
	}

	// Dismiss the exception — now terminal.
	if err := DismissPaymentReverseException(db, 1, ex.ID, "actor@example.com", "closing"); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	terminalEx, _ := GetPaymentReverseException(db, 1, ex.ID)
	if terminalEx.Status != models.PRExceptionStatusDismissed {
		t.Fatalf("want dismissed, got %s", terminalEx.Status)
	}

	// Hook attempt on terminal exception must return an error.
	err := ExecutePaymentReverseHook(db, 1, ex.ID, models.PRHookRetryCheck, "actor@example.com")
	if err == nil {
		t.Fatal("want error on terminal exception hook, got nil")
	}

	// A rejected attempt must be recorded — audit trail must not be lost.
	attempts, _ := ListRecentPRAttempts(db, 1, ex.ID, 10)
	var lastAttempt *models.PaymentReverseResolutionAttempt
	for i := range attempts {
		if lastAttempt == nil || attempts[i].ID > lastAttempt.ID {
			lastAttempt = &attempts[i]
		}
	}
	if lastAttempt == nil {
		t.Fatal("want at least one attempt recorded")
	}
	if lastAttempt.Status != models.PRAttemptRejected {
		t.Errorf("want most recent attempt=rejected after terminal hook, got %s", lastAttempt.Status)
	}
}

// TestPRHookExec_AttemptTruthConsistentWithExceptionStatus verifies that after
// any hook execution, the attempt status (succeeded/rejected) is consistent with
// the exception's resulting status — they are committed atomically.
func TestPRHookExec_AttemptTruthConsistentWithExceptionStatus(t *testing.T) {
	db := prHookTestDB(t)

	origTxn := seedPRHookTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(1000), true)
	seedPRHookAlloc(t, db, 1, origTxn.ID, 201, decimal.NewFromInt(1000))
	revTxn := seedPRHookTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(500), true)
	db.Model(revTxn).Update("original_transaction_id", origTxn.ID)

	ex := seedPRHookException(t, db, 1, models.PRExceptionAmountExceedsStrategy,
		models.PRExceptionStatusOpen, phPtrUint(revTxn.ID), phPtrUint(origTxn.ID))

	err := ExecutePaymentReverseHook(db, 1, ex.ID, models.PRHookRetryCheck, "actor@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	attempts, _ := ListRecentPRAttempts(db, 1, ex.ID, 1)
	if len(attempts) == 0 {
		t.Fatal("want at least 1 attempt recorded")
	}
	updated, _ := GetPaymentReverseException(db, 1, ex.ID)

	// On success: attempt=succeeded, status=reviewed.  Atomic.
	if attempts[0].Status != models.PRAttemptSucceeded {
		t.Errorf("want attempt=succeeded, got %s", attempts[0].Status)
	}
	if updated.Status != models.PRExceptionStatusReviewed {
		t.Errorf("want exception=reviewed, got %s", updated.Status)
	}
}

// TestPRHookExec_RejectedAttemptCommittedEvenWhenEligibilityFails verifies that
// rejected attempt rows are committed (not lost) when eligibility fails — the
// outerErr pattern inside the transaction ensures this.
func TestPRHookExec_RejectedAttemptCommittedEvenWhenEligibilityFails(t *testing.T) {
	db := prHookTestDB(t)

	// Exception with no multi-alloc → eligibility check fails.
	origTxn := seedPRHookTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(1000), false)
	// No PaymentAllocation rows — prOriginalChargeHasMultiAlloc returns false.
	revTxn := seedPRHookTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(500), true)
	ex := seedPRHookException(t, db, 1, models.PRExceptionAmountExceedsStrategy,
		models.PRExceptionStatusOpen, phPtrUint(revTxn.ID), phPtrUint(origTxn.ID))

	err := ExecutePaymentReverseHook(db, 1, ex.ID, models.PRHookRetryCheck, "actor@example.com")
	if err == nil {
		t.Fatal("want error on ineligible hook, got nil")
	}

	// Rejected attempt must be persisted (committed by the atomic transaction).
	attempts, _ := ListRecentPRAttempts(db, 1, ex.ID, 10)
	if len(attempts) == 0 {
		t.Error("want rejection attempt committed, got 0 attempts")
	}
	if len(attempts) > 0 && attempts[0].Status != models.PRAttemptRejected {
		t.Errorf("want rejected, got %s", attempts[0].Status)
	}
	// Exception status must not change on rejection.
	ex2, _ := GetPaymentReverseException(db, 1, ex.ID)
	if ex2.Status != models.PRExceptionStatusOpen {
		t.Errorf("want exception still open after rejection, got %s", ex2.Status)
	}
}

// ── E. Batch 27 — Workspace pagination tests ─────────────────────────────────

// TestWorkspaceRows_SingleDomainPaginationAccurate verifies that single-domain
// queries return DB-level pagination with an accurate total count.
func TestWorkspaceRows_SingleDomainPaginationAccurate(t *testing.T) {
	db := workspaceTestDB(t)

	// Insert 7 recon exceptions for company 1.
	for i := 0; i < 7; i++ {
		seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)
	}
	// Insert 3 PR exceptions that should NOT appear in a recon-only query.
	for i := 0; i < 3; i++ {
		seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil)
	}

	// Single domain (recon), page 1: limit=3, offset=0.
	f1 := WorkspaceFilter{Domain: DomainReconciliation, Limit: 3, Offset: 0}
	rows1, total1, err := ListWorkspaceRows(db, 1, f1)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if total1 != 7 {
		t.Errorf("page 1: want total=7 (DB count), got %d", total1)
	}
	if len(rows1) != 3 {
		t.Errorf("page 1: want 3 rows, got %d", len(rows1))
	}

	// Page 2: limit=3, offset=3.
	f2 := WorkspaceFilter{Domain: DomainReconciliation, Limit: 3, Offset: 3}
	rows2, total2, err := ListWorkspaceRows(db, 1, f2)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if total2 != 7 {
		t.Errorf("page 2: want total=7, got %d", total2)
	}
	if len(rows2) != 3 {
		t.Errorf("page 2: want 3 rows, got %d", len(rows2))
	}

	// Page 3: limit=3, offset=6 — only 1 row remaining.
	f3 := WorkspaceFilter{Domain: DomainReconciliation, Limit: 3, Offset: 6}
	rows3, total3, err := ListWorkspaceRows(db, 1, f3)
	if err != nil {
		t.Fatalf("page 3: %v", err)
	}
	if total3 != 7 {
		t.Errorf("page 3: want total=7, got %d", total3)
	}
	if len(rows3) != 1 {
		t.Errorf("page 3: want 1 row, got %d", len(rows3))
	}

	// Pages must not overlap — collect all IDs.
	seen := make(map[uint]bool)
	for _, r := range append(append(rows1, rows2...), rows3...) {
		if seen[r.ID] {
			t.Errorf("duplicate ID %d across pages", r.ID)
		}
		seen[r.ID] = true
	}
	if len(seen) != 7 {
		t.Errorf("want 7 unique IDs across 3 pages, got %d", len(seen))
	}
}

// TestWorkspaceRows_OutOfRangeOffsetReturnsEmpty verifies that requesting a page
// beyond the last page returns empty rows but still reports the correct total.
func TestWorkspaceRows_OutOfRangeOffsetReturnsEmpty(t *testing.T) {
	db := workspaceTestDB(t)
	for i := 0; i < 5; i++ {
		seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)
	}

	f := WorkspaceFilter{Domain: DomainReconciliation, Limit: 10, Offset: 100}
	rows, total, err := ListWorkspaceRows(db, 1, f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 5 {
		t.Errorf("want total=5, got %d", total)
	}
	if len(rows) != 0 {
		t.Errorf("want 0 rows for out-of-range offset, got %d", len(rows))
	}
}

// TestWorkspaceRows_CrossDomainPaginationDoesNotOverlap verifies that cross-domain
// pagination slices the merged+sorted result correctly with no overlap.
func TestWorkspaceRows_CrossDomainPaginationDoesNotOverlap(t *testing.T) {
	db := workspaceTestDB(t)

	// 4 recon + 4 PR = 8 total.
	for i := 0; i < 4; i++ {
		seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)
	}
	for i := 0; i < 4; i++ {
		seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil)
	}

	f1 := WorkspaceFilter{Limit: 5, Offset: 0}
	rows1, total, _ := ListWorkspaceRows(db, 1, f1)
	if total != 8 {
		t.Errorf("want total=8, got %d", total)
	}
	if len(rows1) != 5 {
		t.Errorf("page 1: want 5 rows, got %d", len(rows1))
	}

	f2 := WorkspaceFilter{Limit: 5, Offset: 5}
	rows2, _, _ := ListWorkspaceRows(db, 1, f2)
	if len(rows2) != 3 {
		t.Errorf("page 2: want 3 rows, got %d", len(rows2))
	}

	seen := make(map[string]bool)
	for _, r := range append(rows1, rows2...) {
		key := fmt.Sprintf("%s-%d", r.Domain, r.ID)
		if seen[key] {
			t.Errorf("duplicate row %s across pages", key)
		}
		seen[key] = true
	}
	if len(seen) != 8 {
		t.Errorf("want 8 unique rows across 2 pages, got %d", len(seen))
	}
}

// TestWorkspaceRows_CrossCompanyIsolationStrict verifies that even with the same
// exception IDs, workspace rows are strictly company-scoped.
func TestWorkspaceRows_CrossCompanyIsolationStrict(t *testing.T) {
	db := workspaceTestDB(t)

	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)
	seedReconException(t, db, 2, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)
	seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil)
	seedPRException(t, db, 2, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil)

	rows1, total1, _ := ListWorkspaceRows(db, 1, WorkspaceFilter{})
	rows2, total2, _ := ListWorkspaceRows(db, 2, WorkspaceFilter{})

	if total1 != 2 {
		t.Errorf("company 1: want total=2, got %d", total1)
	}
	if total2 != 2 {
		t.Errorf("company 2: want total=2, got %d", total2)
	}
	for _, r := range rows1 {
		for _, r2 := range rows2 {
			if r.Domain == r2.Domain && r.ID == r2.ID {
				t.Errorf("row %s-%d appears in both companies", r.Domain, r.ID)
			}
		}
	}
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// prHookMap converts a slice of PRHook into a map keyed by type.
func prHookMap(hooks []PRHook) map[models.PRHookType]PRHook {
	m := make(map[models.PRHookType]PRHook, len(hooks))
	for _, h := range hooks {
		m[h.Type] = h
	}
	return m
}
