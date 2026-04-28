// 遵循project_guide.md
package services

import (
	"testing"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/datatypes"
)

// Uses testPaymentApplicationDB, setupPayApp, postChargeTxn from payment_application_test.go.

// ── Unapply tests ────────────────────────────────────────────────────────────

func TestUnapply_RestoresBalanceDue(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	txnID := postChargeTxn(t, db, s, 1000)
	ApplyPaymentTransactionToInvoice(db, s.companyID, txnID, "test")

	// Invoice should be paid.
	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPaid {
		t.Fatalf("Expected paid, got %s", inv.Status)
	}

	// Unapply.
	err := UnapplyPaymentTransaction(db, s.companyID, txnID, "test")
	if err != nil {
		t.Fatalf("Unapply failed: %v", err)
	}

	db.First(&inv, s.invoiceID)
	if !inv.BalanceDue.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("Expected BalanceDue 1000, got %s", inv.BalanceDue)
	}
	if inv.Status != models.InvoiceStatusIssued {
		t.Errorf("Expected issued after full unapply, got %s", inv.Status)
	}
}

func TestUnapply_PartialPayment_ReturnsToPartiallyPaid(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	// Apply two charges: 600 + 400 = 1000 (paid).
	txn1 := postChargeTxn(t, db, s, 600)
	ApplyPaymentTransactionToInvoice(db, s.companyID, txn1, "test")

	// Need a second payment request for second charge.
	req2 := models.PaymentRequest{
		CompanyID: s.companyID, GatewayAccountID: s.gwID, InvoiceID: &s.invoiceID,
		Amount: decimal.NewFromInt(400), Status: models.PaymentRequestCreated,
	}
	CreatePaymentRequest(db, &req2)
	txn2 := models.PaymentTransaction{
		CompanyID: s.companyID, GatewayAccountID: s.gwID, PaymentRequestID: &req2.ID,
		TransactionType: models.TxnTypeCharge, Amount: decimal.NewFromInt(400),
		CurrencyCode: "CAD", Status: "completed", RawPayload: datatypes.JSON("{}"),
	}
	CreatePaymentTransaction(db, &txn2)
	PostPaymentTransactionToJournalEntry(db, s.companyID, txn2.ID, "test")
	ApplyPaymentTransactionToInvoice(db, s.companyID, txn2.ID, "test")

	// Now unapply the second charge (400).
	UnapplyPaymentTransaction(db, s.companyID, txn2.ID, "test")

	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPartiallyPaid {
		t.Errorf("Expected partially_paid, got %s", inv.Status)
	}
	if !inv.BalanceDue.Equal(decimal.NewFromInt(400)) {
		t.Errorf("Expected BalanceDue 400, got %s", inv.BalanceDue)
	}
}

func TestUnapply_ClearsAppliedState(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	txnID := postChargeTxn(t, db, s, 500)
	ApplyPaymentTransactionToInvoice(db, s.companyID, txnID, "test")
	UnapplyPaymentTransaction(db, s.companyID, txnID, "test")

	var txn models.PaymentTransaction
	db.First(&txn, txnID)
	if txn.AppliedInvoiceID != nil {
		t.Error("AppliedInvoiceID should be nil after unapply")
	}
	if txn.AppliedAt != nil {
		t.Error("AppliedAt should be nil after unapply")
	}
}

func TestUnapply_NotApplied_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	txnID := postChargeTxn(t, db, s, 500)
	// NOT applied.

	err := ValidatePaymentTransactionUnapplicable(db, s.companyID, txnID)
	if err == nil {
		t.Fatal("Expected not-applied error")
	}
}

func TestUnapply_RefundType_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	// Post and apply a refund.
	refundTxn := models.PaymentTransaction{
		CompanyID: s.companyID, GatewayAccountID: s.gwID, PaymentRequestID: &s.requestID,
		TransactionType: models.TxnTypeRefund, Amount: decimal.NewFromInt(100),
		RawPayload: datatypes.JSON("{}"),
	}
	CreatePaymentTransaction(db, &refundTxn)
	PostPaymentTransactionToJournalEntry(db, s.companyID, refundTxn.ID, "test")

	// Manually set applied state (normally done by refund apply).
	db.Model(&refundTxn).Updates(map[string]any{"applied_invoice_id": s.invoiceID})

	err := ValidatePaymentTransactionUnapplicable(db, s.companyID, refundTxn.ID)
	if err == nil {
		t.Fatal("Expected refund type blocked for unapply")
	}
}

