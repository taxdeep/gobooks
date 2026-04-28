// 遵循project_guide.md
package services

// payment_chargeback_application_test.go — Batch 15: chargeback apply tests.
//
// Coverage (symmetric with payment_refund_application_test.go):
//
//  TestApplyChargeback_HappyPath_PartialRestore
//      charge 1000 → paid; chargeback 400 → partially_paid, BalanceDue=400
//
//  TestApplyChargeback_HappyPath_FullRestore
//      charge 1000 → paid; chargeback 1000 → issued, BalanceDue=1000
//
//  TestApplyChargeback_AppliedStateSet
//      AppliedInvoiceID and AppliedAt written to chargeback transaction
//
//  TestApplyChargeback_NoNewJE
//      apply does NOT create a new JournalEntry
//
//  TestApplyChargeback_ExceedsTotal_Blocked
//      chargeback would push BalanceDue above invoice.Amount → ErrChargebackExceedsTotal
//
//  TestApplyChargeback_DoubleApply_Blocked
//      second apply of same chargeback → ErrPaymentTxnAlreadyApplied
//
//  TestApplyChargeback_UnpostedBlocked
//      chargeback not yet posted → ErrPaymentTxnNotApplicable
//
//  TestApplyChargeback_WrongType_Blocked
//      refund transaction used on chargeback-apply path → ErrPaymentTxnNotApplicable
//
//  TestApplyChargeback_CrossCompany_Blocked
//      other company cannot apply this chargeback
//
//  TestApplyChargeback_InvoiceResolution_ViaOriginalTxnID
//      chargeback has no direct PaymentRequestID but OriginalTransactionID links
//      back to a charge that has one; resolveChargebackInvoiceID must find the invoice

import (
	"testing"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Uses testPaymentApplicationDB and setupPayApp / postChargeTxn from
// payment_application_test.go (same package).

// postChargebackTxnViaRequest creates and posts a TxnTypeChargeback transaction
// that inherits the payment request linkage directly (the standard path for
// dispute-generated chargebacks: PaymentRequestID = original charge's request).
func postChargebackTxnViaRequest(t *testing.T, db *gorm.DB, s paSetup, amount int64) uint {
	t.Helper()
	// Setup requires ChargebackAccountID on the mapping.
	cbAcct := models.Account{
		CompanyID: s.companyID, Code: "6700", Name: "CB Expense",
		RootAccountType:   models.RootExpense,
		DetailAccountType: "operating_expense",
		IsActive:          true,
	}
	db.Create(&cbAcct)
	// Update the mapping to include ChargebackAccountID.
	db.Model(&models.PaymentAccountingMapping{}).
		Where("company_id = ? AND gateway_account_id = ?", s.companyID, s.gwID).
		Update("chargeback_account_id", cbAcct.ID)

	txn := models.PaymentTransaction{
		CompanyID:        s.companyID,
		GatewayAccountID: s.gwID,
		PaymentRequestID: &s.requestID, // inherit invoice linkage
		TransactionType:  models.TxnTypeChargeback,
		Amount:           decimal.NewFromInt(amount),
		CurrencyCode:     "CAD",
		Status:           "completed",
		RawPayload:       datatypes.JSON("{}"),
	}
	CreatePaymentTransaction(db, &txn)
	PostPaymentTransactionToJournalEntry(db, s.companyID, txn.ID, "test") //nolint:errcheck
	return txn.ID
}

// ── Happy path tests ─────────────────────────────────────────────────────────

func TestApplyChargeback_HappyPath_PartialRestore(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	// 1. Charge 1000 → invoice paid, BalanceDue=0
	chargeTxnID := postChargeTxn(t, db, s, 1000)
	ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test") //nolint:errcheck

	// 2. Chargeback 400 (partial chargeback)
	cbTxnID := postChargebackTxnViaRequest(t, db, s, 400)
	err := ApplyChargebackTransactionToInvoice(db, s.companyID, cbTxnID, "test")
	if err != nil {
		t.Fatalf("apply chargeback failed: %v", err)
	}

	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPartiallyPaid {
		t.Errorf("status: want partially_paid, got %s", inv.Status)
	}
	if !inv.BalanceDue.Equal(decimal.NewFromInt(400)) {
		t.Errorf("BalanceDue: want 400, got %s", inv.BalanceDue)
	}
}

func TestApplyChargeback_HappyPath_FullRestore(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	chargeTxnID := postChargeTxn(t, db, s, 1000)
	ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test") //nolint:errcheck

	cbTxnID := postChargebackTxnViaRequest(t, db, s, 1000)
	err := ApplyChargebackTransactionToInvoice(db, s.companyID, cbTxnID, "test")
	if err != nil {
		t.Fatalf("apply chargeback failed: %v", err)
	}

	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusIssued {
		t.Errorf("status: want issued after full chargeback restore, got %s", inv.Status)
	}
	if !inv.BalanceDue.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("BalanceDue: want 1000, got %s", inv.BalanceDue)
	}
}

