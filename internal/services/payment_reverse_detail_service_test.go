// 遵循project_guide.md
package services

// payment_reverse_detail_service_test.go — Batch 25: Rollup service tests.
//
// Test groups:
//   A — BuildPaymentReverseDetailRollup: nil exception, no txn links, multi-alloc
//       path, single-invoice path, reverse applied (multi), reverse applied (single),
//       strategy=none when original has no allocs, cross-company isolation,
//       next-step guidance per type.

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

var rdSeedSeq uint64

// ── Test DB ───────────────────────────────────────────────────────────────────

func reverseDetailTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:revdetail_%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
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
		&models.Invoice{},
	); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

// ── Seed helpers ──────────────────────────────────────────────────────────────

func rdPtrUint(v uint) *uint { return &v }

func rdPtrDecimal(v decimal.Decimal) *decimal.Decimal { return &v }

// seedTxn inserts a minimal PaymentTransaction into the DB.
func seedTxn(t *testing.T, db *gorm.DB, companyID uint, txnType models.PaymentTransactionType, amount decimal.Decimal) *models.PaymentTransaction {
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
	if err := db.Create(txn).Error; err != nil {
		t.Fatalf("seed txn: %v", err)
	}
	return txn
}

// seedInvoice inserts a minimal Invoice with the given invoice number.
func seedRDInvoice(t *testing.T, db *gorm.DB, companyID uint, number string) *models.Invoice {
	t.Helper()
	inv := &models.Invoice{
		CompanyID:     companyID,
		InvoiceNumber: number,
		CustomerID:    1,
		Amount:        decimal.NewFromInt(1000),
		BalanceDue:    decimal.NewFromInt(1000),
		Status:        models.InvoiceStatusIssued,
		InvoiceDate: time.Now(),
		
	}
	if err := db.Create(inv).Error; err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	return inv
}

// seedForwardAlloc inserts a PaymentAllocation (multi-path forward alloc).
func seedForwardAlloc(t *testing.T, db *gorm.DB, companyID, txnID, invoiceID uint, amount decimal.Decimal) *models.PaymentAllocation {
	t.Helper()
	alloc := &models.PaymentAllocation{
		CompanyID:            companyID,
		PaymentTransactionID: txnID,
		InvoiceID:            invoiceID,
		AllocatedAmount:      amount,
	}
	if err := db.Create(alloc).Error; err != nil {
		t.Fatalf("seed forward alloc: %v", err)
	}
	return alloc
}

// seedReverseAlloc inserts a PaymentReverseAllocation (multi-path reverse alloc).
func seedReverseAlloc(t *testing.T, db *gorm.DB, companyID, revTxnID, origTxnID, payAllocID, invoiceID uint, amount decimal.Decimal) *models.PaymentReverseAllocation {
	t.Helper()
	ra := &models.PaymentReverseAllocation{
		CompanyID:           companyID,
		ReverseTxnID:        revTxnID,
		OriginalTxnID:       origTxnID,
		PaymentAllocationID: payAllocID,
		InvoiceID:           invoiceID,
		Amount:              amount,
		ReverseType:         models.ReverseAllocRefund,
	}
	if err := db.Create(ra).Error; err != nil {
		t.Fatalf("seed reverse alloc: %v", err)
	}
	return ra
}

func seedPREx(t *testing.T, db *gorm.DB, companyID uint, exType models.PaymentReverseExceptionType, revTxnID, origTxnID *uint) *models.PaymentReverseException {
	t.Helper()
	ex := &models.PaymentReverseException{
		CompanyID:      companyID,
		ExceptionType:  exType,
		Status:         models.PRExceptionStatusOpen,
		ReverseTxnID:   revTxnID,
		OriginalTxnID:  origTxnID,
		DedupKey:       fmt.Sprintf("rdtest-%s-%d-%d", exType, companyID, atomic.AddUint64(&rdSeedSeq, 1)),
		Summary:        "test",
		CreatedByActor: "test",
	}
	if err := db.Create(ex).Error; err != nil {
		t.Fatalf("seed PR exception: %v", err)
	}
	return ex
}

// ── A. BuildPaymentReverseDetailRollup ───────────────────────────────────────

