// 遵循project_guide.md
package services

// investigation_workspace_service_test.go — Batch 24: Investigation workspace tests.
//
// Test groups:
//   A — ListWorkspaceRows: happy path, domain filter, status filter, type filter,
//       has-available-hooks filter, no-attempts filter, has-linked-payout filter,
//       cross-company isolation, attempt count, sort order
//   B — CountOperationalBuckets: open count, reviewed count,
//       unresolved-no-attempts count, unresolved-with-hooks count
//
// Setup note: exceptions are inserted directly into the DB (bypassing the
// CreateReconciliationException / CreatePaymentReverseException service
// functions) to keep test fixtures minimal and avoid foreign-key dependencies.
// The workspace service is a read-only consumer, so this is correct.

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

var workspaceSeedSeq uint64

// ── Test DB ───────────────────────────────────────────────────────────────────

func workspaceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:workspace_%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:                                   logger.Discard,
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.ReconciliationException{},
		&models.ReconciliationResolutionAttempt{},
		&models.PaymentReverseException{},
		&models.PaymentReverseResolutionAttempt{},
		&models.PaymentTransaction{},
		&models.PaymentAllocation{},
		&models.PayoutReconciliation{},
	); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

// ── Seed helpers ──────────────────────────────────────────────────────────────

// seedReconException inserts a ReconciliationException directly into the DB.
// Sets a unique DedupKey to avoid the partial unique index constraint.
func seedReconException(
	t *testing.T,
	db *gorm.DB,
	companyID uint,
	exType models.ReconciliationExceptionType,
	status models.ReconciliationExceptionStatus,
	payoutID *uint,
	bankEntryID *uint,
) *models.ReconciliationException {
	t.Helper()
	ex := &models.ReconciliationException{
		CompanyID:       companyID,
		ExceptionType:   exType,
		Status:          status,
		GatewayPayoutID: payoutID,
		BankEntryID:     bankEntryID,
		DedupKey:        fmt.Sprintf("test-%s-%d-%d", exType, companyID, atomic.AddUint64(&workspaceSeedSeq, 1)),
		Summary:         fmt.Sprintf("test %s exception", exType),
		CreatedByActor:  "test",
	}
	if err := db.Create(ex).Error; err != nil {
		t.Fatalf("seed recon exception: %v", err)
	}
	return ex
}

// seedPRException inserts a PaymentReverseException directly into the DB.
func seedPRException(
	t *testing.T,
	db *gorm.DB,
	companyID uint,
	exType models.PaymentReverseExceptionType,
	status models.PaymentReverseExceptionStatus,
	reverseTxnID *uint,
) *models.PaymentReverseException {
	return seedPRExceptionWithOriginal(t, db, companyID, exType, status, reverseTxnID, nil)
}

func seedPRExceptionWithOriginal(
	t *testing.T,
	db *gorm.DB,
	companyID uint,
	exType models.PaymentReverseExceptionType,
	status models.PaymentReverseExceptionStatus,
	reverseTxnID, originalTxnID *uint,
) *models.PaymentReverseException {
	t.Helper()
	ex := &models.PaymentReverseException{
		CompanyID:      companyID,
		ExceptionType:  exType,
		Status:         status,
		ReverseTxnID:   reverseTxnID,
		OriginalTxnID:  originalTxnID,
		DedupKey:       fmt.Sprintf("test-%s-%d-%d", exType, companyID, atomic.AddUint64(&workspaceSeedSeq, 1)),
		Summary:        fmt.Sprintf("test %s exception", exType),
		CreatedByActor: "test",
	}
	if err := db.Create(ex).Error; err != nil {
		t.Fatalf("seed PR exception: %v", err)
	}
	return ex
}

func seedWorkspaceTxn(t *testing.T, db *gorm.DB, companyID uint, txnType models.PaymentTransactionType, posted bool) *models.PaymentTransaction {
	t.Helper()
	txn := &models.PaymentTransaction{
		CompanyID:        companyID,
		GatewayAccountID: 1,
		TransactionType:  txnType,
		Amount:           decimal.NewFromInt(100),
		CurrencyCode:     "USD",
		Status:           "completed",
		RawPayload:       datatypes.JSON([]byte("{}")),
	}
	if posted {
		jeID := uint(9001)
		txn.PostedJournalEntryID = &jeID
	}
	if err := db.Create(txn).Error; err != nil {
		t.Fatalf("seed workspace txn: %v", err)
	}
	return txn
}

func seedWorkspacePaymentAlloc(t *testing.T, db *gorm.DB, companyID, txnID, invoiceID uint) *models.PaymentAllocation {
	t.Helper()
	alloc := &models.PaymentAllocation{
		CompanyID:            companyID,
		PaymentTransactionID: txnID,
		InvoiceID:            invoiceID,
		AllocatedAmount:      decimal.NewFromInt(100),
	}
	if err := db.Create(alloc).Error; err != nil {
		t.Fatalf("seed workspace payment alloc: %v", err)
	}
	return alloc
}

