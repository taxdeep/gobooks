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

func testPaymentPostingDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:pgpost_%s?mode=memory&cache=shared", t.Name())
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
	); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

type ppSetup struct {
	companyID    uint
	gwID         uint
	clearingID   uint
	feeID        uint
	refundID     uint
	chargebackID uint
	bankID       uint
	arID         uint
	invoiceID    uint
	requestID    uint
}

func setupPaymentPosting(t *testing.T, db *gorm.DB) ppSetup {
	t.Helper()
	co := models.Company{Name: "PP Co", IsActive: true, BaseCurrencyCode: "CAD"}
	db.Create(&co)
	cust := models.Customer{CompanyID: co.ID, Name: "C", AddrStreet1: "1"}
	db.Create(&cust)

	clearing := models.Account{CompanyID: co.ID, Code: "1500", Name: "GW Clearing", RootAccountType: models.RootAsset, DetailAccountType: "other_current_asset", IsActive: true}
	db.Create(&clearing)
	fee := models.Account{CompanyID: co.ID, Code: "6500", Name: "GW Fees", RootAccountType: models.RootExpense, DetailAccountType: "operating_expense", IsActive: true}
	db.Create(&fee)
	refund := models.Account{CompanyID: co.ID, Code: "6600", Name: "GW Refunds", RootAccountType: models.RootExpense, DetailAccountType: "operating_expense", IsActive: true}
	db.Create(&refund)
	chargeback := models.Account{CompanyID: co.ID, Code: "6700", Name: "GW Chargebacks", RootAccountType: models.RootExpense, DetailAccountType: "operating_expense", IsActive: true}
	db.Create(&chargeback)
	bank := models.Account{CompanyID: co.ID, Code: "1000", Name: "Bank", RootAccountType: models.RootAsset, DetailAccountType: models.DetailBank, IsActive: true}
	db.Create(&bank)
	ar := models.Account{CompanyID: co.ID, Code: "1100", Name: "AR", RootAccountType: models.RootAsset, DetailAccountType: models.DetailAccountsReceivable, IsActive: true}
	db.Create(&ar)

	gw := models.PaymentGatewayAccount{CompanyID: co.ID, ProviderType: models.ProviderStripe, DisplayName: "Stripe", AuthStatus: "ok", IsActive: true}
	db.Create(&gw)

	SavePaymentAccountingMapping(db, &models.PaymentAccountingMapping{
		CompanyID: co.ID, GatewayAccountID: gw.ID,
		ClearingAccountID:   &clearing.ID,
		FeeExpenseAccountID: &fee.ID,
		RefundAccountID:     &refund.ID,
		PayoutBankAccountID: &bank.ID,
		ChargebackAccountID: &chargeback.ID,
	})

	inv := models.Invoice{
		CompanyID: co.ID, InvoiceNumber: "INV-PP", CustomerID: cust.ID,
		InvoiceDate: time.Now(), Status: models.InvoiceStatusIssued,
		Amount: decimal.NewFromInt(500), BalanceDue: decimal.NewFromInt(500),
		CustomerNameSnapshot: "C",
	}
	db.Create(&inv)

	req := models.PaymentRequest{
		CompanyID: co.ID, GatewayAccountID: gw.ID, InvoiceID: &inv.ID,
		Amount: decimal.NewFromInt(500), Status: models.PaymentRequestCreated,
	}
	CreatePaymentRequest(db, &req)

	return ppSetup{
		companyID:    co.ID,
		gwID:         gw.ID,
		clearingID:   clearing.ID,
		feeID:        fee.ID,
		refundID:     refund.ID,
		chargebackID: chargeback.ID,
		bankID:       bank.ID,
		arID:         ar.ID,
		invoiceID:    inv.ID,
		requestID:    req.ID,
	}
}

func createTxn(t *testing.T, db *gorm.DB, s ppSetup, txnType models.PaymentTransactionType, amount int64, requestID *uint) uint {
	t.Helper()
	txn := models.PaymentTransaction{
		CompanyID: s.companyID, GatewayAccountID: s.gwID,
		PaymentRequestID: requestID, TransactionType: txnType,
		Amount: decimal.NewFromInt(amount), CurrencyCode: "CAD",
		Status: "completed", RawPayload: datatypes.JSON("{}"),
	}
	CreatePaymentTransaction(db, &txn)
	return txn.ID
}

