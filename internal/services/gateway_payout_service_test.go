// 遵循project_guide.md
package services

// gateway_payout_service_test.go — Batch 14 gateway payout bridge tests.
//
// Coverage:
//  TestGatewayPayout_HappyPath_FeePositive
//      multi-settlement payout with fee > 0
//      JE: Dr Bank=net, Dr FeeExpense=fee, Cr Clearing=gross
//
//  TestGatewayPayout_HappyPath_FeeZero
//      fee=0 → only 2 JE lines (Dr Bank, Cr Clearing); no fee line
//
//  TestGatewayPayout_DuplicateProviderPayoutID
//      second call with same provider_payout_id → ErrPayoutDuplicate, no extra JE
//
//  TestGatewayPayout_SettlementAlreadyBridged
//      settlement already in another payout → ErrPayoutSettlementAlreadyBridged
//
//  TestGatewayPayout_CrossCompanySettlement
//      settlement from a different company → ErrPayoutSettlementNotFound
//
//  TestGatewayPayout_MixedGatewayAccount
//      settlements from different gateway accounts → ErrPayoutSettlementGatewayMismatch
//
//  TestGatewayPayout_InvalidBankAccount
//      non-existent or inactive bank account → ErrPayoutBankAccountInvalid / Inactive
//
//  TestGatewayPayout_BankAccountNotAsset
//      bank account with non-asset root type → ErrPayoutBankAccountNotAsset
//
//  TestGatewayPayout_MissingClearingMapping
//      ClearingAccountID nil → ErrPayoutNoClearingAccount
//
//  TestGatewayPayout_MissingFeeExpenseMapping
//      fee>0 but FeeExpenseAccountID nil → ErrPayoutNoFeeExpenseAccount
//
//  TestGatewayPayout_FeeExceedsGross
//      fee > gross → ErrPayoutFeeExceedsGross
//
//  TestGatewayPayout_NoInvoiceARTruthUnchanged
//      payout does not modify invoice status, balance_due, or AR-related JEs
//
//  TestGatewayPayout_ConcurrentSameSettlements_ExactlyOnce
//      two goroutines race to bridge the same settlements; exactly one succeeds

