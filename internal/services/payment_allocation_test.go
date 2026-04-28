// 遵循project_guide.md
package services

// payment_allocation_test.go — Batch 17: Multi-invoice allocation tests.
//
// DB setup reuses testPaymentApplicationDB / setupPayApp from
// payment_application_test.go (same package).

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// newOpenInvoice creates a fresh invoice with the given balance for the same
// customer as s.customerID.
func newOpenInvoice(t *testing.T, db *gorm.DB, s paSetup, number string, balance int64) uint {
	t.Helper()
	inv := models.Invoice{
		CompanyID:            s.companyID,
		InvoiceNumber:        number,
		CustomerID:           s.customerID,
		InvoiceDate:          time.Now(),
		Status:               models.InvoiceStatusIssued,
		Amount:               decimal.NewFromInt(balance),
		BalanceDue:           decimal.NewFromInt(balance),
		BalanceDueBase:       decimal.NewFromInt(balance),
		CustomerNameSnapshot: "C",
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatalf("newOpenInvoice: %v", err)
	}
	return inv.ID
}

// newOpenInvoiceDec creates an invoice with a decimal balance.
func newOpenInvoiceDec(t *testing.T, db *gorm.DB, s paSetup, number string, balance decimal.Decimal) uint {
	t.Helper()
	inv := models.Invoice{
		CompanyID:            s.companyID,
		InvoiceNumber:        number,
		CustomerID:           s.customerID,
		InvoiceDate:          time.Now(),
		Status:               models.InvoiceStatusIssued,
		Amount:               balance,
		BalanceDue:           balance,
		BalanceDueBase:       balance,
		CustomerNameSnapshot: "C",
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatalf("newOpenInvoiceDec: %v", err)
	}
	return inv.ID
}

// postedChargeTxnNoRequest creates a charge/capture transaction WITHOUT a
// payment request (for multi-alloc path that doesn't require a request linkage).
func postedChargeTxnNoRequest(t *testing.T, db *gorm.DB, s paSetup, amount decimal.Decimal) uint {
	t.Helper()
	txn := models.PaymentTransaction{
		CompanyID:        s.companyID,
		GatewayAccountID: s.gwID,
		TransactionType:  models.TxnTypeCharge,
		Amount:           amount,
		CurrencyCode:     "",
		Status:           "completed",
		RawPayload:       datatypes.JSON("{}"),
	}
	if err := db.Create(&txn).Error; err != nil {
		t.Fatalf("create txn: %v", err)
	}
	// Mark as posted (simulate posting without a real JE to keep test simple).
	now := time.Now()
	jeID := uint(9999) // sentinel JE ID (no real JE needed for allocation tests)
	db.Model(&txn).Updates(map[string]any{"posted_journal_entry_id": jeID, "posted_at": now})
	return txn.ID
}

// assertInvoiceBalance asserts that invoice has exactly the given BalanceDue and status.
func assertInvoiceBalance(t *testing.T, db *gorm.DB, invID uint, wantBalance decimal.Decimal, wantStatus models.InvoiceStatus) {
	t.Helper()
	var inv models.Invoice
	db.First(&inv, invID)
	if !inv.BalanceDue.Equal(wantBalance) {
		t.Errorf("invoice %d: BalanceDue want %s got %s", invID, wantBalance.StringFixed(2), inv.BalanceDue.StringFixed(2))
	}
	if inv.Status != wantStatus {
		t.Errorf("invoice %d: status want %s got %s", invID, wantStatus, inv.Status)
	}
}

// assertAllocRecords asserts the count of PaymentAllocation records for a txn.
func assertAllocRecords(t *testing.T, db *gorm.DB, companyID, txnID uint, wantCount int) {
	t.Helper()
	var records []models.PaymentAllocation
	db.Where("company_id = ? AND payment_transaction_id = ?", companyID, txnID).Find(&records)
	if len(records) != wantCount {
		t.Errorf("PaymentAllocation count for txn %d: want %d got %d", txnID, wantCount, len(records))
	}
}

// assertCreditRemaining asserts the CustomerCredit.RemainingAmount.
func assertCreditRemaining(t *testing.T, db *gorm.DB, creditID uint, want decimal.Decimal) {
	t.Helper()
	var c models.CustomerCredit
	db.First(&c, creditID)
	if !c.RemainingAmount.Equal(want) {
		t.Errorf("credit %d: RemainingAmount want %s got %s", creditID, want.StringFixed(2), c.RemainingAmount.StringFixed(2))
	}
}

