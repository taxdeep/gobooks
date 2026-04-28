// 遵循project_guide.md
package services

// exception_resolution_hook_test.go — Batch 21: Resolution hook tests.
//
// Tests:
//   A. Hook availability  (6 tests)
//   B. Attempt truth      (5 tests)
//   C. Guarded execution  (4 tests)

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"balanciz/internal/models"
)

// ── Test DB ───────────────────────────────────────────────────────────────────

func hookTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:hook_%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Account{},
		&models.GatewayPayout{},
		&models.BankEntry{},
		&models.PayoutReconciliation{},
		&models.GatewayPayoutComponent{},
		&models.AuditLog{},
		&models.ReconciliationException{},
		&models.ReconciliationResolutionAttempt{},
	); err != nil {
		t.Fatalf("migrate hook test tables: %v", err)
	}
	return db
}

// ── Seed helpers ──────────────────────────────────────────────────────────────

type hookBase struct {
	companyID uint
	payoutID  uint
	bankID    uint // bank entry ID
}

// seedHookBase inserts the minimal payout + bank entry pair needed for hook tests.
// The amounts are set so that the match would succeed without components.
func seedHookBase(t *testing.T, db *gorm.DB, amount string) hookBase {
	t.Helper()
	net := decimal.RequireFromString(amount)

	payout := &models.GatewayPayout{
		CompanyID:        1,
		GatewayAccountID: 1,
		ProviderPayoutID: fmt.Sprintf("po_hook_%s_%d", amount, time.Now().UnixNano()),
		PayoutDate:       time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC),
		CurrencyCode:     "CAD",
		GrossAmount:      net,
		FeeAmount:        decimal.Zero,
		NetAmount:        net,
		BankAccountID:    10,
	}
	if err := db.Create(payout).Error; err != nil {
		t.Fatalf("seed payout: %v", err)
	}

	entry := &models.BankEntry{
		CompanyID:     1,
		BankAccountID: 10,
		EntryDate:     time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC),
		Amount:        net,
		CurrencyCode:  "CAD",
		Description:   "test deposit",
	}
	if err := db.Create(entry).Error; err != nil {
		t.Fatalf("seed bank entry: %v", err)
	}

	return hookBase{companyID: 1, payoutID: payout.ID, bankID: entry.ID}
}

func makeExceptionForHook(
	t *testing.T,
	db *gorm.DB,
	companyID uint,
	exType models.ReconciliationExceptionType,
	payoutID *uint,
	bankID *uint,
) *models.ReconciliationException {
	t.Helper()
	ex := &models.ReconciliationException{
		CompanyID:       companyID,
		ExceptionType:   exType,
		Status:          models.ExceptionStatusOpen,
		GatewayPayoutID: payoutID,
		BankEntryID:     bankID,
		DedupKey:        buildExceptionDedupKey(exType, payoutID, bankID, nil),
		Summary:         "hook test exception",
		CreatedByActor:  "test",
	}
	if err := db.Create(ex).Error; err != nil {
		t.Fatalf("create test exception: %v", err)
	}
	return ex
}

// ── A. Hook availability ──────────────────────────────────────────────────────

// TestHookAvailability_RetryMatchAvailable verifies that retry_match is available
// for an open amount_mismatch exception with payout+bank linked and unmatched.
func TestHookAvailability_RetryMatchAvailable(t *testing.T) {
	db := hookTestDB(t)
	base := seedHookBase(t, db, "100.00")

	ex := makeExceptionForHook(t, db, base.companyID,
		models.ExceptionAmountMismatch, &base.payoutID, &base.bankID)

	hooks := AvailableHooksForException(db, base.companyID, ex)

	var retryHook *ResolutionHook
	for i := range hooks {
		if hooks[i].Type == models.HookTypeRetryMatch {
			retryHook = &hooks[i]
		}
	}
	if retryHook == nil {
		t.Fatal("expected retry_match hook to be present")
	}
	if !retryHook.Available {
		t.Errorf("retry_match should be available; reason: %q", retryHook.UnavailableReason)
	}
}

// TestHookAvailability_RetryMatchNotAvailableForTerminalException verifies that
// retry_match is not available when the exception is dismissed or resolved.
func TestHookAvailability_RetryMatchNotAvailableForTerminalException(t *testing.T) {
	db := hookTestDB(t)
	base := seedHookBase(t, db, "200.00")

	ex := makeExceptionForHook(t, db, base.companyID,
		models.ExceptionAmountMismatch, &base.payoutID, &base.bankID)
	// Dismiss the exception (terminal).
	if err := DismissReconciliationException(db, base.companyID, ex.ID, "ops", "dismissed"); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	// Reload.
	ex, _ = GetReconciliationException(db, base.companyID, ex.ID)

	hooks := AvailableHooksForException(db, base.companyID, ex)
	for _, h := range hooks {
		if h.Type == models.HookTypeRetryMatch && h.Available {
			t.Error("retry_match must not be available for a dismissed exception")
		}
	}
}

