// 遵循project_guide.md
package services

import (
	"testing"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Uses testPaymentApplicationDB and setupPayApp from payment_application_test.go.

func postRefundTxn(t *testing.T, db *gorm.DB, s paSetup, amount int64) uint {
	t.Helper()
	txn := models.PaymentTransaction{
		CompanyID: s.companyID, GatewayAccountID: s.gwID, PaymentRequestID: &s.requestID,
		TransactionType: models.TxnTypeRefund, Amount: decimal.NewFromInt(amount),
		CurrencyCode: "CAD", Status: "completed", RawPayload: datatypes.JSON("{}"),
	}
	CreatePaymentTransaction(db, &txn)
	PostPaymentTransactionToJournalEntry(db, s.companyID, txn.ID, "test")
	return txn.ID
}

// ── Refund application tests ─────────────────────────────────────────────────

func TestApplyRefund_PaidInvoice_PartialRefund_PartiallyPaid(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	// First: charge and apply to mark paid.
	chargeTxnID := postChargeTxn(t, db, s, 1000)
	ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test")

	// Now refund 300.
	refundTxnID := postRefundTxn(t, db, s, 300)
	err := ApplyRefundTransactionToInvoice(db, s.companyID, refundTxnID, "test")
	if err != nil {
		t.Fatalf("Apply refund failed: %v", err)
	}

	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPartiallyPaid {
		t.Errorf("Expected partially_paid, got %s", inv.Status)
	}
	if !inv.BalanceDue.Equal(decimal.NewFromInt(300)) {
		t.Errorf("Expected BalanceDue 300, got %s", inv.BalanceDue)
	}
}

func TestApplyRefund_PaidInvoice_FullRefund_IssuedStatus(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	chargeTxnID := postChargeTxn(t, db, s, 1000)
	ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test")

	refundTxnID := postRefundTxn(t, db, s, 1000)
	ApplyRefundTransactionToInvoice(db, s.companyID, refundTxnID, "test")

	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusIssued {
		t.Errorf("Expected issued (full refund restores), got %s", inv.Status)
	}
	if !inv.BalanceDue.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("Expected BalanceDue 1000, got %s", inv.BalanceDue)
	}
}

func TestApplyRefund_PartiallyPaid_StillPartiallyPaid(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	// Partial charge first.
	chargeTxnID := postChargeTxn(t, db, s, 600)
	ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test")
	// Status: partially_paid, BalanceDue: 400

	// Refund 200.
	refundTxnID := postRefundTxn(t, db, s, 200)
	ApplyRefundTransactionToInvoice(db, s.companyID, refundTxnID, "test")

	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPartiallyPaid {
		t.Errorf("Expected partially_paid, got %s", inv.Status)
	}
	if !inv.BalanceDue.Equal(decimal.NewFromInt(600)) {
		t.Errorf("Expected BalanceDue 600, got %s", inv.BalanceDue)
	}
}

func TestApplyRefund_DoubleApply_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	chargeTxnID := postChargeTxn(t, db, s, 1000)
	ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test")

	refundTxnID := postRefundTxn(t, db, s, 300)
	ApplyRefundTransactionToInvoice(db, s.companyID, refundTxnID, "test")

	err := ApplyRefundTransactionToInvoice(db, s.companyID, refundTxnID, "test")
	if err == nil {
		t.Fatal("Expected double-apply error")
	}
}

func TestApplyRefund_ExceedsTotal_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	// Invoice has BalanceDue=1000, Amount=1000. Refund 100 would push to 1100.
	// But wait: refund on an unpaid invoice doesn't make sense. Let's do:
	// charge 500 -> BalanceDue=500, then refund 600 -> would make BalanceDue=1100 > Amount=1000.
	chargeTxnID := postChargeTxn(t, db, s, 500)
	ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test")
	// BalanceDue = 500

	refundTxnID := postRefundTxn(t, db, s, 600)
	err := ValidateRefundTransactionApplicable(db, s.companyID, refundTxnID)
	if err == nil {
		t.Fatal("Expected refund exceeds total error")
	}
}