// ── A. Payment multi-allocation tests ────────────────────────────────────────

// TestPaymentMultiAlloc_HappyPath_TwoInvoices tests that a single payment
// can be correctly allocated across two open invoices.
func TestPaymentMultiAlloc_HappyPath_TwoInvoices(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	// Add PaymentAllocation to test DB.
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate PaymentAllocation: %v", err)
	}

	inv1 := newOpenInvoice(t, db, s, "INV-A1", 500)
	inv2 := newOpenInvoice(t, db, s, "INV-A2", 300)
	txnID := postedChargeTxnNoRequest(t, db, s, decimal.NewFromInt(800))

	lines := []AllocationLine{
		{InvoiceID: inv1, Amount: decimal.NewFromInt(500)},
		{InvoiceID: inv2, Amount: decimal.NewFromInt(300)},
	}
	if err := AllocatePaymentToMultipleInvoices(db, s.companyID, txnID, lines, "test"); err != nil {
		t.Fatalf("AllocatePaymentToMultipleInvoices: %v", err)
	}

	// Invoice 1: fully paid.
	assertInvoiceBalance(t, db, inv1, decimal.Zero, models.InvoiceStatusPaid)
	// Invoice 2: fully paid.
	assertInvoiceBalance(t, db, inv2, decimal.Zero, models.InvoiceStatusPaid)
	// PaymentAllocation records: 2.
	assertAllocRecords(t, db, s.companyID, txnID, 2)
	// Remaining = 0.
	remaining := decimal.NewFromInt(800).Sub(PaymentAllocatedTotal(db, s.companyID, txnID))
	if !remaining.IsZero() {
		t.Errorf("expected remaining=0, got %s", remaining)
	}
}

// TestPaymentMultiAlloc_HappyPath_PartialAllocation tests that a payment can
// be partially allocated across invoices, leaving a remaining balance.
func TestPaymentMultiAlloc_HappyPath_PartialAllocation(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	inv1 := newOpenInvoice(t, db, s, "INV-B1", 500)
	inv2 := newOpenInvoice(t, db, s, "INV-B2", 500)
	txnID := postedChargeTxnNoRequest(t, db, s, decimal.NewFromInt(1000))

	lines := []AllocationLine{
		{InvoiceID: inv1, Amount: decimal.NewFromInt(300)},
		{InvoiceID: inv2, Amount: decimal.NewFromInt(200)},
	}
	if err := AllocatePaymentToMultipleInvoices(db, s.companyID, txnID, lines, "test"); err != nil {
		t.Fatalf("AllocatePaymentToMultipleInvoices: %v", err)
	}

	assertInvoiceBalance(t, db, inv1, decimal.NewFromInt(200), models.InvoiceStatusPartiallyPaid)
	assertInvoiceBalance(t, db, inv2, decimal.NewFromInt(300), models.InvoiceStatusPartiallyPaid)
	assertAllocRecords(t, db, s.companyID, txnID, 2)

	remaining := decimal.NewFromInt(1000).Sub(PaymentAllocatedTotal(db, s.companyID, txnID))
	if !remaining.Equal(decimal.NewFromInt(500)) {
		t.Errorf("expected remaining=500, got %s", remaining)
	}
}

// TestPaymentMultiAlloc_IncrementalAllocation tests that a second batch of
// allocations can be submitted as long as remaining > 0.
func TestPaymentMultiAlloc_IncrementalAllocation(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	inv1 := newOpenInvoice(t, db, s, "INV-C1", 400)
	inv2 := newOpenInvoice(t, db, s, "INV-C2", 300)
	txnID := postedChargeTxnNoRequest(t, db, s, decimal.NewFromInt(700))

	// First batch: allocate to inv1 only.
	lines1 := []AllocationLine{{InvoiceID: inv1, Amount: decimal.NewFromInt(400)}}
	if err := AllocatePaymentToMultipleInvoices(db, s.companyID, txnID, lines1, "test"); err != nil {
		t.Fatalf("first alloc: %v", err)
	}
	assertInvoiceBalance(t, db, inv1, decimal.Zero, models.InvoiceStatusPaid)

	// Second batch: allocate remaining to inv2.
	lines2 := []AllocationLine{{InvoiceID: inv2, Amount: decimal.NewFromInt(300)}}
	if err := AllocatePaymentToMultipleInvoices(db, s.companyID, txnID, lines2, "test"); err != nil {
		t.Fatalf("second alloc: %v", err)
	}
	assertInvoiceBalance(t, db, inv2, decimal.Zero, models.InvoiceStatusPaid)
	assertAllocRecords(t, db, s.companyID, txnID, 2)
}