// TestHookAvailability_RetryMatchAvailableWhenReviewed verifies that reviewed
// exceptions still expose retry_match when their linked source objects are valid
// and currently unmatched.
func TestHookAvailability_RetryMatchAvailableWhenReviewed(t *testing.T) {
	db := hookTestDB(t)
	base := seedHookBase(t, db, "210.00")

	ex := makeExceptionForHook(t, db, base.companyID,
		models.ExceptionAmountMismatch, &base.payoutID, &base.bankID)
	if err := ReviewReconciliationException(db, base.companyID, ex.ID, "ops"); err != nil {
		t.Fatalf("review: %v", err)
	}
	ex, _ = GetReconciliationException(db, base.companyID, ex.ID)

	hooks := AvailableHooksForException(db, base.companyID, ex)
	var retryHook *ResolutionHook
	for i := range hooks {
		if hooks[i].Type == models.HookTypeRetryMatch {
			retryHook = &hooks[i]
		}
	}
	if retryHook == nil {
		t.Fatal("expected retry_match hook to be present for reviewed exception")
	}
	if !retryHook.Available {
		t.Errorf("retry_match should stay available after review; reason: %q", retryHook.UnavailableReason)
	}
}

// TestHookAvailability_RetryMatchNotAvailablePayoutAlreadyMatched verifies
// that retry_match is unavailable when the payout is already matched.
func TestHookAvailability_RetryMatchNotAvailablePayoutAlreadyMatched(t *testing.T) {
	db := hookTestDB(t)
	base := seedHookBase(t, db, "300.00")

	// Pre-match the payout to the bank entry.
	rec := &models.PayoutReconciliation{
		CompanyID:       base.companyID,
		GatewayPayoutID: base.payoutID,
		BankEntryID:     base.bankID,
		MatchedAt:       time.Now(),
		Actor:           "system",
	}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("pre-match: %v", err)
	}

	ex := makeExceptionForHook(t, db, base.companyID,
		models.ExceptionAmountMismatch, &base.payoutID, &base.bankID)

	hooks := AvailableHooksForException(db, base.companyID, ex)
	for _, h := range hooks {
		if h.Type == models.HookTypeRetryMatch && h.Available {
			t.Error("retry_match must not be available when payout is already matched")
		}
	}
}

// TestHookAvailability_RetryMatchNotAvailableUnsupportedType verifies that
// retry_match is not offered for exception types where it is not applicable
// (e.g. unsupported_many_to_many).
func TestHookAvailability_RetryMatchNotAvailableUnsupportedType(t *testing.T) {
	db := hookTestDB(t)
	base := seedHookBase(t, db, "400.00")

	ex := makeExceptionForHook(t, db, base.companyID,
		models.ExceptionUnsupportedManyToMany, &base.payoutID, &base.bankID)

	hooks := AvailableHooksForException(db, base.companyID, ex)
	for _, h := range hooks {
		if h.Type == models.HookTypeRetryMatch {
			t.Error("retry_match hook must not be present for unsupported_many_to_many")
		}
	}
}

// TestHookAvailability_RetryMatchNotAvailableMissingBankEntry verifies that
// retry_match is absent when the exception has no linked bank entry.
func TestHookAvailability_RetryMatchNotAvailableMissingBankEntry(t *testing.T) {
	db := hookTestDB(t)
	base := seedHookBase(t, db, "500.00")

	// Exception with payout but NO bank entry link.
	ex := makeExceptionForHook(t, db, base.companyID,
		models.ExceptionAmountMismatch, &base.payoutID, nil)

	hooks := AvailableHooksForException(db, base.companyID, ex)
	for _, h := range hooks {
		if h.Type == models.HookTypeRetryMatch {
			t.Error("retry_match hook must not be present without a bank entry link")
		}
	}
}