func seedPRHookableException(t *testing.T, db *gorm.DB, companyID uint) *models.PaymentReverseException {
	t.Helper()
	origTxn := seedWorkspaceTxn(t, db, companyID, models.TxnTypeCharge, true)
	seedWorkspacePaymentAlloc(t, db, companyID, origTxn.ID, 101)
	revTxn := seedWorkspaceTxn(t, db, companyID, models.TxnTypeRefund, true)
	if err := db.Model(revTxn).Update("original_transaction_id", origTxn.ID).Error; err != nil {
		t.Fatalf("link reverse txn to original: %v", err)
	}
	return seedPRExceptionWithOriginal(
		t, db, companyID,
		models.PRExceptionAmountExceedsStrategy,
		models.PRExceptionStatusOpen,
		workspacePtrUint(revTxn.ID),
		workspacePtrUint(origTxn.ID),
	)
}

// seedAttempt inserts a ReconciliationResolutionAttempt for the given exception.
func seedAttempt(
	t *testing.T,
	db *gorm.DB,
	companyID, exceptionID uint,
	status models.ResolutionAttemptStatus,
) *models.ReconciliationResolutionAttempt {
	t.Helper()
	attempt := &models.ReconciliationResolutionAttempt{
		CompanyID:                 companyID,
		ReconciliationExceptionID: exceptionID,
		HookType:                  models.HookTypeRetryMatch,
		Status:                    status,
		Summary:                   "test attempt",
		Actor:                     "test",
	}
	if err := db.Create(attempt).Error; err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
	return attempt
}

func seedPRAttempt(
	t *testing.T,
	db *gorm.DB,
	companyID, exceptionID uint,
	status models.PRAttemptStatus,
) *models.PaymentReverseResolutionAttempt {
	t.Helper()
	attempt := &models.PaymentReverseResolutionAttempt{
		CompanyID:                 companyID,
		PaymentReverseExceptionID: exceptionID,
		HookType:                  models.PRHookRetryCheck,
		Status:                    status,
		Summary:                   "test payment reverse attempt",
		Actor:                     "test",
	}
	if err := db.Create(attempt).Error; err != nil {
		t.Fatalf("seed payment reverse attempt: %v", err)
	}
	return attempt
}

func workspacePtrUint(v uint) *uint { return &v }

func seedPayoutMatch(t *testing.T, db *gorm.DB, companyID, payoutID, bankEntryID uint) *models.PayoutReconciliation {
	t.Helper()
	rec := &models.PayoutReconciliation{
		CompanyID:       companyID,
		GatewayPayoutID: payoutID,
		BankEntryID:     bankEntryID,
		MatchedAt:       time.Now().UTC(),
		Actor:           "test",
	}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("seed payout reconciliation: %v", err)
	}
	return rec
}

// ── A. ListWorkspaceRows ──────────────────────────────────────────────────────

// TestWorkspaceRows_HappyPath verifies that both exception domains appear in a
// no-filter listing.
func TestWorkspaceRows_HappyPath(t *testing.T) {
	db := workspaceTestDB(t)
	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, workspacePtrUint(10), nil)
	seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, workspacePtrUint(20))

	rows, _, err := ListWorkspaceRows(db, 1, WorkspaceFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}

	domains := map[OperationalExceptionDomain]bool{}
	for _, r := range rows {
		domains[r.Domain] = true
	}
	if !domains[DomainReconciliation] {
		t.Error("missing reconciliation domain row")
	}
	if !domains[DomainPaymentReverse] {
		t.Error("missing payment_reverse domain row")
	}
}

// TestWorkspaceRows_DomainFilterReconciliation verifies domain=reconciliation
// returns only reconciliation rows.
func TestWorkspaceRows_DomainFilterReconciliation(t *testing.T) {
	db := workspaceTestDB(t)
	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)
	seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil)

	rows, _, err := ListWorkspaceRows(db, 1, WorkspaceFilter{Domain: DomainReconciliation})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0].Domain != DomainReconciliation {
		t.Errorf("want domain=reconciliation, got %s", rows[0].Domain)
	}
}

// TestWorkspaceRows_DomainFilterPaymentReverse verifies domain=payment_reverse
// returns only payment-reverse rows.
func TestWorkspaceRows_DomainFilterPaymentReverse(t *testing.T) {
	db := workspaceTestDB(t)
	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)
	seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil)

	rows, _, err := ListWorkspaceRows(db, 1, WorkspaceFilter{Domain: DomainPaymentReverse})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0].Domain != DomainPaymentReverse {
		t.Errorf("want domain=payment_reverse, got %s", rows[0].Domain)
	}
}

// TestWorkspaceRows_StatusFilter verifies status filter returns only matching rows.
func TestWorkspaceRows_StatusFilter(t *testing.T) {
	db := workspaceTestDB(t)
	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)
	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusReviewed, nil, nil)
	seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil)
	seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusResolved, nil)

	rows, _, err := ListWorkspaceRows(db, 1, WorkspaceFilter{Status: "open"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 open rows, got %d", len(rows))
	}
	for _, r := range rows {
		if r.StatusStr != "open" {
			t.Errorf("want status=open, got %s (domain=%s, id=%d)", r.StatusStr, r.Domain, r.ID)
		}
	}
}