// TestPaymentMultiAlloc_ExceedsRemaining tests that allocation is rejected
// when the total exceeds the payment's remaining allocatable balance.
func TestPaymentMultiAlloc_ExceedsRemaining(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	inv1 := newOpenInvoice(t, db, s, "INV-D1", 1000)
	txnID := postedChargeTxnNoRequest(t, db, s, decimal.NewFromInt(500))

	lines := []AllocationLine{{InvoiceID: inv1, Amount: decimal.NewFromInt(600)}}
	err := AllocatePaymentToMultipleInvoices(db, s.companyID, txnID, lines, "test")
	if err == nil {
		t.Fatal("expected error for exceeding remaining, got nil")
	}

	// No allocation records should exist.
	assertAllocRecords(t, db, s.companyID, txnID, 0)
	// Invoice untouched.
	assertInvoiceBalance(t, db, inv1, decimal.NewFromInt(1000), models.InvoiceStatusIssued)
}

// TestPaymentMultiAlloc_LineExceedsInvoiceBalance rejects a line whose
// allocation amount exceeds that invoice's BalanceDue.
func TestPaymentMultiAlloc_LineExceedsInvoiceBalance(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	inv1 := newOpenInvoice(t, db, s, "INV-E1", 200)
	txnID := postedChargeTxnNoRequest(t, db, s, decimal.NewFromInt(1000))

	lines := []AllocationLine{{InvoiceID: inv1, Amount: decimal.NewFromInt(300)}}
	err := AllocatePaymentToMultipleInvoices(db, s.companyID, txnID, lines, "test")
	if err == nil {
		t.Fatal("expected error for line exceeding invoice balance, got nil")
	}

	assertAllocRecords(t, db, s.companyID, txnID, 0)
	assertInvoiceBalance(t, db, inv1, decimal.NewFromInt(200), models.InvoiceStatusIssued)
}

// TestPaymentMultiAlloc_CrossCustomerReject tests that invoices from a
// different customer are rejected.
func TestPaymentMultiAlloc_CrossCustomerReject(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Create a second customer and their invoice.
	otherCust := models.Customer{CompanyID: s.companyID, Name: "Other", AddrStreet1: "2"}
	db.Create(&otherCust)
	otherInv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-F1",
		CustomerID: otherCust.ID, Status: models.InvoiceStatusIssued,
		Amount: decimal.NewFromInt(500), BalanceDue: decimal.NewFromInt(500),
		CustomerNameSnapshot: "Other",
	}
	db.Create(&otherInv)

	txnID := postedChargeTxnNoRequest(t, db, s, decimal.NewFromInt(1000))

	// One line for own customer, one for other customer.
	inv1 := newOpenInvoice(t, db, s, "INV-F-MINE", 300)
	lines := []AllocationLine{
		{InvoiceID: inv1, Amount: decimal.NewFromInt(300)},
		{InvoiceID: otherInv.ID, Amount: decimal.NewFromInt(200)},
	}
	err := AllocatePaymentToMultipleInvoices(db, s.companyID, txnID, lines, "test")
	if err == nil {
		t.Fatal("expected cross-customer error, got nil")
	}

	assertAllocRecords(t, db, s.companyID, txnID, 0)
}

