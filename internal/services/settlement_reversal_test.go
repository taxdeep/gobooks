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

func testReversalDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:rev_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Account{},
		&models.Customer{},
		&models.SalesChannelAccount{},
		&models.ChannelAccountingMapping{},
		&models.ChannelOrder{},
		&models.ChannelOrderLine{},
		&models.ChannelSettlement{},
		&models.ChannelSettlementLine{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.AuditLog{},
		&models.ProductService{},
		&models.Reconciliation{},
	); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

type revSetup struct {
	companyID  uint
	channelID  uint
	clearingID uint
	feeID      uint
	bankID     uint
	arID       uint
	revID      uint
	customerID uint
}

func setupReversal(t *testing.T, db *gorm.DB) revSetup {
	t.Helper()
	co := models.Company{Name: "Rev Co", IsActive: true, BaseCurrencyCode: "CAD"}
	db.Create(&co)
	cust := models.Customer{CompanyID: co.ID, Name: "Cust", AddrStreet1: "1 St"}
	db.Create(&cust)

	clearing := models.Account{CompanyID: co.ID, Code: "1500", Name: "Clearing", RootAccountType: models.RootAsset, DetailAccountType: "other_current_asset", IsActive: true}
	db.Create(&clearing)
	fee := models.Account{CompanyID: co.ID, Code: "6500", Name: "Fees", RootAccountType: models.RootExpense, DetailAccountType: "operating_expense", IsActive: true}
	db.Create(&fee)
	bank := models.Account{CompanyID: co.ID, Code: "1000", Name: "Bank", RootAccountType: models.RootAsset, DetailAccountType: models.DetailBank, IsActive: true}
	db.Create(&bank)
	ar := models.Account{CompanyID: co.ID, Code: "1100", Name: "AR", RootAccountType: models.RootAsset, DetailAccountType: models.DetailAccountsReceivable, IsActive: true}
	db.Create(&ar)
	rev := models.Account{CompanyID: co.ID, Code: "4000", Name: "Revenue", RootAccountType: models.RootRevenue, DetailAccountType: "sales_revenue", IsActive: true}
	db.Create(&rev)

	ch := models.SalesChannelAccount{CompanyID: co.ID, ChannelType: models.ChannelTypeAmazon, DisplayName: "AMZ", AuthStatus: models.ChannelAuthPending, IsActive: true}
	db.Create(&ch)

	SaveAccountingMapping(db, &models.ChannelAccountingMapping{
		CompanyID: co.ID, ChannelAccountID: ch.ID,
		ClearingAccountID: &clearing.ID, FeeExpenseAccountID: &fee.ID,
	})

	return revSetup{companyID: co.ID, channelID: ch.ID, clearingID: clearing.ID, feeID: fee.ID, bankID: bank.ID, arID: ar.ID, revID: rev.ID, customerID: cust.ID}
}

func createPostedSettlement(t *testing.T, db *gorm.DB, s revSetup) uint {
	t.Helper()
	now := time.Now()
	settlement := models.ChannelSettlement{
		CompanyID: s.companyID, ChannelAccountID: s.channelID,
		ExternalSettlementID: fmt.Sprintf("SET-%d", time.Now().UnixNano()),
		SettlementDate: &now, RawPayload: datatypes.JSON("{}"),
	}
	lines := []models.ChannelSettlementLine{
		{LineType: models.SettlementLineSale, Amount: decimal.NewFromInt(1000), RawPayload: datatypes.JSON("{}")},
		{LineType: models.SettlementLineFee, Amount: decimal.NewFromInt(100), RawPayload: datatypes.JSON("{}")},
		{LineType: models.SettlementLinePayout, Amount: decimal.NewFromInt(900), RawPayload: datatypes.JSON("{}")},
	}
	CreateSettlementWithLines(db, &settlement, lines)

	// Post fee JE.
	PostSettlementToJournalEntry(db, s.companyID, settlement.ID, "test")
	return settlement.ID
}

// ── Channel clearing recognition tests ───────────────────────────────────────

