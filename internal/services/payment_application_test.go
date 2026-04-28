// 遵循project_guide.md
package services

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"balanciz/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testPaymentApplicationDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:payapp_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Account{},
		&models.Customer{},
		&models.Invoice{},
		&models.PaymentGatewayAccount{},
		&models.PaymentAccountingMapping{},
		&models.PaymentRequest{},
		&models.PaymentTransaction{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.AuditLog{},
		&models.ChannelOrder{},
		&models.CustomerCredit{},
		&models.CustomerCreditApplication{},
	); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

type paSetup struct {
	companyID  uint
	customerID uint
	gwID       uint
	clearingID uint
	arID       uint
	invoiceID  uint
	requestID  uint
}

func setupPayApp(t *testing.T, db *gorm.DB) paSetup {
	t.Helper()
	co := models.Company{Name: "PA Co", IsActive: true, BaseCurrencyCode: "CAD"}
	db.Create(&co)
	cust := models.Customer{CompanyID: co.ID, Name: "C", AddrStreet1: "1"}
	db.Create(&cust)
	clearing := models.Account{CompanyID: co.ID, Code: "1500", Name: "GW Clear", RootAccountType: models.RootAsset, DetailAccountType: "other_current_asset", IsActive: true}
	db.Create(&clearing)
	ar := models.Account{CompanyID: co.ID, Code: "1100", Name: "AR", RootAccountType: models.RootAsset, DetailAccountType: models.DetailAccountsReceivable, IsActive: true}
	db.Create(&ar)
	fee := models.Account{CompanyID: co.ID, Code: "6500", Name: "Fee", RootAccountType: models.RootExpense, DetailAccountType: "operating_expense", IsActive: true}
	db.Create(&fee)
	bank := models.Account{CompanyID: co.ID, Code: "1000", Name: "Bank", RootAccountType: models.RootAsset, DetailAccountType: models.DetailBank, IsActive: true}
	db.Create(&bank)

	refundAcct := models.Account{CompanyID: co.ID, Code: "6600", Name: "Refunds", RootAccountType: models.RootExpense, DetailAccountType: "operating_expense", IsActive: true}
	db.Create(&refundAcct)

	gw := models.PaymentGatewayAccount{CompanyID: co.ID, ProviderType: models.ProviderStripe, DisplayName: "S", AuthStatus: "ok", IsActive: true}
	db.Create(&gw)
	SavePaymentAccountingMapping(db, &models.PaymentAccountingMapping{
		CompanyID: co.ID, GatewayAccountID: gw.ID,
		ClearingAccountID: &clearing.ID, FeeExpenseAccountID: &fee.ID,
		RefundAccountID: &refundAcct.ID, PayoutBankAccountID: &bank.ID,
	})

	inv := models.Invoice{
		CompanyID: co.ID, InvoiceNumber: "INV-PA", CustomerID: cust.ID,
		InvoiceDate: time.Now(), Status: models.InvoiceStatusIssued,
		Amount: decimal.NewFromInt(1000), BalanceDue: decimal.NewFromInt(1000),
		CustomerNameSnapshot: "C",
	}
	db.Create(&inv)

	req := models.PaymentRequest{
		CompanyID: co.ID, GatewayAccountID: gw.ID, InvoiceID: &inv.ID,
		Amount: decimal.NewFromInt(1000), Status: models.PaymentRequestCreated,
	}
	CreatePaymentRequest(db, &req)

	return paSetup{companyID: co.ID, customerID: cust.ID, gwID: gw.ID, clearingID: clearing.ID, arID: ar.ID, invoiceID: inv.ID, requestID: req.ID}
}

func postChargeTxn(t *testing.T, db *gorm.DB, s paSetup, amount int64) uint {
	t.Helper()
	txn := models.PaymentTransaction{
		CompanyID: s.companyID, GatewayAccountID: s.gwID, PaymentRequestID: &s.requestID,
		TransactionType: models.TxnTypeCharge, Amount: decimal.NewFromInt(amount),
		CurrencyCode: "CAD", Status: "completed", RawPayload: datatypes.JSON("{}"),
	}
	CreatePaymentTransaction(db, &txn)
	PostPaymentTransactionToJournalEntry(db, s.companyID, txn.ID, "test")
	return txn.ID
}