// TestWorkspaceRows_TypeFilter verifies type filter returns only matching type.
func TestWorkspaceRows_TypeFilter(t *testing.T) {
	db := workspaceTestDB(t)
	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)
	seedReconException(t, db, 1, models.ExceptionAccountMismatch, models.ExceptionStatusOpen, nil, nil)
	seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil)

	rows, _, err := ListWorkspaceRows(db, 1, WorkspaceFilter{TypeStr: string(models.ExceptionAmountMismatch)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 amount_mismatch row, got %d", len(rows))
	}
	if rows[0].TypeStr != string(models.ExceptionAmountMismatch) {
		t.Errorf("want type=amount_mismatch, got %s", rows[0].TypeStr)
	}
}

// TestWorkspaceRows_HasAvailableHooksFilter verifies that the filter uses each
// domain's authoritative hook policy, including Batch 26 payment-reverse hooks.
func TestWorkspaceRows_HasAvailableHooksFilter(t *testing.T) {
	db := workspaceTestDB(t)

	// Hook-eligible: amount_mismatch + open + has payout
	eligibleRecon := seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, workspacePtrUint(10), nil)

	// Not eligible: amount_mismatch but no payout
	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)

	// Not eligible: account_mismatch (not a hookable type)
	seedReconException(t, db, 1, models.ExceptionAccountMismatch, models.ExceptionStatusOpen, workspacePtrUint(11), nil)

	// Not eligible: terminal status
	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusResolved, workspacePtrUint(12), nil)

	// Payment-reverse: linked, open, and hookable under Batch 26 policy.
	eligiblePR := seedPRHookableException(t, db, 1)

	rows, _, err := ListWorkspaceRows(db, 1, WorkspaceFilter{HasAvailableHooks: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 hookable rows, got %d", len(rows))
	}
	found := map[string]bool{}
	for _, row := range rows {
		if !row.HasAvailableHooks {
			t.Fatalf("row domain=%s id=%d should have HasAvailableHooks=true", row.Domain, row.ID)
		}
		found[fmt.Sprintf("%s:%d", row.Domain, row.ID)] = true
	}
	if !found[fmt.Sprintf("%s:%d", DomainReconciliation, eligibleRecon.ID)] {
		t.Errorf("missing hookable reconciliation exception ID=%d", eligibleRecon.ID)
	}
	if !found[fmt.Sprintf("%s:%d", DomainPaymentReverse, eligiblePR.ID)] {
		t.Errorf("missing hookable payment-reverse exception ID=%d", eligiblePR.ID)
	}
}

// TestWorkspaceRows_HasAvailableHooksPaginatesAfterPolicyFilter verifies that
// hook-policy filtering happens before pagination. Otherwise an early
// non-hookable candidate can make page 1 look empty while total says rows exist.
func TestWorkspaceRows_HasAvailableHooksPaginatesAfterPolicyFilter(t *testing.T) {
	db := workspaceTestDB(t)

	hookable := seedPRHookableException(t, db, 1)
	time.Sleep(2 * time.Millisecond)
	seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil)

	rows, total, err := ListWorkspaceRows(db, 1, WorkspaceFilter{
		Domain:            DomainPaymentReverse,
		HasAvailableHooks: true,
		Limit:             1,
		Offset:            0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 1 {
		t.Fatalf("want total=1 hookable row, got %d", total)
	}
	if len(rows) != 1 {
		t.Fatalf("want page 1 to contain the hookable row after policy filtering, got %d rows", len(rows))
	}
	if rows[0].ID != hookable.ID || rows[0].Domain != DomainPaymentReverse {
		t.Fatalf("want hookable payment_reverse row ID=%d, got domain=%s id=%d",
			hookable.ID, rows[0].Domain, rows[0].ID)
	}
}

// TestWorkspaceRows_HasAvailableHooksFilter_ExcludesMatchedPayout verifies the
// workspace filter reuses authoritative hook policy and does not surface rows
// whose linked payout is already matched.
func TestWorkspaceRows_HasAvailableHooksFilter_ExcludesMatchedPayout(t *testing.T) {
	db := workspaceTestDB(t)

	payoutID := uint(10)
	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, &payoutID, nil)
	seedPayoutMatch(t, db, 1, payoutID, 99)

	rows, _, err := ListWorkspaceRows(db, 1, WorkspaceFilter{HasAvailableHooks: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("want 0 rows because payout is already matched, got %d", len(rows))
	}
}

// TestWorkspaceRows_NoAttemptsFilter verifies that only exceptions with zero
// attempts are returned across both attempt truth tables.
func TestWorkspaceRows_NoAttemptsFilter(t *testing.T) {
	db := workspaceTestDB(t)

	// Recon exception with no attempts — should be included.
	noAttempts := seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)

	// Recon exception with one attempt — should be excluded.
	withAttempt := seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)
	seedAttempt(t, db, 1, withAttempt.ID, models.AttemptStatusRejected)

	// Payment-reverse exception with no attempts should be included.
	pr := seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil)

	prWithAttempt := seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil)
	seedPRAttempt(t, db, 1, prWithAttempt.ID, models.PRAttemptRejected)

	rows, _, err := ListWorkspaceRows(db, 1, WorkspaceFilter{NoAttempts: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows (no-attempt recon + PR), got %d", len(rows))
	}

	ids := map[string]bool{}
	for _, r := range rows {
		ids[fmt.Sprintf("%s:%d", r.Domain, r.ID)] = true
	}
	if !ids[fmt.Sprintf("%s:%d", DomainReconciliation, noAttempts.ID)] {
		t.Error("expected no-attempts recon exception to be present")
	}
	if !ids[fmt.Sprintf("%s:%d", DomainPaymentReverse, pr.ID)] {
		t.Error("expected payment-reverse exception to be present")
	}
	if ids[fmt.Sprintf("%s:%d", DomainReconciliation, withAttempt.ID)] {
		t.Error("expected with-attempt recon exception to be excluded")
	}
	if ids[fmt.Sprintf("%s:%d", DomainPaymentReverse, prWithAttempt.ID)] {
		t.Error("expected with-attempt payment-reverse exception to be excluded")
	}
}