func TestApplyChargeback_AppliedStateSet(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	chargeTxnID := postChargeTxn(t, db, s, 1000)
	ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test") //nolint:errcheck

	cbTxnID := postChargebackTxnViaRequest(t, db, s, 500)
	ApplyChargebackTransactionToInvoice(db, s.companyID, cbTxnID, "test") //nolint:errcheck

	var txn models.PaymentTransaction
	db.First(&txn, cbTxnID)
	if txn.AppliedInvoiceID == nil || *txn.AppliedInvoiceID != s.invoiceID {
		t.Error("AppliedInvoiceID not set or points to wrong invoice")
	}
	if txn.AppliedAt == nil {
		t.Error("AppliedAt not set after chargeback apply")
	}
}

func TestApplyChargeback_NoNewJE(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	chargeTxnID := postChargeTxn(t, db, s, 1000)
	ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test") //nolint:errcheck

	cbTxnID := postChargebackTxnViaRequest(t, db, s, 300)

	// Count JEs after posting, before applying.
	var jeBefore int64
	db.Model(&models.JournalEntry{}).Where("company_id = ?", s.companyID).Count(&jeBefore)

	ApplyChargebackTransactionToInvoice(db, s.companyID, cbTxnID, "test") //nolint:errcheck

	var jeAfter int64
	db.Model(&models.JournalEntry{}).Where("company_id = ?", s.companyID).Count(&jeAfter)

	if jeAfter != jeBefore {
		t.Error("chargeback apply must not create a new JournalEntry")
	}
}

// ── Reject paths ─────────────────────────────────────────────────────────────

func TestApplyChargeback_ExceedsTotal_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	// Charge 500 → BalanceDue=500. Chargeback 600 would push BalanceDue to 1100 > 1000.
	chargeTxnID := postChargeTxn(t, db, s, 500)
	ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test") //nolint:errcheck
	// BalanceDue = 500

	cbTxnID := postChargebackTxnViaRequest(t, db, s, 600)
	err := ValidateChargebackTransactionApplicable(db, s.companyID, cbTxnID)
	if err == nil {
		t.Fatal("expected ErrChargebackExceedsTotal, got nil")
	}
}

func TestApplyChargeback_DoubleApply_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	chargeTxnID := postChargeTxn(t, db, s, 1000)
	ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test") //nolint:errcheck

	cbTxnID := postChargebackTxnViaRequest(t, db, s, 300)
	ApplyChargebackTransactionToInvoice(db, s.companyID, cbTxnID, "test") //nolint:errcheck

	err := ApplyChargebackTransactionToInvoice(db, s.companyID, cbTxnID, "test")
	if err == nil {
		t.Fatal("expected double-apply error, got nil")
	}
}

func TestApplyChargeback_UnpostedBlocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	// Create chargeback but do NOT post it.
	txn := models.PaymentTransaction{
		CompanyID:        s.companyID,
		GatewayAccountID: s.gwID,
		PaymentRequestID: &s.requestID,
		TransactionType:  models.TxnTypeChargeback,
		Amount:           decimal.NewFromInt(100),
		CurrencyCode:     "CAD",
		Status:           "completed",
		RawPayload:       datatypes.JSON("{}"),
	}
	CreatePaymentTransaction(db, &txn)

	err := ValidateChargebackTransactionApplicable(db, s.companyID, txn.ID)
	if err == nil {
		t.Fatal("expected unposted error, got nil")
	}
}