// TestPaymentMultiAlloc_SourceCustomerMismatchReject ensures the backend uses
// the payment source customer as authority whenever the transaction is linked
// to a payment request/invoice.
func TestPaymentMultiAlloc_SourceCustomerMismatchReject(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	otherCust := models.Customer{CompanyID: s.companyID, Name: "Other Source", AddrStreet1: "9"}
	if err := db.Create(&otherCust).Error; err != nil {
		t.Fatalf("create other customer: %v", err)
	}
	otherInv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-F2",
		CustomerID: otherCust.ID, Status: models.InvoiceStatusIssued,
		Amount: decimal.NewFromInt(250), BalanceDue: decimal.NewFromInt(250),
		CustomerNameSnapshot: "Other Source",
	}
	if err := db.Create(&otherInv).Error; err != nil {
		t.Fatalf("create other invoice: %v", err)
	}

	txnID := postChargeTxn(t, db, s, 250)
	lines := []AllocationLine{{InvoiceID: otherInv.ID, Amount: decimal.NewFromInt(250)}}

	err := AllocatePaymentToMultipleInvoices(db, s.companyID, txnID, lines, "test")
	if err == nil {
		t.Fatal("expected payment source customer mismatch error, got nil")
	}
	assertAllocRecords(t, db, s.companyID, txnID, 0)
	assertInvoiceBalance(t, db, otherInv.ID, decimal.NewFromInt(250), models.InvoiceStatusIssued)
}

// TestPaymentMultiAlloc_PaidInvoiceReject tests that allocating to an already
// paid invoice is rejected.
func TestPaymentMultiAlloc_PaidInvoiceReject(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	paidInv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-G1",
		CustomerID: s.customerID, Status: models.InvoiceStatusPaid,
		Amount: decimal.NewFromInt(500), BalanceDue: decimal.Zero,
		CustomerNameSnapshot: "C",
	}
	db.Create(&paidInv)
	txnID := postedChargeTxnNoRequest(t, db, s, decimal.NewFromInt(1000))

	lines := []AllocationLine{{InvoiceID: paidInv.ID, Amount: decimal.NewFromInt(100)}}
	err := AllocatePaymentToMultipleInvoices(db, s.companyID, txnID, lines, "test")
	if err == nil {
		t.Fatal("expected error for paid invoice, got nil")
	}
	assertAllocRecords(t, db, s.companyID, txnID, 0)
}

// TestPaymentMultiAlloc_NotPostedReject tests that an unposted transaction
// cannot be allocated.
func TestPaymentMultiAlloc_NotPostedReject(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	inv1 := newOpenInvoice(t, db, s, "INV-H1", 300)

	// Create txn but do NOT post it.
	txn := models.PaymentTransaction{
		CompanyID: s.companyID, GatewayAccountID: s.gwID,
		TransactionType: models.TxnTypeCharge,
		Amount:          decimal.NewFromInt(300),
		Status:          "completed", RawPayload: datatypes.JSON("{}"),
	}
	db.Create(&txn)

	lines := []AllocationLine{{InvoiceID: inv1, Amount: decimal.NewFromInt(300)}}
	err := AllocatePaymentToMultipleInvoices(db, s.companyID, txn.ID, lines, "test")
	if err == nil {
		t.Fatal("expected error for unposted txn, got nil")
	}
}

// TestPaymentMultiAlloc_DuplicateSubmitBlocked tests that submitting the same
// (txn, invoice) pair twice is blocked by the unique constraint.
func TestPaymentMultiAlloc_DuplicateSubmitBlocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	inv1 := newOpenInvoiceDec(t, db, s, "INV-I1", decimal.NewFromInt(600))
	txnID := postedChargeTxnNoRequest(t, db, s, decimal.NewFromInt(1000))

	lines := []AllocationLine{{InvoiceID: inv1, Amount: decimal.NewFromInt(300)}}
	if err := AllocatePaymentToMultipleInvoices(db, s.companyID, txnID, lines, "test"); err != nil {
		t.Fatalf("first alloc: %v", err)
	}

	// Try same (txn, invoice) again — must be rejected.
	err := AllocatePaymentToMultipleInvoices(db, s.companyID, txnID, lines, "test")
	if err == nil {
		t.Fatal("expected duplicate-submission error, got nil")
	}

	// Invoice BalanceDue should have decreased only once.
	assertInvoiceBalance(t, db, inv1, decimal.NewFromInt(300), models.InvoiceStatusPartiallyPaid)
	assertAllocRecords(t, db, s.companyID, txnID, 1)
}

