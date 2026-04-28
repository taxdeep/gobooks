// 遵循project_guide.md
package services

// payment_application_overpayment_test.go — Batch 16: overpayment → credit tests.
//
// Uses testPaymentApplicationDB / setupPayApp / postChargeTxn from
// payment_application_test.go (same package).

import (
	"testing"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
)

// TestOverpayment_ExactMatch_NoCredit verifies that a payment exactly matching
// the invoice BalanceDue does NOT create a CustomerCredit.
func TestOverpayment_ExactMatch_NoCredit(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db) // invoice BalanceDue = 1000

	txnID := postChargeTxn(t, db, s, 1000)
	if err := ApplyPaymentTransactionToInvoice(db, s.companyID, txnID, "test"); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	var credits []models.CustomerCredit
	db.Where("company_id = ?", s.companyID).Find(&credits)
	if len(credits) != 0 {
		t.Errorf("exact payment should not create credit; got %d credits", len(credits))
	}

	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPaid {
		t.Errorf("status: want paid, got %s", inv.Status)
	}
}

// TestOverpayment_Partial_NoCredit verifies that a partial payment (less than
// BalanceDue) creates no credit and leaves the correct partially_paid balance.
func TestOverpayment_Partial_NoCredit(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db) // invoice BalanceDue = 1000

	txnID := postChargeTxn(t, db, s, 400)
	if err := ApplyPaymentTransactionToInvoice(db, s.companyID, txnID, "test"); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	var credits []models.CustomerCredit
	db.Where("company_id = ?", s.companyID).Find(&credits)
	if len(credits) != 0 {
		t.Errorf("partial payment should not create credit; got %d", len(credits))
	}

	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPartiallyPaid {
		t.Errorf("status: want partially_paid, got %s", inv.Status)
	}
	if !inv.BalanceDue.Equal(decimal.NewFromInt(600)) {
		t.Errorf("BalanceDue: want 600, got %s", inv.BalanceDue)
	}
}

// TestOverpayment_HappyPath verifies that invoice is capped at zero and excess
// creates a credit with correct original/remaining amounts.
func TestOverpayment_HappyPath(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db) // invoice BalanceDue = 1000

	txnID := postChargeTxn(t, db, s, 1300) // $300 excess

	if err := ApplyPaymentTransactionToInvoice(db, s.companyID, txnID, "test"); err != nil {
		t.Fatalf("apply overpayment failed: %v", err)
	}

	// Invoice must be paid at BalanceDue = 0.
	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPaid {
		t.Errorf("invoice status: want paid, got %s", inv.Status)
	}
	if !inv.BalanceDue.IsZero() {
		t.Errorf("invoice BalanceDue: want 0, got %s", inv.BalanceDue)
	}

	// One credit of 300.
	var credits []models.CustomerCredit
	db.Where("company_id = ? AND customer_id = ?", s.companyID, s.customerID).Find(&credits)
	if len(credits) != 1 {
		t.Fatalf("expected 1 credit, got %d", len(credits))
	}
	c := credits[0]
	if !c.OriginalAmount.Equal(decimal.NewFromInt(300)) {
		t.Errorf("credit original_amount: want 300, got %s", c.OriginalAmount)
	}
	if !c.RemainingAmount.Equal(decimal.NewFromInt(300)) {
		t.Errorf("credit remaining_amount: want 300, got %s", c.RemainingAmount)
	}
	if c.Status != models.CustomerCreditActive {
		t.Errorf("credit status: want active, got %s", c.Status)
	}
	if c.SourceType != models.CreditSourceOverpayment {
		t.Errorf("credit source_type: want overpayment, got %s", c.SourceType)
	}
	if c.CustomerID != s.customerID {
		t.Errorf("credit customer_id: want %d, got %d", s.customerID, c.CustomerID)
	}
}

// TestOverpayment_Idempotency_NoDoubleCredit verifies that the same transaction
// cannot create two credits even if apply is called a second time.
func TestOverpayment_Idempotency_NoDoubleCredit(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	txnID := postChargeTxn(t, db, s, 1500) // $500 excess

	// First apply.
	if err := ApplyPaymentTransactionToInvoice(db, s.companyID, txnID, "test"); err != nil {
		t.Fatalf("first apply failed: %v", err)
	}

	// Second apply on the same transaction must be rejected.
	err := ApplyPaymentTransactionToInvoice(db, s.companyID, txnID, "test")
	if err == nil {
		t.Fatal("second apply should have been rejected")
	}

	// Exactly one credit.
	var credits []models.CustomerCredit
	db.Where("company_id = ?", s.companyID).Find(&credits)
	if len(credits) != 1 {
		t.Errorf("expected exactly 1 credit, got %d", len(credits))
	}
}

// TestOverpayment_CrossCompany_Rejected verifies that applying a transaction
// from a different company is rejected and no credit is created.
func TestOverpayment_CrossCompany_Rejected(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	// Create a second company.
	co2 := models.Company{Name: "Co2", IsActive: true, BaseCurrencyCode: "CAD"}
	db.Create(&co2)

	txnID := postChargeTxn(t, db, s, 1500)

	// Attempt apply under co2's company ID.
	err := ApplyPaymentTransactionToInvoice(db, co2.ID, txnID, "test")
	if err == nil {
		t.Fatal("cross-company apply should be rejected")
	}

	var credits []models.CustomerCredit
	db.Find(&credits)
	if len(credits) != 0 {
		t.Errorf("no credit should exist, got %d", len(credits))
	}
}

// TestOverpayment_ConcurrentDoubleApply verifies concurrent-apply safety.
// Skipped on SQLite (FOR UPDATE is a no-op); PostgreSQL enforces via row lock.
func TestOverpayment_ConcurrentDoubleApply(t *testing.T) {
	t.Skip("applyLockForUpdate is a no-op on SQLite; concurrent-safety is provided by SELECT FOR UPDATE inside the transaction on PostgreSQL — verified by code inspection")
}