// ── Charge/capture posting ───────────────────────────────────────────────────

func TestPostPaymentTxn_Charge_DrClearingCrAR(t *testing.T) {
	db := testPaymentPostingDB(t)
	s := setupPaymentPosting(t, db)
	txnID := createTxn(t, db, s, models.TxnTypeCharge, 500, &s.requestID)

	je, err := PostPaymentTransactionToJournalEntry(db, s.companyID, txnID, "test")
	if err != nil {
		t.Fatalf("Post failed: %v", err)
	}
	if je.SourceType != models.LedgerSourcePaymentGateway {
		t.Errorf("Wrong source type: %s", je.SourceType)
	}

	var lines []models.JournalLine
	db.Where("journal_entry_id = ?", je.ID).Find(&lines)
	if len(lines) != 2 {
		t.Fatalf("Expected 2 lines, got %d", len(lines))
	}

	var clearingDebit, arCredit decimal.Decimal
	for _, l := range lines {
		if l.AccountID == s.clearingID {
			clearingDebit = l.Debit
		}
		if l.AccountID == s.arID {
			arCredit = l.Credit
		}
	}
	if !clearingDebit.Equal(decimal.NewFromInt(500)) {
		t.Errorf("Clearing debit expected 500, got %s", clearingDebit)
	}
	if !arCredit.Equal(decimal.NewFromInt(500)) {
		t.Errorf("AR credit expected 500, got %s", arCredit)
	}
}

// ── Fee posting ──────────────────────────────────────────────────────────────

func TestPostPaymentTxn_Fee_DrFeeExpCrClearing(t *testing.T) {
	db := testPaymentPostingDB(t)
	s := setupPaymentPosting(t, db)
	txnID := createTxn(t, db, s, models.TxnTypeFee, 25, nil)

	je, err := PostPaymentTransactionToJournalEntry(db, s.companyID, txnID, "test")
	if err != nil {
		t.Fatalf("Post failed: %v", err)
	}

	var lines []models.JournalLine
	db.Where("journal_entry_id = ?", je.ID).Find(&lines)

	var feeDebit, clearingCredit decimal.Decimal
	for _, l := range lines {
		if l.AccountID == s.feeID {
			feeDebit = l.Debit
		}
		if l.AccountID == s.clearingID {
			clearingCredit = l.Credit
		}
	}
	if !feeDebit.Equal(decimal.NewFromInt(25)) {
		t.Errorf("Fee debit expected 25, got %s", feeDebit)
	}
	if !clearingCredit.Equal(decimal.NewFromInt(25)) {
		t.Errorf("Clearing credit expected 25, got %s", clearingCredit)
	}
}

// ── Refund posting ───────────────────────────────────────────────────────────

func TestPostPaymentTxn_Refund_DrRefundCrClearing(t *testing.T) {
	db := testPaymentPostingDB(t)
	s := setupPaymentPosting(t, db)
	txnID := createTxn(t, db, s, models.TxnTypeRefund, 100, nil)

	je, _ := PostPaymentTransactionToJournalEntry(db, s.companyID, txnID, "test")

	var lines []models.JournalLine
	db.Where("journal_entry_id = ?", je.ID).Find(&lines)

	for _, l := range lines {
		if l.AccountID == s.refundID && !l.Debit.Equal(decimal.NewFromInt(100)) {
			t.Errorf("Refund debit expected 100, got %s", l.Debit)
		}
	}
}

// ── Payout posting ───────────────────────────────────────────────────────────

func TestPostPaymentTxn_Payout_DrBankCrClearing(t *testing.T) {
	db := testPaymentPostingDB(t)
	s := setupPaymentPosting(t, db)
	txnID := createTxn(t, db, s, models.TxnTypePayout, 450, nil)

	je, _ := PostPaymentTransactionToJournalEntry(db, s.companyID, txnID, "test")

	var lines []models.JournalLine
	db.Where("journal_entry_id = ?", je.ID).Find(&lines)

	var bankDebit decimal.Decimal
	for _, l := range lines {
		if l.AccountID == s.bankID {
			bankDebit = l.Debit
		}
	}
	if !bankDebit.Equal(decimal.NewFromInt(450)) {
		t.Errorf("Bank debit expected 450, got %s", bankDebit)
	}
}

