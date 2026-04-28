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

func testClearingReportDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:clrrpt_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Account{},
		&models.Customer{},
		&models.TaxCode{},
		&models.ProductService{},
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
	); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

type clrSetup struct {
	companyID  uint
	channelID  uint
	clearingID uint
	feeID      uint
	bankID     uint
	revID      uint
	customerID uint
	itemID     uint
}

func setupClearingReport(t *testing.T, db *gorm.DB) clrSetup {
	t.Helper()
	co := models.Company{Name: "Clr Co", IsActive: true, BaseCurrencyCode: "CAD"}
	db.Create(&co)
	cust := models.Customer{CompanyID: co.ID, Name: "C", AddrStreet1: "1 St"}
	db.Create(&cust)

	clearing := models.Account{CompanyID: co.ID, Code: "1500", Name: "Clearing", RootAccountType: models.RootAsset, DetailAccountType: "other_current_asset", IsActive: true}
	db.Create(&clearing)
	fee := models.Account{CompanyID: co.ID, Code: "6500", Name: "Fees", RootAccountType: models.RootExpense, DetailAccountType: "operating_expense", IsActive: true}
	db.Create(&fee)
	bank := models.Account{CompanyID: co.ID, Code: "1000", Name: "Bank", RootAccountType: models.RootAsset, DetailAccountType: models.DetailBank, IsActive: true}
	db.Create(&bank)
	rev := models.Account{CompanyID: co.ID, Code: "4000", Name: "Revenue", RootAccountType: models.RootRevenue, DetailAccountType: "sales_revenue", IsActive: true}
	db.Create(&rev)

	item := models.ProductService{CompanyID: co.ID, Name: "Svc", Type: models.ProductServiceTypeService, RevenueAccountID: rev.ID, IsActive: true}
	item.ApplyTypeDefaults()
	db.Create(&item)

	ch := models.SalesChannelAccount{CompanyID: co.ID, ChannelType: models.ChannelTypeAmazon, DisplayName: "AMZ US", AuthStatus: models.ChannelAuthPending, IsActive: true}
	db.Create(&ch)

	SaveAccountingMapping(db, &models.ChannelAccountingMapping{
		CompanyID: co.ID, ChannelAccountID: ch.ID,
		ClearingAccountID: &clearing.ID, FeeExpenseAccountID: &fee.ID,
	})

	return clrSetup{companyID: co.ID, channelID: ch.ID, clearingID: clearing.ID, feeID: fee.ID, bankID: bank.ID, revID: rev.ID, customerID: cust.ID, itemID: item.ID}
}

func TestClearingReport_FullCycle_BalanceZero(t *testing.T) {
	db := testClearingReportDB(t)
	s := setupClearingReport(t, db)

	// 1. Channel-origin invoice: sale $1000.
	order := models.ChannelOrder{CompanyID: s.companyID, ChannelAccountID: s.channelID, ExternalOrderID: "ORD-CLR-RPT", OrderStatus: "imported", RawPayload: datatypes.JSON("{}")}
	db.Create(&order)

	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-CLR-RPT",
		CustomerID: s.customerID, InvoiceDate: time.Now(),
		ChannelOrderID: &order.ID,
		Status: models.InvoiceStatusDraft,
		Subtotal: decimal.NewFromInt(1000), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(1000), BalanceDue: decimal.NewFromInt(1000),
		CustomerNameSnapshot: "C",
	}
	db.Create(&inv)
	db.Create(&models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &s.itemID, Description: "Svc",
		Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(1000),
		LineNet: decimal.NewFromInt(1000), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(1000),
	})
	PostInvoice(db, s.companyID, inv.ID, "test", nil)

	// 2. Settlement: fee $150, payout $850.
	now := time.Now()
	settlement := models.ChannelSettlement{
		CompanyID: s.companyID, ChannelAccountID: s.channelID,
		ExternalSettlementID: "SET-CLR-RPT", SettlementDate: &now,
		RawPayload: datatypes.JSON("{}"),
	}
	sLines := []models.ChannelSettlementLine{
		{LineType: models.SettlementLineSale, Amount: decimal.NewFromInt(1000), RawPayload: datatypes.JSON("{}")},
		{LineType: models.SettlementLineFee, Amount: decimal.NewFromInt(150), RawPayload: datatypes.JSON("{}")},
		{LineType: models.SettlementLinePayout, Amount: decimal.NewFromInt(850), RawPayload: datatypes.JSON("{}")},
	}
	CreateSettlementWithLines(db, &settlement, sLines)
	PostSettlementToJournalEntry(db, s.companyID, settlement.ID, "test")

	// 3. Record payout.
	RecordPayout(db, RecordPayoutInput{
		CompanyID: s.companyID, SettlementID: settlement.ID, BankAccountID: s.bankID, EntryDate: time.Now(),
	}, "test")

	// 4. Check clearing summary.
	summary, err := GetClearingSummary(db, s.companyID, s.channelID)
	if err != nil || summary == nil {
		t.Fatalf("GetClearingSummary: %v", err)
	}

	// Balance should be zero: +1000 (sale) -150 (fee) -850 (payout) = 0.
	if !summary.CurrentBalance.IsZero() {
		t.Errorf("Expected zero clearing balance, got %s", summary.CurrentBalance)
	}
}

