// 遵循project_guide.md
package services

// gateway_settlement_service_test.go — Batch 11 settlement bridge tests.
//
// Coverage:
//  TestGatewaySettlement_HappyPath_FullSettlement
//      eligible verified payment → settlement created exactly once
//      invoice balance/status updated correctly
//      journal entry created (Dr Clearing / Cr AR)
//      PaymentTransaction posted+applied
//      GatewaySettlement linked to attempt, txn, JE, invoice
//
//  TestGatewaySettlement_Idempotent_SecondCallReturnsAlreadyDone
//      second call on same attempt → ErrSettlementAlreadyDone, no duplicate mutation
//
//  TestGatewaySettlement_AmountMismatch_NoSettlement
//      attempt.Amount != invoice.BalanceDue → ineligible, no mutation
//
//  TestGatewaySettlement_InvoiceAlreadyPaid_NoSettlement
//      paid invoice → ineligible, no mutation
//
//  TestGatewaySettlement_MissingClearingAccount_NoSettlement
//      no PaymentAccountingMapping → ineligible, no mutation
//
//  TestGatewaySettlement_MissingARAccount_NoSettlement
//      no AR account in COA → ineligible, no mutation
//
//  TestGatewaySettlement_CompanyIsolation
//      wrong companyID → ErrSettlementAttemptNotFound
//
//  TestGatewaySettlement_AttemptNotSucceeded_NoSettlement
//      attempt in redirected status → ineligible
//
//  TestGatewaySettlement_CurrencyMismatch_NoSettlement
//      attempt currency != invoice currency → ineligible
//
//  TestGatewaySettlement_NoPaymentTransaction_NoSettlement
//      PaymentTransaction not found → ineligible
//
//  TestGatewaySettlement_TransactionAlreadyApplied_NoSettlement
//      PaymentTransaction.AppliedInvoiceID already set → ineligible
//
//  TestGatewaySettlement_NoDoubleJournal
//      second trigger after success → only one JE exists
//
//  TestTryAutoSettleAfterIngestion_HappyPath
//      end-to-end: ingestion + auto-settle fires correctly
//
//  TestGetSettlementForInvoice_ReturnsNilWhenNone
//      query helper returns nil when no settlement exists
//
//  TestEvaluateGatewaySettlementEligibility_AllRules
//      unit tests for the pure eligibility evaluator function

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"balanciz/internal/models"
)

// ── Test DB ───────────────────────────────────────────────────────────────────

func settlementTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:settle_%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Account{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.InvoiceHostedLink{},
		&models.PaymentGatewayAccount{},
		&models.PaymentAccountingMapping{},
		&models.PaymentRequest{},
		&models.PaymentTransaction{},
		&models.HostedPaymentAttempt{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.GatewaySettlement{},
		&models.WebhookEvent{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

// settleSeed is everything needed to run a settlement test.
type settleSeed struct {
	companyID   uint
	invoiceID   uint
	gatewayID   uint
	attemptID   uint
	txnID       uint
	clearingID  uint // GW clearing account
	arAccountID uint // AR account
}

// seedSettlementBase creates the minimal set of records for a happy-path test:
// company, customer, invoice (issued, balance=100), gateway, accounting mapping
// (clearing + AR), payment request, payment transaction, hosted attempt (payment_succeeded).
func seedSettlementBase(t *testing.T, db *gorm.DB) settleSeed {
	t.Helper()

	co := models.Company{Name: fmt.Sprintf("SettleCo%d", time.Now().UnixNano()), BaseCurrencyCode: "CAD", IsActive: true}
	db.Create(&co)

	cust := models.Customer{CompanyID: co.ID, Name: "SettleCust"}
	db.Create(&cust)

	// Chart of accounts: GW Clearing + AR
	clearing := models.Account{
		CompanyID:         co.ID,
		Name:              "GW Clearing",
		Code:              "2100",
		DetailAccountType: models.DetailOtherCurrentLiability,
		IsActive:          true,
	}
	db.Create(&clearing)

	arAcct := models.Account{
		CompanyID:         co.ID,
		Name:              "Accounts Receivable",
		Code:              "1100",
		DetailAccountType: models.DetailAccountsReceivable,
		IsActive:          true,
	}
	db.Create(&arAcct)

	// Invoice: issued, balance = 100 CAD
	inv := models.Invoice{
		CompanyID:     co.ID,
		CustomerID:    cust.ID,
		InvoiceNumber: fmt.Sprintf("INV-S-%d", time.Now().UnixNano()),
		InvoiceDate:   time.Now(),
		Status:        models.InvoiceStatusIssued,
		Amount:        decimal.NewFromInt(100),
		Subtotal:      decimal.NewFromInt(100),
		TaxTotal:      decimal.Zero,
		BalanceDue:    decimal.NewFromInt(100),
		BalanceDueBase: decimal.NewFromInt(100),
		CurrencyCode:  "CAD",
		CustomerNameSnapshot: "SettleCust",
	}
	db.Create(&inv)

	// Gateway
	gw := models.PaymentGatewayAccount{
		CompanyID:          co.ID,
		ProviderType:       models.ProviderStripe,
		DisplayName:        "Stripe Test",
		ExternalAccountRef: "sk_test_settle",
		WebhookSecret:      "whsec_settle",
		IsActive:           true,
	}
	db.Create(&gw)

	// Accounting mapping: clearing account set
	mapping := models.PaymentAccountingMapping{
		CompanyID:        co.ID,
		GatewayAccountID: gw.ID,
		ClearingAccountID: &clearing.ID,
	}
	db.Create(&mapping)

	// Hosted link
	link := models.InvoiceHostedLink{
		CompanyID: co.ID,
		InvoiceID: inv.ID,
		TokenHash: fmt.Sprintf("thash%d", time.Now().UnixNano()),
		Status:    models.InvoiceHostedLinkStatusActive,
	}
	db.Create(&link)

	// Attempt (payment_succeeded)
	const sessionID = "cs_settle_test_001"
	attempt := models.HostedPaymentAttempt{
		CompanyID:        co.ID,
		InvoiceID:        inv.ID,
		HostedLinkID:     link.ID,
		GatewayAccountID: gw.ID,
		ProviderType:     models.ProviderStripe,
		Amount:           decimal.NewFromInt(100),
		CurrencyCode:     "CAD",
		Status:           models.HostedPaymentAttemptPaymentSucceeded,
		ProviderRef:      sessionID,
	}
	db.Create(&attempt)

	// PaymentRequest (created by webhook ingestion)
	pr := models.PaymentRequest{
		CompanyID:        co.ID,
		GatewayAccountID: gw.ID,
		InvoiceID:        &inv.ID,
		Amount:           decimal.NewFromInt(100),
		CurrencyCode:     "CAD",
		Status:           models.PaymentRequestPaid,
		Description:      "Stripe checkout payment",
		ExternalRef:      sessionID,
	}
	db.Create(&pr)

	// PaymentTransaction (charge, completed — created by webhook ingestion)
	txn := models.PaymentTransaction{
		CompanyID:        co.ID,
		GatewayAccountID: gw.ID,
		PaymentRequestID: &pr.ID,
		TransactionType:  models.TxnTypeCharge,
		Amount:           decimal.NewFromInt(100),
		CurrencyCode:     "CAD",
		Status:           "completed",
		ExternalTxnRef:   "pi_settle_test_001",
		RawPayload:       datatypes.JSON(`{"source":"test"}`),
	}
	if err := db.Create(&txn).Error; err != nil {
		t.Fatalf("create PaymentTransaction: %v", err)
	}

	return settleSeed{
		companyID:   co.ID,
		invoiceID:   inv.ID,
		gatewayID:   gw.ID,
		attemptID:   attempt.ID,
		txnID:       txn.ID,
		clearingID:  clearing.ID,
		arAccountID: arAcct.ID,
	}
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestGatewaySettlement_HappyPath_FullSettlement(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	result, err := ExecuteGatewaySettlement(db, s.companyID, s.attemptID)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if !result.Eligibility.Eligible {
		t.Fatalf("expected eligible, got reason: %q", result.Eligibility.Reason)
	}
	if result.Settlement == nil {
		t.Fatal("expected non-nil Settlement")
	}

	// Verify GatewaySettlement fields.
	gs := result.Settlement
	if gs.CompanyID != s.companyID {
		t.Errorf("settlement.CompanyID: want %d, got %d", s.companyID, gs.CompanyID)
	}
	if gs.HostedAttemptID != s.attemptID {
		t.Errorf("settlement.HostedAttemptID: want %d, got %d", s.attemptID, gs.HostedAttemptID)
	}
	if gs.InvoiceID != s.invoiceID {
		t.Errorf("settlement.InvoiceID: want %d, got %d", s.invoiceID, gs.InvoiceID)
	}
	if gs.PaymentTransactionID != s.txnID {
		t.Errorf("settlement.PaymentTransactionID: want %d, got %d", s.txnID, gs.PaymentTransactionID)
	}
	if gs.JournalEntryID == 0 {
		t.Error("settlement.JournalEntryID must be non-zero")
	}
	if !gs.Amount.Equal(decimal.NewFromInt(100)) {
		t.Errorf("settlement.Amount: want 100, got %s", gs.Amount)
	}
	if gs.CurrencyCode != "CAD" {
		t.Errorf("settlement.CurrencyCode: want CAD, got %q", gs.CurrencyCode)
	}

	// Invoice must be paid with zero balance.
	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPaid {
		t.Errorf("invoice status: want paid, got %q", inv.Status)
	}
	if !inv.BalanceDue.IsZero() {
		t.Errorf("invoice BalanceDue: want 0, got %s", inv.BalanceDue)
	}
	if !inv.BalanceDueBase.IsZero() {
		t.Errorf("invoice BalanceDueBase: want 0, got %s", inv.BalanceDueBase)
	}

	// PaymentTransaction must be posted AND applied.
	var txn models.PaymentTransaction
	db.First(&txn, s.txnID)
	if txn.PostedJournalEntryID == nil {
		t.Error("txn.PostedJournalEntryID must be set after settlement")
	}
	if txn.AppliedInvoiceID == nil {
		t.Error("txn.AppliedInvoiceID must be set after settlement")
	}
	if *txn.AppliedInvoiceID != s.invoiceID {
		t.Errorf("txn.AppliedInvoiceID: want %d, got %d", s.invoiceID, *txn.AppliedInvoiceID)
	}

	// Journal entry must exist: Dr Clearing / Cr AR.
	var je models.JournalEntry
	db.First(&je, gs.JournalEntryID)
	if je.Status != models.JournalEntryStatusPosted {
		t.Errorf("JE status: want posted, got %q", je.Status)
	}
	if je.SourceType != models.LedgerSourcePaymentGateway {
		t.Errorf("JE source type: want payment_gateway, got %q", je.SourceType)
	}

	// Two journal lines.
	var lines []models.JournalLine
	db.Where("journal_entry_id = ?", je.ID).Find(&lines)
	if len(lines) != 2 {
		t.Fatalf("expected 2 JournalLines, got %d", len(lines))
	}
	var debit, credit models.JournalLine
	for _, l := range lines {
		if l.Debit.IsPositive() {
			debit = l
		} else {
			credit = l
		}
	}
	if debit.AccountID != s.clearingID {
		t.Errorf("debit line AccountID: want clearing %d, got %d", s.clearingID, debit.AccountID)
	}
	if credit.AccountID != s.arAccountID {
		t.Errorf("credit line AccountID: want AR %d, got %d", s.arAccountID, credit.AccountID)
	}
	if !debit.Debit.Equal(decimal.NewFromInt(100)) {
		t.Errorf("debit amount: want 100, got %s", debit.Debit)
	}
	if !credit.Credit.Equal(decimal.NewFromInt(100)) {
		t.Errorf("credit amount: want 100, got %s", credit.Credit)
	}

	// Ledger entries must be projected.
	var ledgerCount int64
	db.Model(&models.LedgerEntry{}).Where("journal_entry_id = ?", je.ID).Count(&ledgerCount)
	if ledgerCount != 2 {
		t.Errorf("expected 2 LedgerEntries, got %d", ledgerCount)
	}
}

func TestGatewaySettlement_Idempotent_SecondCallReturnsAlreadyDone(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	// First call — should succeed.
	r1, err1 := ExecuteGatewaySettlement(db, s.companyID, s.attemptID)
	if err1 != nil {
		t.Fatalf("first call: expected nil error, got: %v", err1)
	}
	firstSettlementID := r1.Settlement.ID

	// Second call — must return ErrSettlementAlreadyDone.
	r2, err2 := ExecuteGatewaySettlement(db, s.companyID, s.attemptID)
	if !errors.Is(err2, ErrSettlementAlreadyDone) {
		t.Errorf("second call: expected ErrSettlementAlreadyDone, got %v", err2)
	}
	if r2.Settlement == nil {
		t.Fatal("second call: Settlement must be non-nil on idempotent re-execution")
	}
	if r2.Settlement.ID != firstSettlementID {
		t.Errorf("second call: settlement ID should match first; got %d, want %d",
			r2.Settlement.ID, firstSettlementID)
	}

	// Only one GatewaySettlement row.
	var count int64
	db.Model(&models.GatewaySettlement{}).Where("hosted_attempt_id = ?", s.attemptID).Count(&count)
	if count != 1 {
		t.Errorf("expected 1 GatewaySettlement, got %d", count)
	}

	// Only one JournalEntry for this txn.
	var txn models.PaymentTransaction
	db.First(&txn, s.txnID)
	var jeCount int64
	db.Model(&models.JournalEntry{}).Where("source_id = ? AND source_type = ?", txn.ID, models.LedgerSourcePaymentGateway).Count(&jeCount)
	if jeCount != 1 {
		t.Errorf("expected 1 JournalEntry, got %d (no double-posting)", jeCount)
	}
}

func TestGatewaySettlement_AmountMismatch_NoSettlement(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	// Set invoice BalanceDue to 80 (attempt is 100 — mismatch).
	db.Model(&models.Invoice{}).Where("id = ?", s.invoiceID).
		Update("balance_due", decimal.NewFromInt(80))

	result, err := ExecuteGatewaySettlement(db, s.companyID, s.attemptID)
	if err != nil {
		t.Fatalf("expected nil error on ineligible, got: %v", err)
	}
	if result.Eligibility.Eligible {
		t.Error("expected ineligible for amount mismatch")
	}
	if result.Settlement != nil {
		t.Error("expected nil Settlement on ineligible")
	}

	// Invoice must be unchanged.
	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusIssued {
		t.Errorf("invoice status must not change on ineligible; got %q", inv.Status)
	}
	if !inv.BalanceDue.Equal(decimal.NewFromInt(80)) {
		t.Errorf("invoice BalanceDue must not change; got %s", inv.BalanceDue)
	}

	// No GatewaySettlement created.
	var count int64
	db.Model(&models.GatewaySettlement{}).Where("hosted_attempt_id = ?", s.attemptID).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 GatewaySettlement on ineligible, got %d", count)
	}
}

func TestGatewaySettlement_InvoiceAlreadyPaid_NoSettlement(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	// Mark invoice as already paid.
	db.Model(&models.Invoice{}).Where("id = ?", s.invoiceID).Updates(map[string]any{
		"status":     string(models.InvoiceStatusPaid),
		"balance_due": decimal.Zero,
	})

	result, err := ExecuteGatewaySettlement(db, s.companyID, s.attemptID)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if result.Eligibility.Eligible {
		t.Error("expected ineligible for already-paid invoice")
	}
	// No settlement created.
	var count int64
	db.Model(&models.GatewaySettlement{}).Where("hosted_attempt_id = ?", s.attemptID).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 GatewaySettlement, got %d", count)
	}
}

func TestGatewaySettlement_MissingClearingAccount_NoSettlement(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	// Remove clearing account from mapping.
	db.Model(&models.PaymentAccountingMapping{}).
		Where("gateway_account_id = ?", s.gatewayID).
		Update("clearing_account_id", nil)

	result, err := ExecuteGatewaySettlement(db, s.companyID, s.attemptID)
	if err != nil {
		t.Fatalf("expected nil error on missing config, got: %v", err)
	}
	if result.Eligibility.Eligible {
		t.Error("expected ineligible when clearing account not configured")
	}

	// Invoice must be unchanged.
	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusIssued {
		t.Errorf("invoice status must not change; got %q", inv.Status)
	}
}

func TestGatewaySettlement_MissingARAccount_NoSettlement(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	// Deactivate the AR account.
	db.Model(&models.Account{}).Where("id = ?", s.arAccountID).Update("is_active", false)

	result, err := ExecuteGatewaySettlement(db, s.companyID, s.attemptID)
	if err != nil {
		t.Fatalf("expected nil error on missing AR, got: %v", err)
	}
	if result.Eligibility.Eligible {
		t.Error("expected ineligible when no active AR account")
	}

	// Invoice unchanged.
	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusIssued {
		t.Errorf("invoice status must not change; got %q", inv.Status)
	}
}

func TestGatewaySettlement_CompanyIsolation(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	// Wrong company ID.
	_, err := ExecuteGatewaySettlement(db, s.companyID+999, s.attemptID)
	if !errors.Is(err, ErrSettlementAttemptNotFound) {
		t.Errorf("expected ErrSettlementAttemptNotFound, got %v", err)
	}

	// Invoice must be unchanged.
	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusIssued {
		t.Errorf("invoice status must not change on isolation failure; got %q", inv.Status)
	}
}

func TestGatewaySettlement_AttemptNotSucceeded_NoSettlement(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	// Set attempt back to redirected.
	db.Model(&models.HostedPaymentAttempt{}).Where("id = ?", s.attemptID).
		Update("status", models.HostedPaymentAttemptRedirected)

	result, err := ExecuteGatewaySettlement(db, s.companyID, s.attemptID)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if result.Eligibility.Eligible {
		t.Error("expected ineligible for non-succeeded attempt")
	}
}

func TestGatewaySettlement_CurrencyMismatch_NoSettlement(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	// Change invoice currency to USD (attempt is CAD).
	db.Model(&models.Invoice{}).Where("id = ?", s.invoiceID).Update("currency_code", "USD")

	result, err := ExecuteGatewaySettlement(db, s.companyID, s.attemptID)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if result.Eligibility.Eligible {
		t.Errorf("expected ineligible for currency mismatch; got eligible with reason=%q", result.Eligibility.Reason)
	}
}

func TestGatewaySettlement_NoPaymentTransaction_NoSettlement(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	// Delete the payment transaction (simulates missing ingestion).
	db.Delete(&models.PaymentTransaction{}, s.txnID)
	// Also delete the payment request to break the lookup.
	db.Model(&models.PaymentTransaction{}).Where("company_id = ?", s.companyID).Delete(&models.PaymentTransaction{})
	db.Model(&models.PaymentRequest{}).Where("company_id = ?", s.companyID).Delete(&models.PaymentRequest{})

	result, err := ExecuteGatewaySettlement(db, s.companyID, s.attemptID)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if result.Eligibility.Eligible {
		t.Error("expected ineligible when no PaymentTransaction exists")
	}
}

func TestGatewaySettlement_TransactionAlreadyApplied_NoSettlement(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	// Pre-set applied_invoice_id on the transaction (simulates manual application).
	db.Model(&models.PaymentTransaction{}).Where("id = ?", s.txnID).
		Update("applied_invoice_id", s.invoiceID)

	result, err := ExecuteGatewaySettlement(db, s.companyID, s.attemptID)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if result.Eligibility.Eligible {
		t.Error("expected ineligible when transaction already applied")
	}
}

func TestGatewaySettlement_NoDoubleJournal(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	// Run settlement twice.
	ExecuteGatewaySettlement(db, s.companyID, s.attemptID) //nolint:errcheck
	ExecuteGatewaySettlement(db, s.companyID, s.attemptID) //nolint:errcheck

	// Only one JE for this gateway transaction.
	var txn models.PaymentTransaction
	db.First(&txn, s.txnID)
	var jeCount int64
	db.Model(&models.JournalEntry{}).
		Where("source_type = ? AND source_id = ?", models.LedgerSourcePaymentGateway, txn.ID).
		Count(&jeCount)
	if jeCount != 1 {
		t.Errorf("expected exactly 1 JournalEntry after two calls, got %d", jeCount)
	}

	// Only one GatewaySettlement.
	var gsCount int64
	db.Model(&models.GatewaySettlement{}).Where("hosted_attempt_id = ?", s.attemptID).Count(&gsCount)
	if gsCount != 1 {
		t.Errorf("expected exactly 1 GatewaySettlement, got %d", gsCount)
	}

	// Invoice status is paid (not changed back or double-applied).
	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPaid {
		t.Errorf("invoice status: want paid, got %q", inv.Status)
	}
}

func TestTryAutoSettleAfterIngestion_HappyPath(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	// Load the attempt to get its ProviderRef.
	var attempt models.HostedPaymentAttempt
	db.First(&attempt, s.attemptID)

	// Call TryAutoSettleAfterIngestion (the post-ingestion trigger).
	TryAutoSettleAfterIngestion(db, s.gatewayID, attempt.ProviderRef)

	// Settlement must have been created.
	var count int64
	db.Model(&models.GatewaySettlement{}).Where("hosted_attempt_id = ?", s.attemptID).Count(&count)
	if count != 1 {
		t.Errorf("TryAutoSettleAfterIngestion: expected 1 GatewaySettlement, got %d", count)
	}

	// Invoice must be paid.
	var inv models.Invoice
	db.First(&inv, s.invoiceID)
	if inv.Status != models.InvoiceStatusPaid {
		t.Errorf("invoice status: want paid, got %q", inv.Status)
	}
}

func TestTryAutoSettleAfterIngestion_SecondCall_Idempotent(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	var attempt models.HostedPaymentAttempt
	db.First(&attempt, s.attemptID)

	TryAutoSettleAfterIngestion(db, s.gatewayID, attempt.ProviderRef)
	TryAutoSettleAfterIngestion(db, s.gatewayID, attempt.ProviderRef) // second time — must be safe

	var count int64
	db.Model(&models.GatewaySettlement{}).Where("hosted_attempt_id = ?", s.attemptID).Count(&count)
	if count != 1 {
		t.Errorf("second auto-settle should be idempotent; expected 1 GatewaySettlement, got %d", count)
	}
}

func TestGetSettlementForInvoice_ReturnsNilWhenNone(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	got := GetSettlementForInvoice(db, s.companyID, s.invoiceID)
	if got != nil {
		t.Errorf("expected nil when no settlement exists, got %+v", got)
	}
}

func TestGetSettlementForInvoice_ReturnsRecordAfterSettlement(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	ExecuteGatewaySettlement(db, s.companyID, s.attemptID) //nolint:errcheck

	got := GetSettlementForInvoice(db, s.companyID, s.invoiceID)
	if got == nil {
		t.Error("expected non-nil settlement after execution")
	}
}

func TestGetSettlementForInvoice_CompanyIsolation(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	ExecuteGatewaySettlement(db, s.companyID, s.attemptID) //nolint:errcheck

	// Query with a different company should return nil.
	got := GetSettlementForInvoice(db, s.companyID+999, s.invoiceID)
	if got != nil {
		t.Errorf("company isolation violated: got %+v", got)
	}
}

// ── Unit tests for EvaluateGatewaySettlementEligibility ──────────────────────

func makeTestAttempt(amount string, currency string, status models.HostedPaymentAttemptStatus) models.HostedPaymentAttempt {
	return models.HostedPaymentAttempt{
		ID:               1,
		CompanyID:        1,
		InvoiceID:        1,
		GatewayAccountID: 1,
		Amount:           decimal.RequireFromString(amount),
		CurrencyCode:     currency,
		Status:           status,
		ProviderRef:      "cs_test",
	}
}

func makeTestInvoice(amount string, balance string, status models.InvoiceStatus, currency string, companyID uint) models.Invoice {
	return models.Invoice{
		ID:           1,
		CompanyID:    companyID,
		Status:       status,
		Amount:       decimal.RequireFromString(amount),
		BalanceDue:   decimal.RequireFromString(balance),
		CurrencyCode: currency,
	}
}

func ptrUint(v uint) *uint { return &v }

func TestEvaluateGatewaySettlementEligibility_AllRules(t *testing.T) {
	baseAttempt := makeTestAttempt("100.00", "CAD", models.HostedPaymentAttemptPaymentSucceeded)
	baseInvoice := makeTestInvoice("100.00", "100.00", models.InvoiceStatusIssued, "CAD", 1)
	baseTxn := &models.PaymentTransaction{ID: 1}
	baseMapping := &models.PaymentAccountingMapping{ClearingAccountID: ptrUint(10)}
	const baseARID = uint(20)
	const baseCurrency = "CAD"

	cases := []struct {
		name        string
		attempt     models.HostedPaymentAttempt
		inv         models.Invoice
		txn         *models.PaymentTransaction
		mapping     *models.PaymentAccountingMapping
		arAccountID uint
		wantEligible bool
	}{
		{
			name:         "all rules pass",
			attempt:      baseAttempt,
			inv:          baseInvoice,
			txn:          baseTxn,
			mapping:      baseMapping,
			arAccountID:  baseARID,
			wantEligible: true,
		},
		{
			name:         "attempt not succeeded",
			attempt:      makeTestAttempt("100.00", "CAD", models.HostedPaymentAttemptRedirected),
			inv:          baseInvoice,
			txn:          baseTxn,
			mapping:      baseMapping,
			arAccountID:  baseARID,
			wantEligible: false,
		},
		{
			name: "company mismatch",
			attempt: baseAttempt,
			inv:  makeTestInvoice("100.00", "100.00", models.InvoiceStatusIssued, "CAD", 2), // different company
			txn:          baseTxn,
			mapping:      baseMapping,
			arAccountID:  baseARID,
			wantEligible: false,
		},
		{
			name:         "invoice paid",
			attempt:      baseAttempt,
			inv:          makeTestInvoice("100.00", "0.00", models.InvoiceStatusPaid, "CAD", 1),
			txn:          baseTxn,
			mapping:      baseMapping,
			arAccountID:  baseARID,
			wantEligible: false,
		},
		{
			name:         "amount mismatch",
			attempt:      makeTestAttempt("100.00", "CAD", models.HostedPaymentAttemptPaymentSucceeded),
			inv:          makeTestInvoice("120.00", "80.00", models.InvoiceStatusIssued, "CAD", 1),
			txn:          baseTxn,
			mapping:      baseMapping,
			arAccountID:  baseARID,
			wantEligible: false,
		},
		{
			name:         "currency mismatch",
			attempt:      makeTestAttempt("100.00", "USD", models.HostedPaymentAttemptPaymentSucceeded),
			inv:          baseInvoice,
			txn:          baseTxn,
			mapping:      baseMapping,
			arAccountID:  baseARID,
			wantEligible: false,
		},
		{
			name:         "nil txn",
			attempt:      baseAttempt,
			inv:          baseInvoice,
			txn:          nil,
			mapping:      baseMapping,
			arAccountID:  baseARID,
			wantEligible: false,
		},
		{
			name:    "txn already posted",
			attempt: baseAttempt,
			inv:     baseInvoice,
			txn:     &models.PaymentTransaction{ID: 1, PostedJournalEntryID: ptrUint(5)},
			mapping:      baseMapping,
			arAccountID:  baseARID,
			wantEligible: false,
		},
		{
			name:    "txn already applied",
			attempt: baseAttempt,
			inv:     baseInvoice,
			txn:     &models.PaymentTransaction{ID: 1, AppliedInvoiceID: ptrUint(1)},
			mapping:      baseMapping,
			arAccountID:  baseARID,
			wantEligible: false,
		},
		{
			name:         "nil mapping",
			attempt:      baseAttempt,
			inv:          baseInvoice,
			txn:          baseTxn,
			mapping:      nil,
			arAccountID:  baseARID,
			wantEligible: false,
		},
		{
			name:         "nil clearing account",
			attempt:      baseAttempt,
			inv:          baseInvoice,
			txn:          baseTxn,
			mapping:      &models.PaymentAccountingMapping{ClearingAccountID: nil},
			arAccountID:  baseARID,
			wantEligible: false,
		},
		{
			name:         "no AR account",
			attempt:      baseAttempt,
			inv:          baseInvoice,
			txn:          baseTxn,
			mapping:      baseMapping,
			arAccountID:  0,
			wantEligible: false,
		},
		{
			name:         "empty invoice currency matches base currency",
			attempt:      baseAttempt,
			inv:          makeTestInvoice("100.00", "100.00", models.InvoiceStatusIssued, "", 1), // empty = base currency
			txn:          baseTxn,
			mapping:      baseMapping,
			arAccountID:  baseARID,
			wantEligible: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateGatewaySettlementEligibility(
				tc.attempt, tc.inv, tc.txn, tc.mapping, tc.arAccountID, baseCurrency,
			)
			if got.Eligible != tc.wantEligible {
				t.Errorf("Eligible=%v, want %v (reason=%q)", got.Eligible, tc.wantEligible, got.Reason)
			}
			if !tc.wantEligible && got.Reason == "" {
				t.Error("ineligible result must have a non-empty Reason")
			}
		})
	}
}

// ── Batch 12 tests: settlement operability / outcome persistence / retry ──────

// TestBatch12_AutoSettle_PersistsAppliedOutcome verifies that a successful auto-settlement
// writes SettlementStatus="applied" and clears SettlementReason on the attempt.
func TestBatch12_AutoSettle_PersistsAppliedOutcome(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	TryAutoSettleAfterIngestion(db, s.gatewayID, "cs_settle_test_001")

	var attempt models.HostedPaymentAttempt
	db.First(&attempt, s.attemptID)
	if attempt.SettlementStatus != models.SettlementOutcomeApplied {
		t.Errorf("SettlementStatus: want %q, got %q", models.SettlementOutcomeApplied, attempt.SettlementStatus)
	}
	if attempt.SettlementReason != "" {
		t.Errorf("SettlementReason: want empty, got %q", attempt.SettlementReason)
	}
	if attempt.SettlementLastAttemptedAt == nil {
		t.Error("SettlementLastAttemptedAt must be set after successful auto-settlement")
	}
}

// TestBatch12_AutoSettle_PersistsPendingReview_MissingClearingAccount verifies that
// when auto-settlement is ineligible (no clearing account), the outcome is persisted
// as "pending_review" with a non-empty reason — not just logged.
func TestBatch12_AutoSettle_PersistsPendingReview_MissingClearingAccount(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	// Remove the clearing account from the mapping.
	db.Model(&models.PaymentAccountingMapping{}).
		Where("gateway_account_id = ? AND company_id = ?", s.gatewayID, s.companyID).
		Update("clearing_account_id", nil)

	TryAutoSettleAfterIngestion(db, s.gatewayID, "cs_settle_test_001")

	var attempt models.HostedPaymentAttempt
	db.First(&attempt, s.attemptID)
	if attempt.SettlementStatus != models.SettlementOutcomePendingReview {
		t.Errorf("SettlementStatus: want %q, got %q",
			models.SettlementOutcomePendingReview, attempt.SettlementStatus)
	}
	if attempt.SettlementReason == "" {
		t.Error("SettlementReason must be non-empty for ineligible outcome")
	}
	if attempt.SettlementLastAttemptedAt == nil {
		t.Error("SettlementLastAttemptedAt must be set even on ineligible outcome")
	}
	// No GatewaySettlement must have been created.
	gs := GetSettlementForAttempt(db, s.companyID, s.attemptID)
	if gs != nil {
		t.Error("GatewaySettlement must not exist when settlement was ineligible")
	}
}

// TestBatch12_AutoSettle_PersistsPendingReview_AmountMismatch verifies that an
// amount mismatch persists the reason so the operator knows why settlement was skipped.
func TestBatch12_AutoSettle_PersistsPendingReview_AmountMismatch(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	// Make the invoice balance different from the attempt amount.
	db.Model(&models.Invoice{}).Where("id = ?", s.invoiceID).
		Update("balance_due", decimal.NewFromInt(50))

	TryAutoSettleAfterIngestion(db, s.gatewayID, "cs_settle_test_001")

	var attempt models.HostedPaymentAttempt
	db.First(&attempt, s.attemptID)
	if attempt.SettlementStatus != models.SettlementOutcomePendingReview {
		t.Errorf("SettlementStatus: want pending_review, got %q", attempt.SettlementStatus)
	}
	if attempt.SettlementReason == "" {
		t.Error("SettlementReason must be non-empty for amount mismatch")
	}
}

// TestBatch12_RetryAfterConfigFix_SettlesExactlyOnce verifies that:
// 1. Initial auto-settle with missing clearing account → pending_review
// 2. Config is fixed
// 3. RetryGatewaySettlement → settlement applied
// 4. Second retry → ErrSettlementAlreadyDone, no duplicate JE/apply
func TestBatch12_RetryAfterConfigFix_SettlesExactlyOnce(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	// Step 1: Remove clearing account → ineligible.
	db.Model(&models.PaymentAccountingMapping{}).
		Where("gateway_account_id = ? AND company_id = ?", s.gatewayID, s.companyID).
		Update("clearing_account_id", nil)

	TryAutoSettleAfterIngestion(db, s.gatewayID, "cs_settle_test_001")

	var attempt models.HostedPaymentAttempt
	db.First(&attempt, s.attemptID)
	if attempt.SettlementStatus != models.SettlementOutcomePendingReview {
		t.Fatalf("expected pending_review after missing clearing, got %q", attempt.SettlementStatus)
	}

	// Step 2: Fix the config — restore clearing account.
	db.Model(&models.PaymentAccountingMapping{}).
		Where("gateway_account_id = ? AND company_id = ?", s.gatewayID, s.companyID).
		Update("clearing_account_id", s.clearingID)

	// Step 3: Retry → should now succeed.
	result, err := RetryGatewaySettlement(db, s.companyID, s.invoiceID)
	if err != nil {
		t.Fatalf("RetryGatewaySettlement unexpected error: %v", err)
	}
	if !result.Eligibility.Eligible {
		t.Fatalf("expected eligible after config fix, reason: %q", result.Eligibility.Reason)
	}
	if result.Settlement == nil {
		t.Fatal("expected non-nil settlement after retry")
	}

	// Outcome field must reflect applied.
	db.First(&attempt, s.attemptID)
	if attempt.SettlementStatus != models.SettlementOutcomeApplied {
		t.Errorf("SettlementStatus after retry: want applied, got %q", attempt.SettlementStatus)
	}
	if attempt.SettlementReason != "" {
		t.Errorf("SettlementReason after applied: want empty, got %q", attempt.SettlementReason)
	}

	// Step 4: Second retry → ErrSettlementAlreadyDone, no duplicate JE.
	result2, err2 := RetryGatewaySettlement(db, s.companyID, s.invoiceID)
	if err2 != ErrSettlementAlreadyDone {
		t.Errorf("second retry: want ErrSettlementAlreadyDone, got %v", err2)
	}
	_ = result2

	// Verify only one JournalEntry exists for this settlement.
	var jeCount int64
	db.Model(&models.JournalEntry{}).
		Where("source_type = ? AND source_id = ?", models.LedgerSourcePaymentGateway, s.txnID).
		Count(&jeCount)
	if jeCount != 1 {
		t.Errorf("expected exactly 1 JournalEntry, got %d", jeCount)
	}

	// Verify only one GatewaySettlement row exists.
	var gsCount int64
	db.Model(&models.GatewaySettlement{}).Where("hosted_attempt_id = ?", s.attemptID).Count(&gsCount)
	if gsCount != 1 {
		t.Errorf("expected exactly 1 GatewaySettlement, got %d", gsCount)
	}
}

// TestBatch12_RetryWhileStillIneligible_PreservesReason verifies that a retry
// when conditions are still not met updates the reason (does not clear it or
// mark as applied) and returns an ineligible result.
func TestBatch12_RetryWhileStillIneligible_PreservesReason(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	// Remove clearing account so settlement is ineligible.
	db.Model(&models.PaymentAccountingMapping{}).
		Where("gateway_account_id = ? AND company_id = ?", s.gatewayID, s.companyID).
		Update("clearing_account_id", nil)

	// First retry: should be ineligible.
	result, err := RetryGatewaySettlement(db, s.companyID, s.invoiceID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Eligibility.Eligible {
		t.Fatal("expected ineligible result")
	}

	var attempt models.HostedPaymentAttempt
	db.First(&attempt, s.attemptID)
	if attempt.SettlementStatus != models.SettlementOutcomePendingReview {
		t.Errorf("want pending_review, got %q", attempt.SettlementStatus)
	}
	if attempt.SettlementReason == "" {
		t.Error("reason must be non-empty when ineligible")
	}
	firstReason := attempt.SettlementReason

	// Second retry: still ineligible — reason must still be set (not cleared).
	RetryGatewaySettlement(db, s.companyID, s.invoiceID)
	db.First(&attempt, s.attemptID)
	if attempt.SettlementStatus != models.SettlementOutcomePendingReview {
		t.Errorf("want pending_review after second retry, got %q", attempt.SettlementStatus)
	}
	if attempt.SettlementReason == "" {
		t.Error("reason must be preserved after second failed retry")
	}
	// Reason should be consistent (same condition).
	if attempt.SettlementReason != firstReason {
		t.Errorf("reason changed unexpectedly: %q → %q", firstReason, attempt.SettlementReason)
	}

	// No settlement must exist.
	if GetSettlementForAttempt(db, s.companyID, s.attemptID) != nil {
		t.Error("GatewaySettlement must not exist while still ineligible")
	}
}

// TestBatch12_RetryNoSucceededAttempt returns ErrNoSucceededAttempt when the
// invoice has no payment_succeeded attempt.
func TestBatch12_RetryNoSucceededAttempt(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	// Reset attempt to redirected status — not succeeded.
	db.Model(&models.HostedPaymentAttempt{}).Where("id = ?", s.attemptID).
		Update("status", models.HostedPaymentAttemptRedirected)

	_, err := RetryGatewaySettlement(db, s.companyID, s.invoiceID)
	if err != ErrNoSucceededAttempt {
		t.Errorf("want ErrNoSucceededAttempt, got %v", err)
	}
}

// TestBatch12_RetryCompanyIsolation verifies that RetryGatewaySettlement with
// a wrong companyID returns ErrNoSucceededAttempt, not a cross-company settlement.
func TestBatch12_RetryCompanyIsolation(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	// Use a different company ID.
	wrongCompany := s.companyID + 9999
	_, err := RetryGatewaySettlement(db, wrongCompany, s.invoiceID)
	if err != ErrNoSucceededAttempt {
		t.Errorf("want ErrNoSucceededAttempt for wrong company, got %v", err)
	}

	// Original data must be untouched.
	gs := GetSettlementForAttempt(db, s.companyID, s.attemptID)
	if gs != nil {
		t.Error("wrong company retry must not create a GatewaySettlement")
	}
}

// TestBatch12_OutcomeField_NotSetBeforeAnyAttempt verifies that a fresh attempt
// has empty SettlementStatus (not attempted state is the zero value).
func TestBatch12_OutcomeField_NotSetBeforeAnyAttempt(t *testing.T) {
	db := settlementTestDB(t)
	s := seedSettlementBase(t, db)

	var attempt models.HostedPaymentAttempt
	db.First(&attempt, s.attemptID)
	if attempt.SettlementStatus != "" {
		t.Errorf("fresh attempt SettlementStatus: want empty, got %q", attempt.SettlementStatus)
	}
	if attempt.SettlementLastAttemptedAt != nil {
		t.Error("fresh attempt SettlementLastAttemptedAt must be nil")
	}
}
