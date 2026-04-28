// 遵循project_guide.md
package services

// payment_reverse_allocation_test.go — Batch 22: Multi-allocated payment reverse allocation tests.
//
// Test groups:
//
//  A. Refund reverse allocation
//     TestRefundReverseAlloc_HappyPath_TwoInvoices
//     TestRefundReverseAlloc_PartialRefund_ProportionalSplit
//     TestRefundReverseAlloc_OverpaymentExcess_NotRestoredToInvoice
//     TestRefundReverseAlloc_DuplicateReplay_Blocked
//     TestRefundReverseAlloc_SingleInvoiceSource_Rejected
//
//  B. Chargeback reverse allocation
//     TestChargebackReverseAlloc_HappyPath_TwoInvoices
//     TestChargebackReverseAlloc_PartialChargeback_ProportionalSplit
//     TestChargebackReverseAlloc_OverpaymentExcess_NotRestoredToInvoice
//     TestChargebackReverseAlloc_ConcurrentApply_ExactlyOneSucceeds
//     TestChargebackReverseAlloc_ViaOriginalTransactionID
//
//  C. Dispute lost downstream
//     TestDisputeLostReverseAlloc_HappyPath
//     TestDisputeLostReverseAlloc_MissingOriginalTxn_Rejected
//
//  D. Main chain non-regression
//     TestSingleInvoiceRefund_UnaffectedByBatch22
//     TestSingleInvoiceChargeback_UnaffectedByBatch22
//     TestMutualExclusion_SinglePathBlockedAfterMultiReverseApplied
//
//  E. ComputeReverseAllocationPlan unit tests
//     TestComputePlan_ProportionalSplit
//     TestComputePlan_ExactLastLineRemainder
//     TestComputePlan_OverpaymentCapAtTotalAllocated
//     TestComputePlan_EmptyAllocations_Rejected

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// ── Test DB setup ─────────────────────────────────────────────────────────────

// reverseAllocTestDB creates an in-memory SQLite DB with all tables needed for
// reverse allocation tests.  Reuses the payment_application_test.go helpers.
func reverseAllocTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testPaymentApplicationDB(t) // sets up company / customer / invoice / payment tables
	if err := db.AutoMigrate(
		&models.PaymentAllocation{},
		&models.PaymentReverseAllocation{},
		&models.GatewayDispute{},
	); err != nil {
		t.Fatalf("AutoMigrate reverse alloc tables: %v", err)
	}
	return db
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// setupMultiAllocCharge creates a charge transaction multi-allocated to invoices
// with the given balances.  Returns the chargeID and invoice IDs.
func setupMultiAllocCharge(t *testing.T, db *gorm.DB, s paSetup, chargeAmount decimal.Decimal, invBalances []int64) (chargeID uint, invIDs []uint) {
	t.Helper()
	chargeID = postedChargeTxnNoRequest(t, db, s, chargeAmount)

	lines := make([]AllocationLine, len(invBalances))
	for i, bal := range invBalances {
		invID := newOpenInvoice(t, db, s, t.Name()+"-inv-"+string(rune('A'+i)), bal)
		invIDs = append(invIDs, invID)
		lines[i] = AllocationLine{InvoiceID: invID, Amount: decimal.NewFromInt(bal)}
	}
	if err := AllocatePaymentToMultipleInvoices(db, s.companyID, chargeID, lines, "test"); err != nil {
		t.Fatalf("AllocatePaymentToMultipleInvoices: %v", err)
	}
	return chargeID, invIDs
}

// postedReverseTxn creates a posted refund or chargeback linked to originalTxnID.
// It marks the transaction as posted by directly setting posted_journal_entry_id
// to a sentinel value (bypasses JE account mapping requirements that are not
// relevant to reverse allocation logic under test).
func postedReverseTxn(t *testing.T, db *gorm.DB, s paSetup, txnType models.PaymentTransactionType, amount decimal.Decimal, originalTxnID uint) uint {
	t.Helper()
	txn := models.PaymentTransaction{
		CompanyID:             s.companyID,
		GatewayAccountID:      s.gwID,
		TransactionType:       txnType,
		Amount:                amount,
		OriginalTransactionID: &originalTxnID,
		CurrencyCode:          "",
		Status:                "completed",
		RawPayload:            datatypes.JSON("{}"),
	}
	if err := db.Create(&txn).Error; err != nil {
		t.Fatalf("create reverse txn: %v", err)
	}
	// Mark as posted directly (same sentinel trick as postedChargeTxnNoRequest).
	// The JE itself is not needed for reverse allocation logic.
	jeID := uint(9999)
	if err := db.Model(&txn).Updates(map[string]any{
		"posted_journal_entry_id": jeID,
	}).Error; err != nil {
		t.Fatalf("mark reverse txn as posted: %v", err)
	}
	return txn.ID
}

