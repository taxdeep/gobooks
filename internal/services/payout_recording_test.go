// 遵循project_guide.md
package services

import (
	"errors"
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

func testPayoutDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:payout_%s?mode=memory&cache=shared", t.Name())
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

type payoutSetup struct {
	companyID  uint
	channelID  uint
	clearingID uint
	bankID     uint
}

func setupPayout(t *testing.T, db *gorm.DB) payoutSetup {
	t.Helper()
	co := models.Company{Name: "Payout Co", IsActive: true}
	db.Create(&co)

	clearing := models.Account{CompanyID: co.ID, Code: "1500", Name: "Clearing", RootAccountType: models.RootAsset, DetailAccountType: "other_current_asset", IsActive: true}
	db.Create(&clearing)
	bank := models.Account{CompanyID: co.ID, Code: "1000", Name: "Bank", RootAccountType: models.RootAsset, DetailAccountType: models.DetailBank, IsActive: true}
	db.Create(&bank)
	fee := models.Account{CompanyID: co.ID, Code: "6500", Name: "Fees", RootAccountType: models.RootExpense, DetailAccountType: "operating_expense", IsActive: true}
	db.Create(&fee)

	ch := models.SalesChannelAccount{CompanyID: co.ID, ChannelType: models.ChannelTypeAmazon, DisplayName: "AMZ", AuthStatus: models.ChannelAuthPending, IsActive: true}
	db.Create(&ch)

	SaveAccountingMapping(db, &models.ChannelAccountingMapping{
		CompanyID: co.ID, ChannelAccountID: ch.ID,
		ClearingAccountID: &clearing.ID, FeeExpenseAccountID: &fee.ID,
	})

	return payoutSetup{companyID: co.ID, channelID: ch.ID, clearingID: clearing.ID, bankID: bank.ID}
}

func createSettlementWithPayout(t *testing.T, db *gorm.DB, s payoutSetup) uint {
	t.Helper()
	now := time.Now()
	settlement := models.ChannelSettlement{
		CompanyID: s.companyID, ChannelAccountID: s.channelID,
		ExternalSettlementID: "SET-PAY-1", SettlementDate: &now,
		RawPayload: datatypes.JSON("{}"),
	}
	lines := []models.ChannelSettlementLine{
		{LineType: models.SettlementLineSale, Amount: decimal.NewFromInt(1000), RawPayload: datatypes.JSON("{}")},
		{LineType: models.SettlementLineFee, Amount: decimal.NewFromInt(150), RawPayload: datatypes.JSON("{}")},
		{LineType: models.SettlementLinePayout, Description: "Disbursement", Amount: decimal.NewFromInt(850), RawPayload: datatypes.JSON("{}")},
	}
	CreateSettlementWithLines(db, &settlement, lines)
	return settlement.ID
}

// ── Validation tests ─────────────────────────────────────────────────────────

func TestValidatePayoutRecordable_OK(t *testing.T) {
	db := testPayoutDB(t)
	s := setupPayout(t, db)
	sid := createSettlementWithPayout(t, db, s)

	err := ValidatePayoutRecordable(db, s.companyID, sid)
	if err != nil {
		t.Fatalf("Expected recordable, got: %v", err)
	}
}

func TestValidatePayoutRecordable_AlreadyRecorded(t *testing.T) {
	db := testPayoutDB(t)
	s := setupPayout(t, db)
	sid := createSettlementWithPayout(t, db, s)

	RecordPayout(db, RecordPayoutInput{
		CompanyID: s.companyID, SettlementID: sid, BankAccountID: s.bankID, EntryDate: time.Now(),
	}, "test")

	err := ValidatePayoutRecordable(db, s.companyID, sid)
	if err == nil {
		t.Fatal("Expected already-recorded error")
	}
}

func TestValidatePayoutRecordable_NoPayout(t *testing.T) {
	db := testPayoutDB(t)
	s := setupPayout(t, db)

	// Settlement with no payout line.
	settlement := models.ChannelSettlement{
		CompanyID: s.companyID, ChannelAccountID: s.channelID,
		RawPayload: datatypes.JSON("{}"),
	}
	lines := []models.ChannelSettlementLine{
		{LineType: models.SettlementLineFee, Amount: decimal.NewFromInt(50), RawPayload: datatypes.JSON("{}")},
	}
	CreateSettlementWithLines(db, &settlement, lines)

	err := ValidatePayoutRecordable(db, s.companyID, settlement.ID)
	if err == nil {
		t.Fatal("Expected no-payout error")
	}
}

func TestValidatePayoutRecordable_PayoutMustMatchNet(t *testing.T) {
	db := testPayoutDB(t)
	s := setupPayout(t, db)

	now := time.Now()
	settlement := models.ChannelSettlement{
		CompanyID: s.companyID, ChannelAccountID: s.channelID,
		ExternalSettlementID: "SET-PAY-MISMATCH", SettlementDate: &now,
		RawPayload: datatypes.JSON("{}"),
	}
	lines := []models.ChannelSettlementLine{
		{LineType: models.SettlementLineSale, Amount: decimal.NewFromInt(1000), RawPayload: datatypes.JSON("{}")},
		{LineType: models.SettlementLineFee, Amount: decimal.NewFromInt(150), RawPayload: datatypes.JSON("{}")},
		{LineType: models.SettlementLinePayout, Amount: decimal.NewFromInt(800), RawPayload: datatypes.JSON("{}")},
	}
	if err := CreateSettlementWithLines(db, &settlement, lines); err != nil {
		t.Fatalf("CreateSettlementWithLines: %v", err)
	}

	err := ValidatePayoutRecordable(db, s.companyID, settlement.ID)
	if err == nil {
		t.Fatal("expected payout/net mismatch error")
	}
	if !errors.Is(err, ErrPayoutNetMismatch) {
		t.Fatalf("expected ErrPayoutNetMismatch, got %v", err)
	}
}

func TestValidatePayoutRecordable_NonPositiveNetBlocked(t *testing.T) {
	db := testPayoutDB(t)
	s := setupPayout(t, db)

	now := time.Now()
	settlement := models.ChannelSettlement{
		CompanyID: s.companyID, ChannelAccountID: s.channelID,
		ExternalSettlementID: "SET-PAY-ZERO", SettlementDate: &now,
		RawPayload: datatypes.JSON("{}"),
	}
	lines := []models.ChannelSettlementLine{
		{LineType: models.SettlementLineSale, Amount: decimal.NewFromInt(100), RawPayload: datatypes.JSON("{}")},
		{LineType: models.SettlementLineRefund, Amount: decimal.NewFromInt(100), RawPayload: datatypes.JSON("{}")},
		{LineType: models.SettlementLinePayout, Amount: decimal.NewFromInt(0), RawPayload: datatypes.JSON("{}")},
	}
	if err := CreateSettlementWithLines(db, &settlement, lines); err != nil {
		t.Fatalf("CreateSettlementWithLines: %v", err)
	}

	err := ValidatePayoutRecordable(db, s.companyID, settlement.ID)
	if err == nil {
		t.Fatal("expected non-positive payout/net block")
	}
	if !errors.Is(err, ErrPayoutAmountInvalid) {
		t.Fatalf("expected ErrPayoutAmountInvalid, got %v", err)
	}
}

// ── Recording tests ──────────────────────────────────────────────────────────

func TestRecordPayout_CreatesJE(t *testing.T) {
	db := testPayoutDB(t)
	s := setupPayout(t, db)
	sid := createSettlementWithPayout(t, db, s)

	result, err := RecordPayout(db, RecordPayoutInput{
		CompanyID: s.companyID, SettlementID: sid, BankAccountID: s.bankID, EntryDate: time.Now(),
	}, "test")
	if err != nil {
		t.Fatalf("RecordPayout failed: %v", err)
	}
	if result.JournalEntryID == 0 {
		t.Fatal("JE not created")
	}
	if !result.PayoutAmount.Equal(decimal.NewFromInt(850)) {
		t.Errorf("Payout amount expected 850, got %s", result.PayoutAmount)
	}

	// Verify JE lines: Dr Bank 850, Cr Clearing 850.
	var jeLines []models.JournalLine
	db.Where("journal_entry_id = ?", result.JournalEntryID).Find(&jeLines)
	if len(jeLines) != 2 {
		t.Fatalf("Expected 2 JE lines, got %d", len(jeLines))
	}

	var bankDebit, clearingCredit decimal.Decimal
	for _, jl := range jeLines {
		if jl.AccountID == s.bankID {
			bankDebit = jl.Debit
		}
		if jl.AccountID == s.clearingID {
			clearingCredit = jl.Credit
		}
	}
	if !bankDebit.Equal(decimal.NewFromInt(850)) {
		t.Errorf("Bank debit expected 850, got %s", bankDebit)
	}
	if !clearingCredit.Equal(decimal.NewFromInt(850)) {
		t.Errorf("Clearing credit expected 850, got %s", clearingCredit)
	}

	// Verify settlement marked.
	updated, _ := GetSettlement(db, s.companyID, sid)
	if updated.PayoutJournalEntryID == nil || *updated.PayoutJournalEntryID != result.JournalEntryID {
		t.Error("Settlement not marked with payout JE")
	}
}

func TestRecordPayout_DoublePayout_Blocked(t *testing.T) {
	db := testPayoutDB(t)
	s := setupPayout(t, db)
	sid := createSettlementWithPayout(t, db, s)

	RecordPayout(db, RecordPayoutInput{
		CompanyID: s.companyID, SettlementID: sid, BankAccountID: s.bankID, EntryDate: time.Now(),
	}, "test")

	_, err := RecordPayout(db, RecordPayoutInput{
		CompanyID: s.companyID, SettlementID: sid, BankAccountID: s.bankID, EntryDate: time.Now(),
	}, "test")
	if err == nil {
		t.Fatal("Expected double-payout error")
	}
}

func TestRecordPayout_NoBankAccount_Blocked(t *testing.T) {
	db := testPayoutDB(t)
	s := setupPayout(t, db)
	sid := createSettlementWithPayout(t, db, s)

	_, err := RecordPayout(db, RecordPayoutInput{
		CompanyID: s.companyID, SettlementID: sid, BankAccountID: 0, EntryDate: time.Now(),
	}, "test")
	if err == nil {
		t.Fatal("Expected no-bank-account error")
	}
}

func TestRecordPayout_CrossCompany_Blocked(t *testing.T) {
	db := testPayoutDB(t)
	s := setupPayout(t, db)
	sid := createSettlementWithPayout(t, db, s)

	otherCo := models.Company{Name: "Other", IsActive: true}
	db.Create(&otherCo)

	err := ValidatePayoutRecordable(db, otherCo.ID, sid)
	if err == nil {
		t.Fatal("Expected cross-company block")
	}
}
