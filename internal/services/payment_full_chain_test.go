// 遵循project_guide.md
package services

// payment_full_chain_test.go — End-to-end integration tests for the complete
// payment lifecycle: invoice → request → transaction → post → apply → unapply
// → refund → refund-apply. Verifies system consistency across all layers.

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/datatypes"
)

// Uses testPaymentApplicationDB, setupPayApp from payment_application_test.go.

func TestFullChain_InvoiceToPaymentToRefund(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	// ── Step 1: Invoice exists (issued, BalanceDue=1000) ─────────────────
	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusIssued || !inv.BalanceDue.Equal(decimal.NewFromInt(1000)) {
		t.Fatalf("Precondition: expected issued/1000, got %s/%s", inv.Status, inv.BalanceDue)
	}

	// ── Step 2: Payment request already exists from setupPayApp ──────────
	// The setup creates a payment request (s.requestID) linked to the invoice.
	// Verify it exists and has correct defaults.
	var req models.PaymentRequest
	db.First(&req, s.requestID)
	if req.InvoiceID == nil || *req.InvoiceID != s.invoiceID {
		t.Fatal("Request should be linked to invoice")
	}

	// ── Step 3: Record charge transaction ────────────────────────────────
	chargeTxn := models.PaymentTransaction{
		CompanyID: s.companyID, GatewayAccountID: s.gwID, PaymentRequestID: &req.ID,
		TransactionType: models.TxnTypeCharge, Amount: decimal.NewFromInt(1000),
		CurrencyCode: "CAD", Status: "completed", RawPayload: datatypes.JSON("{}"),
	}
	CreatePaymentTransaction(db, &chargeTxn)

	// ── Step 4: Post charge → creates JE (Dr GW Clearing, Cr AR) ────────
	je, err := PostPaymentTransactionToJournalEntry(db, s.companyID, chargeTxn.ID, "test")
	if err != nil {
		t.Fatalf("Post charge: %v", err)
	}
	if je.SourceType != models.LedgerSourcePaymentGateway {
		t.Errorf("JE source type expected payment_gateway, got %s", je.SourceType)
	}

	// Invoice still NOT paid (posting ≠ application).
	db.First(&inv, s.invoiceID)
	if inv.Status == models.InvoiceStatusPaid {
		t.Fatal("Invoice should NOT be paid after posting alone")
	}

	// ── Step 5: Apply charge → invoice paid ──────────────────────────────
	err = ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxn.ID, "test")
	if err != nil {
		t.Fatalf("Apply charge: %v", err)
	}

	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPaid {
		t.Errorf("Expected paid, got %s", inv.Status)
	}
	if !inv.BalanceDue.IsZero() {
		t.Errorf("Expected BalanceDue 0, got %s", inv.BalanceDue)
	}

	// ── Step 6: Duplicate request blocked ────────────────────────────────
	// Invoice is now paid — new request should be blocked.
	_, err = CreatePaymentRequestForInvoice(db, InvoicePaymentRequestInput{
		CompanyID: s.companyID, InvoiceID: s.invoiceID, GatewayAccountID: s.gwID,
	})
	if err == nil {
		t.Fatal("Paid invoice should not accept new payment requests")
	}

	// ── Step 7: Unapply → invoice restored ───────────────────────────────
	err = UnapplyPaymentTransaction(db, s.companyID, chargeTxn.ID, "test")
	if err != nil {
		t.Fatalf("Unapply: %v", err)
	}

	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusIssued {
		t.Errorf("Expected issued after unapply, got %s", inv.Status)
	}
	if !inv.BalanceDue.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("Expected BalanceDue 1000, got %s", inv.BalanceDue)
	}

	// ── Step 8: Re-apply → paid again ────────────────────────────────────
	err = ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxn.ID, "test")
	if err != nil {
		t.Fatalf("Re-apply: %v", err)
	}

	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPaid {
		t.Fatalf("Expected paid after re-apply, got %s", inv.Status)
	}

	// ── Step 9: Record refund transaction ────────────────────────────────
	refundTxn := models.PaymentTransaction{
		CompanyID: s.companyID, GatewayAccountID: s.gwID, PaymentRequestID: &req.ID,
		TransactionType: models.TxnTypeRefund, Amount: decimal.NewFromInt(400),
		CurrencyCode: "CAD", Status: "completed", RawPayload: datatypes.JSON("{}"),
	}
	CreatePaymentTransaction(db, &refundTxn)

	// ── Step 10: Post refund → JE (Dr Refund, Cr GW Clearing) ───────────
	_, err = PostPaymentTransactionToJournalEntry(db, s.companyID, refundTxn.ID, "test")
	if err != nil {
		t.Fatalf("Post refund: %v", err)
	}

	// Invoice status should NOT change from posting alone.
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPaid {
		t.Fatalf("Invoice should still be paid after refund posting, got %s", inv.Status)
	}

	// ── Step 11: Apply refund → partially_paid ───────────────────────────
	err = ApplyRefundTransactionToInvoice(db, s.companyID, refundTxn.ID, "test")
	if err != nil {
		t.Fatalf("Apply refund: %v", err)
	}

	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPartiallyPaid {
		t.Errorf("Expected partially_paid after refund apply, got %s", inv.Status)
	}
	if !inv.BalanceDue.Equal(decimal.NewFromInt(400)) {
		t.Errorf("Expected BalanceDue 400, got %s", inv.BalanceDue)
	}

	// ── Step 12: Verify no cross-contamination with settlements/channels ─
	// Settlement and channel tables should have zero rows for this company
	// (no settlement/channel operations were performed).
	var settlementCount int64
	db.Model(&models.ChannelSettlement{}).Where("company_id = ?", s.companyID).Count(&settlementCount)
	if settlementCount != 0 {
		t.Error("Settlement layer should be untouched by payment gateway operations")
	}

	var channelOrderCount int64
	db.Model(&models.ChannelOrder{}).Where("company_id = ?", s.companyID).Count(&channelOrderCount)
	if channelOrderCount != 0 {
		t.Error("Channel order layer should be untouched by payment gateway operations")
	}

	// ── Step 13: Verify JE count is correct ──────────────────────────────
	// Expected: 1 charge JE + 1 refund JE = 2 JEs total.
	var jeCount int64
	db.Model(&models.JournalEntry{}).Where("company_id = ?", s.companyID).Count(&jeCount)
	if jeCount != 2 {
		t.Errorf("Expected 2 JEs (charge + refund), got %d", jeCount)
	}
}

