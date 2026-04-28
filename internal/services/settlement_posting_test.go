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

func testSettlementPostingDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:settlepost_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Account{},
		&models.SalesChannelAccount{},
		&models.ChannelAccountingMapping{},
		&models.ChannelSettlement{},
		&models.ChannelSettlementLine{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.AuditLog{},
	); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

type settlePostSetup struct {
	companyID      uint
	channelAcctID  uint
	clearingID     uint
	feeID          uint
	refundID       uint
	shippingExpID  uint
}

func setupSettlePost(t *testing.T, db *gorm.DB) settlePostSetup {
	t.Helper()
	co := models.Company{Name: "Post Co", IsActive: true}
	db.Create(&co)

	clearing := models.Account{CompanyID: co.ID, Code: "1500", Name: "Clearing", RootAccountType: models.RootAsset, DetailAccountType: "other_current_asset", IsActive: true}
	db.Create(&clearing)
	fee := models.Account{CompanyID: co.ID, Code: "6500", Name: "Fees", RootAccountType: models.RootExpense, DetailAccountType: "operating_expense", IsActive: true}
	db.Create(&fee)
	refund := models.Account{CompanyID: co.ID, Code: "6600", Name: "Refunds", RootAccountType: models.RootExpense, DetailAccountType: "operating_expense", IsActive: true}
	db.Create(&refund)
	shipExp := models.Account{CompanyID: co.ID, Code: "6700", Name: "Ship Exp", RootAccountType: models.RootExpense, DetailAccountType: "operating_expense", IsActive: true}
	db.Create(&shipExp)

	ch := models.SalesChannelAccount{CompanyID: co.ID, ChannelType: models.ChannelTypeAmazon, DisplayName: "AMZ", AuthStatus: models.ChannelAuthPending, IsActive: true}
	db.Create(&ch)

	// Save accounting mapping.
	SaveAccountingMapping(db, &models.ChannelAccountingMapping{
		CompanyID: co.ID, ChannelAccountID: ch.ID,
		ClearingAccountID: &clearing.ID, FeeExpenseAccountID: &fee.ID,
		RefundAccountID: &refund.ID, ShippingExpenseAccountID: &shipExp.ID,
	})

	return settlePostSetup{companyID: co.ID, channelAcctID: ch.ID, clearingID: clearing.ID, feeID: fee.ID, refundID: refund.ID, shippingExpID: shipExp.ID}
}

func createPostableSettlement(t *testing.T, db *gorm.DB, s settlePostSetup) uint {
	t.Helper()
	now := time.Now()
	settlement := models.ChannelSettlement{
		CompanyID: s.companyID, ChannelAccountID: s.channelAcctID,
		ExternalSettlementID: "SET-POST-1", SettlementDate: &now,
		RawPayload: datatypes.JSON("{}"),
	}
	lines := []models.ChannelSettlementLine{
		{LineType: models.SettlementLineSale, Description: "Product sales", Amount: decimal.NewFromInt(1000), RawPayload: datatypes.JSON("{}")},
		{LineType: models.SettlementLineFee, Description: "FBA fee", Amount: decimal.NewFromInt(150), RawPayload: datatypes.JSON("{}")},
		{LineType: models.SettlementLineRefund, Description: "Customer refund", Amount: decimal.NewFromInt(80), RawPayload: datatypes.JSON("{}")},
		{LineType: models.SettlementLinePayout, Description: "Payout", Amount: decimal.NewFromInt(770), RawPayload: datatypes.JSON("{}")},
	}
	CreateSettlementWithLines(db, &settlement, lines)
	return settlement.ID
}

// ── Validation tests ─────────────────────────────────────────────────────────

func TestValidateSettlementPostable_OK(t *testing.T) {
	db := testSettlementPostingDB(t)
	s := setupSettlePost(t, db)
	sid := createPostableSettlement(t, db, s)

	err := ValidateSettlementPostable(db, s.companyID, sid)
	if err != nil {
		t.Fatalf("Expected postable, got: %v", err)
	}
}

func TestValidateSettlementPostable_AlreadyPosted(t *testing.T) {
	db := testSettlementPostingDB(t)
	s := setupSettlePost(t, db)
	sid := createPostableSettlement(t, db, s)

	// Post once.
	PostSettlementToJournalEntry(db, s.companyID, sid, "test")

	// Validate again.
	err := ValidateSettlementPostable(db, s.companyID, sid)
	if err == nil {
		t.Fatal("Expected already-posted error")
	}
}