// TestReverseDetailRollup_NilException verifies that a nil exception returns an error.
func TestReverseDetailRollup_NilException(t *testing.T) {
	db := reverseDetailTestDB(t)
	_, err := BuildPaymentReverseDetailRollup(db, 1, nil)
	if err == nil {
		t.Fatal("want error for nil exception, got nil")
	}
}

// TestReverseDetailRollup_NoTxnLinks verifies that an exception with no linked
// transactions produces a valid rollup with strategy="none" and empty lines.
func TestReverseDetailRollup_NoTxnLinks(t *testing.T) {
	db := reverseDetailTestDB(t)
	ex := seedPREx(t, db, 1, models.PRExceptionReverseAllocationAmbiguous, nil, nil)

	r, err := BuildPaymentReverseDetailRollup(db, 1, ex)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.ReverseTxn != nil {
		t.Error("want ReverseTxn nil")
	}
	if r.OriginalTxn != nil {
		t.Error("want OriginalTxn nil")
	}
	if r.AllocationStrategy != "none" {
		t.Errorf("want strategy=none, got %q", r.AllocationStrategy)
	}
	if len(r.ForwardAllocLines) != 0 {
		t.Errorf("want 0 forward lines, got %d", len(r.ForwardAllocLines))
	}
	if r.IsReverseApplied {
		t.Error("want IsReverseApplied=false")
	}
	if r.NextStepGuidance == "" {
		t.Error("want non-empty NextStepGuidance")
	}
}

// TestReverseDetailRollup_MultiAllocStrategy verifies detection of multi-alloc
// strategy and correct forward line loading with invoice numbers.
func TestReverseDetailRollup_MultiAllocStrategy(t *testing.T) {
	db := reverseDetailTestDB(t)

	origTxn := seedTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(1000))
	revTxn := seedTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(400))
	inv1 := seedRDInvoice(t, db, 1, "INV-001")
	inv2 := seedRDInvoice(t, db, 1, "INV-002")
	seedForwardAlloc(t, db, 1, origTxn.ID, inv1.ID, decimal.NewFromInt(600))
	seedForwardAlloc(t, db, 1, origTxn.ID, inv2.ID, decimal.NewFromInt(400))

	ex := seedPREx(t, db, 1, models.PRExceptionAmountExceedsStrategy, rdPtrUint(revTxn.ID), rdPtrUint(origTxn.ID))
	r, err := BuildPaymentReverseDetailRollup(db, 1, ex)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if r.AllocationStrategy != "multi" {
		t.Errorf("want strategy=multi, got %q", r.AllocationStrategy)
	}
	if len(r.ForwardAllocLines) != 2 {
		t.Fatalf("want 2 forward lines, got %d", len(r.ForwardAllocLines))
	}
	if !r.ForwardTotal.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("want ForwardTotal=1000, got %s", r.ForwardTotal)
	}

	// Verify invoice numbers were loaded.
	invNums := map[string]bool{}
	for _, line := range r.ForwardAllocLines {
		invNums[line.InvoiceNumber] = true
	}
	if !invNums["INV-001"] || !invNums["INV-002"] {
		t.Errorf("want invoice numbers INV-001 and INV-002, got %v", invNums)
	}

	// Reverse not yet applied.
	if r.IsReverseApplied {
		t.Error("want IsReverseApplied=false before reverse allocs exist")
	}
	if len(r.ReverseAllocLines) != 0 {
		t.Errorf("want 0 reverse lines, got %d", len(r.ReverseAllocLines))
	}
}