import (
	"fmt"
	"sync"
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

func payoutTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:gwpayout_%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
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
		&models.GatewayPayout{},
		&models.GatewayPayoutSettlement{},
		&models.WebhookEvent{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

// ── Seed helpers ──────────────────────────────────────────────────────────────

type payoutBase struct {
	companyID    uint
	gatewayID    uint
	bankID       uint // asset/bank account (Dr on payout)
	clearingID   uint // GW clearing (Cr on payout)
	feeExpenseID uint // fee expense (Dr on payout when fee>0)
	arID         uint
}

func seedPayoutBase(t *testing.T, db *gorm.DB) payoutBase {
	t.Helper()
	co := models.Company{
		Name: fmt.Sprintf("PayoutCo%d", time.Now().UnixNano()),
		BaseCurrencyCode: "CAD",
		IsActive:         true,
	}
	db.Create(&co)

	gw := models.PaymentGatewayAccount{
		CompanyID:   co.ID,
		ProviderType: models.ProviderStripe,
		DisplayName: "Stripe",
		IsActive:    true,
	}
	db.Create(&gw)

	// GL accounts
	bank := models.Account{
		CompanyID: co.ID, Code: "1000", Name: "Bank Chequing",
		RootAccountType:   models.RootAsset,
		DetailAccountType: models.DetailBank,
		IsActive:          true,
	}
	db.Create(&bank)

	clearing := models.Account{
		CompanyID: co.ID, Code: "2100", Name: "GW Clearing",
		RootAccountType:   models.RootLiability,
		DetailAccountType: models.DetailOtherCurrentLiability,
		IsActive:          true,
	}
	db.Create(&clearing)

	feeExp := models.Account{
		CompanyID: co.ID, Code: "6100", Name: "Gateway Fee Expense",
		RootAccountType:   models.RootExpense,
		DetailAccountType: models.DetailOperatingExpense,
		IsActive:          true,
	}
	db.Create(&feeExp)

	ar := models.Account{
		CompanyID: co.ID, Code: "1100", Name: "AR",
		RootAccountType:   models.RootAsset,
		DetailAccountType: models.DetailAccountsReceivable,
		IsActive:          true,
	}
	db.Create(&ar)

	// Accounting mapping: clearing + fee expense configured
	mapping := models.PaymentAccountingMapping{
		CompanyID:           co.ID,
		GatewayAccountID:    gw.ID,
		ClearingAccountID:   &clearing.ID,
		FeeExpenseAccountID: &feeExp.ID,
	}
	db.Create(&mapping)

	return payoutBase{
		companyID:    co.ID,
		gatewayID:    gw.ID,
		bankID:       bank.ID,
		clearingID:   clearing.ID,
		feeExpenseID: feeExp.ID,
		arID:         ar.ID,
	}
}

// seedOneSettlement creates one complete GatewaySettlement with all required
// dependencies: invoice, hosted link, hosted attempt, payment request,
// payment transaction, and the settlement JE. Returns the GatewaySettlement.
// Amount defaults to 100 CAD.
func seedOneSettlement(t *testing.T, db *gorm.DB, base payoutBase, amount decimal.Decimal) models.GatewaySettlement {
	t.Helper()
	return seedOneSettlementWithGateway(t, db, base, base.gatewayID, amount)
}

// seedOneSettlementWithGateway is like seedOneSettlement but lets the caller
// override the gateway account ID (used for cross-gateway tests).
func seedOneSettlementWithGateway(t *testing.T, db *gorm.DB, base payoutBase, gwID uint, amount decimal.Decimal) models.GatewaySettlement {
	t.Helper()
	tag := uniqueTestTag()

	cust := models.Customer{CompanyID: base.companyID, Name: "C" + tag}
	db.Create(&cust)

	inv := models.Invoice{
		CompanyID: base.companyID, CustomerID: cust.ID,
		InvoiceNumber: "INV-" + tag,
		InvoiceDate:   time.Now(),
		Status:        models.InvoiceStatusPaid, // already paid (settlement done)
		Amount:        amount, Subtotal: amount, TaxTotal: decimal.Zero,
		BalanceDue: decimal.Zero, BalanceDueBase: decimal.Zero,
		CurrencyCode:         "CAD",
		CustomerNameSnapshot: "C" + tag,
	}
	db.Create(&inv)

	link := models.InvoiceHostedLink{
		CompanyID: base.companyID, InvoiceID: inv.ID,
		TokenHash: "th" + tag,
		Status:    models.InvoiceHostedLinkStatusActive,
	}
	db.Create(&link)

	attempt := models.HostedPaymentAttempt{
		CompanyID: base.companyID, InvoiceID: inv.ID,
		HostedLinkID:     link.ID,
		GatewayAccountID: gwID,
		ProviderType:     models.ProviderStripe,
		Amount:           amount, CurrencyCode: "CAD",
		Status:      models.HostedPaymentAttemptPaymentSucceeded,
		ProviderRef: "cs_" + tag,
		SettlementStatus: models.SettlementOutcomeApplied,
	}
	db.Create(&attempt)

	pr := models.PaymentRequest{
		CompanyID: base.companyID, GatewayAccountID: gwID,
		InvoiceID: &inv.ID, Amount: amount, CurrencyCode: "CAD",
		Status:      models.PaymentRequestPaid,
		ExternalRef: "cs_" + tag,
		Description: "test",
	}
	db.Create(&pr)

	txn := models.PaymentTransaction{
		CompanyID: base.companyID, GatewayAccountID: gwID,
		PaymentRequestID: &pr.ID,
		TransactionType:  models.TxnTypeCharge,
		Amount:           amount, CurrencyCode: "CAD",
		Status: "completed", ExternalTxnRef: "pi_" + tag,
		RawPayload: datatypes.JSON(`{}`),
	}
	db.Create(&txn)

	// Minimal settlement JE (represents the Dr Clearing / Cr AR entry).
	settleJE := models.JournalEntry{
		CompanyID: base.companyID, EntryDate: time.Now(),
		JournalNo:  "GWSETTLE-" + tag,
		Status:     models.JournalEntryStatusPosted,
		SourceType: models.LedgerSourcePaymentGateway,
		SourceID:   txn.ID,
	}
	db.Create(&settleJE)

	gs := models.GatewaySettlement{
		CompanyID:            base.companyID,
		HostedAttemptID:      attempt.ID,
		PaymentTransactionID: txn.ID,
		InvoiceID:            inv.ID,
		JournalEntryID:       settleJE.ID,
		Amount:               amount,
		CurrencyCode:         "CAD",
		SettledAt:            time.Now(),
	}
	if err := db.Create(&gs).Error; err != nil {
		t.Fatalf("seedOneSettlement: create GatewaySettlement: %v", err)
	}
	return gs
}

// defaultPayoutInput builds a CreateGatewayPayoutInput from a base + settlement IDs.
func defaultPayoutInput(base payoutBase, settlementIDs []uint) CreateGatewayPayoutInput {
	return CreateGatewayPayoutInput{
		CompanyID:        base.companyID,
		GatewayAccountID: base.gatewayID,
		// Routed through uniqueTestTag() so concurrent /
		// same-nanosecond calls never collide — see
		// test_helpers_unique_tag_test.go for the flake history.
		ProviderPayoutID: "po_" + uniqueTestTag(),
		PayoutDate:       time.Now(),
		FeeAmount:        decimal.NewFromFloat(2.50),
		BankAccountID:    base.bankID,
		SettlementIDs:    settlementIDs,
	}
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestGatewayPayout_HappyPath_FeePositive(t *testing.T) {
	db := payoutTestDB(t)
	base := seedPayoutBase(t, db)

	gs1 := seedOneSettlement(t, db, base, decimal.NewFromInt(100))
	gs2 := seedOneSettlement(t, db, base, decimal.NewFromInt(200))

	inp := defaultPayoutInput(base, []uint{gs1.ID, gs2.ID})
	inp.FeeAmount = decimal.NewFromFloat(4.50) // fee on gross 300

	result, err := CreateGatewayPayout(db, inp)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	p := result.Payout
	if p.GrossAmount.String() != "300" {
		t.Errorf("gross: want 300, got %s", p.GrossAmount)
	}
	if p.FeeAmount.String() != "4.5" {
		t.Errorf("fee: want 4.5, got %s", p.FeeAmount)
	}
	if p.NetAmount.String() != "295.5" {
		t.Errorf("net: want 295.5, got %s", p.NetAmount)
	}
	if p.JournalEntryID == nil {
		t.Fatal("JournalEntryID should be set after creation")
	}
	if p.CurrencyCode != "CAD" {
		t.Errorf("currency: want CAD, got %s", p.CurrencyCode)
	}

	// Verify JE lines: Dr Bank (295.5), Dr FeeExpense (4.5), Cr Clearing (300)
	var lines []models.JournalLine
	db.Where("journal_entry_id = ? AND company_id = ?", *p.JournalEntryID, base.companyID).Find(&lines)
	if len(lines) != 3 {
		t.Fatalf("want 3 JE lines, got %d", len(lines))
	}

	var drBank, drFee, crClearing decimal.Decimal
	for _, l := range lines {
		if l.AccountID == base.bankID {
			drBank = l.Debit
		} else if l.AccountID == base.feeExpenseID {
			drFee = l.Debit
		} else if l.AccountID == base.clearingID {
			crClearing = l.Credit
		}
	}
	if !drBank.Equal(decimal.NewFromFloat(295.5)) {
		t.Errorf("Dr Bank: want 295.5, got %s", drBank)
	}
	if !drFee.Equal(decimal.NewFromFloat(4.5)) {
		t.Errorf("Dr FeeExpense: want 4.5, got %s", drFee)
	}
	if !crClearing.Equal(decimal.NewFromInt(300)) {
		t.Errorf("Cr Clearing: want 300, got %s", crClearing)
	}

	// Verify join rows exist.
	var joinCount int64
	db.Model(&models.GatewayPayoutSettlement{}).
		Where("gateway_payout_id = ? AND company_id = ?", p.ID, base.companyID).
		Count(&joinCount)
	if joinCount != 2 {
		t.Errorf("want 2 join rows, got %d", joinCount)
	}

	// Verify ledger entries (one per JE line).
	var ledgerCount int64
	db.Model(&models.LedgerEntry{}).
		Where("journal_entry_id = ? AND company_id = ?", *p.JournalEntryID, base.companyID).
		Count(&ledgerCount)
	if ledgerCount != 3 {
		t.Errorf("want 3 ledger entries, got %d", ledgerCount)
	}
}

func TestGatewayPayout_HappyPath_FeeZero(t *testing.T) {
	db := payoutTestDB(t)
	base := seedPayoutBase(t, db)

	gs := seedOneSettlement(t, db, base, decimal.NewFromInt(150))

	inp := defaultPayoutInput(base, []uint{gs.ID})
	inp.FeeAmount = decimal.Zero

	result, err := CreateGatewayPayout(db, inp)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	p := result.Payout
	if !p.NetAmount.Equal(decimal.NewFromInt(150)) {
		t.Errorf("net: want 150, got %s", p.NetAmount)
	}

	// With fee=0, only 2 lines: Dr Bank, Cr Clearing — no fee line.
	var lines []models.JournalLine
	db.Where("journal_entry_id = ?", *p.JournalEntryID).Find(&lines)
	if len(lines) != 2 {
		t.Errorf("fee=0: want 2 JE lines, got %d", len(lines))
	}
	for _, l := range lines {
		if l.AccountID == base.feeExpenseID {
			t.Error("fee=0: unexpected fee expense line in JE")
		}
	}
}

func TestGatewayPayout_DuplicateProviderPayoutID(t *testing.T) {
	db := payoutTestDB(t)
	base := seedPayoutBase(t, db)

	gs1 := seedOneSettlement(t, db, base, decimal.NewFromInt(100))
	gs2 := seedOneSettlement(t, db, base, decimal.NewFromInt(100))

	providerID := fmt.Sprintf("po_dup_%d", time.Now().UnixNano())

	inp1 := defaultPayoutInput(base, []uint{gs1.ID})
	inp1.ProviderPayoutID = providerID
	inp1.FeeAmount = decimal.Zero

	_, err := CreateGatewayPayout(db, inp1)
	if err != nil {
		t.Fatalf("first payout failed: %v", err)
	}

	// Second call with same provider_payout_id, different settlement.
	inp2 := defaultPayoutInput(base, []uint{gs2.ID})
	inp2.ProviderPayoutID = providerID
	inp2.FeeAmount = decimal.Zero

	_, err = CreateGatewayPayout(db, inp2)
	if err != ErrPayoutDuplicate {
		t.Errorf("want ErrPayoutDuplicate, got %v", err)
	}

	// Verify exactly one JE was created for this provider_payout_id.
	var jeCount int64
	db.Model(&models.JournalEntry{}).
		Where("journal_no = ?", "GWPAYOUT-"+providerID).
		Count(&jeCount)
	if jeCount != 1 {
		t.Errorf("want exactly 1 JE, got %d", jeCount)
	}
}

func TestGatewayPayout_SettlementAlreadyBridged(t *testing.T) {
	db := payoutTestDB(t)
	base := seedPayoutBase(t, db)

	gs := seedOneSettlement(t, db, base, decimal.NewFromInt(100))

	// First payout bridges the settlement.
	inp1 := defaultPayoutInput(base, []uint{gs.ID})
	inp1.FeeAmount = decimal.Zero
	_, err := CreateGatewayPayout(db, inp1)
	if err != nil {
		t.Fatalf("first payout failed: %v", err)
	}

	// Second payout tries to bridge the same settlement.
	inp2 := defaultPayoutInput(base, []uint{gs.ID})
	inp2.FeeAmount = decimal.Zero
	_, err = CreateGatewayPayout(db, inp2)
	if err != ErrPayoutSettlementAlreadyBridged {
		t.Errorf("want ErrPayoutSettlementAlreadyBridged, got %v", err)
	}

	// No partial writes: exactly one payout row exists.
	var payoutCount int64
	db.Model(&models.GatewayPayout{}).Where("company_id = ?", base.companyID).Count(&payoutCount)
	if payoutCount != 1 {
		t.Errorf("want exactly 1 payout, got %d", payoutCount)
	}
}

func TestGatewayPayout_CrossCompanySettlement(t *testing.T) {
	db := payoutTestDB(t)

	// Two separate companies.
	base1 := seedPayoutBase(t, db)
	base2 := seedPayoutBase(t, db)

	// Settlement belongs to company 2.
	gsForeign := seedOneSettlement(t, db, base2, decimal.NewFromInt(100))

	// Company 1 tries to bridge company 2's settlement.
	inp := defaultPayoutInput(base1, []uint{gsForeign.ID})
	inp.FeeAmount = decimal.Zero

	_, err := CreateGatewayPayout(db, inp)
	if err != ErrPayoutSettlementNotFound {
		t.Errorf("want ErrPayoutSettlementNotFound, got %v", err)
	}

	// No payout rows for either company.
	var count int64
	db.Model(&models.GatewayPayout{}).Count(&count)
	if count != 0 {
		t.Errorf("want 0 payout rows after cross-company reject, got %d", count)
	}
}

func TestGatewayPayout_MixedGatewayAccount(t *testing.T) {
	db := payoutTestDB(t)
	base := seedPayoutBase(t, db)

	// Second gateway account for the same company.
	gw2 := models.PaymentGatewayAccount{
		CompanyID:   base.companyID,
		ProviderType: models.ProviderStripe,
		DisplayName: "Stripe 2",
		IsActive:    true,
	}
	db.Create(&gw2)
	mapping2 := models.PaymentAccountingMapping{
		CompanyID: base.companyID, GatewayAccountID: gw2.ID,
		ClearingAccountID: &base.clearingID,
	}
	db.Create(&mapping2)

	gsOwn := seedOneSettlement(t, db, base, decimal.NewFromInt(100))
	gsForeign := seedOneSettlementWithGateway(t, db, base, gw2.ID, decimal.NewFromInt(100))

	inp := defaultPayoutInput(base, []uint{gsOwn.ID, gsForeign.ID})
	inp.FeeAmount = decimal.Zero

	_, err := CreateGatewayPayout(db, inp)
	if err != ErrPayoutSettlementGatewayMismatch {
		t.Errorf("want ErrPayoutSettlementGatewayMismatch, got %v", err)
	}

	var count int64
	db.Model(&models.GatewayPayout{}).Where("company_id = ?", base.companyID).Count(&count)
	if count != 0 {
		t.Errorf("want 0 payout rows after gateway mismatch, got %d", count)
	}
}

func TestGatewayPayout_InvalidBankAccount(t *testing.T) {
	db := payoutTestDB(t)
	base := seedPayoutBase(t, db)
	gs := seedOneSettlement(t, db, base, decimal.NewFromInt(100))

	inp := defaultPayoutInput(base, []uint{gs.ID})
	inp.BankAccountID = 99999 // non-existent
	inp.FeeAmount = decimal.Zero

	_, err := CreateGatewayPayout(db, inp)
	if err != ErrPayoutBankAccountInvalid {
		t.Errorf("want ErrPayoutBankAccountInvalid, got %v", err)
	}
}

func TestGatewayPayout_InactiveBankAccount(t *testing.T) {
	db := payoutTestDB(t)
	base := seedPayoutBase(t, db)
	gs := seedOneSettlement(t, db, base, decimal.NewFromInt(100))

	// Create a bank account then explicitly deactivate it.
	// IsActive has gorm:"default:true" so we cannot rely on the zero-value false
	// being persisted during Create — GORM skips zero values and the DB default fires.
	inactive := models.Account{
		CompanyID: base.companyID, Code: "1001", Name: "Inactive Bank",
		RootAccountType: models.RootAsset, DetailAccountType: models.DetailBank,
		IsActive: true,
	}
	db.Create(&inactive)
	db.Model(&inactive).Update("is_active", false)

	inp := defaultPayoutInput(base, []uint{gs.ID})
	inp.BankAccountID = inactive.ID
	inp.FeeAmount = decimal.Zero

	_, err := CreateGatewayPayout(db, inp)
	if err != ErrPayoutBankAccountInactive {
		t.Errorf("want ErrPayoutBankAccountInactive, got %v", err)
	}
}

func TestGatewayPayout_BankAccountNotAsset(t *testing.T) {
	db := payoutTestDB(t)
	base := seedPayoutBase(t, db)
	gs := seedOneSettlement(t, db, base, decimal.NewFromInt(100))

	// A liability account (not an asset).
	liability := models.Account{
		CompanyID: base.companyID, Code: "2200", Name: "Liability Acct",
		RootAccountType: models.RootLiability, DetailAccountType: models.DetailOtherCurrentLiability,
		IsActive: true,
	}
	db.Create(&liability)

	inp := defaultPayoutInput(base, []uint{gs.ID})
	inp.BankAccountID = liability.ID
	inp.FeeAmount = decimal.Zero

	_, err := CreateGatewayPayout(db, inp)
	if err != ErrPayoutBankAccountNotAsset {
		t.Errorf("want ErrPayoutBankAccountNotAsset, got %v", err)
	}
}

func TestGatewayPayout_MissingClearingMapping(t *testing.T) {
	db := payoutTestDB(t)
	base := seedPayoutBase(t, db)
	gs := seedOneSettlement(t, db, base, decimal.NewFromInt(100))

	// Null out the clearing account in the mapping.
	db.Model(&models.PaymentAccountingMapping{}).
		Where("company_id = ? AND gateway_account_id = ?", base.companyID, base.gatewayID).
		Update("clearing_account_id", nil)

	inp := defaultPayoutInput(base, []uint{gs.ID})
	inp.FeeAmount = decimal.Zero

	_, err := CreateGatewayPayout(db, inp)
	if err != ErrPayoutNoClearingAccount {
		t.Errorf("want ErrPayoutNoClearingAccount, got %v", err)
	}
	var count int64
	db.Model(&models.GatewayPayout{}).Count(&count)
	if count != 0 {
		t.Errorf("want 0 payouts after missing clearing, got %d", count)
	}
}

func TestGatewayPayout_MissingFeeExpenseMapping(t *testing.T) {
	db := payoutTestDB(t)
	base := seedPayoutBase(t, db)
	gs := seedOneSettlement(t, db, base, decimal.NewFromInt(100))

	// Null out the fee expense account.
	db.Model(&models.PaymentAccountingMapping{}).
		Where("company_id = ? AND gateway_account_id = ?", base.companyID, base.gatewayID).
		Update("fee_expense_account_id", nil)

	inp := defaultPayoutInput(base, []uint{gs.ID})
	inp.FeeAmount = decimal.NewFromFloat(2.50) // fee > 0 requires fee mapping

	_, err := CreateGatewayPayout(db, inp)
	if err != ErrPayoutNoFeeExpenseAccount {
		t.Errorf("want ErrPayoutNoFeeExpenseAccount, got %v", err)
	}
	var jeCount int64
	db.Model(&models.JournalEntry{}).
		Where("source_type = ? AND company_id = ?", models.LedgerSourceGatewayPayout, base.companyID).
		Count(&jeCount)
	if jeCount != 0 {
		t.Errorf("want 0 payout JEs after missing fee mapping, got %d", jeCount)
	}
}

func TestGatewayPayout_FeeExceedsGross(t *testing.T) {
	db := payoutTestDB(t)
	base := seedPayoutBase(t, db)
	gs := seedOneSettlement(t, db, base, decimal.NewFromInt(100))

	inp := defaultPayoutInput(base, []uint{gs.ID})
	inp.FeeAmount = decimal.NewFromInt(150) // 150 > gross 100

	_, err := CreateGatewayPayout(db, inp)
	if err != ErrPayoutFeeExceedsGross {
		t.Errorf("want ErrPayoutFeeExceedsGross, got %v", err)
	}
}

func TestGatewayPayout_NoInvoiceARTruthUnchanged(t *testing.T) {
	db := payoutTestDB(t)
	base := seedPayoutBase(t, db)
	gs := seedOneSettlement(t, db, base, decimal.NewFromInt(100))

	// Capture invoice state before payout.
	var invBefore models.Invoice
	db.First(&invBefore, gs.InvoiceID)

	inp := defaultPayoutInput(base, []uint{gs.ID})
	inp.FeeAmount = decimal.Zero
	_, err := CreateGatewayPayout(db, inp)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	// Invoice status and balance must be unchanged.
	var invAfter models.Invoice
	db.First(&invAfter, gs.InvoiceID)
	if invAfter.Status != invBefore.Status {
		t.Errorf("invoice status changed: before=%s after=%s", invBefore.Status, invAfter.Status)
	}
	if !invAfter.BalanceDue.Equal(invBefore.BalanceDue) {
		t.Errorf("invoice balance_due changed: before=%s after=%s", invBefore.BalanceDue, invAfter.BalanceDue)
	}

	// No extra AR JE created (no LedgerSourcePayment / LedgerSourceInvoice entries from payout).
	var arLedgerCount int64
	db.Model(&models.LedgerEntry{}).
		Where("company_id = ? AND source_type IN ?", base.companyID,
			[]string{string(models.LedgerSourcePayment), string(models.LedgerSourceInvoice)}).
		Count(&arLedgerCount)
	if arLedgerCount != 0 {
		t.Errorf("payout should not create AR/invoice ledger entries; found %d", arLedgerCount)
	}
}

func TestGatewayPayout_ConcurrentSameSettlements_ExactlyOnce(t *testing.T) {
	db := payoutTestDB(t)
	base := seedPayoutBase(t, db)
	gs := seedOneSettlement(t, db, base, decimal.NewFromInt(100))

	providerID := fmt.Sprintf("po_race_%d", time.Now().UnixNano())

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		successes int
		errs    []error
	)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inp := CreateGatewayPayoutInput{
				CompanyID:        base.companyID,
				GatewayAccountID: base.gatewayID,
				ProviderPayoutID: providerID,
				PayoutDate:       time.Now(),
				FeeAmount:        decimal.Zero,
				BankAccountID:    base.bankID,
				SettlementIDs:    []uint{gs.ID},
			}
			_, err := CreateGatewayPayout(db, inp)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes++
			} else {
				errs = append(errs, err)
			}
		}()
	}
	wg.Wait()

	if successes != 1 {
		t.Errorf("want exactly 1 success from concurrent races, got %d", successes)
	}

	// Exactly one payout and one payout JE.
	var payoutCount int64
	db.Model(&models.GatewayPayout{}).Where("company_id = ?", base.companyID).Count(&payoutCount)
	if payoutCount != 1 {
		t.Errorf("want exactly 1 GatewayPayout, got %d", payoutCount)
	}

	var jeCount int64
	db.Model(&models.JournalEntry{}).
		Where("source_type = ? AND company_id = ?", models.LedgerSourceGatewayPayout, base.companyID).
		Count(&jeCount)
	if jeCount != 1 {
		t.Errorf("want exactly 1 payout JE, got %d", jeCount)
	}

	// Settlement must appear in exactly one join row.
	var joinCount int64
	db.Model(&models.GatewayPayoutSettlement{}).
		Where("gateway_settlement_id = ?", gs.ID).
		Count(&joinCount)
	if joinCount != 1 {
		t.Errorf("want exactly 1 join row for settlement, got %d", joinCount)
	}
}