// assertReverseAllocRecords asserts the count of PaymentReverseAllocation rows for a reverse txn.
func assertReverseAllocRecords(t *testing.T, db *gorm.DB, companyID, reverseTxnID uint, wantCount int) {
	t.Helper()
	rows, err := ListReverseAllocationsForTxn(db, companyID, reverseTxnID)
	if err != nil {
		t.Fatalf("ListReverseAllocationsForTxn: %v", err)
	}
	if len(rows) != wantCount {
		t.Errorf("reverse alloc count for txn %d: want %d got %d", reverseTxnID, wantCount, len(rows))
	}
}

// ── A. Refund reverse allocation ─────────────────────────────────────────────

// TestRefundReverseAlloc_HappyPath_TwoInvoices verifies that a full refund of a
// multi-allocated payment restores each invoice proportionally and writes
// PaymentReverseAllocation records.
func TestRefundReverseAlloc_HappyPath_TwoInvoices(t *testing.T) {
	db := reverseAllocTestDB(t)
	s := setupPayApp(t, db)

	// charge 1000 → INV-A 600, INV-B 400
	chargeID, invIDs := setupMultiAllocCharge(t, db, s, decimal.NewFromInt(1000), []int64{600, 400})

	// Full refund of 1000.
	refundID := postedReverseTxn(t, db, s, models.TxnTypeRefund, decimal.NewFromInt(1000), chargeID)

	err := ApplyRefundReverseAllocations(db, s.companyID, refundID, "op@test.com")
	if err != nil {
		t.Fatalf("ApplyRefundReverseAllocations: %v", err)
	}

	// Each invoice should have its balance restored.
	assertInvoiceBalance(t, db, invIDs[0], decimal.NewFromInt(600), models.InvoiceStatusIssued)
	assertInvoiceBalance(t, db, invIDs[1], decimal.NewFromInt(400), models.InvoiceStatusIssued)

	// Exactly 2 reverse allocation records.
	assertReverseAllocRecords(t, db, s.companyID, refundID, 2)

	// Records reference the correct invoice IDs.
	rows, _ := ListReverseAllocationsForTxn(db, s.companyID, refundID)
	restored := decimal.Zero
	for _, r := range rows {
		restored = restored.Add(r.Amount)
		if r.ReverseType != models.ReverseAllocRefund {
			t.Errorf("expected reverse type refund, got %s", r.ReverseType)
		}
		if r.OriginalTxnID != chargeID {
			t.Errorf("expected OriginalTxnID=%d got %d", chargeID, r.OriginalTxnID)
		}
	}
	if !restored.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("total restored: want 1000 got %s", restored.StringFixed(2))
	}
}

// TestRefundReverseAlloc_PartialRefund_ProportionalSplit verifies proportional
// split when the refund is less than the total allocated.
func TestRefundReverseAlloc_PartialRefund_ProportionalSplit(t *testing.T) {
	db := reverseAllocTestDB(t)
	s := setupPayApp(t, db)

	// charge 1000 → INV-A 600, INV-B 400
	chargeID, invIDs := setupMultiAllocCharge(t, db, s, decimal.NewFromInt(1000), []int64{600, 400})

	// Partial refund of 500.
	refundID := postedReverseTxn(t, db, s, models.TxnTypeRefund, decimal.NewFromInt(500), chargeID)

	err := ApplyRefundReverseAllocations(db, s.companyID, refundID, "op@test.com")
	if err != nil {
		t.Fatalf("ApplyRefundReverseAllocations: %v", err)
	}

	// INV-A: 600/1000 * 500 = 300 restored (was 0 balance, now 300)
	// INV-B: 400/1000 * 500 = 200 restored (was 0 balance, now 200)
	assertInvoiceBalance(t, db, invIDs[0], decimal.NewFromInt(300), models.InvoiceStatusPartiallyPaid)
	assertInvoiceBalance(t, db, invIDs[1], decimal.NewFromInt(200), models.InvoiceStatusPartiallyPaid)
	assertReverseAllocRecords(t, db, s.companyID, refundID, 2)
}