// ── Eligibility / blocking tests ─────────────────────────────────────────────

func TestPostPaymentTxn_Dispute_Blocked(t *testing.T) {
	db := testPaymentPostingDB(t)
	s := setupPaymentPosting(t, db)
	txnID := createTxn(t, db, s, models.TxnTypeDispute, 50, nil)

	err := ValidatePaymentTransactionPostable(db, s.companyID, txnID)
	if err == nil {
		t.Fatal("Expected dispute to be blocked")
	}
}

func TestPostPaymentTxn_ChargeWithoutInvoice_Blocked(t *testing.T) {
	db := testPaymentPostingDB(t)
	s := setupPaymentPosting(t, db)
	txnID := createTxn(t, db, s, models.TxnTypeCharge, 500, nil) // no request link

	err := ValidatePaymentTransactionPostable(db, s.companyID, txnID)
	if err == nil {
		t.Fatal("Expected charge without invoice link to be blocked")
	}
}

func TestPostPaymentTxn_FeeMissingAccount_Blocked(t *testing.T) {
	db := testPaymentPostingDB(t)
	co := models.Company{Name: "NoFee", IsActive: true}
	db.Create(&co)
	gw := models.PaymentGatewayAccount{CompanyID: co.ID, ProviderType: models.ProviderManual, DisplayName: "M", AuthStatus: "ok", IsActive: true}
	db.Create(&gw)
	clearing := models.Account{CompanyID: co.ID, Code: "1500", Name: "C", RootAccountType: models.RootAsset, DetailAccountType: "other_current_asset", IsActive: true}
	db.Create(&clearing)
	// Mapping with no fee account.
	SavePaymentAccountingMapping(db, &models.PaymentAccountingMapping{CompanyID: co.ID, GatewayAccountID: gw.ID, ClearingAccountID: &clearing.ID})

	txn := models.PaymentTransaction{CompanyID: co.ID, GatewayAccountID: gw.ID, TransactionType: models.TxnTypeFee, Amount: decimal.NewFromInt(10), RawPayload: datatypes.JSON("{}")}
	CreatePaymentTransaction(db, &txn)

	err := ValidatePaymentTransactionPostable(db, co.ID, txn.ID)
	if err == nil {
		t.Fatal("Expected fee missing account to be blocked")
	}
}

func TestPostPaymentTxn_DoublePost_Blocked(t *testing.T) {
	db := testPaymentPostingDB(t)
	s := setupPaymentPosting(t, db)
	txnID := createTxn(t, db, s, models.TxnTypeFee, 25, nil)

	PostPaymentTransactionToJournalEntry(db, s.companyID, txnID, "test")

	_, err := PostPaymentTransactionToJournalEntry(db, s.companyID, txnID, "test")
	if err == nil {
		t.Fatal("Expected double post error")
	}
}

func TestPostPaymentTxn_CrossCompany_Blocked(t *testing.T) {
	db := testPaymentPostingDB(t)
	s := setupPaymentPosting(t, db)
	txnID := createTxn(t, db, s, models.TxnTypeFee, 25, nil)

	otherCo := models.Company{Name: "Other", IsActive: true}
	db.Create(&otherCo)

	err := ValidatePaymentTransactionPostable(db, otherCo.ID, txnID)
	if err == nil {
		t.Fatal("Expected cross-company block")
	}
}

// ── Non-auto-application test ────────────────────────────────────────────────

func TestPostPaymentTxn_Charge_DoesNotChangeInvoiceStatus(t *testing.T) {
	db := testPaymentPostingDB(t)
	s := setupPaymentPosting(t, db)
	txnID := createTxn(t, db, s, models.TxnTypeCharge, 500, &s.requestID)

	PostPaymentTransactionToJournalEntry(db, s.companyID, txnID, "test")

	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status == models.InvoiceStatusPaid {
		t.Error("Posting charge should NOT auto-mark invoice as paid")
	}
	if !inv.BalanceDue.Equal(decimal.NewFromInt(500)) {
		t.Errorf("BalanceDue should remain 500, got %s", inv.BalanceDue)
	}
}

// ── State persistence ────────────────────────────────────────────────────────