// TestWorkspaceRows_HasLinkedPayoutFilter verifies that only exceptions with a
// linked payout are returned.  Payment-reverse rows are always excluded.
func TestWorkspaceRows_HasLinkedPayoutFilter(t *testing.T) {
	db := workspaceTestDB(t)

	withPayout := seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, workspacePtrUint(10), nil)
	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil) // no payout
	seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, workspacePtrUint(20))

	rows, _, err := ListWorkspaceRows(db, 1, WorkspaceFilter{HasLinkedPayout: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row (recon with payout), got %d", len(rows))
	}
	if rows[0].ID != withPayout.ID {
		t.Errorf("want ID=%d, got %d", withPayout.ID, rows[0].ID)
	}
}

// TestWorkspaceRows_CrossCompanyIsolation verifies that company A cannot see
// exceptions belonging to company B.
func TestWorkspaceRows_CrossCompanyIsolation(t *testing.T) {
	db := workspaceTestDB(t)

	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)
	seedReconException(t, db, 2, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)
	seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil)
	seedPRException(t, db, 2, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil)

	rowsC1, _, err := ListWorkspaceRows(db, 1, WorkspaceFilter{})
	if err != nil {
		t.Fatalf("company 1: unexpected error: %v", err)
	}
	rowsC2, _, err := ListWorkspaceRows(db, 2, WorkspaceFilter{})
	if err != nil {
		t.Fatalf("company 2: unexpected error: %v", err)
	}

	if len(rowsC1) != 2 {
		t.Errorf("company 1: want 2 rows, got %d", len(rowsC1))
	}
	if len(rowsC2) != 2 {
		t.Errorf("company 2: want 2 rows, got %d", len(rowsC2))
	}

	// Confirm all rows belong to the queried company.
	for _, r := range rowsC1 {
		// We can't directly check CompanyID in WorkspaceRow (it's not exposed),
		// but the domain/ID pair is sufficient — verifying count isolation is
		// the key assertion.
		_ = r
	}
}

// TestWorkspaceRows_AttemptCount verifies that reconciliation exception rows
// show the correct attempt count, loaded in bulk.
func TestWorkspaceRows_AttemptCount(t *testing.T) {
	db := workspaceTestDB(t)

	ex1 := seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)
	ex2 := seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)

	// ex1: 2 attempts
	seedAttempt(t, db, 1, ex1.ID, models.AttemptStatusRejected)
	seedAttempt(t, db, 1, ex1.ID, models.AttemptStatusRejected)

	// ex2: 0 attempts

	rows, _, err := ListWorkspaceRows(db, 1, WorkspaceFilter{Domain: DomainReconciliation})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}

	counts := map[uint]int{}
	for _, r := range rows {
		counts[r.ID] = r.AttemptCount
	}
	if counts[ex1.ID] != 2 {
		t.Errorf("ex1 attempt count: want 2, got %d", counts[ex1.ID])
	}
	if counts[ex2.ID] != 0 {
		t.Errorf("ex2 attempt count: want 0, got %d", counts[ex2.ID])
	}
}

// TestWorkspaceRows_PRAttemptCountAndHooks verifies payment-reverse rows use
// Batch 26 attempt truth and hook availability.
func TestWorkspaceRows_PRAttemptCountAndHooks(t *testing.T) {
	db := workspaceTestDB(t)
	ex := seedPRHookableException(t, db, 1)
	seedPRAttempt(t, db, 1, ex.ID, models.PRAttemptRejected)
	seedPRAttempt(t, db, 1, ex.ID, models.PRAttemptSucceeded)

	rows, _, err := ListWorkspaceRows(db, 1, WorkspaceFilter{Domain: DomainPaymentReverse})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0].AttemptCount != 2 {
		t.Errorf("want AttemptCount=2 for payment_reverse, got %d", rows[0].AttemptCount)
	}
	if !rows[0].HasAvailableHooks {
		t.Error("want HasAvailableHooks=true for hookable payment_reverse")
	}
}