// TestRefundReverseAlloc_OverpaymentExcess_NotRestoredToInvoice verifies that
// a refund equal to the full charge amount only restores the allocated portion
// when there was an overpayment excess that became a CustomerCredit.
func TestRefundReverseAlloc_OverpaymentExcess_NotRestoredToInvoice(t *testing.T) {
	db := reverseAllocTestDB(t)
	s := setupPayApp(t, db)

	// charge 1200, but only 800 was allocated to invoices (200 not allocated — would be excess)
	// Simulate: charge 1200, alloc 500+300=800 to two invoices.
	chargeID, invIDs := setupMultiAllocCharge(t, db, s, decimal.NewFromInt(1200), []int64{500, 300})

	// Full refund of 1200 (includes the 400 that was NOT allocated to any invoice).
	refundID := postedReverseTxn(t, db, s, models.TxnTypeRefund, decimal.NewFromInt(1200), chargeID)

	err := ApplyRefundReverseAllocations(db, s.companyID, refundID, "op@test.com")
	if err != nil {
		t.Fatalf("ApplyRefundReverseAllocations: %v", err)
	}

	// Only 800 should be restored (500+300), not 1200.
	// INV-A: 500/800 * 800 = 500 restored
	// INV-B: 300/800 * 800 = 300 restored
	assertInvoiceBalance(t, db, invIDs[0], decimal.NewFromInt(500), models.InvoiceStatusIssued)
	assertInvoiceBalance(t, db, invIDs[1], decimal.NewFromInt(300), models.InvoiceStatusIssued)

	total := ReverseAllocTotalForTxn(db, s.companyID, refundID)
	if !total.Equal(decimal.NewFromInt(800)) {
		t.Errorf("total restored should be 800 (capped at allocated total), got %s", total.StringFixed(2))
	}
}

// TestRefundReverseAlloc_DuplicateReplay_Blocked verifies that a second apply
// of the same refund transaction is rejected.
func TestRefundReverseAlloc_DuplicateReplay_Blocked(t *testing.T) {
	db := reverseAllocTestDB(t)
	s := setupPayApp(t, db)

	chargeID, _ := setupMultiAllocCharge(t, db, s, decimal.NewFromInt(1000), []int64{600, 400})
	refundID := postedReverseTxn(t, db, s, models.TxnTypeRefund, decimal.NewFromInt(1000), chargeID)

	// First apply succeeds.
	if err := ApplyRefundReverseAllocations(db, s.companyID, refundID, "op"); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	// Second apply is rejected.
	if err := ApplyRefundReverseAllocations(db, s.companyID, refundID, "op"); err == nil {
		t.Fatal("expected error on duplicate apply, got nil")
	}
	// Still exactly 2 records (not 4).
	assertReverseAllocRecords(t, db, s.companyID, refundID, 2)
}

// TestRefundReverseAlloc_ExceedsRemainingReversibleTotal_Rejected verifies that
// a second reverse against the same original charge cannot restore more than the
// unreversed forward-allocation ceiling, even if the invoices were repaid later.
func TestRefundReverseAlloc_ExceedsRemainingReversibleTotal_Rejected(t *testing.T) {
	db := reverseAllocTestDB(t)
	s := setupPayApp(t, db)

	chargeID, invIDs := setupMultiAllocCharge(t, db, s, decimal.NewFromInt(1000), []int64{600, 400})

	firstRefundID := postedReverseTxn(t, db, s, models.TxnTypeRefund, decimal.NewFromInt(500), chargeID)
	if err := ApplyRefundReverseAllocations(db, s.companyID, firstRefundID, "op"); err != nil {
		t.Fatalf("first reverse apply: %v", err)
	}
	assertInvoiceBalance(t, db, invIDs[0], decimal.NewFromInt(300), models.InvoiceStatusPartiallyPaid)
	assertInvoiceBalance(t, db, invIDs[1], decimal.NewFromInt(200), models.InvoiceStatusPartiallyPaid)

	repayID := postedChargeTxnNoRequest(t, db, s, decimal.NewFromInt(500))
	if err := AllocatePaymentToMultipleInvoices(db, s.companyID, repayID, []AllocationLine{
		{InvoiceID: invIDs[0], Amount: decimal.NewFromInt(300)},
		{InvoiceID: invIDs[1], Amount: decimal.NewFromInt(200)},
	}, "test"); err != nil {
		t.Fatalf("repay restored balances: %v", err)
	}
	assertInvoiceBalance(t, db, invIDs[0], decimal.Zero, models.InvoiceStatusPaid)
	assertInvoiceBalance(t, db, invIDs[1], decimal.Zero, models.InvoiceStatusPaid)

	secondRefundID := postedReverseTxn(t, db, s, models.TxnTypeRefund, decimal.NewFromInt(600), chargeID)
	err := ApplyRefundReverseAllocations(db, s.companyID, secondRefundID, "op")
	if !errors.Is(err, ErrReverseAllocExceedsReversibleTotal) {
		t.Fatalf("expected ErrReverseAllocExceedsReversibleTotal, got %v", err)
	}

	assertReverseAllocRecords(t, db, s.companyID, secondRefundID, 0)
	assertInvoiceBalance(t, db, invIDs[0], decimal.Zero, models.InvoiceStatusPaid)
	assertInvoiceBalance(t, db, invIDs[1], decimal.Zero, models.InvoiceStatusPaid)
}