// TestReverseDetailRollup_MultiAllocReverseApplied verifies that existing
// PaymentReverseAllocation rows are loaded as reverse lines.
func TestReverseDetailRollup_MultiAllocReverseApplied(t *testing.T) {
	db := reverseDetailTestDB(t)

	origTxn := seedTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(1000))
	revTxn := seedTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(1000))
	inv1 := seedRDInvoice(t, db, 1, "INV-010")
	inv2 := seedRDInvoice(t, db, 1, "INV-011")
	fwd1 := seedForwardAlloc(t, db, 1, origTxn.ID, inv1.ID, decimal.NewFromInt(600))
	fwd2 := seedForwardAlloc(t, db, 1, origTxn.ID, inv2.ID, decimal.NewFromInt(400))
	seedReverseAlloc(t, db, 1, revTxn.ID, origTxn.ID, fwd1.ID, inv1.ID, decimal.NewFromInt(600))
	seedReverseAlloc(t, db, 1, revTxn.ID, origTxn.ID, fwd2.ID, inv2.ID, decimal.NewFromInt(400))

	ex := seedPREx(t, db, 1, models.PRExceptionAmountExceedsStrategy, rdPtrUint(revTxn.ID), rdPtrUint(origTxn.ID))
	r, err := BuildPaymentReverseDetailRollup(db, 1, ex)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !r.IsReverseApplied {
		t.Error("want IsReverseApplied=true")
	}
	if len(r.ReverseAllocLines) != 2 {
		t.Fatalf("want 2 reverse lines, got %d", len(r.ReverseAllocLines))
	}
	if !r.ReverseTotal.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("want ReverseTotal=1000, got %s", r.ReverseTotal)
	}

	invNums := map[string]bool{}
	for _, line := range r.ReverseAllocLines {
		invNums[line.InvoiceNumber] = true
	}
	if !invNums["INV-010"] || !invNums["INV-011"] {
		t.Errorf("want invoice numbers INV-010 and INV-011, got %v", invNums)
	}
}

// TestReverseDetailRollup_SingleInvoiceStrategy verifies detection of single-invoice
// strategy when original txn has AppliedInvoiceID set but no PaymentAllocation rows.
func TestReverseDetailRollup_SingleInvoiceStrategy(t *testing.T) {
	db := reverseDetailTestDB(t)

	origTxn := seedTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(500))
	revTxn := seedTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(500))
	inv := seedRDInvoice(t, db, 1, "INV-020")

	// Set single-invoice application on original txn.
	applied := decimal.NewFromInt(500)
	db.Model(origTxn).Updates(map[string]any{
		"applied_invoice_id": inv.ID,
		"applied_amount":     applied,
	})

	ex := seedPREx(t, db, 1, models.PRExceptionRequiresManualSplit, rdPtrUint(revTxn.ID), rdPtrUint(origTxn.ID))
	r, err := BuildPaymentReverseDetailRollup(db, 1, ex)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if r.AllocationStrategy != "single" {
		t.Errorf("want strategy=single, got %q", r.AllocationStrategy)
	}
	if len(r.ForwardAllocLines) != 1 {
		t.Fatalf("want 1 forward line, got %d", len(r.ForwardAllocLines))
	}
	if r.ForwardAllocLines[0].InvoiceNumber != "INV-020" {
		t.Errorf("want invoice number INV-020, got %q", r.ForwardAllocLines[0].InvoiceNumber)
	}
	if !r.ForwardTotal.Equal(decimal.NewFromInt(500)) {
		t.Errorf("want ForwardTotal=500, got %s", r.ForwardTotal)
	}

	// Reverse not yet applied on this path (ReverseTxn.AppliedInvoiceID is nil).
	if r.IsReverseApplied {
		t.Error("want IsReverseApplied=false")
	}
}

// TestReverseDetailRollup_SingleInvoiceReverseApplied verifies that the single-path
// reverse is detected when ReverseTxn.AppliedInvoiceID is set.
func TestReverseDetailRollup_SingleInvoiceReverseApplied(t *testing.T) {
	db := reverseDetailTestDB(t)

	origTxn := seedTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(500))
	revTxn := seedTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(500))
	inv := seedRDInvoice(t, db, 1, "INV-030")

	applied := decimal.NewFromInt(500)
	db.Model(origTxn).Updates(map[string]any{
		"applied_invoice_id": inv.ID,
		"applied_amount":     applied,
	})
	// Mark reverse txn as applied via single-invoice path.
	db.Model(revTxn).Updates(map[string]any{
		"applied_invoice_id": inv.ID,
		"applied_amount":     applied,
	})

	ex := seedPREx(t, db, 1, models.PRExceptionRequiresManualSplit, rdPtrUint(revTxn.ID), rdPtrUint(origTxn.ID))
	r, err := BuildPaymentReverseDetailRollup(db, 1, ex)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !r.IsReverseApplied {
		t.Error("want IsReverseApplied=true")
	}
	if len(r.ReverseAllocLines) != 1 {
		t.Fatalf("want 1 reverse line, got %d", len(r.ReverseAllocLines))
	}
	if r.ReverseAllocLines[0].InvoiceNumber != "INV-030" {
		t.Errorf("want invoice number INV-030, got %q", r.ReverseAllocLines[0].InvoiceNumber)
	}
	if !r.ReverseTotal.Equal(decimal.NewFromInt(500)) {
		t.Errorf("want ReverseTotal=500, got %s", r.ReverseTotal)
	}
}