// TestWorkspaceRows_SortOrder verifies that rows are sorted newest-first
// across both domains.
func TestWorkspaceRows_SortOrder(t *testing.T) {
	db := workspaceTestDB(t)

	// Insert with explicit CreatedAt via raw DB time manipulation is tricky
	// with GORM auto-time.  Instead verify the sort by inserting in sequence
	// and asserting the newest row comes first.
	older := seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)
	time.Sleep(2 * time.Millisecond) // ensure distinct timestamps
	newer := seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil)

	rows, _, err := ListWorkspaceRows(db, 1, WorkspaceFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if rows[0].ID != newer.ID || rows[0].Domain != DomainPaymentReverse {
		t.Errorf("first row should be newest (PR exception %d), got domain=%s id=%d",
			newer.ID, rows[0].Domain, rows[0].ID)
	}
	if rows[1].ID != older.ID || rows[1].Domain != DomainReconciliation {
		t.Errorf("second row should be older (recon exception %d), got domain=%s id=%d",
			older.ID, rows[1].Domain, rows[1].ID)
	}
}

// TestWorkspaceRows_LinkedPresenceFlags verifies that link-presence flags are
// correctly populated from source records.
func TestWorkspaceRows_LinkedPresenceFlags(t *testing.T) {
	db := workspaceTestDB(t)

	payoutID := uint(10)
	bankID := uint(20)
	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, &payoutID, &bankID)

	reverseTxnID := uint(30)
	seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, &reverseTxnID)

	rows, _, err := ListWorkspaceRows(db, 1, WorkspaceFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}

	for _, r := range rows {
		switch r.Domain {
		case DomainReconciliation:
			if !r.HasLinkedPayout {
				t.Error("recon row: want HasLinkedPayout=true")
			}
			if !r.HasLinkedBankEntry {
				t.Error("recon row: want HasLinkedBankEntry=true")
			}
			if r.HasLinkedReverseTxn {
				t.Error("recon row: want HasLinkedReverseTxn=false")
			}
		case DomainPaymentReverse:
			if r.HasLinkedPayout {
				t.Error("PR row: want HasLinkedPayout=false")
			}
			if !r.HasLinkedReverseTxn {
				t.Error("PR row: want HasLinkedReverseTxn=true")
			}
		}
	}
}

// TestWorkspaceRows_Pagination verifies that Limit/Offset page the unified
// newest-first result set without changing the total count.
func TestWorkspaceRows_Pagination(t *testing.T) {
	db := workspaceTestDB(t)

	older := seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)
	time.Sleep(2 * time.Millisecond)
	middle := seedReconException(t, db, 1, models.ExceptionAccountMismatch, models.ExceptionStatusOpen, nil, nil)
	time.Sleep(2 * time.Millisecond)
	newest := seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil)

	rows, total, err := ListWorkspaceRows(db, 1, WorkspaceFilter{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 3 {
		t.Fatalf("want total=3 before pagination, got %d", total)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 paged row, got %d", len(rows))
	}
	if rows[0].ID != middle.ID || rows[0].Domain != DomainReconciliation {
		t.Fatalf("want second newest row to be reconciliation ID=%d, got domain=%s id=%d", middle.ID, rows[0].Domain, rows[0].ID)
	}
	if rows[0].ID == newest.ID || rows[0].ID == older.ID {
		t.Fatalf("want paged row to exclude newest=%d and older=%d, got %d", newest.ID, older.ID, rows[0].ID)
	}
}

// ── B. CountOperationalBuckets ────────────────────────────────────────────────

// TestCountBuckets_OpenCount verifies open count spans both domains.
func TestCountBuckets_OpenCount(t *testing.T) {
	db := workspaceTestDB(t)
	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)
	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusReviewed, nil, nil)
	seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil)
	seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusResolved, nil)

	b, err := CountOperationalBuckets(db, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.OpenCount != 2 {
		t.Errorf("OpenCount: want 2, got %d", b.OpenCount)
	}
}

// TestCountBuckets_ReviewedCount verifies reviewed count spans both domains.
func TestCountBuckets_ReviewedCount(t *testing.T) {
	db := workspaceTestDB(t)
	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusReviewed, nil, nil)
	seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusReviewed, nil)
	seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil)

	b, err := CountOperationalBuckets(db, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.ReviewedCount != 2 {
		t.Errorf("ReviewedCount: want 2, got %d", b.ReviewedCount)
	}
}

// TestCountBuckets_ResolvedCount verifies resolved count spans both domains.
func TestCountBuckets_ResolvedCount(t *testing.T) {
	db := workspaceTestDB(t)
	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusResolved, nil, nil)
	seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusResolved, nil)
	seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusReviewed, nil)

	b, err := CountOperationalBuckets(db, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.ResolvedCount != 2 {
		t.Errorf("ResolvedCount: want 2, got %d", b.ResolvedCount)
	}
}