// TestRefundReverseAlloc_SingleInvoiceSource_Rejected verifies that a refund
// linked to a single-invoice charge (no PaymentAllocation rows) is rejected.
func TestRefundReverseAlloc_SingleInvoiceSource_Rejected(t *testing.T) {
	db := reverseAllocTestDB(t)
	s := setupPayApp(t, db)

	// Create a single-invoice charge (no PaymentAllocation rows).
	chargeTxnID := postChargeTxn(t, db, s, 1000)
	ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test")

	// Refund linked to the single-invoice charge.
	refundID := postedReverseTxn(t, db, s, models.TxnTypeRefund, decimal.NewFromInt(1000), chargeTxnID)

	err := ApplyRefundReverseAllocations(db, s.companyID, refundID, "op")
	if err == nil {
		t.Fatal("expected error (no multi-alloc records), got nil")
	}
}

// ── B. Chargeback reverse allocation ──────────────────────────────────────────

// TestChargebackReverseAlloc_HappyPath_TwoInvoices verifies chargeback reverse
// across two invoices with correct record creation.
func TestChargebackReverseAlloc_HappyPath_TwoInvoices(t *testing.T) {
	db := reverseAllocTestDB(t)
	s := setupPayApp(t, db)

	chargeID, invIDs := setupMultiAllocCharge(t, db, s, decimal.NewFromInt(1000), []int64{700, 300})
	cbID := postedReverseTxn(t, db, s, models.TxnTypeChargeback, decimal.NewFromInt(1000), chargeID)

	if err := ApplyChargebackReverseAllocations(db, s.companyID, cbID, "ops"); err != nil {
		t.Fatalf("ApplyChargebackReverseAllocations: %v", err)
	}

	assertInvoiceBalance(t, db, invIDs[0], decimal.NewFromInt(700), models.InvoiceStatusIssued)
	assertInvoiceBalance(t, db, invIDs[1], decimal.NewFromInt(300), models.InvoiceStatusIssued)
	assertReverseAllocRecords(t, db, s.companyID, cbID, 2)

	rows, _ := ListReverseAllocationsForTxn(db, s.companyID, cbID)
	for _, r := range rows {
		if r.ReverseType != models.ReverseAllocChargeback {
			t.Errorf("expected chargeback type, got %s", r.ReverseType)
		}
	}
}

// TestChargebackReverseAlloc_PartialChargeback_ProportionalSplit verifies a
// partial chargeback splits proportionally.
func TestChargebackReverseAlloc_PartialChargeback_ProportionalSplit(t *testing.T) {
	db := reverseAllocTestDB(t)
	s := setupPayApp(t, db)

	// charge 1000 → INV-A 600, INV-B 400
	chargeID, invIDs := setupMultiAllocCharge(t, db, s, decimal.NewFromInt(1000), []int64{600, 400})
	cbID := postedReverseTxn(t, db, s, models.TxnTypeChargeback, decimal.NewFromInt(200), chargeID)

	if err := ApplyChargebackReverseAllocations(db, s.companyID, cbID, "ops"); err != nil {
		t.Fatalf("ApplyChargebackReverseAllocations: %v", err)
	}

	// INV-A: 600/1000 * 200 = 120
	// INV-B: 400/1000 * 200 = 80
	assertInvoiceBalance(t, db, invIDs[0], decimal.NewFromInt(120), models.InvoiceStatusPartiallyPaid)
	assertInvoiceBalance(t, db, invIDs[1], decimal.NewFromInt(80), models.InvoiceStatusPartiallyPaid)
}