func TestChannelOriginInvoice_UsesClearingAccount(t *testing.T) {
	db := testReversalDB(t)
	s := setupReversal(t, db)

	// Create channel order.
	order := models.ChannelOrder{CompanyID: s.companyID, ChannelAccountID: s.channelID, ExternalOrderID: "ORD-CLR", OrderStatus: "imported", RawPayload: datatypes.JSON("{}")}
	db.Create(&order)

	// Create item.
	item := models.ProductService{CompanyID: s.companyID, Name: "Widget", Type: models.ProductServiceTypeService, RevenueAccountID: s.revID, IsActive: true}
	item.ApplyTypeDefaults()
	db.Create(&item)

	// Create channel-origin invoice.
	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-CLR-1",
		CustomerID: s.customerID, InvoiceDate: time.Now(),
		ChannelOrderID: &order.ID,
		Status: models.InvoiceStatusDraft,
		Subtotal: decimal.NewFromInt(500), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(500), BalanceDue: decimal.NewFromInt(500),
		CustomerNameSnapshot: "Cust",
	}
	db.Create(&inv)
	db.Create(&models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &item.ID, Description: "Widget",
		Qty: decimal.NewFromInt(5), UnitPrice: decimal.NewFromInt(100),
		LineNet: decimal.NewFromInt(500), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(500),
	})

	// Post.
	err := PostInvoice(db, s.companyID, inv.ID, "test", nil)
	if err != nil {
		t.Fatalf("PostInvoice: %v", err)
	}

	// Verify debit-side uses clearing, not AR.
	var jeLines []models.JournalLine
	db.Joins("JOIN journal_entries ON journal_entries.id = journal_lines.journal_entry_id").
		Where("journal_entries.source_type = ? AND journal_entries.source_id = ?", "invoice", inv.ID).
		Find(&jeLines)

	foundClearing := false
	foundAR := false
	for _, jl := range jeLines {
		if jl.AccountID == s.clearingID && jl.Debit.IsPositive() {
			foundClearing = true
		}
		if jl.AccountID == s.arID && jl.Debit.IsPositive() {
			foundAR = true
		}
	}
	if !foundClearing {
		t.Error("Channel-origin invoice should debit clearing account")
	}
	if foundAR {
		t.Error("Channel-origin invoice should NOT debit AR")
	}
}

func TestNonChannelInvoice_StillUsesAR(t *testing.T) {
	db := testReversalDB(t)
	s := setupReversal(t, db)

	item := models.ProductService{CompanyID: s.companyID, Name: "Svc", Type: models.ProductServiceTypeService, RevenueAccountID: s.revID, IsActive: true}
	item.ApplyTypeDefaults()
	db.Create(&item)

	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-NORMAL-AR",
		CustomerID: s.customerID, InvoiceDate: time.Now(),
		Status: models.InvoiceStatusDraft,
		Subtotal: decimal.NewFromInt(300), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(300), BalanceDue: decimal.NewFromInt(300),
		CustomerNameSnapshot: "Cust",
	}
	db.Create(&inv)
	db.Create(&models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &item.ID, Description: "Svc",
		Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(300),
		LineNet: decimal.NewFromInt(300), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(300),
	})

	PostInvoice(db, s.companyID, inv.ID, "test", nil)

	var jeLines []models.JournalLine
	db.Joins("JOIN journal_entries ON journal_entries.id = journal_lines.journal_entry_id").
		Where("journal_entries.source_type = ? AND journal_entries.source_id = ?", "invoice", inv.ID).
		Find(&jeLines)

	foundAR := false
	for _, jl := range jeLines {
		if jl.AccountID == s.arID && jl.Debit.IsPositive() {
			foundAR = true
		}
	}
	if !foundAR {
		t.Error("Normal invoice should use AR account")
	}
}

func TestChannelOriginInvoice_MissingClearing_Blocked(t *testing.T) {
	db := testReversalDB(t)
	co := models.Company{Name: "NoClear Co", IsActive: true, BaseCurrencyCode: "CAD"}
	db.Create(&co)
	cust := models.Customer{CompanyID: co.ID, Name: "C", AddrStreet1: "1"}
	db.Create(&cust)
	rev := models.Account{CompanyID: co.ID, Code: "4000", Name: "Rev", RootAccountType: models.RootRevenue, DetailAccountType: "sales_revenue", IsActive: true}
	db.Create(&rev)
	item := models.ProductService{CompanyID: co.ID, Name: "X", Type: models.ProductServiceTypeService, RevenueAccountID: rev.ID, IsActive: true}
	item.ApplyTypeDefaults()
	db.Create(&item)

	// Channel with NO accounting mapping.
	ch := models.SalesChannelAccount{CompanyID: co.ID, ChannelType: models.ChannelTypeShopify, DisplayName: "Shop", AuthStatus: models.ChannelAuthPending, IsActive: true}
	db.Create(&ch)
	order := models.ChannelOrder{CompanyID: co.ID, ChannelAccountID: ch.ID, ExternalOrderID: "ORD-NC", OrderStatus: "imported", RawPayload: datatypes.JSON("{}")}
	db.Create(&order)

	inv := models.Invoice{
		CompanyID: co.ID, InvoiceNumber: "INV-NC", CustomerID: cust.ID,
		InvoiceDate: time.Now(), ChannelOrderID: &order.ID,
		Status: models.InvoiceStatusDraft,
		Subtotal: decimal.NewFromInt(100), Amount: decimal.NewFromInt(100), BalanceDue: decimal.NewFromInt(100),
		CustomerNameSnapshot: "C",
	}
	db.Create(&inv)
	db.Create(&models.InvoiceLine{
		CompanyID: co.ID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &item.ID, Description: "X",
		Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(100),
		LineNet: decimal.NewFromInt(100), LineTotal: decimal.NewFromInt(100),
	})

	err := PostInvoice(db, co.ID, inv.ID, "test", nil)
	if err == nil {
		t.Fatal("Expected clearing account error")
	}
}

// ── Settlement fee reversal tests ────────────────────────────────────────────