func TestClearingReport_UnsettledBalance(t *testing.T) {
	db := testClearingReportDB(t)
	s := setupClearingReport(t, db)

	// Only invoice posted — no settlement or payout.
	order := models.ChannelOrder{CompanyID: s.companyID, ChannelAccountID: s.channelID, ExternalOrderID: "ORD-UNS", OrderStatus: "imported", RawPayload: datatypes.JSON("{}")}
	db.Create(&order)

	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-UNS",
		CustomerID: s.customerID, InvoiceDate: time.Now(),
		ChannelOrderID: &order.ID,
		Status: models.InvoiceStatusDraft,
		Subtotal: decimal.NewFromInt(500), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(500), BalanceDue: decimal.NewFromInt(500),
		CustomerNameSnapshot: "C",
	}
	db.Create(&inv)
	db.Create(&models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &s.itemID, Description: "Svc",
		Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(500),
		LineNet: decimal.NewFromInt(500), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(500),
	})
	PostInvoice(db, s.companyID, inv.ID, "test", nil)

	summary, _ := GetClearingSummary(db, s.companyID, s.channelID)
	if summary == nil {
		t.Fatal("Expected summary")
	}

	// Balance = 500 (unsettled).
	if !summary.CurrentBalance.Equal(decimal.NewFromInt(500)) {
		t.Errorf("Expected clearing balance 500, got %s", summary.CurrentBalance)
	}
}

func TestClearingMovements_RunningBalance(t *testing.T) {
	db := testClearingReportDB(t)
	s := setupClearingReport(t, db)

	// Post an invoice.
	order := models.ChannelOrder{CompanyID: s.companyID, ChannelAccountID: s.channelID, ExternalOrderID: "ORD-MOV", OrderStatus: "imported", RawPayload: datatypes.JSON("{}")}
	db.Create(&order)

	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-MOV",
		CustomerID: s.customerID, InvoiceDate: time.Now(),
		ChannelOrderID: &order.ID,
		Status: models.InvoiceStatusDraft,
		Subtotal: decimal.NewFromInt(200), TaxTotal: decimal.Zero,
		Amount: decimal.NewFromInt(200), BalanceDue: decimal.NewFromInt(200),
		CustomerNameSnapshot: "C",
	}
	db.Create(&inv)
	db.Create(&models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &s.itemID, Description: "Svc",
		Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(200),
		LineNet: decimal.NewFromInt(200), LineTax: decimal.Zero, LineTotal: decimal.NewFromInt(200),
	})
	PostInvoice(db, s.companyID, inv.ID, "test", nil)

	movements, err := ListClearingMovements(db, s.companyID, s.channelID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(movements) == 0 {
		t.Fatal("Expected at least one movement")
	}

	// First movement should be the invoice debit with running balance = 200.
	found := false
	for _, m := range movements {
		if m.Debit.Equal(decimal.NewFromInt(200)) {
			found = true
			if !m.RunningBalance.Equal(decimal.NewFromInt(200)) {
				t.Errorf("Running balance expected 200, got %s", m.RunningBalance)
			}
		}
	}
	if !found {
		t.Error("Expected clearing debit of 200 from invoice")
	}
}

func TestClearingReport_CompanyIsolation(t *testing.T) {
	db := testClearingReportDB(t)
	s := setupClearingReport(t, db)

	otherCo := models.Company{Name: "Other", IsActive: true}
	db.Create(&otherCo)

	summary, _ := GetClearingSummary(db, otherCo.ID, s.channelID)
	if summary != nil {
		t.Error("Other company should not see this channel's clearing data")
	}
}

func TestClearingReport_SharedClearingAccountBlocked(t *testing.T) {
	db := testClearingReportDB(t)
	s := setupClearingReport(t, db)

	otherChannel := models.SalesChannelAccount{
		CompanyID: s.companyID, ChannelType: models.ChannelTypeShopify,
		DisplayName: "Shopify Store", AuthStatus: models.ChannelAuthPending, IsActive: true,
	}
	if err := db.Create(&otherChannel).Error; err != nil {
		t.Fatal(err)
	}

	// Simulate legacy/shared configuration directly in the DB. New saves are
	// blocked in SaveAccountingMapping, but the report must still fail safely if
	// old shared mappings already exist.
	if err := db.Create(&models.ChannelAccountingMapping{
		CompanyID: s.companyID, ChannelAccountID: otherChannel.ID,
		ClearingAccountID: &s.clearingID, FeeExpenseAccountID: &s.feeID,
	}).Error; err != nil {
		t.Fatal(err)
	}

	summary, err := GetClearingSummary(db, s.companyID, s.channelID)
	if err == nil {
		t.Fatal("expected shared clearing account to block summary")
	}
	if !errors.Is(err, ErrSharedClearingAccount) {
		t.Fatalf("expected ErrSharedClearingAccount, got %v", err)
	}
	if summary != nil {
		t.Fatal("expected no summary when clearing account is shared")
	}

	movements, err := ListClearingMovements(db, s.companyID, s.channelID, 100)
	if err == nil {
		t.Fatal("expected shared clearing account to block movements")
	}
	if !errors.Is(err, ErrSharedClearingAccount) {
		t.Fatalf("expected ErrSharedClearingAccount, got %v", err)
	}
	if movements != nil {
		t.Fatal("expected no movements when clearing account is shared")
	}
}