// ── Additional seed helper ────────────────────────────────────────────────────

// seedOneSettlementWithCurrency is like seedOneSettlement but lets the caller
// override the currency code. Used for multi-currency rejection tests.
func seedOneSettlementWithCurrency(t *testing.T, db *gorm.DB, base payoutBase, currency string, amount decimal.Decimal) models.GatewaySettlement {
	t.Helper()
	tag := uniqueTestTag()

	cust := models.Customer{CompanyID: base.companyID, Name: "C" + tag}
	db.Create(&cust)

	inv := models.Invoice{
		CompanyID: base.companyID, CustomerID: cust.ID,
		InvoiceNumber: "INV-" + tag,
		InvoiceDate:   time.Now(),
		Status:        models.InvoiceStatusPaid,
		Amount:        amount, Subtotal: amount, TaxTotal: decimal.Zero,
		BalanceDue: decimal.Zero, BalanceDueBase: decimal.Zero,
		CurrencyCode:         currency,
		CustomerNameSnapshot: "C" + tag,
	}
	db.Create(&inv)

	link := models.InvoiceHostedLink{
		CompanyID: base.companyID, InvoiceID: inv.ID,
		TokenHash: "th" + tag,
		Status:    models.InvoiceHostedLinkStatusActive,
	}
	db.Create(&link)

	attempt := models.HostedPaymentAttempt{
		CompanyID: base.companyID, InvoiceID: inv.ID,
		HostedLinkID:     link.ID,
		GatewayAccountID: base.gatewayID,
		ProviderType:     models.ProviderStripe,
		Amount:           amount, CurrencyCode: currency,
		Status:           models.HostedPaymentAttemptPaymentSucceeded,
		ProviderRef:      "cs_" + tag,
		SettlementStatus: models.SettlementOutcomeApplied,
	}
	db.Create(&attempt)

	pr := models.PaymentRequest{
		CompanyID: base.companyID, GatewayAccountID: base.gatewayID,
		InvoiceID: &inv.ID, Amount: amount, CurrencyCode: currency,
		Status:      models.PaymentRequestPaid,
		ExternalRef: "cs_" + tag,
		Description: "test",
	}
	db.Create(&pr)

	txn := models.PaymentTransaction{
		CompanyID: base.companyID, GatewayAccountID: base.gatewayID,
		PaymentRequestID: &pr.ID,
		TransactionType:  models.TxnTypeCharge,
		Amount:           amount, CurrencyCode: currency,
		Status: "completed", ExternalTxnRef: "pi_" + tag,
		RawPayload: datatypes.JSON(`{}`),
	}
	db.Create(&txn)

	settleJE := models.JournalEntry{
		CompanyID: base.companyID, EntryDate: time.Now(),
		JournalNo:  "GWSETTLE-" + tag,
		Status:     models.JournalEntryStatusPosted,
		SourceType: models.LedgerSourcePaymentGateway,
		SourceID:   txn.ID,
	}
	db.Create(&settleJE)

	gs := models.GatewaySettlement{
		CompanyID:            base.companyID,
		HostedAttemptID:      attempt.ID,
		PaymentTransactionID: txn.ID,
		InvoiceID:            inv.ID,
		JournalEntryID:       settleJE.ID,
		Amount:               amount,
		CurrencyCode:         currency,
		SettledAt:            time.Now(),
	}
	if err := db.Create(&gs).Error; err != nil {
		t.Fatalf("seedOneSettlementWithCurrency: create GatewaySettlement: %v", err)
	}
	return gs
}