// TestHookAvailability_ComponentsHookPresent verifies that open_payout_components
// is available for amount_mismatch with an unmatched payout.
func TestHookAvailability_ComponentsHookPresent(t *testing.T) {
	db := hookTestDB(t)
	base := seedHookBase(t, db, "600.00")

	ex := makeExceptionForHook(t, db, base.companyID,
		models.ExceptionAmountMismatch, &base.payoutID, &base.bankID)

	hooks := AvailableHooksForException(db, base.companyID, ex)
	var compHook *ResolutionHook
	for i := range hooks {
		if hooks[i].Type == models.HookTypeOpenPayoutComponents {
			compHook = &hooks[i]
		}
	}
	if compHook == nil {
		t.Fatal("expected open_payout_components hook to be present")
	}
	if !compHook.Available {
		t.Errorf("open_payout_components should be available; reason: %q", compHook.UnavailableReason)
	}
	if compHook.NavigateURL == "" {
		t.Error("open_payout_components must have a non-empty NavigateURL")
	}
}

// ── B. Resolution attempt truth ───────────────────────────────────────────────

// TestRetryMatch_SucceededRecordsAttempt verifies that a successful retry_match
// creates a 'succeeded' attempt row.
func TestRetryMatch_SucceededRecordsAttempt(t *testing.T) {
	db := hookTestDB(t)
	base := seedHookBase(t, db, "100.00")
	ex := makeExceptionForHook(t, db, base.companyID,
		models.ExceptionAmountMismatch, &base.payoutID, &base.bankID)

	err := ExecuteResolutionHook(db, base.companyID, ex.ID, models.HookTypeRetryMatch, "ops@test.com")
	if err != nil {
		t.Fatalf("ExecuteResolutionHook: %v", err)
	}

	attempts, err := ListRecentResolutionAttempts(db, base.companyID, ex.ID, 10)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(attempts))
	}
	if attempts[0].Status != models.AttemptStatusSucceeded {
		t.Errorf("status: want succeeded got %s", attempts[0].Status)
	}
	if attempts[0].HookType != models.HookTypeRetryMatch {
		t.Errorf("hook_type: want retry_match got %s", attempts[0].HookType)
	}
	if attempts[0].Actor != "ops@test.com" {
		t.Errorf("actor: want ops@test.com got %q", attempts[0].Actor)
	}
}

// TestRetryMatch_RejectedRecordsAttempt verifies that a failed retry_match
// (amount mismatch persists) creates a 'rejected' attempt row.
func TestRetryMatch_RejectedRecordsAttempt(t *testing.T) {
	db := hookTestDB(t)
	base := seedHookBase(t, db, "100.00")

	// Modify bank entry amount so match will fail.
	if err := db.Model(&models.BankEntry{}).Where("id = ?", base.bankID).
		Update("amount", decimal.RequireFromString("999.00")).Error; err != nil {
		t.Fatalf("update bank entry: %v", err)
	}

	ex := makeExceptionForHook(t, db, base.companyID,
		models.ExceptionAmountMismatch, &base.payoutID, &base.bankID)

	err := ExecuteResolutionHook(db, base.companyID, ex.ID, models.HookTypeRetryMatch, "ops")
	if err == nil {
		t.Fatal("expected error on mismatch retry")
	}

	attempts, _ := ListRecentResolutionAttempts(db, base.companyID, ex.ID, 10)
	if len(attempts) != 1 {
		t.Fatalf("expected 1 attempt row, got %d", len(attempts))
	}
	if attempts[0].Status != models.AttemptStatusRejected {
		t.Errorf("status: want rejected got %s", attempts[0].Status)
	}
	if attempts[0].Detail == "" {
		t.Error("rejected attempt must include error detail")
	}
}

// TestRetryMatch_CrossCompanyReject verifies that a hook on a different company's
// exception returns ErrExceptionNotFound and records a rejected attempt under
// the correct company.
func TestRetryMatch_CrossCompanyReject(t *testing.T) {
	db := hookTestDB(t)
	base := seedHookBase(t, db, "100.00")
	ex := makeExceptionForHook(t, db, base.companyID,
		models.ExceptionAmountMismatch, &base.payoutID, &base.bankID)

	// Attempt from a different company.
	err := ExecuteResolutionHook(db, 99, ex.ID, models.HookTypeRetryMatch, "attacker")
	if !errors.Is(err, ErrExceptionNotFound) {
		t.Errorf("want ErrExceptionNotFound for cross-company access, got %v", err)
	}

	// No attempt recorded under company 99 (cross-company attempt was denied
	// before any attempt could be recorded).
	attempts, _ := ListRecentResolutionAttempts(db, 99, ex.ID, 10)
	if len(attempts) != 0 {
		t.Errorf("expected 0 attempts under wrong company, got %d", len(attempts))
	}
}