// ── Application tests ────────────────────────────────────────────────────────

func TestApplyPayment_FullPayment_InvoicePaid(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	txnID := postChargeTxn(t, db, s, 1000)

	err := ApplyPaymentTransactionToInvoice(db, s.companyID, txnID, "test")
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPaid {
		t.Errorf("Expected paid, got %s", inv.Status)
	}
	if !inv.BalanceDue.IsZero() {
		t.Errorf("Expected BalanceDue 0, got %s", inv.BalanceDue)
	}

	var txn models.PaymentTransaction
	db.First(&txn, txnID)
	if txn.AppliedInvoiceID == nil || *txn.AppliedInvoiceID != s.invoiceID {
		t.Error("AppliedInvoiceID not saved")
	}
}

func TestApplyPayment_PartialPayment_InvoicePartiallyPaid(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	txnID := postChargeTxn(t, db, s, 600)

	ApplyPaymentTransactionToInvoice(db, s.companyID, txnID, "test")

	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPartiallyPaid {
		t.Errorf("Expected partially_paid, got %s", inv.Status)
	}
	if !inv.BalanceDue.Equal(decimal.NewFromInt(400)) {
		t.Errorf("Expected BalanceDue 400, got %s", inv.BalanceDue)
	}
}

func TestApplyPayment_DoubleApply_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	txnID := postChargeTxn(t, db, s, 1000)

	ApplyPaymentTransactionToInvoice(db, s.companyID, txnID, "test")

	err := ApplyPaymentTransactionToInvoice(db, s.companyID, txnID, "test")
	if err == nil {
		t.Fatal("Expected double-apply error")
	}
}

// TestApplyPayment_Overpayment_CreatesCredit verifies that when a payment
// exceeds the invoice BalanceDue, the invoice is capped at zero and the
// excess creates a CustomerCredit record (Batch 16 behaviour).
func TestApplyPayment_Overpayment_CreatesCredit(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	// Invoice BalanceDue = 500; charge = 1000 → excess = 500.
	db.Model(&models.Invoice{}).Where("id = ?", s.invoiceID).Updates(map[string]any{
		"balance_due":      "500",
		"balance_due_base": "500",
	})
	txnID := postChargeTxn(t, db, s, 1000)

	// Overpayment should now be ALLOWED by the validator.
	if err := ValidatePaymentTransactionApplicable(db, s.companyID, txnID); err != nil {
		t.Fatalf("overpayment should be allowed by validator: %v", err)
	}

	// Apply should succeed and create credit.
	if err := ApplyPaymentTransactionToInvoice(db, s.companyID, txnID, "test"); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	// Invoice should be paid at BalanceDue = 0.
	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPaid {
		t.Errorf("invoice status: want paid, got %s", inv.Status)
	}
	if !inv.BalanceDue.IsZero() {
		t.Errorf("invoice BalanceDue: want 0, got %s", inv.BalanceDue)
	}

	// CustomerCredit for 500 should exist.
	var credits []models.CustomerCredit
	db.Where("company_id = ? AND customer_id = ?", s.companyID, s.customerID).Find(&credits)
	if len(credits) != 1 {
		t.Fatalf("expected 1 credit, got %d", len(credits))
	}
	c := credits[0]
	if !c.OriginalAmount.Equal(decimal.NewFromInt(500)) {
		t.Errorf("credit original_amount: want 500, got %s", c.OriginalAmount)
	}
	if !c.RemainingAmount.Equal(decimal.NewFromInt(500)) {
		t.Errorf("credit remaining_amount: want 500, got %s", c.RemainingAmount)
	}
	if c.Status != models.CustomerCreditActive {
		t.Errorf("credit status: want active, got %s", c.Status)
	}
	if c.SourceType != models.CreditSourceOverpayment {
		t.Errorf("credit source_type: want overpayment, got %s", c.SourceType)
	}
	if c.SourcePaymentTxnID == nil || *c.SourcePaymentTxnID != txnID {
		t.Errorf("credit source_payment_txn_id: want %d, got %v", txnID, c.SourcePaymentTxnID)
	}
	if c.SourceApplicationInvID == nil || *c.SourceApplicationInvID != s.invoiceID {
		t.Errorf("credit source_application_inv_id: want %d, got %v", s.invoiceID, c.SourceApplicationInvID)
	}
}

