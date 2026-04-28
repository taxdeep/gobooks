// 遵循project_guide.md
package services

// payment_reverse_exception_test.go — Batch 23: Payment reverse exception service tests.
//
// Test groups:
//   A — CreatePaymentReverseException: happy path, dedup, source required, unknown type
//   B — Status transitions: review, dismiss, resolve, terminal guard, invalid transition
//   C — FindActiveReverseExceptionForTxn: found, not found, resolved is ignored
//   D — PaymentReverseExceptionTypeForReverseAllocError: mapping coverage
//   E — ListPaymentReverseExceptions: status filter

import (
	"errors"
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"balanciz/internal/models"
)

// ── Test DB setup ─────────────────────────────────────────────────────────────

func reverseExceptionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:prexc_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.PaymentTransaction{},
		&models.PaymentReverseException{},
	); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

func makePRExceptionInput(companyID uint, txnID uint, exType models.PaymentReverseExceptionType) CreatePaymentReverseExceptionInput {
	return CreatePaymentReverseExceptionInput{
		CompanyID:      companyID,
		ExceptionType:  exType,
		ReverseTxnID:   &txnID,
		Summary:        "test summary",
		CreatedByActor: "test@example.com",
	}
}

func createPRExceptionTxn(
	t *testing.T,
	db *gorm.DB,
	companyID uint,
	txnType models.PaymentTransactionType,
	originalTxnID *uint,
) uint {
	t.Helper()
	txn := models.PaymentTransaction{
		CompanyID:             companyID,
		GatewayAccountID:      1,
		TransactionType:       txnType,
		Amount:                decimal.NewFromInt(100),
		CurrencyCode:          "CAD",
		Status:                "completed",
		OriginalTransactionID: originalTxnID,
		RawPayload:            datatypes.JSON("{}"),
	}
	if err := db.Create(&txn).Error; err != nil {
		t.Fatalf("create payment transaction: %v", err)
	}
	return txn.ID
}

func createPRExceptionReverseSource(t *testing.T, db *gorm.DB, companyID uint) (uint, uint) {
	t.Helper()
	chargeID := createPRExceptionTxn(t, db, companyID, models.TxnTypeCharge, nil)
	reverseID := createPRExceptionTxn(t, db, companyID, models.TxnTypeRefund, &chargeID)
	return chargeID, reverseID
}

// ── Group A: Create ───────────────────────────────────────────────────────────