// TestChargebackReverseAlloc_OverpaymentExcess_NotRestoredToInvoice verifies
// that the unallocated portion of an over-charged payment is not pushed to invoices.
func TestChargebackReverseAlloc_OverpaymentExcess_NotRestoredToInvoice(t *testing.T) {
	db := reverseAllocTestDB(t)
	s := setupPayApp(t, db)

	// charge 1000, alloc only 600+300=900 to invoices.
	chargeID, invIDs := setupMultiAllocCharge(t, db, s, decimal.NewFromInt(1000), []int64{600, 300})
	cbID := postedReverseTxn(t, db, s, models.TxnTypeChargeback, decimal.NewFromInt(1000), chargeID)

	if err := ApplyChargebackReverseAllocations(db, s.companyID, cbID, "ops"); err != nil {
		t.Fatalf("ApplyChargebackReverseAllocations: %v", err)
	}

	// Only 900 restored (capped at allocated total).
	total := ReverseAllocTotalForTxn(db, s.companyID, cbID)
	if !total.Equal(decimal.NewFromInt(900)) {
		t.Errorf("expected 900 restored, got %s", total.StringFixed(2))
	}
	assertInvoiceBalance(t, db, invIDs[0], decimal.NewFromInt(600), models.InvoiceStatusIssued)
	assertInvoiceBalance(t, db, invIDs[1], decimal.NewFromInt(300), models.InvoiceStatusIssued)
}

// TestChargebackReverseAlloc_ExceedsRemainingReversibleTotal_Rejected verifies
// the same remaining-ceiling guard for chargebacks.
func TestChargebackReverseAlloc_ExceedsRemainingReversibleTotal_Rejected(t *testing.T) {
	db := reverseAllocTestDB(t)
	s := setupPayApp(t, db)

	chargeID, invIDs := setupMultiAllocCharge(t, db, s, decimal.NewFromInt(1000), []int64{600, 400})

	firstCBID := postedReverseTxn(t, db, s, models.TxnTypeChargeback, decimal.NewFromInt(400), chargeID)
	if err := ApplyChargebackReverseAllocations(db, s.companyID, firstCBID, "ops"); err != nil {
		t.Fatalf("first chargeback reverse: %v", err)
	}
	assertInvoiceBalance(t, db, invIDs[0], decimal.NewFromInt(240), models.InvoiceStatusPartiallyPaid)
	assertInvoiceBalance(t, db, invIDs[1], decimal.NewFromInt(160), models.InvoiceStatusPartiallyPaid)

	repayID := postedChargeTxnNoRequest(t, db, s, decimal.NewFromInt(400))
	if err := AllocatePaymentToMultipleInvoices(db, s.companyID, repayID, []AllocationLine{
		{InvoiceID: invIDs[0], Amount: decimal.NewFromInt(240)},
		{InvoiceID: invIDs[1], Amount: decimal.NewFromInt(160)},
	}, "test"); err != nil {
		t.Fatalf("repay restored balances: %v", err)
	}
	assertInvoiceBalance(t, db, invIDs[0], decimal.Zero, models.InvoiceStatusPaid)
	assertInvoiceBalance(t, db, invIDs[1], decimal.Zero, models.InvoiceStatusPaid)

	secondCBID := postedReverseTxn(t, db, s, models.TxnTypeChargeback, decimal.NewFromInt(700), chargeID)
	err := ApplyChargebackReverseAllocations(db, s.companyID, secondCBID, "ops")
	if !errors.Is(err, ErrReverseAllocExceedsReversibleTotal) {
		t.Fatalf("expected ErrReverseAllocExceedsReversibleTotal, got %v", err)
	}

	assertReverseAllocRecords(t, db, s.companyID, secondCBID, 0)
	assertInvoiceBalance(t, db, invIDs[0], decimal.Zero, models.InvoiceStatusPaid)
	assertInvoiceBalance(t, db, invIDs[1], decimal.Zero, models.InvoiceStatusPaid)
}

// TestChargebackReverseAlloc_ConcurrentApply_ExactlyOneSucceeds tests that
// concurrent applies of the same chargeback transaction result in exactly one
// success (unique constraint acts as race guard).
func TestChargebackReverseAlloc_ConcurrentApply_ExactlyOneSucceeds(t *testing.T) {
	db := reverseAllocTestDB(t)
	s := setupPayApp(t, db)

	chargeID, _ := setupMultiAllocCharge(t, db, s, decimal.NewFromInt(1000), []int64{600, 400})
	cbID := postedReverseTxn(t, db, s, models.TxnTypeChargeback, decimal.NewFromInt(1000), chargeID)

	const goroutines = 5
	results := make([]error, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = ApplyChargebackReverseAllocations(db, s.companyID, cbID, "ops")
		}(i)
	}
	wg.Wait()

	// Exactly one should succeed.
	successCount := 0
	for _, err := range results {
		if err == nil {
			successCount++
		}
	}
	if successCount != 1 {
		t.Errorf("expected exactly 1 success, got %d", successCount)
	}

	// Exactly 2 reverse alloc records (not 2 × successes).
	assertReverseAllocRecords(t, db, s.companyID, cbID, 2)
}

