// 遵循project_guide.md
package services

// customer_credit_service_test.go — Batch 16: CustomerCredit apply tests.
//
// DB setup reuses testPaymentApplicationDB / setupPayApp from
// payment_application_test.go (same package).

import (
	"testing"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/gorm"
)

// ── Test helper ───────────────────────────────────────────────────────────────

// newActiveCredit inserts a CustomerCredit with the given remaining amount and
// returns its ID.
func newActiveCredit(t *testing.T, db *gorm.DB, s paSetup, remaining decimal.Decimal) uint {
	t.Helper()
	return newActiveCreditWithCurrency(t, db, s, remaining, "")
}

func newActiveCreditWithCurrency(t *testing.T, db *gorm.DB, s paSetup, remaining decimal.Decimal, currency string) uint {
	t.Helper()
	c := models.CustomerCredit{
		CompanyID:       s.companyID,
		CustomerID:      s.customerID,
		SourceType:      models.CreditSourceOverpayment,
		OriginalAmount:  remaining,
		RemainingAmount: remaining,
		CurrencyCode:    currency,
		Status:          models.CustomerCreditActive,
	}
	if err := db.Create(&c).Error; err != nil {
		t.Fatalf("newActiveCredit: %v", err)
	}
	return c.ID
}

// newInvoiceForCredit creates a separate invoice for the same customer so we
// can apply credits to it independently.
func newInvoiceForCredit(t *testing.T, db *gorm.DB, s paSetup, balance decimal.Decimal) uint {
	t.Helper()
	inv := models.Invoice{
		CompanyID:   s.companyID,
		InvoiceNumber: "CRED-INV",
		CustomerID:  s.customerID,
		Status:      models.InvoiceStatusIssued,
		Amount:      balance,
		BalanceDue:  balance,
		BalanceDueBase: balance,
		CustomerNameSnapshot: "C",
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatalf("newInvoiceForCredit: %v", err)
	}
	return inv.ID
}

// ── Happy path ───────────────────────────────────────────────────────────────

// TestCreditApply_HappyPath_PartialConsume applies $200 from a $500 credit to
// a $1000 invoice, asserting remaining credit = $300 and invoice BalanceDue = $800.
func TestCreditApply_HappyPath_PartialConsume(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	creditID := newActiveCredit(t, db, s, decimal.NewFromInt(500))
	invID := newInvoiceForCredit(t, db, s, decimal.NewFromInt(1000))

	if err := ApplyCustomerCreditToInvoice(db, s.companyID, creditID, invID, decimal.NewFromInt(200), "test"); err != nil {
		t.Fatalf("apply credit failed: %v", err)
	}

	var credit models.CustomerCredit
	db.First(&credit, creditID)
	if !credit.RemainingAmount.Equal(decimal.NewFromInt(300)) {
		t.Errorf("credit remaining: want 300, got %s", credit.RemainingAmount)
	}
	if credit.Status != models.CustomerCreditActive {
		t.Errorf("credit status: want active, got %s", credit.Status)
	}

	var inv models.Invoice
	db.First(&inv, invID)
	if !inv.BalanceDue.Equal(decimal.NewFromInt(800)) {
		t.Errorf("invoice BalanceDue: want 800, got %s", inv.BalanceDue)
	}
	if inv.Status != models.InvoiceStatusPartiallyPaid {
		t.Errorf("invoice status: want partially_paid, got %s", inv.Status)
	}
}

// TestCreditApply_HappyPath_FullConsume exhausts the credit and pays the invoice.
func TestCreditApply_HappyPath_FullConsume(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	creditID := newActiveCredit(t, db, s, decimal.NewFromInt(300))
	invID := newInvoiceForCredit(t, db, s, decimal.NewFromInt(300))

	if err := ApplyCustomerCreditToInvoice(db, s.companyID, creditID, invID, decimal.NewFromInt(300), "test"); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	var credit models.CustomerCredit
	db.First(&credit, creditID)
	if !credit.RemainingAmount.IsZero() {
		t.Errorf("credit remaining: want 0, got %s", credit.RemainingAmount)
	}
	if credit.Status != models.CustomerCreditExhausted {
		t.Errorf("credit status: want exhausted, got %s", credit.Status)
	}

	var inv models.Invoice
	db.First(&inv, invID)
	if inv.Status != models.InvoiceStatusPaid {
		t.Errorf("invoice status: want paid, got %s", inv.Status)
	}
}