func TestFullChain_PartialPayments(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	// Invoice: $1000.

	// ── Partial charge 1: $600 ───────────────────────────────────────────
	req1 := models.PaymentRequest{
		CompanyID: s.companyID, GatewayAccountID: s.gwID, InvoiceID: &s.invoiceID,
		Amount: decimal.NewFromInt(600), Status: models.PaymentRequestCreated,
	}
	CreatePaymentRequest(db, &req1)

	txn1 := models.PaymentTransaction{
		CompanyID: s.companyID, GatewayAccountID: s.gwID, PaymentRequestID: &req1.ID,
		TransactionType: models.TxnTypeCharge, Amount: decimal.NewFromInt(600),
		CurrencyCode: "CAD", Status: "completed", RawPayload: datatypes.JSON("{}"),
	}
	CreatePaymentTransaction(db, &txn1)
	PostPaymentTransactionToJournalEntry(db, s.companyID, txn1.ID, "test")
	ApplyPaymentTransactionToInvoice(db, s.companyID, txn1.ID, "test")

	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPartiallyPaid {
		t.Fatalf("Expected partially_paid after first charge, got %s", inv.Status)
	}
	if !inv.BalanceDue.Equal(decimal.NewFromInt(400)) {
		t.Fatalf("Expected BalanceDue 400, got %s", inv.BalanceDue)
	}

	// ── Partial charge 2: $400 → fully paid ──────────────────────────────
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

	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPaid {
		t.Fatalf("Expected paid after second charge, got %s", inv.Status)
	}
	if !inv.BalanceDue.IsZero() {
		t.Fatalf("Expected BalanceDue 0, got %s", inv.BalanceDue)
	}

	// ── Fee transaction (should NOT affect invoice) ──────────────────────
	feeTxn := models.PaymentTransaction{
		CompanyID: s.companyID, GatewayAccountID: s.gwID,
		TransactionType: models.TxnTypeFee, Amount: decimal.NewFromInt(30),
		CurrencyCode: "CAD", Status: "completed", RawPayload: datatypes.JSON("{}"),
	}
	CreatePaymentTransaction(db, &feeTxn)
	PostPaymentTransactionToJournalEntry(db, s.companyID, feeTxn.ID, "test")

	// Fee should NOT be applicable to invoice.
	err := ValidatePaymentTransactionApplicable(db, s.companyID, feeTxn.ID)
	if err == nil {
		t.Fatal("Fee should not be applicable to invoice")
	}

	// Invoice should still be paid.
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPaid {
		t.Fatalf("Fee posting should not change invoice status, got %s", inv.Status)
	}
}

func TestFullChain_PaymentActionState(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	// Create and post a charge.
	chargeTxn := models.PaymentTransaction{
		CompanyID: s.companyID, GatewayAccountID: s.gwID, PaymentRequestID: &s.requestID,
		TransactionType: models.TxnTypeCharge, Amount: decimal.NewFromInt(500),
		CurrencyCode: "CAD", Status: "completed",
		CreatedAt: time.Now(), RawPayload: datatypes.JSON("{}"),
	}
	CreatePaymentTransaction(db, &chargeTxn)

	// State before posting.
	state := ComputePaymentActionState(db, s.companyID, chargeTxn)
	if state.IsPosted {
		t.Error("Should not be posted yet")
	}
	if !state.CanPost {
		t.Error("Should be postable")
	}
	if state.CanApply {
		t.Error("Should not be applicable (not posted)")
	}

	// Post.
	PostPaymentTransactionToJournalEntry(db, s.companyID, chargeTxn.ID, "test")
	db.First(&chargeTxn, chargeTxn.ID)

	state = ComputePaymentActionState(db, s.companyID, chargeTxn)
	if !state.IsPosted {
		t.Error("Should be posted")
	}
	if !state.CanApply {
		t.Errorf("Should be applicable, blocker: %s", state.ApplyBlocker)
	}
	if state.CanUnapply {
		t.Error("Should not be unapplicable (not applied)")
	}

	// Apply.
	ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxn.ID, "test")
	db.First(&chargeTxn, chargeTxn.ID)

	state = ComputePaymentActionState(db, s.companyID, chargeTxn)
	if !state.IsApplied {
		t.Error("Should be applied")
	}
	if !state.CanUnapply {
		t.Errorf("Should be unapplicable, blocker: %s", state.UnapplyBlocker)
	}
	if state.CanApply {
		t.Error("Should not be re-applicable while applied")
	}
}