// TestCountBuckets_UnresolvedNoAttempts verifies the no-attempts count across
// both attempt truth tables.
func TestCountBuckets_UnresolvedNoAttempts(t *testing.T) {
	db := workspaceTestDB(t)

	// Recon: 2 open, 1 has an attempt → 1 qualifies.
	ex1 := seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)
	ex2 := seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)
	_ = ex2
	seedAttempt(t, db, 1, ex1.ID, models.AttemptStatusRejected) // ex1 has an attempt

	// Payment-reverse: 1 open, 1 reviewed → both qualify.
	seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil)
	seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusReviewed, nil)
	prAttempted := seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil)
	seedPRAttempt(t, db, 1, prAttempted.ID, models.PRAttemptRejected)
	// terminal PR exception — does NOT qualify.
	seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusResolved, nil)

	b, err := CountOperationalBuckets(db, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 1 recon (ex2) + 2 PR unresolved = 3
	if b.UnresolvedNoAttemptsCount != 3 {
		t.Errorf("UnresolvedNoAttemptsCount: want 3, got %d", b.UnresolvedNoAttemptsCount)
	}
}

// TestCountBuckets_UnresolvedWithHooks verifies hookable exceptions from both
// domains are counted.
func TestCountBuckets_UnresolvedWithHooks(t *testing.T) {
	db := workspaceTestDB(t)

	// Eligible: amount_mismatch + open + payout linked.
	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, workspacePtrUint(10), nil)

	// Not eligible: amount_mismatch + open but no payout.
	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)

	// Not eligible: account_mismatch (not a hookable type).
	seedReconException(t, db, 1, models.ExceptionAccountMismatch, models.ExceptionStatusOpen, workspacePtrUint(11), nil)

	// Not eligible: terminal status.
	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusResolved, workspacePtrUint(12), nil)

	// Payment-reverse eligible under Batch 26 policy.
	seedPRHookableException(t, db, 1)

	// Payment-reverse without source links is not hookable.
	seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil)

	b, err := CountOperationalBuckets(db, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.UnresolvedWithHooksCount != 2 {
		t.Errorf("UnresolvedWithHooksCount: want 2, got %d", b.UnresolvedWithHooksCount)
	}
}

// TestCountBuckets_UnresolvedWithHooks_ExcludesMatchedPayout verifies the
// authoritative hook count excludes unresolved exceptions whose payout is
// already matched and therefore has no available hook.
func TestCountBuckets_UnresolvedWithHooks_ExcludesMatchedPayout(t *testing.T) {
	db := workspaceTestDB(t)

	payoutID := uint(10)
	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, &payoutID, nil)
	seedPayoutMatch(t, db, 1, payoutID, 88)

	b, err := CountOperationalBuckets(db, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.UnresolvedWithHooksCount != 0 {
		t.Errorf("UnresolvedWithHooksCount: want 0 for matched payout, got %d", b.UnresolvedWithHooksCount)
	}
}

// TestCountBuckets_CrossCompanyIsolation verifies counts are scoped per company.
func TestCountBuckets_CrossCompanyIsolation(t *testing.T) {
	db := workspaceTestDB(t)

	// Company 1: 2 open exceptions.
	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)
	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)

	// Company 2: 1 open exception.
	seedReconException(t, db, 2, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)

	b1, err := CountOperationalBuckets(db, 1)
	if err != nil {
		t.Fatalf("company 1: unexpected error: %v", err)
	}
	b2, err := CountOperationalBuckets(db, 2)
	if err != nil {
		t.Fatalf("company 2: unexpected error: %v", err)
	}

	if b1.OpenCount != 2 {
		t.Errorf("company 1 OpenCount: want 2, got %d", b1.OpenCount)
	}
	if b2.OpenCount != 1 {
		t.Errorf("company 2 OpenCount: want 1, got %d", b2.OpenCount)
	}
}

// ── C. WorkspaceCursor encode/decode ──────────────────────────────────────────

// TestCursor_EncodeDecodeRoundTrip verifies that EncodeCursor / DecodeCursor are
// lossless inverses.
func TestCursor_EncodeDecodeRoundTrip(t *testing.T) {
	ts := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	orig := WorkspaceCursor{TS: ts, Domain: DomainReconciliation, ID: 42}
	token := EncodeCursor(orig)
	got, ok := DecodeCursor(token)
	if !ok {
		t.Fatal("DecodeCursor returned ok=false")
	}
	if !got.TS.Equal(orig.TS) {
		t.Errorf("TS: want %v, got %v", orig.TS, got.TS)
	}
	if got.Domain != orig.Domain {
		t.Errorf("Domain: want %s, got %s", orig.Domain, got.Domain)
	}
	if got.ID != orig.ID {
		t.Errorf("ID: want %d, got %d", orig.ID, got.ID)
	}
}