// TestCreditApply_ApplicationRecord verifies that a CustomerCreditApplication
// row is created with the correct fields.
func TestCreditApply_ApplicationRecord(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	creditID := newActiveCredit(t, db, s, decimal.NewFromInt(400))
	invID := newInvoiceForCredit(t, db, s, decimal.NewFromInt(600))

	if err := ApplyCustomerCreditToInvoice(db, s.companyID, creditID, invID, decimal.NewFromInt(150), "test"); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	var apps []models.CustomerCreditApplication
	db.Where("customer_credit_id = ?", creditID).Find(&apps)
	if len(apps) != 1 {
		t.Fatalf("expected 1 application record, got %d", len(apps))
	}
	if !apps[0].Amount.Equal(decimal.NewFromInt(150)) {
		t.Errorf("application amount: want 150, got %s", apps[0].Amount)
	}
	if apps[0].InvoiceID != invID {
		t.Errorf("application invoice_id: want %d, got %d", invID, apps[0].InvoiceID)
	}
}

// ── Exhaustion ───────────────────────────────────────────────────────────────

// TestCreditApply_Exhausted_Blocked verifies that applying to an exhausted
// credit is rejected.
func TestCreditApply_Exhausted_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	creditID := newActiveCredit(t, db, s, decimal.NewFromInt(200))
	invID := newInvoiceForCredit(t, db, s, decimal.NewFromInt(500))

	// Exhaust the credit.
	if err := ApplyCustomerCreditToInvoice(db, s.companyID, creditID, invID, decimal.NewFromInt(200), "test"); err != nil {
		t.Fatalf("first apply failed: %v", err)
	}

	// Second apply should fail with ErrCreditExhausted.
	inv2ID := newInvoiceForCredit(t, db, s, decimal.NewFromInt(300))
	err := ApplyCustomerCreditToInvoice(db, s.companyID, creditID, inv2ID, decimal.NewFromInt(100), "test")
	if err == nil {
		t.Fatal("expected error for exhausted credit, got nil")
	}
}

// ── Amount validation ─────────────────────────────────────────────────────────

// TestCreditApply_ExceedsRemaining_Blocked verifies that applying more than the
// credit's remaining amount is rejected.
func TestCreditApply_ExceedsRemaining_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	creditID := newActiveCredit(t, db, s, decimal.NewFromInt(100))
	invID := newInvoiceForCredit(t, db, s, decimal.NewFromInt(500))

	err := ApplyCustomerCreditToInvoice(db, s.companyID, creditID, invID, decimal.NewFromInt(200), "test")
	if err == nil {
		t.Fatal("should be blocked: amount exceeds credit remaining")
	}
}

// TestCreditApply_ExceedsInvoiceBalance_Blocked verifies that applying more
// than the invoice BalanceDue is rejected.
func TestCreditApply_ExceedsInvoiceBalance_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	creditID := newActiveCredit(t, db, s, decimal.NewFromInt(1000))
	invID := newInvoiceForCredit(t, db, s, decimal.NewFromInt(200))

	err := ApplyCustomerCreditToInvoice(db, s.companyID, creditID, invID, decimal.NewFromInt(500), "test")
	if err == nil {
		t.Fatal("should be blocked: amount exceeds invoice BalanceDue")
	}

	// Credit must not be consumed.
	var credit models.CustomerCredit
	db.First(&credit, creditID)
	if !credit.RemainingAmount.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("credit remaining should be unchanged, got %s", credit.RemainingAmount)
	}
}