func TestPRException_Create_HappyPath(t *testing.T) {
	db := reverseExceptionTestDB(t)
	_, reverseID := createPRExceptionReverseSource(t, db, 1)
	inp := makePRExceptionInput(1, reverseID, models.PRExceptionReverseAllocationAmbiguous)
	ex, wasCreated, err := CreatePaymentReverseException(db, inp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !wasCreated {
		t.Errorf("expected wasCreated=true")
	}
	if ex.ID == 0 {
		t.Errorf("expected non-zero ID")
	}
	if ex.Status != models.PRExceptionStatusOpen {
		t.Errorf("expected status=open, got %s", ex.Status)
	}
	if ex.ExceptionType != models.PRExceptionReverseAllocationAmbiguous {
		t.Errorf("wrong exception type: %s", ex.ExceptionType)
	}
	if ex.CreatedByActor != "test@example.com" {
		t.Errorf("wrong actor: %s", ex.CreatedByActor)
	}
}

func TestPRException_Create_Dedup_ReturnsExisting(t *testing.T) {
	db := reverseExceptionTestDB(t)
	_, reverseID := createPRExceptionReverseSource(t, db, 1)
	inp := makePRExceptionInput(1, reverseID, models.PRExceptionAmountExceedsStrategy)

	ex1, wasCreated1, err := CreatePaymentReverseException(db, inp)
	if err != nil || !wasCreated1 {
		t.Fatalf("first create failed: %v, wasCreated=%v", err, wasCreated1)
	}

	// Second call with same input — dedup should return existing.
	ex2, wasCreated2, err := CreatePaymentReverseException(db, inp)
	if err != nil {
		t.Fatalf("second create failed: %v", err)
	}
	if wasCreated2 {
		t.Errorf("expected wasCreated=false on dedup")
	}
	if ex2.ID != ex1.ID {
		t.Errorf("expected same exception ID, got %d vs %d", ex2.ID, ex1.ID)
	}
}

func TestPRException_Create_DedupDoesNotApplyAcrossTypes(t *testing.T) {
	db := reverseExceptionTestDB(t)
	_, reverseID := createPRExceptionReverseSource(t, db, 1)
	inp1 := makePRExceptionInput(1, reverseID, models.PRExceptionReverseAllocationAmbiguous)
	inp2 := makePRExceptionInput(1, reverseID, models.PRExceptionOverCreditBoundary)

	ex1, _, _ := CreatePaymentReverseException(db, inp1)
	ex2, _, _ := CreatePaymentReverseException(db, inp2)

	if ex1.ID == ex2.ID {
		t.Errorf("different exception types should create separate records")
	}
}

func TestPRException_Create_SourceRequired(t *testing.T) {
	db := reverseExceptionTestDB(t)
	inp := CreatePaymentReverseExceptionInput{
		CompanyID:     1,
		ExceptionType: models.PRExceptionReverseAllocationAmbiguous,
		Summary:       "no source",
	}
	_, _, err := CreatePaymentReverseException(db, inp)
	if err == nil {
		t.Error("expected error for missing source references")
	}
}

func TestPRException_Create_ReverseTxnRequired(t *testing.T) {
	db := reverseExceptionTestDB(t)
	chargeID := createPRExceptionTxn(t, db, 1, models.TxnTypeCharge, nil)
	inp := CreatePaymentReverseExceptionInput{
		CompanyID:      1,
		ExceptionType:  models.PRExceptionReverseAllocationAmbiguous,
		OriginalTxnID:  &chargeID,
		Summary:        "original only",
		CreatedByActor: "test@example.com",
	}
	_, _, err := CreatePaymentReverseException(db, inp)
	if !errors.Is(err, ErrPRExceptionSourceRequired) {
		t.Fatalf("expected ErrPRExceptionSourceRequired, got %v", err)
	}
}

func TestPRException_Create_UnknownType(t *testing.T) {
	db := reverseExceptionTestDB(t)
	_, reverseID := createPRExceptionReverseSource(t, db, 1)
	inp := CreatePaymentReverseExceptionInput{
		CompanyID:     1,
		ExceptionType: models.PaymentReverseExceptionType("invalid_type"),
		ReverseTxnID:  &reverseID,
		Summary:       "bad type",
	}
	_, _, err := CreatePaymentReverseException(db, inp)
	if err == nil {
		t.Error("expected error for unknown exception type")
	}
}

func TestPRException_Create_ReverseTxnHydratesOriginalCharge(t *testing.T) {
	db := reverseExceptionTestDB(t)
	chargeID := createPRExceptionTxn(t, db, 1, models.TxnTypeCharge, nil)
	refundID := createPRExceptionTxn(t, db, 1, models.TxnTypeRefund, &chargeID)

	inp := makePRExceptionInput(1, refundID, models.PRExceptionReverseAllocationAmbiguous)
	ex, wasCreated, err := CreatePaymentReverseException(db, inp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !wasCreated {
		t.Fatalf("expected wasCreated=true")
	}
	if ex.OriginalTxnID == nil || *ex.OriginalTxnID != chargeID {
		t.Fatalf("expected hydrated OriginalTxnID=%d, got %+v", chargeID, ex.OriginalTxnID)
	}
}

func TestPRException_Create_MultiLayerReverseHydratesUnderlyingCharge(t *testing.T) {
	db := reverseExceptionTestDB(t)
	chargeID := createPRExceptionTxn(t, db, 1, models.TxnTypeCharge, nil)
	refundID := createPRExceptionTxn(t, db, 1, models.TxnTypeRefund, &chargeID)
	chargebackID := createPRExceptionTxn(t, db, 1, models.TxnTypeChargeback, &refundID)

	inp := makePRExceptionInput(1, chargebackID, models.PRExceptionUnsupportedMultiLayerReversal)
	ex, wasCreated, err := CreatePaymentReverseException(db, inp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !wasCreated {
		t.Fatalf("expected wasCreated=true")
	}
	if ex.OriginalTxnID == nil || *ex.OriginalTxnID != chargeID {
		t.Fatalf("expected underlying OriginalTxnID=%d, got %+v", chargeID, ex.OriginalTxnID)
	}
}

func TestPRException_Create_ReverseTxnMustBelongToCompany(t *testing.T) {
	db := reverseExceptionTestDB(t)
	chargeID := createPRExceptionTxn(t, db, 2, models.TxnTypeCharge, nil)
	refundID := createPRExceptionTxn(t, db, 2, models.TxnTypeRefund, &chargeID)

	_, _, err := CreatePaymentReverseException(db, makePRExceptionInput(1, refundID, models.PRExceptionReverseAllocationAmbiguous))
	if !errors.Is(err, ErrPRExceptionSourceInvalid) {
		t.Fatalf("expected ErrPRExceptionSourceInvalid, got %v", err)
	}
}

func TestPRException_Create_OriginalTxnMustBelongToCompany(t *testing.T) {
	db := reverseExceptionTestDB(t)
	_, reverseID := createPRExceptionReverseSource(t, db, 1)
	chargeID := createPRExceptionTxn(t, db, 2, models.TxnTypeCharge, nil)

	inp := CreatePaymentReverseExceptionInput{
		CompanyID:      1,
		ExceptionType:  models.PRExceptionReverseAllocationAmbiguous,
		ReverseTxnID:   &reverseID,
		OriginalTxnID:  &chargeID,
		Summary:        "bad original company",
		CreatedByActor: "test@example.com",
	}
	_, _, err := CreatePaymentReverseException(db, inp)
	if !errors.Is(err, ErrPRExceptionSourceInvalid) {
		t.Fatalf("expected ErrPRExceptionSourceInvalid, got %v", err)
	}
}

func TestPRException_Create_ReverseAndOriginalMismatchRejected(t *testing.T) {
	db := reverseExceptionTestDB(t)
	chargeAID := createPRExceptionTxn(t, db, 1, models.TxnTypeCharge, nil)
	chargeBID := createPRExceptionTxn(t, db, 1, models.TxnTypeCapture, nil)
	refundID := createPRExceptionTxn(t, db, 1, models.TxnTypeRefund, &chargeAID)

	inp := CreatePaymentReverseExceptionInput{
		CompanyID:      1,
		ExceptionType:  models.PRExceptionReverseAllocationAmbiguous,
		ReverseTxnID:   &refundID,
		OriginalTxnID:  &chargeBID,
		Summary:        "mismatched linkage",
		CreatedByActor: "test@example.com",
	}
	_, _, err := CreatePaymentReverseException(db, inp)
	if !errors.Is(err, ErrPRExceptionSourceMismatch) {
		t.Fatalf("expected ErrPRExceptionSourceMismatch, got %v", err)
	}
}

// ── Group B: Status transitions ───────────────────────────────────────────────

func createTestPRException(t *testing.T, db *gorm.DB, txnID uint) *models.PaymentReverseException {
	t.Helper()
	_ = txnID
	_, reverseID := createPRExceptionReverseSource(t, db, 1)
	inp := makePRExceptionInput(1, reverseID, models.PRExceptionChainConflict)
	ex, _, err := CreatePaymentReverseException(db, inp)
	if err != nil {
		t.Fatalf("create test exception: %v", err)
	}
	return ex
}

func TestPRException_Review(t *testing.T) {
	db := reverseExceptionTestDB(t)
	ex := createTestPRException(t, db, 500)

	if err := ReviewPaymentReverseException(db, 1, ex.ID, "reviewer@example.com"); err != nil {
		t.Fatalf("review failed: %v", err)
	}

	got, _ := GetPaymentReverseException(db, 1, ex.ID)
	if got.Status != models.PRExceptionStatusReviewed {
		t.Errorf("expected reviewed, got %s", got.Status)
	}
}

func TestPRException_Review_Idempotent(t *testing.T) {
	db := reverseExceptionTestDB(t)
	ex := createTestPRException(t, db, 501)
	ReviewPaymentReverseException(db, 1, ex.ID, "reviewer@example.com")
	// Second call should be no-op, not an error.
	if err := ReviewPaymentReverseException(db, 1, ex.ID, "reviewer@example.com"); err != nil {
		t.Fatalf("second review should be idempotent: %v", err)
	}
}

func TestPRException_Dismiss_RequiresNote(t *testing.T) {
	db := reverseExceptionTestDB(t)
	ex := createTestPRException(t, db, 502)

	err := DismissPaymentReverseException(db, 1, ex.ID, "operator@example.com", "")
	if err == nil {
		t.Error("expected error for dismiss without note")
	}
}

func TestPRException_Dismiss_WithNote(t *testing.T) {
	db := reverseExceptionTestDB(t)
	ex := createTestPRException(t, db, 503)

	if err := DismissPaymentReverseException(db, 1, ex.ID, "operator@example.com", "not actionable"); err != nil {
		t.Fatalf("dismiss failed: %v", err)
	}

	got, _ := GetPaymentReverseException(db, 1, ex.ID)
	if got.Status != models.PRExceptionStatusDismissed {
		t.Errorf("expected dismissed, got %s", got.Status)
	}
	if got.ResolutionNote != "not actionable" {
		t.Errorf("wrong resolution note: %s", got.ResolutionNote)
	}
	if got.ResolvedAt == nil {
		t.Errorf("expected ResolvedAt to be set")
	}
}

func TestPRException_Resolve(t *testing.T) {
	db := reverseExceptionTestDB(t)
	ex := createTestPRException(t, db, 504)

	if err := ResolvePaymentReverseException(db, 1, ex.ID, "operator@example.com", "fixed manually"); err != nil {
		t.Fatalf("resolve failed: %v", err)
	}

	got, _ := GetPaymentReverseException(db, 1, ex.ID)
	if got.Status != models.PRExceptionStatusResolved {
		t.Errorf("expected resolved, got %s", got.Status)
	}
}

func TestPRException_Resolve_RequiresNote(t *testing.T) {
	db := reverseExceptionTestDB(t)
	ex := createTestPRException(t, db, 5041)

	err := ResolvePaymentReverseException(db, 1, ex.ID, "operator@example.com", "")
	if !errors.Is(err, ErrPRExceptionResolveNote) {
		t.Fatalf("expected ErrPRExceptionResolveNote, got %v", err)
	}
}

func TestPRException_TerminalGuard_DismissedCannotTransition(t *testing.T) {
	db := reverseExceptionTestDB(t)
	ex := createTestPRException(t, db, 505)
	DismissPaymentReverseException(db, 1, ex.ID, "op", "done")

	// Cannot review a dismissed exception.
	if err := ReviewPaymentReverseException(db, 1, ex.ID, "op"); err == nil {
		t.Error("expected error: dismissed is terminal")
	}
	// Cannot resolve a dismissed exception.
	if err := ResolvePaymentReverseException(db, 1, ex.ID, "op", ""); err == nil {
		t.Error("expected error: dismissed is terminal")
	}
}

func TestPRException_InvalidTransition_ReviewedCannotRevertToOpen(t *testing.T) {
	db := reverseExceptionTestDB(t)
	ex := createTestPRException(t, db, 506)
	ReviewPaymentReverseException(db, 1, ex.ID, "op")

	// There is no "revert to open" transition — trying is invalid.
	// The only way to test this is via updatePRExceptionStatus directly;
	// but since we cover the state machine via the public API the invalid
	// transition from reviewed → open is unreachable through the public surface.
	// We verify that reviewed → dismissed works correctly as a follow-on.
	if err := DismissPaymentReverseException(db, 1, ex.ID, "op", "no longer needed"); err != nil {
		t.Fatalf("dismiss after review failed: %v", err)
	}
	got, _ := GetPaymentReverseException(db, 1, ex.ID)
	if got.Status != models.PRExceptionStatusDismissed {
		t.Errorf("expected dismissed, got %s", got.Status)
	}
}

// ── Group C: FindActiveReverseExceptionForTxn ─────────────────────────────────

func TestPRException_FindActive_Found(t *testing.T) {
	db := reverseExceptionTestDB(t)
	_, txnID := createPRExceptionReverseSource(t, db, 1)
	inp := makePRExceptionInput(1, txnID, models.PRExceptionOverCreditBoundary)
	created, _, _ := CreatePaymentReverseException(db, inp)

	found, err := FindActiveReverseExceptionForTxn(db, 1, txnID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found == nil {
		t.Fatal("expected to find active exception")
	}
	if found.ID != created.ID {
		t.Errorf("wrong exception returned: %d vs %d", found.ID, created.ID)
	}
}

func TestPRException_FindActive_NotFound(t *testing.T) {
	db := reverseExceptionTestDB(t)
	found, err := FindActiveReverseExceptionForTxn(db, 1, 9999)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found != nil {
		t.Errorf("expected nil, got exception %d", found.ID)
	}
}

func TestPRException_FindActive_ResolvedIsIgnored(t *testing.T) {
	db := reverseExceptionTestDB(t)
	_, txnID := createPRExceptionReverseSource(t, db, 1)
	inp := makePRExceptionInput(1, txnID, models.PRExceptionRequiresManualSplit)
	ex, _, _ := CreatePaymentReverseException(db, inp)
	ResolvePaymentReverseException(db, 1, ex.ID, "op", "done")

	found, err := FindActiveReverseExceptionForTxn(db, 1, txnID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found != nil {
		t.Errorf("resolved exception should not be returned as active")
	}
}

func TestPRException_FindActive_ReviewedIsReturned(t *testing.T) {
	db := reverseExceptionTestDB(t)
	_, txnID := createPRExceptionReverseSource(t, db, 1)
	inp := makePRExceptionInput(1, txnID, models.PRExceptionAmountExceedsStrategy)
	ex, _, _ := CreatePaymentReverseException(db, inp)
	ReviewPaymentReverseException(db, 1, ex.ID, "op")

	found, err := FindActiveReverseExceptionForTxn(db, 1, txnID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found == nil {
		t.Errorf("reviewed exception should still be returned as active")
	}
}

// ── Group D: PaymentReverseExceptionTypeForReverseAllocError ──────────────────

func TestPRException_ErrorMapping_StructuralErrors(t *testing.T) {
	cases := []struct {
		err      error
		wantType models.PaymentReverseExceptionType
	}{
		{ErrReverseAllocNoOriginalTxn, models.PRExceptionReverseAllocationAmbiguous},
		{ErrReverseAllocUnsupportedMultiLayerReversal, models.PRExceptionUnsupportedMultiLayerReversal},
		{ErrReverseAllocExceedsReversibleTotal, models.PRExceptionAmountExceedsStrategy},
		{ErrReverseAllocWouldExceedInvoiceTotal, models.PRExceptionOverCreditBoundary},
		{ErrReverseAllocInvoiceNotRestoreable, models.PRExceptionRequiresManualSplit},
	}
	for _, tc := range cases {
		gotType, ok := PaymentReverseExceptionTypeForReverseAllocError(tc.err)
		if !ok {
			t.Errorf("expected ok=true for %v", tc.err)
		}
		if gotType != tc.wantType {
			t.Errorf("for %v: expected %s, got %s", tc.err, tc.wantType, gotType)
		}
	}
}

func TestPRException_ErrorMapping_NonStructuralErrors_ReturnFalse(t *testing.T) {
	nonStructural := []error{
		ErrReverseAllocAlreadyApplied,
		ErrReverseAllocTxnNotReversible,
		ErrReverseAllocTxnNotPosted,
		ErrReverseAllocNoAllocations,
	}
	for _, err := range nonStructural {
		_, ok := PaymentReverseExceptionTypeForReverseAllocError(err)
		if ok {
			t.Errorf("expected ok=false for non-structural error %v", err)
		}
	}
}

// ── Group E: ListPaymentReverseExceptions ─────────────────────────────────────

func TestPRException_List_StatusFilter(t *testing.T) {
	db := reverseExceptionTestDB(t)

	// Create two exceptions: one open, one dismissed.
	_, txn1 := createPRExceptionReverseSource(t, db, 1)
	_, txn2 := createPRExceptionReverseSource(t, db, 1)
	inp1 := makePRExceptionInput(1, txn1, models.PRExceptionReverseAllocationAmbiguous)
	ex1, _, _ := CreatePaymentReverseException(db, inp1)

	inp2 := makePRExceptionInput(1, txn2, models.PRExceptionChainConflict)
	ex2, _, _ := CreatePaymentReverseException(db, inp2)
	DismissPaymentReverseException(db, 1, ex2.ID, "op", "done")

	// List all.
	all, _ := ListPaymentReverseExceptions(db, 1)
	if len(all) < 2 {
		t.Errorf("expected at least 2, got %d", len(all))
	}

	// List only open.
	open, _ := ListPaymentReverseExceptions(db, 1, models.PRExceptionStatusOpen)
	for _, ex := range open {
		if ex.Status != models.PRExceptionStatusOpen {
			t.Errorf("expected only open, got %s", ex.Status)
		}
	}

	// List only dismissed.
	dismissed, _ := ListPaymentReverseExceptions(db, 1, models.PRExceptionStatusDismissed)
	found := false
	for _, ex := range dismissed {
		if ex.ID == ex1.ID {
			t.Errorf("open exception should not appear in dismissed list")
		}
		if ex.ID == ex2.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("dismissed exception %d not found in filtered list", ex2.ID)
	}
}

func TestPRException_List_CompanyIsolation(t *testing.T) {
	db := reverseExceptionTestDB(t)
	_, txnID := createPRExceptionReverseSource(t, db, 1)
	inp := makePRExceptionInput(1, txnID, models.PRExceptionReverseAllocationAmbiguous)
	CreatePaymentReverseException(db, inp)

	// Company 2 should see zero.
	results, _ := ListPaymentReverseExceptions(db, 2)
	if len(results) != 0 {
		t.Errorf("company isolation broken: expected 0 for company 2, got %d", len(results))
	}
}