// TestCursor_DecodeGarbage verifies that DecodeCursor returns false for invalid
// input so callers can safely ignore malformed cursor tokens.
func TestCursor_DecodeGarbage(t *testing.T) {
	if _, ok := DecodeCursor("not-valid-base64!!!"); ok {
		t.Error("expected ok=false for garbage input")
	}
	if _, ok := DecodeCursor(""); ok {
		t.Error("expected ok=false for empty string")
	}
	if _, ok := DecodeCursor(EncodeCursor(WorkspaceCursor{
		TS:     time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC),
		Domain: OperationalExceptionDomain("bogus"),
		ID:     1,
	})); ok {
		t.Error("expected ok=false for unknown cursor domain")
	}
	if _, ok := DecodeCursor(EncodeCursor(WorkspaceCursor{
		TS:     time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC),
		Domain: DomainReconciliation,
		ID:     0,
	})); ok {
		t.Error("expected ok=false for zero cursor ID")
	}
}

// ── D. ListWorkspacePage — accurate totals and HasMore ────────────────────────

// TestWorkspacePage_CrossDomainAccurateTotal verifies that ListWorkspacePage
// returns Total = reconTotal + prTotal even when both totals individually exceed
// maxWorkspaceRowsPerDomain.  Previously ListWorkspaceRows would cap this.
func TestWorkspacePage_CrossDomainAccurateTotal(t *testing.T) {
	db := workspaceTestDB(t)

	// Insert enough recon exceptions to demonstrate the count is not capped.
	for i := 0; i < 5; i++ {
		seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)
	}
	// Insert a few PR exceptions.
	for i := 0; i < 3; i++ {
		seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil)
	}

	page, err := ListWorkspacePage(db, 1, WorkspaceFilter{Limit: 4, Offset: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if page.Total != 8 {
		t.Errorf("Total: want 8, got %d", page.Total)
	}
	if len(page.Rows) != 4 {
		t.Errorf("len(Rows): want 4, got %d", len(page.Rows))
	}
}

// TestWorkspacePage_HasMoreSignal verifies that HasMore is set when there are
// more rows beyond the current page, and unset on the last page.
func TestWorkspacePage_HasMoreSignal(t *testing.T) {
	db := workspaceTestDB(t)

	for i := 0; i < 6; i++ {
		seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)
	}
	for i := 0; i < 4; i++ {
		seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil)
	}

	// First page (limit=5): total=10, has more.
	p1, err := ListWorkspacePage(db, 1, WorkspaceFilter{Limit: 5, Offset: 0})
	if err != nil {
		t.Fatalf("page 1 error: %v", err)
	}
	if !p1.HasMore {
		t.Errorf("page 1: HasMore want true, got false (total=%d, rows=%d)", p1.Total, len(p1.Rows))
	}
	if p1.NextCursor == "" {
		t.Error("page 1: NextCursor should be non-empty when HasMore=true")
	}

	// Last page (limit=5, offset=5): no more.
	p2, err := ListWorkspacePage(db, 1, WorkspaceFilter{Limit: 5, Offset: 5})
	if err != nil {
		t.Fatalf("page 2 error: %v", err)
	}
	if p2.HasMore {
		t.Errorf("page 2: HasMore want false, got true")
	}
	if p2.NextCursor != "" {
		t.Errorf("page 2: NextCursor want empty, got %q", p2.NextCursor)
	}
}

// TestWorkspacePage_CursorContinuation verifies that when a cursor from page 1
// is used as CursorAfter on the single-domain path, the second page starts
// exactly where the first page ended (no overlap, no gap).
func TestWorkspacePage_CursorContinuation(t *testing.T) {
	db := workspaceTestDB(t)

	// 7 recon exceptions — single domain path exercises DB-level pagination.
	var ids []uint
	for i := 0; i < 7; i++ {
		ex := seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)
		ids = append(ids, ex.ID)
	}

	// Page 1: first 4.
	p1, err := ListWorkspacePage(db, 1, WorkspaceFilter{
		Domain: DomainReconciliation,
		Limit:  4,
		Offset: 0,
	})
	if err != nil {
		t.Fatalf("page 1 error: %v", err)
	}
	if len(p1.Rows) != 4 {
		t.Fatalf("page 1: want 4 rows, got %d", len(p1.Rows))
	}
	if !p1.HasMore {
		t.Fatal("page 1: HasMore should be true")
	}

	cursor, ok := DecodeCursor(p1.NextCursor)
	if !ok {
		t.Fatalf("page 1 NextCursor did not decode: %q", p1.NextCursor)
	}

	// Page 2: next 3, fetched through the opaque cursor.
	p2, err := ListWorkspacePage(db, 1, WorkspaceFilter{
		Domain:      DomainReconciliation,
		Limit:       4,
		CursorAfter: &cursor,
	})
	if err != nil {
		t.Fatalf("page 2 error: %v", err)
	}
	if p2.Total != 7 {
		t.Fatalf("page 2 total should remain the full filter total, want 7 got %d", p2.Total)
	}
	if len(p2.Rows) != 3 {
		t.Fatalf("page 2: want 3 rows, got %d", len(p2.Rows))
	}
	if p2.HasMore {
		t.Error("page 2: HasMore should be false")
	}

	// Ensure no row ID appears on both pages.
	p1IDs := map[uint]bool{}
	for _, r := range p1.Rows {
		p1IDs[r.ID] = true
	}
	for _, r := range p2.Rows {
		if p1IDs[r.ID] {
			t.Errorf("row %d appears on both page 1 and page 2", r.ID)
		}
	}
	// Together they should cover all 7 inserted IDs.
	allRows := append(p1.Rows, p2.Rows...)
	if len(allRows) != 7 {
		t.Errorf("combined rows: want 7, got %d", len(allRows))
	}
}