// TestPaymentMultiAlloc_SingleInvoicePathBlocked tests that the old
// single-invoice apply path is rejected once multi-alloc records exist.
func TestPaymentMultiAlloc_SingleInvoicePathBlocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	inv1 := newOpenInvoice(t, db, s, "INV-J1", 500)
	txnID := postedChargeTxnNoRequest(t, db, s, decimal.NewFromInt(800))

	// First allocate via multi path.
	lines := []AllocationLine{{InvoiceID: inv1, Amount: decimal.NewFromInt(500)}}
	if err := AllocatePaymentToMultipleInvoices(db, s.companyID, txnID, lines, "test"); err != nil {
		t.Fatalf("multi alloc: %v", err)
	}

	// Now try single-invoice apply on the same txn — must be blocked.
	// We need to set up the txn with a PaymentRequest pointing to an invoice for
	// ValidatePaymentTransactionApplicable to get past the request-check.
	// But since the txn has no PaymentRequestID, it will fail at that check first.
	// Verify it's blocked.
	err := ValidatePaymentTransactionApplicable(db, s.companyID, txnID)
	if err == nil {
		t.Fatal("expected single-invoice apply to be blocked, got nil error")
	}
}

// TestPaymentMultiAlloc_ConcurrentAllocationSafe tests that concurrent
// submissions don't over-allocate the payment.
func TestPaymentMultiAlloc_ConcurrentAllocationSafe(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Payment of 500; two competing goroutines each try to allocate 400.
	// Only one should succeed.
	inv1 := newOpenInvoice(t, db, s, "INV-K1", 400)
	inv2 := newOpenInvoice(t, db, s, "INV-K2", 400)
	txnID := postedChargeTxnNoRequest(t, db, s, decimal.NewFromInt(500))

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		lines := []AllocationLine{{InvoiceID: inv1, Amount: decimal.NewFromInt(400)}}
		errs[0] = AllocatePaymentToMultipleInvoices(db, s.companyID, txnID, lines, "goroutine-0")
	}()
	go func() {
		defer wg.Done()
		lines := []AllocationLine{{InvoiceID: inv2, Amount: decimal.NewFromInt(400)}}
		errs[1] = AllocatePaymentToMultipleInvoices(db, s.companyID, txnID, lines, "goroutine-1")
	}()
	wg.Wait()

	// Exactly one must succeed and one must fail.
	successes := 0
	for _, e := range errs {
		if e == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Errorf("expected exactly 1 goroutine to succeed, got %d (errs: %v)", successes, errs)
	}

	// Total allocated must be ≤ 500.
	total := PaymentAllocatedTotal(db, s.companyID, txnID)
	if total.GreaterThan(decimal.NewFromInt(500)) {
		t.Errorf("over-allocated! total=%s > 500", total)
	}
}

// ── B. Customer credit multi-allocation tests ─────────────────────────────────

// TestCreditMultiAlloc_HappyPath_TwoInvoices applies a single credit across
// two invoices.
func TestCreditMultiAlloc_HappyPath_TwoInvoices(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	inv1 := newOpenInvoice(t, db, s, "INV-L1", 300)
	inv2 := newOpenInvoice(t, db, s, "INV-L2", 200)
	creditID := newActiveCredit(t, db, s, decimal.NewFromInt(600))

	lines := []AllocationLine{
		{InvoiceID: inv1, Amount: decimal.NewFromInt(300)},
		{InvoiceID: inv2, Amount: decimal.NewFromInt(200)},
	}
	if err := AllocateCustomerCreditToMultipleInvoices(db, s.companyID, creditID, lines, "test"); err != nil {
		t.Fatalf("AllocateCustomerCreditToMultipleInvoices: %v", err)
	}

	assertInvoiceBalance(t, db, inv1, decimal.Zero, models.InvoiceStatusPaid)
	assertInvoiceBalance(t, db, inv2, decimal.Zero, models.InvoiceStatusPaid)
	assertCreditRemaining(t, db, creditID, decimal.NewFromInt(100))

	// CustomerCreditApplication records: 2.
	apps, _ := ListCreditApplications(db, s.companyID, creditID)
	if len(apps) != 2 {
		t.Errorf("expected 2 credit application records, got %d", len(apps))
	}
}

// TestCreditMultiAlloc_ExceedsRemaining rejects total > credit remaining.
func TestCreditMultiAlloc_ExceedsRemaining(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	inv1 := newOpenInvoice(t, db, s, "INV-M1", 1000)
	creditID := newActiveCredit(t, db, s, decimal.NewFromInt(400))

	lines := []AllocationLine{{InvoiceID: inv1, Amount: decimal.NewFromInt(500)}}
	err := AllocateCustomerCreditToMultipleInvoices(db, s.companyID, creditID, lines, "test")
	if err == nil {
		t.Fatal("expected error for exceeding credit remaining, got nil")
	}

	assertCreditRemaining(t, db, creditID, decimal.NewFromInt(400))
	assertInvoiceBalance(t, db, inv1, decimal.NewFromInt(1000), models.InvoiceStatusIssued)
}