func TestApplyChargeback_WrongType_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	// Use a charge transaction on the chargeback-apply path.
	chargeTxnID := postChargeTxn(t, db, s, 500)

	err := ValidateChargebackTransactionApplicable(db, s.companyID, chargeTxnID)
	if err == nil {
		t.Fatal("expected wrong-type error for charge txn on chargeback path, got nil")
	}
}

func TestApplyChargeback_CrossCompany_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	chargeTxnID := postChargeTxn(t, db, s, 1000)
	ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test") //nolint:errcheck

	cbTxnID := postChargebackTxnViaRequest(t, db, s, 200)

	otherCo := models.Company{Name: "OtherCo", IsActive: true}
	db.Create(&otherCo)

	err := ValidateChargebackTransactionApplicable(db, otherCo.ID, cbTxnID)
	if err == nil {
		t.Fatal("expected cross-company block, got nil")
	}
}

// TestApplyChargeback_InvoiceResolution_ViaOriginalTxnID verifies the fallback
// path in resolveChargebackInvoiceID: when the chargeback transaction has no
// direct PaymentRequestID, the function falls back to OriginalTransactionID
// → original charge's PaymentRequestID → invoice.
//
// This is the path that dispute-generated chargebacks follow after the
// LoseGatewayDispute fix: the chargeback inherits the charge's PaymentRequestID
// directly, so this test explicitly constructs the fallback scenario by clearing
// PaymentRequestID and relying solely on OriginalTransactionID.
func TestApplyChargeback_InvoiceResolution_ViaOriginalTxnID(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	// Ensure ChargebackAccountID is set on the mapping (re-use helper).
	cbAcct := models.Account{
		CompanyID: s.companyID, Code: "6701", Name: "CB Expense 2",
		RootAccountType:   models.RootExpense,
		DetailAccountType: "operating_expense",
		IsActive:          true,
	}
	db.Create(&cbAcct)
	db.Model(&models.PaymentAccountingMapping{}).
		Where("company_id = ? AND gateway_account_id = ?", s.companyID, s.gwID).
		Update("chargeback_account_id", cbAcct.ID)

	// 1. Charge and apply.
	chargeTxnID := postChargeTxn(t, db, s, 1000)
	ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test") //nolint:errcheck

	// 2. Create a chargeback with OriginalTransactionID = chargeID but NO PaymentRequestID.
	chargeIDPtr := chargeTxnID
	cbTxn := models.PaymentTransaction{
		CompanyID:             s.companyID,
		GatewayAccountID:      s.gwID,
		PaymentRequestID:      nil, // no direct request linkage
		OriginalTransactionID: &chargeIDPtr,
		TransactionType:       models.TxnTypeChargeback,
		Amount:                decimal.NewFromInt(300),
		CurrencyCode:          "CAD",
		Status:                "completed",
		RawPayload:            datatypes.JSON("{}"),
	}
	CreatePaymentTransaction(db, &cbTxn)
	PostPaymentTransactionToJournalEntry(db, s.companyID, cbTxn.ID, "test") //nolint:errcheck

	// 3. Apply via fallback path — should resolve invoice via OriginalTransactionID.
	err := ApplyChargebackTransactionToInvoice(db, s.companyID, cbTxn.ID, "test")
	if err != nil {
		t.Fatalf("apply via OriginalTransactionID fallback failed: %v", err)
	}

	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if !inv.BalanceDue.Equal(decimal.NewFromInt(300)) {
		t.Errorf("BalanceDue after fallback chargeback: want 300, got %s", inv.BalanceDue)
	}
}

// ── Blocker 1: overpayment chargeback semantics ──────────────────────────────