func TestUnapply_ChannelOrigin_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	txnID := postChargeTxn(t, db, s, 500)
	ApplyPaymentTransactionToInvoice(db, s.companyID, txnID, "test")

	// Make invoice channel-origin.
	order := models.ChannelOrder{CompanyID: s.companyID, RawPayload: datatypes.JSON("{}")}
	db.Create(&order)
	db.Model(&models.Invoice{}).Where("id = ?", s.invoiceID).Update("channel_order_id", order.ID)

	err := ValidatePaymentTransactionUnapplicable(db, s.companyID, txnID)
	if err == nil {
		t.Fatal("Expected channel-origin block")
	}
}

func TestUnapply_CrossCompany_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	txnID := postChargeTxn(t, db, s, 500)
	ApplyPaymentTransactionToInvoice(db, s.companyID, txnID, "test")

	otherCo := models.Company{Name: "Other", IsActive: true}
	db.Create(&otherCo)

	err := ValidatePaymentTransactionUnapplicable(db, otherCo.ID, txnID)
	if err == nil {
		t.Fatal("Expected cross-company block")
	}
}

func TestUnapply_NoNewJE(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	txnID := postChargeTxn(t, db, s, 500)
	ApplyPaymentTransactionToInvoice(db, s.companyID, txnID, "test")

	var jeBefore int64
	db.Model(&models.JournalEntry{}).Where("company_id = ?", s.companyID).Count(&jeBefore)

	UnapplyPaymentTransaction(db, s.companyID, txnID, "test")

	var jeAfter int64
	db.Model(&models.JournalEntry{}).Where("company_id = ?", s.companyID).Count(&jeAfter)

	if jeAfter != jeBefore {
		t.Error("Unapply should NOT create new JE")
	}
}

// ── Blocker 1: overpayment unapply ───────────────────────────────────────────

// TestUnapply_Overpayment_RestoresCorrectAmount verifies that when a txn that
// caused an overpayment (txn.Amount > invoice.BalanceDue) is unapplied, only the
// actually-applied portion (AppliedAmount) is restored to the invoice — not the
// full txn.Amount.
//
// Setup: invoice.Amount = 1000, txn.Amount = 1400 → applyAmount=1000, credit=400.
// After unapply: invoice.BalanceDue should be 1000 again (not 1400).
func TestUnapply_Overpayment_RestoresCorrectAmount(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db) // invoice.Amount = invoice.BalanceDue = 1000

	txnID := postChargeTxn(t, db, s, 1400) // 400 excess → creates credit
	if err := ApplyPaymentTransactionToInvoice(db, s.companyID, txnID, "test"); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Verify state after apply.
	var txn models.PaymentTransaction
	db.First(&txn, txnID)
	if txn.AppliedAmount == nil {
		t.Fatal("AppliedAmount must be set after apply")
	}
	if !txn.AppliedAmount.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("AppliedAmount: want 1000, got %s", txn.AppliedAmount)
	}

	// Unapply should be allowed (validate must pass).
	if err := ValidatePaymentTransactionUnapplicable(db, s.companyID, txnID); err != nil {
		t.Fatalf("validate unapply: %v", err)
	}

	if err := UnapplyPaymentTransaction(db, s.companyID, txnID, "test"); err != nil {
		t.Fatalf("unapply: %v", err)
	}

	// Invoice should be back at 1000, not 1400.
	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if !inv.BalanceDue.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("BalanceDue after unapply: want 1000, got %s", inv.BalanceDue)
	}
	if inv.Status != models.InvoiceStatusIssued {
		t.Errorf("Status after unapply: want issued, got %s", inv.Status)
	}

	// AppliedAmount must be cleared.
	// Use a fresh struct: GORM may not set a previously-non-nil *decimal.Decimal pointer
	// to nil when scanning SQL NULL into a reused struct. A fresh zero struct always starts
	// with nil, and stays nil if the column is NULL.
	var txnAfter models.PaymentTransaction
	db.First(&txnAfter, txnID)
	if txnAfter.AppliedAmount != nil {
		t.Errorf("AppliedAmount should be nil after unapply, got %s", txnAfter.AppliedAmount)
	}
	if txnAfter.AppliedInvoiceID != nil {
		t.Errorf("AppliedInvoiceID should be nil after unapply")
	}
}

// ── Full cycle: apply → unapply → re-apply ──────────────────────────────────

func TestFullCycle_Apply_Unapply_Reapply(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	txnID := postChargeTxn(t, db, s, 1000)

	// Apply → paid.
	ApplyPaymentTransactionToInvoice(db, s.companyID, txnID, "test")
	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPaid {
		t.Fatalf("Step 1: expected paid, got %s", inv.Status)
	}

	// Unapply → issued.
	UnapplyPaymentTransaction(db, s.companyID, txnID, "test")
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusIssued {
		t.Fatalf("Step 2: expected issued, got %s", inv.Status)
	}

	// Re-apply → paid again.
	ApplyPaymentTransactionToInvoice(db, s.companyID, txnID, "test")
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPaid {
		t.Fatalf("Step 3: expected paid, got %s", inv.Status)
	}
}