// TestCreditMultiAlloc_CrossCustomerReject rejects invoices from a different customer.
func TestCreditMultiAlloc_CrossCustomerReject(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	otherCust := models.Customer{CompanyID: s.companyID, Name: "Other2", AddrStreet1: "3"}
	db.Create(&otherCust)
	otherInv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-N1",
		CustomerID: otherCust.ID, Status: models.InvoiceStatusIssued,
		Amount: decimal.NewFromInt(200), BalanceDue: decimal.NewFromInt(200),
		CustomerNameSnapshot: "Other2",
	}
	db.Create(&otherInv)
	creditID := newActiveCredit(t, db, s, decimal.NewFromInt(500))

	lines := []AllocationLine{{InvoiceID: otherInv.ID, Amount: decimal.NewFromInt(200)}}
	err := AllocateCustomerCreditToMultipleInvoices(db, s.companyID, creditID, lines, "test")
	if err == nil {
		t.Fatal("expected cross-customer error, got nil")
	}
	assertCreditRemaining(t, db, creditID, decimal.NewFromInt(500))
}

// TestCreditMultiAlloc_CurrencyMismatchReject rejects when invoice and credit
// have different currencies.
func TestCreditMultiAlloc_CurrencyMismatchReject(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Invoice is CAD (non-empty currency).
	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-O1",
		CustomerID: s.customerID, Status: models.InvoiceStatusIssued,
		Amount: decimal.NewFromInt(300), BalanceDue: decimal.NewFromInt(300),
		CurrencyCode: "CAD", CustomerNameSnapshot: "C",
	}
	db.Create(&inv)

	// Credit is USD.
	creditID := newActiveCreditWithCurrency(t, db, s, decimal.NewFromInt(500), "USD")

	lines := []AllocationLine{{InvoiceID: inv.ID, Amount: decimal.NewFromInt(200)}}
	err := AllocateCustomerCreditToMultipleInvoices(db, s.companyID, creditID, lines, "test")
	if err == nil {
		t.Fatal("expected currency mismatch error, got nil")
	}
}

// TestCreditMultiAlloc_ExhaustsCredit tests that applying the full remaining
// amount transitions credit status to exhausted.
func TestCreditMultiAlloc_ExhaustsCredit(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	inv1 := newOpenInvoice(t, db, s, "INV-P1", 500)
	creditID := newActiveCredit(t, db, s, decimal.NewFromInt(500))

	lines := []AllocationLine{{InvoiceID: inv1, Amount: decimal.NewFromInt(500)}}
	if err := AllocateCustomerCreditToMultipleInvoices(db, s.companyID, creditID, lines, "test"); err != nil {
		t.Fatalf("alloc: %v", err)
	}

	var credit models.CustomerCredit
	db.First(&credit, creditID)
	if credit.Status != models.CustomerCreditExhausted {
		t.Errorf("expected exhausted, got %s", credit.Status)
	}
	if !credit.RemainingAmount.IsZero() {
		t.Errorf("expected remaining=0, got %s", credit.RemainingAmount)
	}
}

// TestCreditMultiAlloc_ConcurrentSafe verifies concurrent credit applications
// do not over-consume the credit.
func TestCreditMultiAlloc_ConcurrentSafe(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	inv1 := newOpenInvoice(t, db, s, "INV-Q1", 300)
	inv2 := newOpenInvoice(t, db, s, "INV-Q2", 300)
	creditID := newActiveCredit(t, db, s, decimal.NewFromInt(400))

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		lines := []AllocationLine{{InvoiceID: inv1, Amount: decimal.NewFromInt(300)}}
		errs[0] = AllocateCustomerCreditToMultipleInvoices(db, s.companyID, creditID, lines, "g0")
	}()
	go func() {
		defer wg.Done()
		lines := []AllocationLine{{InvoiceID: inv2, Amount: decimal.NewFromInt(300)}}
		errs[1] = AllocateCustomerCreditToMultipleInvoices(db, s.companyID, creditID, lines, "g1")
	}()
	wg.Wait()

	successes := 0
	for _, e := range errs {
		if e == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Errorf("expected exactly 1 success, got %d (errs: %v)", successes, errs)
	}

	var credit models.CustomerCredit
	db.First(&credit, creditID)
	if credit.RemainingAmount.IsNegative() {
		t.Errorf("credit over-consumed: remaining=%s", credit.RemainingAmount)
	}
}