func TestValidateSettlementPostable_AutoMappedAccountSuffices(t *testing.T) {
	db := testSettlementPostingDB(t)
	s := setupSettlePost(t, db)

	// Create settlement with a fee line that has no pre-assigned mapped_account.
	// The accounting mapping exists so auto-assignment should make it postable.
	now := time.Now()
	settlement := models.ChannelSettlement{
		CompanyID: s.companyID, ChannelAccountID: s.channelAcctID,
		SettlementDate: &now, RawPayload: datatypes.JSON("{}"),
	}
	lines := []models.ChannelSettlementLine{
		{LineType: models.SettlementLineFee, Description: "Fee", Amount: decimal.NewFromInt(50), RawPayload: datatypes.JSON("{}")},
	}
	CreateSettlementWithLines(db, &settlement, lines)

	err := ValidateSettlementPostable(db, s.companyID, settlement.ID)
	if err != nil {
		t.Fatalf("Should be postable (auto-mapped): %v", err)
	}
}

func TestPostSettlement_NegativeAdjustment_CorrectDirection(t *testing.T) {
	db := testSettlementPostingDB(t)
	s := setupSettlePost(t, db)

	// Create settlement with a negative adjustment only.
	now := time.Now()
	settlement := models.ChannelSettlement{
		CompanyID: s.companyID, ChannelAccountID: s.channelAcctID,
		SettlementDate: &now, RawPayload: datatypes.JSON("{}"),
	}
	lines := []models.ChannelSettlementLine{
		{LineType: models.SettlementLineAdjustment, Description: "Chargeback reversal",
			Amount: decimal.NewFromInt(-200), RawPayload: datatypes.JSON("{}")},
	}
	CreateSettlementWithLines(db, &settlement, lines)

	je, err := PostSettlementToJournalEntry(db, s.companyID, settlement.ID, "test")
	if err != nil {
		t.Fatalf("Post failed: %v", err)
	}

	// Negative adjustment: Dr Clearing, Cr MappedAccount (clearing acts as the mapped default).
	var jeLines []models.JournalLine
	db.Where("journal_entry_id = ?", je.ID).Find(&jeLines)
	if len(jeLines) != 2 {
		t.Fatalf("Expected 2 JE lines, got %d", len(jeLines))
	}

	// Clearing should have a debit of 200.
	foundClearingDebit := false
	for _, jl := range jeLines {
		if jl.AccountID == s.clearingID && jl.Debit.Equal(decimal.NewFromInt(200)) {
			foundClearingDebit = true
		}
	}
	if !foundClearingDebit {
		t.Error("Expected Dr Clearing 200 for negative adjustment")
	}
}

func TestValidateSettlementPostable_NoClearingAccount(t *testing.T) {
	db := testSettlementPostingDB(t)
	co := models.Company{Name: "NoClear", IsActive: true}
	db.Create(&co)
	ch := models.SalesChannelAccount{CompanyID: co.ID, ChannelType: models.ChannelTypeShopify, DisplayName: "Shop", AuthStatus: models.ChannelAuthPending, IsActive: true}
	db.Create(&ch)
	feeAcct := models.Account{CompanyID: co.ID, Code: "6500", Name: "Fee", RootAccountType: models.RootExpense, DetailAccountType: "operating_expense", IsActive: true}
	db.Create(&feeAcct)

	// No accounting mapping → no clearing account.
	settlement := models.ChannelSettlement{CompanyID: co.ID, ChannelAccountID: ch.ID, RawPayload: datatypes.JSON("{}")}
	lines := []models.ChannelSettlementLine{
		{LineType: models.SettlementLineFee, Amount: decimal.NewFromInt(10), MappedAccountID: &feeAcct.ID, RawPayload: datatypes.JSON("{}")},
	}
	CreateSettlementWithLines(db, &settlement, lines)

	err := ValidateSettlementPostable(db, co.ID, settlement.ID)
	if err == nil {
		t.Fatal("Expected no-clearing-account error")
	}
}

// ── Posting tests ────────────────────────────────────────────────────────────