// TestReverseDetailRollup_StrategyNoneWhenNoAllocs verifies that original txn
// with no allocation state produces strategy="none".
func TestReverseDetailRollup_StrategyNoneWhenNoAllocs(t *testing.T) {
	db := reverseDetailTestDB(t)

	origTxn := seedTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(300))
	revTxn := seedTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(300))
	// No PaymentAllocation rows, no AppliedInvoiceID.

	ex := seedPREx(t, db, 1, models.PRExceptionChainConflict, rdPtrUint(revTxn.ID), rdPtrUint(origTxn.ID))
	r, err := BuildPaymentReverseDetailRollup(db, 1, ex)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if r.AllocationStrategy != "none" {
		t.Errorf("want strategy=none, got %q", r.AllocationStrategy)
	}
	if len(r.ForwardAllocLines) != 0 {
		t.Errorf("want 0 forward lines, got %d", len(r.ForwardAllocLines))
	}
}

// TestReverseDetailRollup_CrossCompanyIsolation verifies that allocations and
// invoices from another company are not visible.
func TestReverseDetailRollup_CrossCompanyIsolation(t *testing.T) {
	db := reverseDetailTestDB(t)

	// Company 1 origTxn with allocs.
	origTxn1 := seedTxn(t, db, 1, models.TxnTypeCharge, decimal.NewFromInt(500))
	revTxn1 := seedTxn(t, db, 1, models.TxnTypeRefund, decimal.NewFromInt(500))
	inv1 := seedRDInvoice(t, db, 1, "INV-C1")
	seedForwardAlloc(t, db, 1, origTxn1.ID, inv1.ID, decimal.NewFromInt(500))

	// Company 2 — seed its own txns and allocs so they could pollute company 1's results.
	origTxn2 := seedTxn(t, db, 2, models.TxnTypeCharge, decimal.NewFromInt(500))
	_ = seedTxn(t, db, 2, models.TxnTypeRefund, decimal.NewFromInt(500)) // revTxn2 unused
	inv2 := seedRDInvoice(t, db, 2, "INV-C2")
	seedForwardAlloc(t, db, 2, origTxn2.ID, inv2.ID, decimal.NewFromInt(500))

	// Company 1 exception using company 1 txns.
	ex1 := seedPREx(t, db, 1, models.PRExceptionAmountExceedsStrategy, rdPtrUint(revTxn1.ID), rdPtrUint(origTxn1.ID))
	r, err := BuildPaymentReverseDetailRollup(db, 1, ex1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should see company 1's invoice, not company 2's.
	if len(r.ForwardAllocLines) != 1 {
		t.Fatalf("want 1 forward line for company 1, got %d", len(r.ForwardAllocLines))
	}
	if r.ForwardAllocLines[0].InvoiceNumber != "INV-C1" {
		t.Errorf("want INV-C1, got %q", r.ForwardAllocLines[0].InvoiceNumber)
	}
}

// TestReverseDetailRollup_NextStepGuidance verifies that each exception type
// returns non-empty, type-specific guidance.
func TestReverseDetailRollup_NextStepGuidance(t *testing.T) {
	db := reverseDetailTestDB(t)

	for _, exType := range models.AllPaymentReverseExceptionTypes() {
		ex := seedPREx(t, db, 1, exType, nil, nil)
		r, err := BuildPaymentReverseDetailRollup(db, 1, ex)
		if err != nil {
			t.Fatalf("type=%s: unexpected error: %v", exType, err)
		}
		if r.NextStepGuidance == "" {
			t.Errorf("type=%s: want non-empty NextStepGuidance", exType)
		}
	}
}