// ── New tests ─────────────────────────────────────────────────────────────────

func TestGatewayPayout_CurrencyMismatch(t *testing.T) {
	db := payoutTestDB(t)
	base := seedPayoutBase(t, db)

	// Two settlements under the same company and same gateway account,
	// but different currencies — must be rejected before any write.
	gsCAD := seedOneSettlement(t, db, base, decimal.NewFromInt(100))                      // CAD
	gsUSD := seedOneSettlementWithCurrency(t, db, base, "USD", decimal.NewFromInt(100)) // USD

	inp := defaultPayoutInput(base, []uint{gsCAD.ID, gsUSD.ID})
	inp.FeeAmount = decimal.Zero

	_, err := CreateGatewayPayout(db, inp)
	if err != ErrPayoutSettlementCurrencyMismatch {
		t.Errorf("want ErrPayoutSettlementCurrencyMismatch, got %v", err)
	}

	// No partial writes.
	var payoutCount int64
	db.Model(&models.GatewayPayout{}).Where("company_id = ?", base.companyID).Count(&payoutCount)
	if payoutCount != 0 {
		t.Errorf("want 0 payout rows after currency mismatch, got %d", payoutCount)
	}

	var joinCount int64
	db.Model(&models.GatewayPayoutSettlement{}).Where("company_id = ?", base.companyID).Count(&joinCount)
	if joinCount != 0 {
		t.Errorf("want 0 join rows after currency mismatch, got %d", joinCount)
	}

	var jeCount int64
	db.Model(&models.JournalEntry{}).
		Where("source_type = ? AND company_id = ?", models.LedgerSourceGatewayPayout, base.companyID).
		Count(&jeCount)
	if jeCount != 0 {
		t.Errorf("want 0 payout JEs after currency mismatch, got %d", jeCount)
	}
}

func TestGatewayPayout_GatewayAccountNotInCompany(t *testing.T) {
	db := payoutTestDB(t)

	// Two separate companies.
	base1 := seedPayoutBase(t, db)
	base2 := seedPayoutBase(t, db)

	gs := seedOneSettlement(t, db, base1, decimal.NewFromInt(100))

	// Company 1 tries to create a payout using company 2's gateway account ID.
	inp := defaultPayoutInput(base1, []uint{gs.ID})
	inp.GatewayAccountID = base2.gatewayID // belongs to base2, not base1
	inp.FeeAmount = decimal.Zero

	_, err := CreateGatewayPayout(db, inp)
	if err != ErrPayoutGatewayAccountInvalid {
		t.Errorf("want ErrPayoutGatewayAccountInvalid, got %v", err)
	}

	// No writes for either company.
	var count int64
	db.Model(&models.GatewayPayout{}).Count(&count)
	if count != 0 {
		t.Errorf("want 0 payout rows after gateway ownership reject, got %d", count)
	}
}