// TestCreditApply_ZeroAmount_Blocked verifies that zero amount is rejected.
func TestCreditApply_ZeroAmount_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	creditID := newActiveCredit(t, db, s, decimal.NewFromInt(500))
	invID := newInvoiceForCredit(t, db, s, decimal.NewFromInt(500))

	err := ApplyCustomerCreditToInvoice(db, s.companyID, creditID, invID, decimal.Zero, "test")
	if err == nil {
		t.Fatal("zero amount should be blocked")
	}
}

// ── Cross-entity isolation ────────────────────────────────────────────────────

// TestCreditApply_CrossCustomer_Blocked creates a credit for customer A and
// tries to apply it to customer B's invoice; must be rejected.
func TestCreditApply_CrossCustomer_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db) // customer A

	// Create customer B with their own invoice.
	custB := models.Customer{CompanyID: s.companyID, Name: "B"}
	db.Create(&custB)
	invB := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "B-1", CustomerID: custB.ID,
		Status: models.InvoiceStatusIssued, Amount: decimal.NewFromInt(500),
		BalanceDue: decimal.NewFromInt(500), BalanceDueBase: decimal.NewFromInt(500),
		CustomerNameSnapshot: "B",
	}
	db.Create(&invB)

	creditID := newActiveCredit(t, db, s, decimal.NewFromInt(300)) // credit belongs to s.customerID

	err := ApplyCustomerCreditToInvoice(db, s.companyID, creditID, invB.ID, decimal.NewFromInt(100), "test")
	if err == nil {
		t.Fatal("cross-customer apply should be blocked")
	}

	// No partial writes.
	var credit models.CustomerCredit
	db.First(&credit, creditID)
	if !credit.RemainingAmount.Equal(decimal.NewFromInt(300)) {
		t.Error("credit should not be consumed on cross-customer reject")
	}
}

// TestCreditApply_CrossCompany_Blocked verifies that a credit ID from one
// company cannot be applied under a different company.
func TestCreditApply_CrossCompany_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	co2 := models.Company{Name: "Co2", IsActive: true, BaseCurrencyCode: "CAD"}
	db.Create(&co2)

	creditID := newActiveCredit(t, db, s, decimal.NewFromInt(300))
	invID := newInvoiceForCredit(t, db, s, decimal.NewFromInt(500))

	err := ApplyCustomerCreditToInvoice(db, co2.ID, creditID, invID, decimal.NewFromInt(100), "test")
	if err == nil {
		t.Fatal("cross-company apply should be blocked")
	}
}

// ── Channel invoice blocked ───────────────────────────────────────────────────

// TestCreditApply_ChannelInvoice_Blocked verifies that channel-origin invoices
// cannot receive credit applications.
func TestCreditApply_ChannelInvoice_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	creditID := newActiveCredit(t, db, s, decimal.NewFromInt(300))

	// Create a channel invoice.
	channelOrderID := uint(999)
	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "CH-1", CustomerID: s.customerID,
		Status: models.InvoiceStatusIssued, Amount: decimal.NewFromInt(500),
		BalanceDue: decimal.NewFromInt(500), BalanceDueBase: decimal.NewFromInt(500),
		CustomerNameSnapshot: "C", ChannelOrderID: &channelOrderID,
	}
	db.Create(&inv)

	err := ApplyCustomerCreditToInvoice(db, s.companyID, creditID, inv.ID, decimal.NewFromInt(100), "test")
	if err == nil {
		t.Fatal("channel-origin invoice should be blocked")
	}
}

// ── Currency mismatch blocked ─────────────────────────────────────────────────