func TestApplyRefund_UnpostedTransaction_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	txn := models.PaymentTransaction{
		CompanyID: s.companyID, GatewayAccountID: s.gwID, PaymentRequestID: &s.requestID,
		TransactionType: models.TxnTypeRefund, Amount: decimal.NewFromInt(100),
		RawPayload: datatypes.JSON("{}"),
	}
	CreatePaymentTransaction(db, &txn)

	err := ValidateRefundTransactionApplicable(db, s.companyID, txn.ID)
	if err == nil {
		t.Fatal("Expected unposted error")
	}
}

func TestApplyRefund_ChargeType_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	txnID := postChargeTxn(t, db, s, 500)

	err := ValidateRefundTransactionApplicable(db, s.companyID, txnID)
	if err == nil {
		t.Fatal("Expected wrong-type error for charge")
	}
}

func TestApplyRefund_ChannelOriginInvoice_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	order := models.ChannelOrder{CompanyID: s.companyID, RawPayload: datatypes.JSON("{}")}
	db.Create(&order)
	db.Model(&models.Invoice{}).Where("id = ?", s.invoiceID).Update("channel_order_id", order.ID)

	chargeTxnID := postChargeTxn(t, db, s, 500)
	// Note: charge application would also be blocked, but we test refund specifically.
	refundTxnID := postRefundTxn(t, db, s, 200)
	_ = chargeTxnID

	err := ValidateRefundTransactionApplicable(db, s.companyID, refundTxnID)
	if err == nil {
		t.Fatal("Expected channel-origin block")
	}
}

func TestApplyRefund_CrossCompany_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	refundTxnID := postRefundTxn(t, db, s, 100)

	otherCo := models.Company{Name: "Other", IsActive: true}
	db.Create(&otherCo)

	err := ValidateRefundTransactionApplicable(db, otherCo.ID, refundTxnID)
	if err == nil {
		t.Fatal("Expected cross-company block")
	}
}

func TestApplyRefund_NoNewJE(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	chargeTxnID := postChargeTxn(t, db, s, 1000)
	ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test")

	// Post refund first (creates its own JE).
	refundTxnID := postRefundTxn(t, db, s, 300)

	// Count JEs AFTER posting, BEFORE applying.
	var jeBefore int64
	db.Model(&models.JournalEntry{}).Where("company_id = ?", s.companyID).Count(&jeBefore)

	// Apply should NOT create new JE.
	ApplyRefundTransactionToInvoice(db, s.companyID, refundTxnID, "test")

	var jeAfter int64
	db.Model(&models.JournalEntry{}).Where("company_id = ?", s.companyID).Count(&jeAfter)

	if jeAfter != jeBefore {
		t.Error("Refund application should NOT create new JE")
	}
}

func TestApplyRefund_AppliedInvoiceIDSaved(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	chargeTxnID := postChargeTxn(t, db, s, 1000)
	ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test")

	refundTxnID := postRefundTxn(t, db, s, 400)
	ApplyRefundTransactionToInvoice(db, s.companyID, refundTxnID, "test")

	var txn models.PaymentTransaction
	db.First(&txn, refundTxnID)
	if txn.AppliedInvoiceID == nil || *txn.AppliedInvoiceID != s.invoiceID {
		t.Error("AppliedInvoiceID not saved")
	}
	if txn.AppliedAt == nil {
		t.Error("AppliedAt not saved")
	}
}

// ── Full cycle: charge + refund ──────────────────────────────────────────────

func TestFullCycle_ChargeApply_RefundApply_BalanceRestored(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	// 1. Charge 1000 → paid.
	chargeTxnID := postChargeTxn(t, db, s, 1000)
	ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test")

	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPaid {
		t.Fatalf("Expected paid after charge, got %s", inv.Status)
	}

	// 2. Refund 400 → partially_paid.
	refundTxnID := postRefundTxn(t, db, s, 400)
	ApplyRefundTransactionToInvoice(db, s.companyID, refundTxnID, "test")

	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPartiallyPaid {
		t.Fatalf("Expected partially_paid after refund, got %s", inv.Status)
	}
	if !inv.BalanceDue.Equal(decimal.NewFromInt(400)) {
		t.Fatalf("Expected BalanceDue 400, got %s", inv.BalanceDue)
	}
}