// TestChargebackReverseAlloc_ViaOriginalTransactionID verifies that a
// chargeback linked via OriginalTransactionID (not PaymentRequestID) correctly
// resolves the original charge and its allocations.
func TestChargebackReverseAlloc_ViaOriginalTransactionID(t *testing.T) {
	db := reverseAllocTestDB(t)
	s := setupPayApp(t, db)

	// chargeID has no PaymentRequestID (postedChargeTxnNoRequest).
	chargeID, invIDs := setupMultiAllocCharge(t, db, s, decimal.NewFromInt(800), []int64{500, 300})

	// Chargeback linked only via OriginalTransactionID.
	cbID := postedReverseTxn(t, db, s, models.TxnTypeChargeback, decimal.NewFromInt(800), chargeID)

	if err := ApplyChargebackReverseAllocations(db, s.companyID, cbID, "ops"); err != nil {
		t.Fatalf("ApplyChargebackReverseAllocations: %v", err)
	}

	assertInvoiceBalance(t, db, invIDs[0], decimal.NewFromInt(500), models.InvoiceStatusIssued)
	assertInvoiceBalance(t, db, invIDs[1], decimal.NewFromInt(300), models.InvoiceStatusIssued)
}

// ── C. Dispute lost downstream ────────────────────────────────────────────────

// TestDisputeLostReverseAlloc_HappyPath verifies that the dispute_lost path
// correctly uses ReverseAllocDisputeLost type.
func TestDisputeLostReverseAlloc_HappyPath(t *testing.T) {
	db := reverseAllocTestDB(t)
	s := setupPayApp(t, db)

	chargeID, invIDs := setupMultiAllocCharge(t, db, s, decimal.NewFromInt(500), []int64{300, 200})
	// Dispute-lost generates a chargeback-type txn linked via OriginalTransactionID.
	cbID := postedReverseTxn(t, db, s, models.TxnTypeChargeback, decimal.NewFromInt(500), chargeID)

	if err := ApplyDisputeLostReverseAllocations(db, s.companyID, cbID, "ops"); err != nil {
		t.Fatalf("ApplyDisputeLostReverseAllocations: %v", err)
	}

	assertInvoiceBalance(t, db, invIDs[0], decimal.NewFromInt(300), models.InvoiceStatusIssued)
	assertInvoiceBalance(t, db, invIDs[1], decimal.NewFromInt(200), models.InvoiceStatusIssued)

	rows, _ := ListReverseAllocationsForTxn(db, s.companyID, cbID)
	for _, r := range rows {
		if r.ReverseType != models.ReverseAllocDisputeLost {
			t.Errorf("expected dispute_lost type, got %s", r.ReverseType)
		}
	}
}

// TestChargebackReverseAlloc_DisputeLinkedChargeback_UsesDisputeLostType verifies
// that the real runtime chargeback entry point still records dispute_lost when
// the source chargeback came from a lost dispute.
func TestChargebackReverseAlloc_DisputeLinkedChargeback_UsesDisputeLostType(t *testing.T) {
	db := reverseAllocTestDB(t)
	s := setupPayApp(t, db)

	chargeID, _ := setupMultiAllocCharge(t, db, s, decimal.NewFromInt(500), []int64{300, 200})
	cbID := postedReverseTxn(t, db, s, models.TxnTypeChargeback, decimal.NewFromInt(500), chargeID)

	dispute := models.GatewayDispute{
		CompanyID:               s.companyID,
		GatewayAccountID:        s.gwID,
		ProviderDisputeID:       t.Name(),
		PaymentTransactionID:    chargeID,
		Amount:                  decimal.NewFromInt(500),
		CurrencyCode:            "CAD",
		Status:                  models.DisputeStatusLost,
		OpenedAt:                time.Now(),
		ChargebackTransactionID: &cbID,
	}
	now := time.Now()
	dispute.ResolvedAt = &now
	if err := db.Create(&dispute).Error; err != nil {
		t.Fatalf("create dispute: %v", err)
	}

	if err := ApplyChargebackReverseAllocations(db, s.companyID, cbID, "ops"); err != nil {
		t.Fatalf("ApplyChargebackReverseAllocations: %v", err)
	}

	rows, err := ListReverseAllocationsForTxn(db, s.companyID, cbID)
	if err != nil {
		t.Fatalf("ListReverseAllocationsForTxn: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 reverse rows, got %d", len(rows))
	}
	for _, r := range rows {
		if r.ReverseType != models.ReverseAllocDisputeLost {
			t.Fatalf("expected dispute_lost type on runtime path, got %s", r.ReverseType)
		}
	}
}