func TestPostPaymentTxn_SavesPostedState(t *testing.T) {
	db := testPaymentPostingDB(t)
	s := setupPaymentPosting(t, db)
	txnID := createTxn(t, db, s, models.TxnTypeFee, 10, nil)

	je, _ := PostPaymentTransactionToJournalEntry(db, s.companyID, txnID, "test")

	var txn models.PaymentTransaction
	db.First(&txn, txnID)
	if txn.PostedJournalEntryID == nil || *txn.PostedJournalEntryID != je.ID {
		t.Error("PostedJournalEntryID not saved")
	}
	if txn.PostedAt == nil {
		t.Error("PostedAt not saved")
	}
}

// ── Chargeback posting ────────────────────────────────────────────────────────

// TestPostPaymentTxn_Chargeback_DrChargebackCrClearing verifies the JE direction
// for a chargeback transaction: Dr ChargebackAccount (loss), Cr GW Clearing.
//
// This is the accounting entry that records the card-network-forced reversal:
// the clearing account balance decreases and the chargeback loss account increases.
func TestPostPaymentTxn_Chargeback_DrChargebackCrClearing(t *testing.T) {
	db := testPaymentPostingDB(t)
	s := setupPaymentPosting(t, db)
	txnID := createTxn(t, db, s, models.TxnTypeChargeback, 150, nil)

	je, err := PostPaymentTransactionToJournalEntry(db, s.companyID, txnID, "test")
	if err != nil {
		t.Fatalf("chargeback post failed: %v", err)
	}
	if je == nil {
		t.Fatal("expected non-nil JournalEntry")
	}

	var lines []models.JournalLine
	db.Where("journal_entry_id = ?", je.ID).Find(&lines)
	if len(lines) != 2 {
		t.Fatalf("expected 2 JE lines, got %d", len(lines))
	}

	var chargebackDebit, clearingCredit decimal.Decimal
	for _, l := range lines {
		if l.AccountID == s.chargebackID {
			chargebackDebit = l.Debit
		}
		if l.AccountID == s.clearingID {
			clearingCredit = l.Credit
		}
	}

	// Dr ChargebackAccount 150
	if !chargebackDebit.Equal(decimal.NewFromInt(150)) {
		t.Errorf("chargeback debit: want 150, got %s", chargebackDebit)
	}
	// Cr GW Clearing 150
	if !clearingCredit.Equal(decimal.NewFromInt(150)) {
		t.Errorf("clearing credit: want 150, got %s", clearingCredit)
	}

	// Ensure no debit on clearing and no credit on chargeback account.
	for _, l := range lines {
		if l.AccountID == s.clearingID && l.Debit.IsPositive() {
			t.Errorf("clearing should not be debited in a chargeback JE, got debit=%s", l.Debit)
		}
		if l.AccountID == s.chargebackID && l.Credit.IsPositive() {
			t.Errorf("chargeback account should not be credited, got credit=%s", l.Credit)
		}
	}

	// Verify PostedJournalEntryID written back to the transaction.
	var txnRow models.PaymentTransaction
	db.First(&txnRow, txnID)
	if txnRow.PostedJournalEntryID == nil || *txnRow.PostedJournalEntryID != je.ID {
		t.Error("PostedJournalEntryID not persisted on chargeback transaction")
	}
	if txnRow.PostedAt == nil {
		t.Error("PostedAt not persisted on chargeback transaction")
	}
}

// TestPostPaymentTxn_Chargeback_MissingMapping_Blocked verifies that posting a
// chargeback transaction fails when ChargebackAccountID is not configured.
func TestPostPaymentTxn_Chargeback_MissingMapping_Blocked(t *testing.T) {
	db := testPaymentPostingDB(t)
	s := setupPaymentPosting(t, db)

	// Remove ChargebackAccountID from the mapping.
	SavePaymentAccountingMapping(db, &models.PaymentAccountingMapping{
		CompanyID:           s.companyID,
		GatewayAccountID:    s.gwID,
		ClearingAccountID:   &s.clearingID,
		FeeExpenseAccountID: &s.feeID,
		RefundAccountID:     &s.refundID,
		PayoutBankAccountID: &s.bankID,
		// ChargebackAccountID intentionally nil
	})

	txnID := createTxn(t, db, s, models.TxnTypeChargeback, 50, nil)
	err := ValidatePaymentTransactionPostable(db, s.companyID, txnID)
	if err == nil {
		t.Fatal("expected error when ChargebackAccountID is nil")
	}
}