func TestPostSettlement_CreatesJE(t *testing.T) {
	db := testSettlementPostingDB(t)
	s := setupSettlePost(t, db)
	sid := createPostableSettlement(t, db, s)

	je, err := PostSettlementToJournalEntry(db, s.companyID, sid, "test")
	if err != nil {
		t.Fatalf("Posting failed: %v", err)
	}
	if je.ID == 0 {
		t.Fatal("JE not created")
	}
	if je.SourceType != models.LedgerSourceSettlement {
		t.Errorf("Expected source_type settlement, got %s", je.SourceType)
	}

	// Verify settlement marked posted.
	updated, _ := GetSettlement(db, s.companyID, sid)
	if updated.PostedJournalEntryID == nil || *updated.PostedJournalEntryID != je.ID {
		t.Error("Settlement not marked posted")
	}

	// Verify JE lines: fee (Dr fee, Cr clearing) + refund (Dr refund, Cr clearing).
	// sale and payout lines should be skipped.
	var jeLines []models.JournalLine
	db.Where("journal_entry_id = ?", je.ID).Find(&jeLines)
	// 2 postable lines × 2 sides = 4 JE lines.
	if len(jeLines) != 4 {
		t.Fatalf("Expected 4 JE lines (fee+refund, each Dr+Cr), got %d", len(jeLines))
	}

	// Verify fee debit.
	var feeDebit decimal.Decimal
	for _, jl := range jeLines {
		if jl.AccountID == s.feeID && jl.Debit.IsPositive() {
			feeDebit = jl.Debit
		}
	}
	if !feeDebit.Equal(decimal.NewFromInt(150)) {
		t.Errorf("Fee debit expected 150, got %s", feeDebit)
	}

	// Verify clearing credit total = fee + refund = 150 + 80 = 230.
	var clearingCredit decimal.Decimal
	for _, jl := range jeLines {
		if jl.AccountID == s.clearingID {
			clearingCredit = clearingCredit.Add(jl.Credit)
		}
	}
	if !clearingCredit.Equal(decimal.NewFromInt(230)) {
		t.Errorf("Clearing credit expected 230, got %s", clearingCredit)
	}
}

func TestPostSettlement_DoublePost_Blocked(t *testing.T) {
	db := testSettlementPostingDB(t)
	s := setupSettlePost(t, db)
	sid := createPostableSettlement(t, db, s)

	PostSettlementToJournalEntry(db, s.companyID, sid, "test")

	_, err := PostSettlementToJournalEntry(db, s.companyID, sid, "test")
	if err == nil {
		t.Fatal("Expected double-post error")
	}
}

func TestPostSettlement_CrossCompany_Blocked(t *testing.T) {
	db := testSettlementPostingDB(t)
	s := setupSettlePost(t, db)
	sid := createPostableSettlement(t, db, s)

	otherCo := models.Company{Name: "Other", IsActive: true}
	db.Create(&otherCo)

	err := ValidateSettlementPostable(db, otherCo.ID, sid)
	if err == nil {
		t.Fatal("Expected cross-company block")
	}
}

// ── Totals auto-recompute test ───────────────────────────────────────────────

func TestSettlement_TotalsAutoRecomputed(t *testing.T) {
	db := testSettlementPostingDB(t)
	s := setupSettlePost(t, db)

	settlement := models.ChannelSettlement{
		CompanyID: s.companyID, ChannelAccountID: s.channelAcctID,
		// Deliberately wrong header totals — should be recomputed.
		GrossAmount: decimal.NewFromInt(999),
		FeeAmount:   decimal.NewFromInt(999),
		NetAmount:   decimal.NewFromInt(999),
		RawPayload:  datatypes.JSON("{}"),
	}
	lines := []models.ChannelSettlementLine{
		{LineType: models.SettlementLineSale, Amount: decimal.NewFromInt(500), RawPayload: datatypes.JSON("{}")},
		{LineType: models.SettlementLineFee, Amount: decimal.NewFromInt(-75), RawPayload: datatypes.JSON("{}")},
	}
	CreateSettlementWithLines(db, &settlement, lines)

	// Reload and check.
	var reloaded models.ChannelSettlement
	db.First(&reloaded, settlement.ID)
	if !reloaded.GrossAmount.Equal(decimal.NewFromInt(500)) {
		t.Errorf("Gross expected 500, got %s", reloaded.GrossAmount)
	}
	if !reloaded.FeeAmount.Equal(decimal.NewFromInt(75)) {
		t.Errorf("Fee expected 75, got %s", reloaded.FeeAmount)
	}
	if !reloaded.NetAmount.Equal(decimal.NewFromInt(425)) {
		t.Errorf("Net expected 425, got %s", reloaded.NetAmount)
	}
}