// TestDisputeLostReverseAlloc_MissingOriginalTxn_Rejected verifies that a
// chargeback with no resolvable original charge is rejected.
func TestDisputeLostReverseAlloc_MissingOriginalTxn_Rejected(t *testing.T) {
	db := reverseAllocTestDB(t)
	s := setupPayApp(t, db)

	// Chargeback with no OriginalTransactionID and no PaymentRequestID.
	cb := models.PaymentTransaction{
		CompanyID:        s.companyID,
		GatewayAccountID: s.gwID,
		TransactionType:  models.TxnTypeChargeback,
		Amount:           decimal.NewFromInt(100),
		CurrencyCode:     "",
		Status:           "completed",
		RawPayload:       datatypes.JSON("{}"),
	}
	db.Create(&cb)
	PostPaymentTransactionToJournalEntry(db, s.companyID, cb.ID, "test")

	err := ApplyDisputeLostReverseAllocations(db, s.companyID, cb.ID, "ops")
	if err == nil {
		t.Fatal("expected error for missing original txn, got nil")
	}
}

// TestReverseAlloc_UnsupportedMultiLayerReversal_Rejected verifies that a
// reversal of a reversal is explicitly rejected instead of falling back to the
// original charge/capture via PaymentRequest heuristics.
func TestReverseAlloc_UnsupportedMultiLayerReversal_Rejected(t *testing.T) {
	db := reverseAllocTestDB(t)
	s := setupPayApp(t, db)

	chargeID, _ := setupMultiAllocCharge(t, db, s, decimal.NewFromInt(500), []int64{300, 200})
	refundID := postedReverseTxn(t, db, s, models.TxnTypeRefund, decimal.NewFromInt(500), chargeID)
	chargebackID := postedReverseTxn(t, db, s, models.TxnTypeChargeback, decimal.NewFromInt(500), refundID)

	err := ApplyChargebackReverseAllocations(db, s.companyID, chargebackID, "ops")
	if !errors.Is(err, ErrReverseAllocUnsupportedMultiLayerReversal) {
		t.Fatalf("expected ErrReverseAllocUnsupportedMultiLayerReversal, got %v", err)
	}

	assertReverseAllocRecords(t, db, s.companyID, chargebackID, 0)
}

// ── D. Main chain non-regression ─────────────────────────────────────────────

// TestSingleInvoiceRefund_UnaffectedByBatch22 verifies that the existing
// single-invoice refund path continues to work for non-multi-allocated payments.
func TestSingleInvoiceRefund_UnaffectedByBatch22(t *testing.T) {
	db := reverseAllocTestDB(t)
	s := setupPayApp(t, db)

	// Normal single-invoice payment + refund (Batch 15/16 path).
	chargeTxnID := postChargeTxn(t, db, s, 1000)
	if err := ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test"); err != nil {
		t.Fatalf("apply charge: %v", err)
	}
	refundTxnID := postRefundTxn(t, db, s, 1000)
	if err := ApplyRefundTransactionToInvoice(db, s.companyID, refundTxnID, "test"); err != nil {
		t.Fatalf("single-invoice refund: %v", err)
	}
	// Invoice restored to full amount.
	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if !inv.BalanceDue.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("expected balance 1000, got %s", inv.BalanceDue.StringFixed(2))
	}
}

// TestSingleInvoiceChargeback_UnaffectedByBatch22 verifies that the existing
// single-invoice chargeback path continues to work for non-multi-allocated payments.
func TestSingleInvoiceChargeback_UnaffectedByBatch22(t *testing.T) {
	db := reverseAllocTestDB(t)
	s := setupPayApp(t, db)

	chargeTxnID := postChargeTxn(t, db, s, 1000)
	ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test")

	// Chargeback linked via OriginalTransactionID.
	cb := models.PaymentTransaction{
		CompanyID: s.companyID, GatewayAccountID: s.gwID,
		PaymentRequestID: &s.requestID, TransactionType: models.TxnTypeChargeback,
		OriginalTransactionID: &chargeTxnID,
		Amount:                decimal.NewFromInt(1000), CurrencyCode: "CAD", Status: "completed",
		RawPayload: datatypes.JSON("{}"),
	}
	db.Create(&cb)
	// Mark posted directly — chargeback account mapping not needed for this test.
	jeID := uint(9999)
	db.Model(&cb).Updates(map[string]any{"posted_journal_entry_id": jeID})

	if err := ApplyChargebackTransactionToInvoice(db, s.companyID, cb.ID, "test"); err != nil {
		t.Fatalf("single-invoice chargeback: %v", err)
	}
	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if !inv.BalanceDue.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("expected 1000, got %s", inv.BalanceDue.StringFixed(2))
	}
}