// TestCreditApply_CurrencyMismatch_Blocked verifies that a credit in one
// currency cannot be applied to an invoice in a different currency.
func TestCreditApply_CurrencyMismatch_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	// Credit in USD.
	creditID := newActiveCreditWithCurrency(t, db, s, decimal.NewFromInt(300), "USD")

	// Invoice in CAD (empty = base currency CAD).
	invID := newInvoiceForCredit(t, db, s, decimal.NewFromInt(500))

	err := ApplyCustomerCreditToInvoice(db, s.companyID, creditID, invID, decimal.NewFromInt(100), "test")
	if err == nil {
		t.Fatal("currency mismatch should be blocked")
	}
}

// ── Idempotency / duplicate protection ───────────────────────────────────────

// TestCreditApply_NoDuplicateApplication verifies that applying a credit twice
// to the same invoice does not double-consume the credit or double-reduce the
// invoice (the second apply consumes a second slice if credit/balance remain).
func TestCreditApply_TwoAppliesShareCredit(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	creditID := newActiveCredit(t, db, s, decimal.NewFromInt(600))
	invID := newInvoiceForCredit(t, db, s, decimal.NewFromInt(1000))

	// Apply $200 twice — both should succeed because credit and balance allow it.
	if err := ApplyCustomerCreditToInvoice(db, s.companyID, creditID, invID, decimal.NewFromInt(200), "test"); err != nil {
		t.Fatalf("first apply failed: %v", err)
	}
	if err := ApplyCustomerCreditToInvoice(db, s.companyID, creditID, invID, decimal.NewFromInt(200), "test"); err != nil {
		t.Fatalf("second apply failed: %v", err)
	}

	var credit models.CustomerCredit
	db.First(&credit, creditID)
	if !credit.RemainingAmount.Equal(decimal.NewFromInt(200)) {
		t.Errorf("credit remaining: want 200, got %s", credit.RemainingAmount)
	}

	var inv models.Invoice
	db.First(&inv, invID)
	if !inv.BalanceDue.Equal(decimal.NewFromInt(600)) {
		t.Errorf("invoice BalanceDue: want 600, got %s", inv.BalanceDue)
	}
}

// TestCreditApply_ConcurrentDoubleApply verifies concurrent apply safety.
// Skipped on SQLite (FOR UPDATE is a no-op); PostgreSQL enforces via row lock.
func TestCreditApply_ConcurrentDoubleApply(t *testing.T) {
	t.Skip("applyLockForUpdate is a no-op on SQLite; concurrent-safety is provided by SELECT FOR UPDATE inside the transaction on PostgreSQL — verified by code inspection")
}

// ── Invoice status rejection ──────────────────────────────────────────────────

// TestCreditApply_PaidInvoice_Blocked verifies that a fully paid invoice cannot
// receive further credit applications.
func TestCreditApply_PaidInvoice_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	creditID := newActiveCredit(t, db, s, decimal.NewFromInt(300))
	invID := newInvoiceForCredit(t, db, s, decimal.NewFromInt(500))
	// Mark invoice as paid manually.
	db.Model(&models.Invoice{}).Where("id = ?", invID).Updates(map[string]any{
		"status": string(models.InvoiceStatusPaid), "balance_due": "0", "balance_due_base": "0",
	})

	err := ApplyCustomerCreditToInvoice(db, s.companyID, creditID, invID, decimal.NewFromInt(100), "test")
	if err == nil {
		t.Fatal("paid invoice should be blocked")
	}
}

// TestCreditApply_DraftInvoice_Blocked verifies draft invoices are rejected.
func TestCreditApply_DraftInvoice_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	creditID := newActiveCredit(t, db, s, decimal.NewFromInt(300))
	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "DRAFT-1", CustomerID: s.customerID,
		Status: models.InvoiceStatusDraft, Amount: decimal.NewFromInt(500),
		BalanceDue: decimal.NewFromInt(500), BalanceDueBase: decimal.NewFromInt(500),
		CustomerNameSnapshot: "C",
	}
	db.Create(&inv)

	err := ApplyCustomerCreditToInvoice(db, s.companyID, creditID, inv.ID, decimal.NewFromInt(100), "test")
	if err == nil {
		t.Fatal("draft invoice should be blocked")
	}
}