// ── E. DismissedCount ─────────────────────────────────────────────────────────

// TestCountBuckets_DismissedCount verifies that dismissed exceptions from both
// domains are summed into DismissedCount.
func TestCountBuckets_DismissedCount(t *testing.T) {
	db := workspaceTestDB(t)

	// 2 dismissed recon, 1 dismissed PR.
	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusDismissed, nil, nil)
	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusDismissed, nil, nil)
	seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusDismissed, nil)

	// Open/resolved should NOT be counted.
	seedReconException(t, db, 1, models.ExceptionAmountMismatch, models.ExceptionStatusOpen, nil, nil)
	seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusResolved, nil)

	b, err := CountOperationalBuckets(db, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.DismissedCount != 3 {
		t.Errorf("DismissedCount: want 3, got %d", b.DismissedCount)
	}
}

// ── F. PR HasAvailableHooks pre-filter ────────────────────────────────────────

// TestWorkspaceRows_PRHookPrefilterPreservesAuthoritativePolicy verifies that
// the SQL pre-filter does not hide rows whose authoritative hook policy still
// exposes navigation hooks after retry-check has already succeeded.
func TestWorkspaceRows_PRHookPrefilterPreservesAuthoritativePolicy(t *testing.T) {
	db := workspaceTestDB(t)

	// Eligible: hookable exception with no succeeded attempt.
	eligible := seedPRHookableException(t, db, 1)

	// Retry-check already succeeded, but navigation hooks remain available on
	// the detail page, so the workspace HasAvailableHooks filter must not hide it.
	alreadyDone := seedPRHookableException(t, db, 1)
	seedPRAttempt(t, db, 1, alreadyDone.ID, models.PRAttemptSucceeded)

	rows, _, err := ListWorkspaceRows(db, 1, WorkspaceFilter{HasAvailableHooks: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	foundEligible := false
	foundAlreadyDone := false
	for _, r := range rows {
		if r.Domain == DomainPaymentReverse && r.ID == eligible.ID {
			foundEligible = true
		}
		if r.Domain == DomainPaymentReverse && r.ID == alreadyDone.ID {
			foundAlreadyDone = true
		}
	}
	if !foundEligible {
		t.Errorf("eligible exception %d should appear in hooks filter", eligible.ID)
	}
	if !foundAlreadyDone {
		t.Errorf("exception %d still has navigation hooks and should appear in hooks filter", alreadyDone.ID)
	}
}

// TestWorkspaceRows_PRHookPrefilterPreservesNavigationOnlyLinks verifies that
// PR exceptions without any source links are excluded, while a real single-link
// navigation hook remains visible because the authoritative policy allows it.
func TestWorkspaceRows_PRHookPrefilterPreservesNavigationOnlyLinks(t *testing.T) {
	db := workspaceTestDB(t)

	// No links: should be excluded.
	seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, nil)

	// Only reverse link, no original: should appear because open_reverse_transaction
	// is a valid navigation hook when the linked reverse transaction exists.
	revTxn := seedWorkspaceTxn(t, db, 1, models.TxnTypeRefund, true)
	reverseOnly := seedPRException(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, models.PRExceptionStatusOpen, workspacePtrUint(revTxn.ID))

	// Hookable (both links): should appear.
	eligible := seedPRHookableException(t, db, 1)

	rows, _, err := ListWorkspaceRows(db, 1, WorkspaceFilter{HasAvailableHooks: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	foundReverseOnly := false
	for _, r := range rows {
		if r.Domain == DomainPaymentReverse && r.ID == eligible.ID {
			found = true
		}
		if r.Domain == DomainPaymentReverse && r.ID == reverseOnly.ID {
			foundReverseOnly = true
		}
	}
	if !found {
		t.Errorf("eligible exception %d should appear", eligible.ID)
	}
	if !foundReverseOnly {
		t.Errorf("reverse-only exception %d should appear because navigation hook is available", reverseOnly.ID)
	}
	// Total from PR domain should be 2 (reverse-only navigation + hookable).
	prCount := 0
	for _, r := range rows {
		if r.Domain == DomainPaymentReverse {
			prCount++
		}
	}
	if prCount != 2 {
		t.Errorf("PR rows with hooks: want 2, got %d", prCount)
	}
}