// TestMutualExclusion_SinglePathBlockedAfterMultiReverseApplied verifies that
// after a multi-alloc reverse is applied, the single-invoice refund path is blocked.
func TestMutualExclusion_SinglePathBlockedAfterMultiReverseApplied(t *testing.T) {
	db := reverseAllocTestDB(t)
	s := setupPayApp(t, db)

	chargeID, _ := setupMultiAllocCharge(t, db, s, decimal.NewFromInt(1000), []int64{600, 400})
	refundID := postedReverseTxn(t, db, s, models.TxnTypeRefund, decimal.NewFromInt(1000), chargeID)

	// Apply via multi-alloc path.
	if err := ApplyRefundReverseAllocations(db, s.companyID, refundID, "op"); err != nil {
		t.Fatalf("multi-alloc reverse: %v", err)
	}

	// Single-invoice path should now be blocked.
	err := ApplyRefundTransactionToInvoice(db, s.companyID, refundID, "op")
	if err == nil {
		t.Fatal("expected single-invoice path to be blocked after multi-alloc reverse applied")
	}
}

// ── E. ComputeReverseAllocationPlan unit tests ────────────────────────────────

func TestComputePlan_ProportionalSplit(t *testing.T) {
	allocs := []models.PaymentAllocation{
		{ID: 1, InvoiceID: 10, AllocatedAmount: decimal.NewFromInt(600)},
		{ID: 2, InvoiceID: 20, AllocatedAmount: decimal.NewFromInt(400)},
	}
	plan, err := ComputeReverseAllocationPlan(decimal.NewFromInt(1000), allocs)
	if err != nil {
		t.Fatalf("ComputeReverseAllocationPlan: %v", err)
	}
	if len(plan) != 2 {
		t.Fatalf("expected 2 plan lines, got %d", len(plan))
	}
	if !plan[0].Amount.Equal(decimal.NewFromInt(600)) {
		t.Errorf("INV 10: want 600, got %s", plan[0].Amount.StringFixed(2))
	}
	if !plan[1].Amount.Equal(decimal.NewFromInt(400)) {
		t.Errorf("INV 20: want 400, got %s", plan[1].Amount.StringFixed(2))
	}
}

func TestComputePlan_ExactLastLineRemainder(t *testing.T) {
	// 3-way split with uneven proportions to trigger last-line remainder logic.
	allocs := []models.PaymentAllocation{
		{ID: 1, InvoiceID: 1, AllocatedAmount: decimal.NewFromInt(333)},
		{ID: 2, InvoiceID: 2, AllocatedAmount: decimal.NewFromInt(333)},
		{ID: 3, InvoiceID: 3, AllocatedAmount: decimal.NewFromInt(334)},
	}
	reversal := decimal.NewFromInt(1000)
	plan, err := ComputeReverseAllocationPlan(reversal, allocs)
	if err != nil {
		t.Fatalf("ComputeReverseAllocationPlan: %v", err)
	}
	total := decimal.Zero
	for _, l := range plan {
		total = total.Add(l.Amount)
	}
	if !total.Equal(reversal) {
		t.Errorf("plan total should equal reversal %s, got %s", reversal.StringFixed(2), total.StringFixed(2))
	}
}

func TestComputePlan_OverpaymentCapAtTotalAllocated(t *testing.T) {
	allocs := []models.PaymentAllocation{
		{ID: 1, InvoiceID: 10, AllocatedAmount: decimal.NewFromInt(500)},
		{ID: 2, InvoiceID: 20, AllocatedAmount: decimal.NewFromInt(300)},
	}
	// Reversal of 1200 — but only 800 was allocated to invoices.
	plan, err := ComputeReverseAllocationPlan(decimal.NewFromInt(1200), allocs)
	if err != nil {
		t.Fatalf("ComputeReverseAllocationPlan: %v", err)
	}
	total := decimal.Zero
	for _, l := range plan {
		total = total.Add(l.Amount)
	}
	// Should be capped at 800.
	if !total.Equal(decimal.NewFromInt(800)) {
		t.Errorf("plan total should be capped at 800, got %s", total.StringFixed(2))
	}
}

func TestComputePlan_EmptyAllocations_Rejected(t *testing.T) {
	_, err := ComputeReverseAllocationPlan(decimal.NewFromInt(100), nil)
	if err == nil {
		t.Fatal("expected error for empty allocations, got nil")
	}
}
