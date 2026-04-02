// 遵循project_guide.md
package services

import (
	"testing"

	"github.com/shopspring/decimal"
	"gobooks/internal/models"
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