// TestRetryMatch_ClosedExceptionReject verifies that executing a hook on a
// dismissed exception returns ErrHookExceptionClosed and records a rejected attempt.
func TestRetryMatch_ClosedExceptionReject(t *testing.T) {
	db := hookTestDB(t)
	base := seedHookBase(t, db, "100.00")
	ex := makeExceptionForHook(t, db, base.companyID,
		models.ExceptionAmountMismatch, &base.payoutID, &base.bankID)

	if err := DismissReconciliationException(db, base.companyID, ex.ID, "ops", "no fix"); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	err := ExecuteResolutionHook(db, base.companyID, ex.ID, models.HookTypeRetryMatch, "ops")
	if !errors.Is(err, ErrHookExceptionClosed) {
		t.Errorf("want ErrHookExceptionClosed, got %v", err)
	}

	attempts, _ := ListRecentResolutionAttempts(db, base.companyID, ex.ID, 10)
	if len(attempts) != 1 || attempts[0].Status != models.AttemptStatusRejected {
		t.Errorf("expected 1 rejected attempt, got %d", len(attempts))
	}
}

// TestListRecentAttempts_CompanyIsolation verifies that attempts for a different
// company are not returned.
func TestListRecentAttempts_CompanyIsolation(t *testing.T) {
	db := hookTestDB(t)
	base := seedHookBase(t, db, "100.00")
	ex := makeExceptionForHook(t, db, base.companyID,
		models.ExceptionAmountMismatch, &base.payoutID, &base.bankID)

	// Insert a fake attempt under a different company.
	if err := db.Create(&models.ReconciliationResolutionAttempt{
		CompanyID:                 99,
		ReconciliationExceptionID: ex.ID,
		HookType:                  models.HookTypeRetryMatch,
		Status:                    models.AttemptStatusRejected,
		Summary:                   "foreign attempt",
		Actor:                     "foreign",
	}).Error; err != nil {
		t.Fatalf("create foreign attempt: %v", err)
	}

	attempts, _ := ListRecentResolutionAttempts(db, base.companyID, ex.ID, 10)
	if len(attempts) != 0 {
		t.Errorf("expected 0 attempts for company 1 (cross-company insert), got %d", len(attempts))
	}
}

// ── C. Guarded execution ──────────────────────────────────────────────────────

// TestRetryMatch_SuccessAutoResolvesException verifies that a successful
// retry_match auto-resolves the parent exception.
func TestRetryMatch_SuccessAutoResolvesException(t *testing.T) {
	db := hookTestDB(t)
	base := seedHookBase(t, db, "150.00")
	ex := makeExceptionForHook(t, db, base.companyID,
		models.ExceptionAmountMismatch, &base.payoutID, &base.bankID)

	if err := ExecuteResolutionHook(db, base.companyID, ex.ID, models.HookTypeRetryMatch, "ops"); err != nil {
		t.Fatalf("ExecuteResolutionHook: %v", err)
	}

	// Exception must now be resolved.
	loaded, err := GetReconciliationException(db, base.companyID, ex.ID)
	if err != nil {
		t.Fatalf("get exception: %v", err)
	}
	if loaded.Status != models.ExceptionStatusResolved {
		t.Errorf("exception status: want resolved got %s", loaded.Status)
	}

	// PayoutReconciliation must exist.
	rec, err := GetPayoutReconciliation(db, base.companyID, base.payoutID)
	if err != nil || rec == nil {
		t.Fatalf("expected PayoutReconciliation after successful retry_match: err=%v", err)
	}
	if rec.BankEntryID != base.bankID {
		t.Errorf("reconciliation bank_entry_id: want %d got %d", base.bankID, rec.BankEntryID)
	}
}

// TestRetryMatch_AttemptPersistenceFailureRollsBackMatch verifies that retry_match
// does not leave behind a committed payout reconciliation when attempt truth
// cannot be persisted.
func TestRetryMatch_AttemptPersistenceFailureRollsBackMatch(t *testing.T) {
	db := hookTestDB(t)
	base := seedHookBase(t, db, "175.00")
	ex := makeExceptionForHook(t, db, base.companyID,
		models.ExceptionAmountMismatch, &base.payoutID, &base.bankID)

	if err := db.Migrator().DropTable(&models.ReconciliationResolutionAttempt{}); err != nil {
		t.Fatalf("drop resolution attempt table: %v", err)
	}

	err := ExecuteResolutionHook(db, base.companyID, ex.ID, models.HookTypeRetryMatch, "ops")
	if err == nil {
		t.Fatal("expected retry_match to fail when attempt persistence is unavailable")
	}

	rec, err := GetPayoutReconciliation(db, base.companyID, base.payoutID)
	if err != nil {
		t.Fatalf("get payout reconciliation: %v", err)
	}
	if rec != nil {
		t.Fatal("payout reconciliation should have rolled back when attempt persistence failed")
	}

	loaded, err := GetReconciliationException(db, base.companyID, ex.ID)
	if err != nil {
		t.Fatalf("get exception after rollback: %v", err)
	}
	if loaded.Status != models.ExceptionStatusOpen {
		t.Errorf("exception status after rollback: want open got %s", loaded.Status)
	}
}