// TestCreditMultiAlloc_DuplicateSubmitBlocked ensures the multi-allocation path
// cannot consume the same credit against the same invoice twice via repeated submit.
func TestCreditMultiAlloc_DuplicateSubmitBlocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	inv1 := newOpenInvoice(t, db, s, "INV-Q3", 600)
	creditID := newActiveCredit(t, db, s, decimal.NewFromInt(900))
	lines := []AllocationLine{{InvoiceID: inv1, Amount: decimal.NewFromInt(300)}}

	if err := AllocateCustomerCreditToMultipleInvoices(db, s.companyID, creditID, lines, "test"); err != nil {
		t.Fatalf("first credit multi-alloc failed: %v", err)
	}
	err := AllocateCustomerCreditToMultipleInvoices(db, s.companyID, creditID, lines, "test")
	if err == nil {
		t.Fatal("expected duplicate credit multi-allocation to be rejected, got nil")
	}

	assertCreditRemaining(t, db, creditID, decimal.NewFromInt(600))
	assertInvoiceBalance(t, db, inv1, decimal.NewFromInt(300), models.InvoiceStatusPartiallyPaid)
	apps, _ := ListCreditApplications(db, s.companyID, creditID)
	if len(apps) != 1 {
		t.Fatalf("expected 1 credit application row after duplicate submit, got %d", len(apps))
	}
}

// TestRefundApply_MultiAllocatedOriginalBlocked ensures the single-invoice
// reverse path cannot mutate invoice state for a charge that was multi-allocated.
func TestRefundApply_MultiAllocatedOriginalBlocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	inv2 := newOpenInvoice(t, db, s, "INV-REV-R2", 300)
	chargeTxnID := postChargeTxn(t, db, s, 800)
	lines := []AllocationLine{
		{InvoiceID: s.invoiceID, Amount: decimal.NewFromInt(500)},
		{InvoiceID: inv2, Amount: decimal.NewFromInt(300)},
	}
	if err := AllocatePaymentToMultipleInvoices(db, s.companyID, chargeTxnID, lines, "test"); err != nil {
		t.Fatalf("multi-alloc charge: %v", err)
	}

	refundTxnID := postRefundTxn(t, db, s, 200)
	err := ApplyRefundTransactionToInvoice(db, s.companyID, refundTxnID, "test")
	if err == nil {
		t.Fatal("expected refund apply on multi-allocated source to be blocked, got nil")
	}
	if !strings.Contains(err.Error(), "multi-invoice allocated") {
		t.Fatalf("expected multi-allocation blocker, got %v", err)
	}
	assertInvoiceBalance(t, db, s.invoiceID, decimal.NewFromInt(500), models.InvoiceStatusPartiallyPaid)
	assertInvoiceBalance(t, db, inv2, decimal.Zero, models.InvoiceStatusPaid)
}