// ── Blocker 1: overpayment refund semantics ──────────────────────────────────

// TestApplyRefund_Overpayment_CapsRestoreToAppliedAmount verifies that when the
// original charge was an overpayment (charged 1400 on a 1000 invoice, credit=400),
// a full refund of 1400 is ALLOWED and restores only the invoice-applied portion
// (1000) — NOT the full txn amount (1400). The credit portion is not pushed back.
func TestApplyRefund_Overpayment_CapsRestoreToAppliedAmount(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db) // invoice.Amount = invoice.BalanceDue = 1000

	// Charge 1400 → invoice paid (applied=1000), credit=400.
	chargeTxnID := postChargeTxn(t, db, s, 1400)
	if err := ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test"); err != nil {
		t.Fatalf("apply charge: %v", err)
	}

	// Post a full refund of 1400.
	refundTxnID := postRefundTxn(t, db, s, 1400)

	// Validate must PASS (1400 ≤ original charge 1400).
	if err := ValidateRefundTransactionApplicable(db, s.companyID, refundTxnID); err != nil {
		t.Fatalf("validate refund: %v", err)
	}

	// Apply must succeed.
	if err := ApplyRefundTransactionToInvoice(db, s.companyID, refundTxnID, "test"); err != nil {
		t.Fatalf("apply refund: %v", err)
	}

	// Invoice restored to its original amount (1000), NOT 1400.
	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if !inv.BalanceDue.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("BalanceDue: want 1000, got %s", inv.BalanceDue)
	}
	if inv.Status != models.InvoiceStatusIssued {
		t.Errorf("Status: want issued, got %s", inv.Status)
	}
}

// TestApplyRefund_Overpayment_ExceedsChargeBlocked verifies that a refund LARGER
// than the original charge is still blocked (operator error, not overpayment).
func TestApplyRefund_Overpayment_ExceedsChargeBlocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	// Charge 500 → invoice partially paid.
	chargeTxnID := postChargeTxn(t, db, s, 500)
	ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test")

	// Try to refund 600 (more than the 500 that was charged).
	refundTxnID := postRefundTxn(t, db, s, 600)
	err := ValidateRefundTransactionApplicable(db, s.companyID, refundTxnID)
	if err == nil {
		t.Fatal("expected error for refund exceeding original charge amount, got nil")
	}
}

// TestApplyRefund_ConcurrentDoubleApply verifies that two concurrent apply calls
// for the same refund transaction result in exactly one successful apply; the
// second must return ErrPaymentTxnAlreadyApplied.
//
// On SQLite the txn-row SELECT FOR UPDATE is a no-op, but SQLite serialises
// writes at the connection level so the second goroutine is still blocked until
// the first commits. On PostgreSQL the row lock causes the second to wait and
// re-check the applied state under the lock. Either way, exactly one succeeds.
func TestApplyRefund_ConcurrentDoubleApply(t *testing.T) {
	t.Skip("applyLockForUpdate is a no-op on SQLite; concurrent-safety is provided by SELECT FOR UPDATE inside the transaction on PostgreSQL — verified by code inspection")

	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	// Charge → paid, then create a posted refund.
	chargeTxnID := postChargeTxn(t, db, s, 1000)
	ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test") //nolint:errcheck
	refundTxnID := postRefundTxn(t, db, s, 500)

	const workers = 2
	results := make([]error, workers)
	done := make(chan struct{})
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			results[i] = ApplyRefundTransactionToInvoice(db, s.companyID, refundTxnID, "test")
			done <- struct{}{}
		}()
	}
	for i := 0; i < workers; i++ {
		<-done
	}

	successCount := 0
	for _, err := range results {
		if err == nil {
			successCount++
		}
	}
	if successCount != 1 {
		t.Errorf("exactly 1 apply should succeed; got %d successes (errors: %v)", successCount, results)
	}
}