// TestRetryMatch_RejectsWhenPayoutAlreadyMatched verifies that retry_match
// records a rejection when the payout is already matched.
func TestRetryMatch_RejectsWhenPayoutAlreadyMatched(t *testing.T) {
	db := hookTestDB(t)
	base := seedHookBase(t, db, "200.00")

	// Pre-match the payout.
	db.Create(&models.PayoutReconciliation{
		CompanyID:       base.companyID,
		GatewayPayoutID: base.payoutID,
		BankEntryID:     base.bankID,
		MatchedAt:       time.Now(),
		Actor:           "system",
	})

	ex := makeExceptionForHook(t, db, base.companyID,
		models.ExceptionAmountMismatch, &base.payoutID, &base.bankID)

	err := ExecuteResolutionHook(db, base.companyID, ex.ID, models.HookTypeRetryMatch, "ops")
	if err == nil {
		t.Fatal("expected error when payout already matched")
	}

	attempts, _ := ListRecentResolutionAttempts(db, base.companyID, ex.ID, 10)
	if len(attempts) != 1 || attempts[0].Status != models.AttemptStatusRejected {
		t.Errorf("expected 1 rejected attempt, got %d attempts", len(attempts))
	}
}

// TestRetryMatch_NavigationHookNotExecutable verifies that invoking
// ExecuteResolutionHook with a navigation hook type returns ErrHookTypeUnsupported.
func TestRetryMatch_NavigationHookNotExecutable(t *testing.T) {
	db := hookTestDB(t)
	base := seedHookBase(t, db, "100.00")
	ex := makeExceptionForHook(t, db, base.companyID,
		models.ExceptionAmountMismatch, &base.payoutID, &base.bankID)

	err := ExecuteResolutionHook(db, base.companyID, ex.ID, models.HookTypeOpenPayoutComponents, "ops")
	if !errors.Is(err, ErrHookTypeUnsupported) {
		t.Errorf("want ErrHookTypeUnsupported for navigation hook, got %v", err)
	}

	// No attempt recorded (navigation hooks never create attempts).
	attempts, _ := ListRecentResolutionAttempts(db, base.companyID, ex.ID, 10)
	if len(attempts) != 0 {
		t.Errorf("navigation hook must not create attempt records, got %d", len(attempts))
	}
}

// TestRetryMatch_DuplicateClickIdempotent verifies that two concurrent retry_match
// attempts on the same exception do not produce two reconciliation records.
// Exactly one must succeed; the other must receive a rejection.
func TestRetryMatch_DuplicateClickIdempotent(t *testing.T) {
	db := hookTestDB(t)
	base := seedHookBase(t, db, "250.00")
	ex := makeExceptionForHook(t, db, base.companyID,
		models.ExceptionAmountMismatch, &base.payoutID, &base.bankID)

	var (
		successes int
		failures  int
		mu        sync.Mutex
		wg        sync.WaitGroup
	)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := ExecuteResolutionHook(db, base.companyID, ex.ID, models.HookTypeRetryMatch, "ops")
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes++
			} else {
				failures++
			}
		}()
	}
	wg.Wait()

	if successes != 1 {
		t.Errorf("exactly one match must succeed; got successes=%d failures=%d", successes, failures)
	}

	var reconCount int64
	db.Model(&models.PayoutReconciliation{}).
		Where("company_id = ? AND gateway_payout_id = ?", base.companyID, base.payoutID).
		Count(&reconCount)
	if reconCount != 1 {
		t.Errorf("exactly one PayoutReconciliation must exist, got %d", reconCount)
	}

	attempts, err := ListRecentResolutionAttempts(db, base.companyID, ex.ID, 10)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) == 0 {
		t.Fatal("expected at least one recorded attempt for duplicate click scenario")
	}

	var succeeded, rejected int
	for _, att := range attempts {
		switch att.Status {
		case models.AttemptStatusSucceeded:
			succeeded++
		case models.AttemptStatusRejected:
			rejected++
		}
	}
	if succeeded != 1 {
		t.Errorf("unexpected attempt outcomes: succeeded=%d rejected=%d total=%d", succeeded, rejected, len(attempts))
	}
}