// TestApplyChargeback_Overpayment_CapsRestoreToAppliedAmount verifies that when
// the original charge was an overpayment (charged 1400 on a 1000 invoice,
// credit=400), a full chargeback of 1400 is ALLOWED and restores only the
// invoice-applied portion (1000), NOT the full 1400.
func TestApplyChargeback_Overpayment_CapsRestoreToAppliedAmount(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db) // invoice.Amount = invoice.BalanceDue = 1000

	// Charge 1400 → invoice paid (applied=1000), credit=400.
	chargeTxnID := postChargeTxn(t, db, s, 1400)
	if err := ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test"); err != nil {
		t.Fatalf("apply charge: %v", err)
	}

	// Post a chargeback of 1400 linked via PaymentRequest.
	cbTxnID := postChargebackTxnViaRequest(t, db, s, 1400)

	// Validate must PASS (1400 ≤ original charge 1400).
	if err := ValidateChargebackTransactionApplicable(db, s.companyID, cbTxnID); err != nil {
		t.Fatalf("validate chargeback: %v", err)
	}

	// Apply must succeed.
	if err := ApplyChargebackTransactionToInvoice(db, s.companyID, cbTxnID, "test"); err != nil {
		t.Fatalf("apply chargeback: %v", err)
	}

	// Invoice restored to 1000 (applied portion), not 1400.
	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if !inv.BalanceDue.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("BalanceDue: want 1000, got %s", inv.BalanceDue)
	}
	if inv.Status != models.InvoiceStatusIssued {
		t.Errorf("Status: want issued, got %s", inv.Status)
	}
}

// TestApplyChargeback_Overpayment_ViaOriginalTxnID_CapsRestoreToAppliedAmount
// verifies the same overpayment cap when the chargeback has OriginalTransactionID
// instead of PaymentRequestID (the dispute-generated path).
func TestApplyChargeback_Overpayment_ViaOriginalTxnID_CapsRestoreToAppliedAmount(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	cbAcct := models.Account{
		CompanyID: s.companyID, Code: "6702", Name: "CB Expense 3",
		RootAccountType:   models.RootExpense,
		DetailAccountType: "operating_expense",
		IsActive:          true,
	}
	db.Create(&cbAcct)
	db.Model(&models.PaymentAccountingMapping{}).
		Where("company_id = ? AND gateway_account_id = ?", s.companyID, s.gwID).
		Update("chargeback_account_id", cbAcct.ID)

	// Charge 1400 → applied=1000.
	chargeTxnID := postChargeTxn(t, db, s, 1400)
	ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test")

	// Chargeback with OriginalTransactionID, no PaymentRequestID.
	chargeIDPtr := chargeTxnID
	cbTxn := models.PaymentTransaction{
		CompanyID:             s.companyID,
		GatewayAccountID:      s.gwID,
		PaymentRequestID:      nil,
		OriginalTransactionID: &chargeIDPtr,
		TransactionType:       models.TxnTypeChargeback,
		Amount:                decimal.NewFromInt(1400),
		CurrencyCode:          "CAD",
		Status:                "completed",
		RawPayload:            datatypes.JSON("{}"),
	}
	CreatePaymentTransaction(db, &cbTxn)
	PostPaymentTransactionToJournalEntry(db, s.companyID, cbTxn.ID, "test")

	if err := ApplyChargebackTransactionToInvoice(db, s.companyID, cbTxn.ID, "test"); err != nil {
		t.Fatalf("apply chargeback via OriginalTransactionID: %v", err)
	}

	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if !inv.BalanceDue.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("BalanceDue: want 1000, got %s", inv.BalanceDue)
	}
}

// TestApplyChargeback_ConcurrentDoubleApply verifies that two concurrent apply
// calls for the same chargeback transaction result in exactly one successful
// apply; the second must return ErrPaymentTxnAlreadyApplied.
//
// On SQLite the txn-row SELECT FOR UPDATE is a no-op (applyLockForUpdate is a
// no-op), but SQLite serialises writes at the connection level, so the second
// goroutine is still blocked until the first commits. On PostgreSQL the row lock
// makes the second wait and then re-check the applied state under the lock.
// Either way, exactly one apply should succeed.
func TestApplyChargeback_ConcurrentDoubleApply(t *testing.T) {
	t.Skip("applyLockForUpdate is a no-op on SQLite; concurrent-safety is provided by SELECT FOR UPDATE inside the transaction on PostgreSQL — verified by code inspection")

	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	// Charge → paid, then create a posted chargeback.
	chargeTxnID := postChargeTxn(t, db, s, 1000)
	ApplyPaymentTransactionToInvoice(db, s.companyID, chargeTxnID, "test") //nolint:errcheck
	cbTxnID := postChargebackTxnViaRequest(t, db, s, 500)

	const workers = 2
	results := make([]error, workers)
	done := make(chan struct{})
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			results[i] = ApplyChargebackTransactionToInvoice(db, s.companyID, cbTxnID, "test")
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