func TestApplyPayment_UnpostedTransaction_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	// Create but don't post.
	txn := models.PaymentTransaction{
		CompanyID: s.companyID, GatewayAccountID: s.gwID, PaymentRequestID: &s.requestID,
		TransactionType: models.TxnTypeCharge, Amount: decimal.NewFromInt(500),
		RawPayload: datatypes.JSON("{}"),
	}
	CreatePaymentTransaction(db, &txn)

	err := ValidatePaymentTransactionApplicable(db, s.companyID, txn.ID)
	if err == nil {
		t.Fatal("Expected unposted error")
	}
}

func TestApplyPayment_FeeTransaction_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	txn := models.PaymentTransaction{
		CompanyID: s.companyID, GatewayAccountID: s.gwID,
		TransactionType: models.TxnTypeFee, Amount: decimal.NewFromInt(25),
		RawPayload: datatypes.JSON("{}"),
	}
	CreatePaymentTransaction(db, &txn)
	PostPaymentTransactionToJournalEntry(db, s.companyID, txn.ID, "test")

	err := ValidatePaymentTransactionApplicable(db, s.companyID, txn.ID)
	if err == nil {
		t.Fatal("Expected fee blocked for application")
	}
}

func TestApplyPayment_ChannelOriginInvoice_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	// Make invoice channel-origin.
	order := models.ChannelOrder{CompanyID: s.companyID, RawPayload: datatypes.JSON("{}")}
	db.Create(&order)
	db.Model(&models.Invoice{}).Where("id = ?", s.invoiceID).Update("channel_order_id", order.ID)

	txnID := postChargeTxn(t, db, s, 1000)

	err := ValidatePaymentTransactionApplicable(db, s.companyID, txnID)
	if err == nil {
		t.Fatal("Expected channel-origin block")
	}
}

func TestApplyPayment_PaidInvoice_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)

	db.Model(&models.Invoice{}).Where("id = ?", s.invoiceID).Updates(map[string]any{"status": "paid", "balance_due": "0"})
	txnID := postChargeTxn(t, db, s, 1000)

	err := ValidatePaymentTransactionApplicable(db, s.companyID, txnID)
	if err == nil {
		t.Fatal("Expected paid invoice block")
	}
}

func TestApplyPayment_CrossCompany_Blocked(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	txnID := postChargeTxn(t, db, s, 1000)

	otherCo := models.Company{Name: "Other", IsActive: true}
	db.Create(&otherCo)

	err := ValidatePaymentTransactionApplicable(db, otherCo.ID, txnID)
	if err == nil {
		t.Fatal("Expected cross-company block")
	}
}

func TestApplyPayment_NoNewJE(t *testing.T) {
	db := testPaymentApplicationDB(t)
	s := setupPayApp(t, db)
	txnID := postChargeTxn(t, db, s, 500)

	var jeBefore int64
	db.Model(&models.JournalEntry{}).Where("company_id = ?", s.companyID).Count(&jeBefore)

	ApplyPaymentTransactionToInvoice(db, s.companyID, txnID, "test")

	var jeAfter int64
	db.Model(&models.JournalEntry{}).Where("company_id = ?", s.companyID).Count(&jeAfter)

	if jeAfter != jeBefore {
		t.Error("Application should NOT create new JE")
	}
}