func TestReverseSettlementFee_OK(t *testing.T) {
	db := testReversalDB(t)
	s := setupReversal(t, db)
	sid := createPostedSettlement(t, db, s)

	revJEID, err := ReverseSettlementFeePosting(db, s.companyID, sid, "test")
	if err != nil {
		t.Fatalf("Reverse failed: %v", err)
	}
	if revJEID == 0 {
		t.Fatal("Reversal JE not created")
	}

	// Verify settlement marked.
	updated, _ := GetSettlement(db, s.companyID, sid)
	if updated.PostedReversalJEID == nil || *updated.PostedReversalJEID != revJEID {
		t.Error("Settlement not marked with reversal JE")
	}
}

func TestReverseSettlementFee_DoubleReverse_Blocked(t *testing.T) {
	db := testReversalDB(t)
	s := setupReversal(t, db)
	sid := createPostedSettlement(t, db, s)

	ReverseSettlementFeePosting(db, s.companyID, sid, "test")

	_, err := ReverseSettlementFeePosting(db, s.companyID, sid, "test")
	if err == nil {
		t.Fatal("Expected double-reverse error")
	}
}

func TestReverseSettlementFee_CrossCompany_Blocked(t *testing.T) {
	db := testReversalDB(t)
	s := setupReversal(t, db)
	sid := createPostedSettlement(t, db, s)

	otherCo := models.Company{Name: "Other", IsActive: true}
	db.Create(&otherCo)

	err := ValidateSettlementFeeReversible(db, otherCo.ID, sid)
	if err == nil {
		t.Fatal("Expected cross-company block")
	}
}

// ── Payout reversal tests ────────────────────────────────────────────────────

func TestReversePayoutRecording_OK(t *testing.T) {
	db := testReversalDB(t)
	s := setupReversal(t, db)
	sid := createPostedSettlement(t, db, s)

	// Record payout.
	RecordPayout(db, RecordPayoutInput{
		CompanyID: s.companyID, SettlementID: sid, BankAccountID: s.bankID, EntryDate: time.Now(),
	}, "test")

	revJEID, err := ReversePayoutRecording(db, s.companyID, sid, "test")
	if err != nil {
		t.Fatalf("Reverse payout failed: %v", err)
	}
	if revJEID == 0 {
		t.Fatal("Reversal JE not created")
	}

	updated, _ := GetSettlement(db, s.companyID, sid)
	if updated.PayoutReversalJEID == nil || *updated.PayoutReversalJEID != revJEID {
		t.Error("Settlement not marked with payout reversal JE")
	}
}

func TestReversePayoutRecording_DoubleReverse_Blocked(t *testing.T) {
	db := testReversalDB(t)
	s := setupReversal(t, db)
	sid := createPostedSettlement(t, db, s)

	RecordPayout(db, RecordPayoutInput{
		CompanyID: s.companyID, SettlementID: sid, BankAccountID: s.bankID, EntryDate: time.Now(),
	}, "test")

	ReversePayoutRecording(db, s.companyID, sid, "test")

	_, err := ReversePayoutRecording(db, s.companyID, sid, "test")
	if err == nil {
		t.Fatal("Expected double-reverse error")
	}
}

func TestReversePayoutRecording_Reconciled_Blocked(t *testing.T) {
	db := testReversalDB(t)
	s := setupReversal(t, db)
	sid := createPostedSettlement(t, db, s)

	result, _ := RecordPayout(db, RecordPayoutInput{
		CompanyID: s.companyID, SettlementID: sid, BankAccountID: s.bankID, EntryDate: time.Now(),
	}, "test")

	// Simulate bank line being reconciled.
	var bankLine models.JournalLine
	db.Where("journal_entry_id = ? AND account_id = ?", result.JournalEntryID, s.bankID).First(&bankLine)
	reconID := uint(999)
	db.Model(&bankLine).Update("reconciliation_id", reconID)

	err := ValidatePayoutReversible(db, s.companyID, sid)
	if err == nil {
		t.Fatal("Expected reconciled payout block")
	}
}

func TestReversePayoutRecording_CrossCompany_Blocked(t *testing.T) {
	db := testReversalDB(t)
	s := setupReversal(t, db)
	sid := createPostedSettlement(t, db, s)

	RecordPayout(db, RecordPayoutInput{
		CompanyID: s.companyID, SettlementID: sid, BankAccountID: s.bankID, EntryDate: time.Now(),
	}, "test")

	otherCo := models.Company{Name: "Other", IsActive: true}
	db.Create(&otherCo)

	err := ValidatePayoutReversible(db, otherCo.ID, sid)
	if err == nil {
		t.Fatal("Expected cross-company block")
	}
}

// ── Integrity: original JE untouched ─────────────────────────────────────────

func TestReversal_OriginalJE_StatusReversed(t *testing.T) {
	db := testReversalDB(t)
	s := setupReversal(t, db)
	sid := createPostedSettlement(t, db, s)

	settlement, _ := GetSettlement(db, s.companyID, sid)
	origJEID := *settlement.PostedJournalEntryID

	ReverseSettlementFeePosting(db, s.companyID, sid, "test")

	var origJE models.JournalEntry
	db.First(&origJE, origJEID)
	if origJE.Status != models.JournalEntryStatusReversed {
		t.Errorf("Original JE expected reversed, got %s", origJE.Status)
	}
}