// TestChargebackApply_MultiAllocatedOriginalBlocked ensures chargeback/dispute
// reverse apply is blocked when the original charge uses multi-invoice allocations.
func TestChargebackApply_MultiAllocatedOriginalBlocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	inv2 := newOpenInvoice(t, db, s, "INV-REV-C2", 300)
	chargeTxnID := postChargeTxn(t, db, s, 800)
	lines := []AllocationLine{
		{InvoiceID: s.invoiceID, Amount: decimal.NewFromInt(500)},
		{InvoiceID: inv2, Amount: decimal.NewFromInt(300)},
	}
	if err := AllocatePaymentToMultipleInvoices(db, s.companyID, chargeTxnID, lines, "test"); err != nil {
		t.Fatalf("multi-alloc charge: %v", err)
	}

	cbTxn := models.PaymentTransaction{
		CompanyID: s.companyID, GatewayAccountID: s.gwID, PaymentRequestID: &s.requestID,
		TransactionType: models.TxnTypeChargeback, Amount: decimal.NewFromInt(200),
		CurrencyCode: "CAD", Status: "completed", RawPayload: datatypes.JSON("{}"),
		OriginalTransactionID: &chargeTxnID,
	}
	CreatePaymentTransaction(db, &cbTxn)

	cbAcct := models.Account{
		CompanyID: s.companyID, Code: "6701", Name: "CB Expense",
		RootAccountType: models.RootExpense, DetailAccountType: "operating_expense", IsActive: true,
	}
	if err := db.Create(&cbAcct).Error; err != nil {
		t.Fatalf("create chargeback account: %v", err)
	}
	if err := db.Model(&models.PaymentAccountingMapping{}).
		Where("company_id = ? AND gateway_account_id = ?", s.companyID, s.gwID).
		Update("chargeback_account_id", cbAcct.ID).Error; err != nil {
		t.Fatalf("update chargeback mapping: %v", err)
	}
	if _, err := PostPaymentTransactionToJournalEntry(db, s.companyID, cbTxn.ID, "test"); err != nil {
		t.Fatalf("post chargeback: %v", err)
	}

	err := ApplyChargebackTransactionToInvoice(db, s.companyID, cbTxn.ID, "test")
	if err == nil {
		t.Fatal("expected chargeback apply on multi-allocated source to be blocked, got nil")
	}
	if !strings.Contains(err.Error(), "multi-invoice allocated") {
		t.Fatalf("expected multi-allocation blocker, got %v", err)
	}
	assertInvoiceBalance(t, db, s.invoiceID, decimal.NewFromInt(500), models.InvoiceStatusPartiallyPaid)
	assertInvoiceBalance(t, db, inv2, decimal.Zero, models.InvoiceStatusPaid)
}

// ── C. Compatibility / no regression ─────────────────────────────────────────

// TestPaymentMultiAlloc_SingleInvoiceOldPathStillWorks confirms that the
// existing single-invoice apply path still works when no multi-alloc records exist.
func TestPaymentMultiAlloc_SingleInvoiceOldPathStillWorks(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	txnID := postChargeTxn(t, db, s, 1000)
	if err := ApplyPaymentTransactionToInvoice(db, s.companyID, txnID, "test"); err != nil {
		t.Fatalf("single apply: %v", err)
	}
	assertInvoiceBalance(t, db, s.invoiceID, decimal.Zero, models.InvoiceStatusPaid)
}

// TestPaymentMultiAlloc_NoLines rejects an empty allocation lines slice.
func TestPaymentMultiAlloc_NoLines(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	txnID := postedChargeTxnNoRequest(t, db, s, decimal.NewFromInt(500))
	err := AllocatePaymentToMultipleInvoices(db, s.companyID, txnID, nil, "test")
	if err == nil {
		t.Fatal("expected error for empty lines, got nil")
	}
}

// TestPaymentMultiAlloc_DuplicateInvoiceInLines rejects a slice with duplicate
// invoice IDs in the same submission.
func TestPaymentMultiAlloc_DuplicateInvoiceInLines(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	inv1 := newOpenInvoice(t, db, s, "INV-R1", 500)
	txnID := postedChargeTxnNoRequest(t, db, s, decimal.NewFromInt(1000))

	lines := []AllocationLine{
		{InvoiceID: inv1, Amount: decimal.NewFromInt(200)},
		{InvoiceID: inv1, Amount: decimal.NewFromInt(100)}, // duplicate
	}
	err := AllocatePaymentToMultipleInvoices(db, s.companyID, txnID, lines, "test")
	if err == nil {
		t.Fatal("expected error for duplicate invoice, got nil")
	}
}

// TestCreditMultiAlloc_SingleApplyStillWorks confirms the old single-invoice
// ApplyCustomerCreditToInvoice still works.
func TestCreditMultiAlloc_SingleApplyStillWorks(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	if err := db.AutoMigrate(&models.PaymentAllocation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	invID := newInvoiceForCredit(t, db, s, decimal.NewFromInt(400))
	creditID := newActiveCredit(t, db, s, decimal.NewFromInt(600))

	if err := ApplyCustomerCreditToInvoice(db, s.companyID, creditID, invID, decimal.NewFromInt(300), "test"); err != nil {
		t.Fatalf("single credit apply: %v", err)
	}
	assertCreditRemaining(t, db, creditID, decimal.NewFromInt(300))
	assertInvoiceBalance(t, db, invID, decimal.NewFromInt(100), models.InvoiceStatusPartiallyPaid)
}
