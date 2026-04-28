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

func testSettlementDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:settle_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Account{},
		&models.ProductService{},
		&models.SalesChannelAccount{},
		&models.ItemChannelMapping{},
		&models.ChannelOrder{},
		&models.ChannelOrderLine{},
		&models.ChannelSettlement{},
		&models.ChannelSettlementLine{},
		&models.ChannelAccountingMapping{},
	); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

type settleSetup struct {
	companyID      uint
	channelAcctID  uint
	clearingAcctID uint
	feeAcctID      uint
	refundAcctID   uint
}

func setupSettle(t *testing.T, db *gorm.DB) settleSetup {
	t.Helper()
	co := models.Company{Name: "Settle Co", IsActive: true}
	db.Create(&co)

	clearing := models.Account{CompanyID: co.ID, Code: "1500", Name: "Clearing", RootAccountType: models.RootAsset, DetailAccountType: "other_current_asset", IsActive: true}
	db.Create(&clearing)
	fee := models.Account{CompanyID: co.ID, Code: "6500", Name: "Marketplace Fees", RootAccountType: models.RootExpense, DetailAccountType: "operating_expense", IsActive: true}
	db.Create(&fee)
	refund := models.Account{CompanyID: co.ID, Code: "6600", Name: "Refunds", RootAccountType: models.RootExpense, DetailAccountType: "operating_expense", IsActive: true}
	db.Create(&refund)

	acct := models.SalesChannelAccount{CompanyID: co.ID, ChannelType: models.ChannelTypeAmazon, DisplayName: "AMZ US", AuthStatus: models.ChannelAuthPending, IsActive: true}
	db.Create(&acct)

	return settleSetup{companyID: co.ID, channelAcctID: acct.ID, clearingAcctID: clearing.ID, feeAcctID: fee.ID, refundAcctID: refund.ID}
}

// ── Accounting Mapping tests ─────────────────────────────────────────────────

func TestAccountingMapping_SaveAndLoad(t *testing.T) {
	db := testSettlementDB(t)
	s := setupSettle(t, db)

	m := models.ChannelAccountingMapping{
		CompanyID:           s.companyID,
		ChannelAccountID:    s.channelAcctID,
		ClearingAccountID:   &s.clearingAcctID,
		FeeExpenseAccountID: &s.feeAcctID,
		RefundAccountID:     &s.refundAcctID,
	}
	if err := SaveAccountingMapping(db, &m); err != nil {
		t.Fatal(err)
	}

	loaded, err := GetAccountingMapping(db, s.companyID, s.channelAcctID)
	if err != nil || loaded == nil {
		t.Fatal("Mapping not found after save")
	}
	if loaded.ClearingAccountID == nil || *loaded.ClearingAccountID != s.clearingAcctID {
		t.Error("Clearing account not saved correctly")
	}
}

func TestAccountingMapping_CompanyIsolation(t *testing.T) {
	db := testSettlementDB(t)
	s := setupSettle(t, db)

	m := models.ChannelAccountingMapping{CompanyID: s.companyID, ChannelAccountID: s.channelAcctID, ClearingAccountID: &s.clearingAcctID}
	SaveAccountingMapping(db, &m)

	otherCo := models.Company{Name: "Other", IsActive: true}
	db.Create(&otherCo)

	loaded, _ := GetAccountingMapping(db, otherCo.ID, s.channelAcctID)
	if loaded != nil {
		t.Error("Other company should not see mapping")
	}
}

func TestAccountingMapping_SharedClearingAccountBlocked(t *testing.T) {
	db := testSettlementDB(t)
	s := setupSettle(t, db)

	otherChannel := models.SalesChannelAccount{
		CompanyID: s.companyID, ChannelType: models.ChannelTypeShopify,
		DisplayName: "Shopify CA", AuthStatus: models.ChannelAuthPending, IsActive: true,
	}
	if err := db.Create(&otherChannel).Error; err != nil {
		t.Fatal(err)
	}

	first := models.ChannelAccountingMapping{
		CompanyID: s.companyID, ChannelAccountID: s.channelAcctID, ClearingAccountID: &s.clearingAcctID,
	}
	if err := SaveAccountingMapping(db, &first); err != nil {
		t.Fatalf("first save: %v", err)
	}

	second := models.ChannelAccountingMapping{
		CompanyID: s.companyID, ChannelAccountID: otherChannel.ID, ClearingAccountID: &s.clearingAcctID,
	}
	err := SaveAccountingMapping(db, &second)
	if err == nil {
		t.Fatal("expected shared clearing account to be blocked")
	}
	if !errors.Is(err, ErrSharedClearingAccount) {
		t.Fatalf("expected ErrSharedClearingAccount, got %v", err)
	}
}

// ── Settlement CRUD tests ────────────────────────────────────────────────────

func TestSettlement_CreateAndList(t *testing.T) {
	db := testSettlementDB(t)
	s := setupSettle(t, db)

	now := time.Now()
	settlement := models.ChannelSettlement{
		CompanyID: s.companyID, ChannelAccountID: s.channelAcctID,
		ExternalSettlementID: "SET-001", SettlementDate: &now,
		GrossAmount: decimal.NewFromInt(1000), FeeAmount: decimal.NewFromInt(150),
		NetAmount: decimal.NewFromInt(850), RawPayload: datatypes.JSON("{}"),
	}
	lines := []models.ChannelSettlementLine{
		{LineType: models.SettlementLineSale, Description: "Product sales", Amount: decimal.NewFromInt(1000), RawPayload: datatypes.JSON("{}")},
		{LineType: models.SettlementLineFee, Description: "FBA fee", Amount: decimal.NewFromInt(-150), RawPayload: datatypes.JSON("{}")},
	}

	if err := CreateSettlementWithLines(db, &settlement, lines); err != nil {
		t.Fatal(err)
	}

	list, err := ListSettlements(db, s.companyID, 50)
	if err != nil || len(list) != 1 {
		t.Fatalf("Expected 1 settlement, got %d", len(list))
	}

	detail, _ := GetSettlement(db, s.companyID, settlement.ID)
	if detail.ExternalSettlementID != "SET-001" {
		t.Error("Settlement not loaded correctly")
	}

	sLines, _ := GetSettlementLines(db, s.companyID, settlement.ID)
	if len(sLines) != 2 {
		t.Fatalf("Expected 2 lines, got %d", len(sLines))
	}
}

func TestSettlement_CompanyIsolation(t *testing.T) {
	db := testSettlementDB(t)
	s := setupSettle(t, db)

	settlement := models.ChannelSettlement{
		CompanyID: s.companyID, ChannelAccountID: s.channelAcctID,
		ExternalSettlementID: "SET-ISO", GrossAmount: decimal.NewFromInt(100),
		RawPayload: datatypes.JSON("{}"),
	}
	CreateSettlementWithLines(db, &settlement, nil)

	otherCo := models.Company{Name: "Other", IsActive: true}
	db.Create(&otherCo)

	list, _ := ListSettlements(db, otherCo.ID, 50)
	if len(list) != 0 {
		t.Error("Other company should not see settlements")
	}
}

func TestComputeSettlementTotals_UsesFullNetComposition(t *testing.T) {
	lines := []models.ChannelSettlementLine{
		{LineType: models.SettlementLineSale, Amount: decimal.RequireFromString("1000.00")},
		{LineType: models.SettlementLineFee, Amount: decimal.RequireFromString("-100.00")},
		{LineType: models.SettlementLineShippingFee, Amount: decimal.RequireFromString("20.00")},
		{LineType: models.SettlementLineRefund, Amount: decimal.RequireFromString("150.00")},
		{LineType: models.SettlementLineReserve, Amount: decimal.RequireFromString("50.00")},
		{LineType: models.SettlementLineAdjustment, Amount: decimal.RequireFromString("30.00")},
		{LineType: models.SettlementLineAdjustment, Amount: decimal.RequireFromString("-25.00")},
		{LineType: models.SettlementLinePayout, Amount: decimal.RequireFromString("685.00")},
	}

	totals := ComputeSettlementTotals(lines)

	if !totals.GrossAmount.Equal(decimal.RequireFromString("1000.00")) {
		t.Fatalf("gross: want 1000.00 got %s", totals.GrossAmount)
	}
	if !totals.FeeAmount.Equal(decimal.RequireFromString("120.00")) {
		t.Fatalf("fees: want 120.00 got %s", totals.FeeAmount)
	}
	if !totals.NetAmount.Equal(decimal.RequireFromString("685.00")) {
		t.Fatalf("net: want 685.00 got %s", totals.NetAmount)
	}
	if !totals.PayoutAmount.Equal(decimal.RequireFromString("685.00")) {
		t.Fatalf("payout: want 685.00 got %s", totals.PayoutAmount)
	}
}

func TestComputeSettlementTotals_PositiveAdjustmentIncreasesNet(t *testing.T) {
	lines := []models.ChannelSettlementLine{
		{LineType: models.SettlementLineSale, Amount: decimal.RequireFromString("100.00")},
		{LineType: models.SettlementLineAdjustment, Amount: decimal.RequireFromString("30.00")},
	}

	totals := ComputeSettlementTotals(lines)

	if !totals.NetAmount.Equal(decimal.RequireFromString("130.00")) {
		t.Fatalf("net: want 130.00 got %s", totals.NetAmount)
	}
}

func TestComputeSettlementTotals_NegativeAdjustmentDecreasesNet(t *testing.T) {
	lines := []models.ChannelSettlementLine{
		{LineType: models.SettlementLineSale, Amount: decimal.RequireFromString("100.00")},
		{LineType: models.SettlementLineAdjustment, Amount: decimal.RequireFromString("-25.00")},
	}

	totals := ComputeSettlementTotals(lines)

	if !totals.NetAmount.Equal(decimal.RequireFromString("75.00")) {
		t.Fatalf("net: want 75.00 got %s", totals.NetAmount)
	}
}

func TestSettlement_CreateAndPersist_RecomputesNetAmountFromAllRelevantLines(t *testing.T) {
	db := testSettlementDB(t)
	s := setupSettle(t, db)

	now := time.Now()
	settlement := models.ChannelSettlement{
		CompanyID: s.companyID, ChannelAccountID: s.channelAcctID,
		ExternalSettlementID: "SET-NET-001", SettlementDate: &now,
		RawPayload: datatypes.JSON("{}"),
	}
	lines := []models.ChannelSettlementLine{
		{LineType: models.SettlementLineSale, Amount: decimal.RequireFromString("1000.00"), RawPayload: datatypes.JSON("{}")},
		{LineType: models.SettlementLineFee, Amount: decimal.RequireFromString("100.00"), RawPayload: datatypes.JSON("{}")},
		{LineType: models.SettlementLineRefund, Amount: decimal.RequireFromString("150.00"), RawPayload: datatypes.JSON("{}")},
		{LineType: models.SettlementLineAdjustment, Amount: decimal.RequireFromString("-50.00"), RawPayload: datatypes.JSON("{}")},
		{LineType: models.SettlementLineReserve, Amount: decimal.RequireFromString("25.00"), RawPayload: datatypes.JSON("{}")},
		{LineType: models.SettlementLinePayout, Amount: decimal.RequireFromString("675.00"), RawPayload: datatypes.JSON("{}")},
	}

	if err := CreateSettlementWithLines(db, &settlement, lines); err != nil {
		t.Fatalf("CreateSettlementWithLines: %v", err)
	}

	saved, err := GetSettlement(db, s.companyID, settlement.ID)
	if err != nil {
		t.Fatalf("GetSettlement: %v", err)
	}

	if !saved.GrossAmount.Equal(decimal.RequireFromString("1000.00")) {
		t.Fatalf("gross: want 1000.00 got %s", saved.GrossAmount)
	}
	if !saved.FeeAmount.Equal(decimal.RequireFromString("100.00")) {
		t.Fatalf("fee amount: want 100.00 got %s", saved.FeeAmount)
	}
	if !saved.NetAmount.Equal(decimal.RequireFromString("675.00")) {
		t.Fatalf("net amount: want 675.00 got %s", saved.NetAmount)
	}
}

// ── Suggested account mapping tests ──────────────────────────────────────────

func TestSuggestAccountForLineType_Fee(t *testing.T) {
	db := testSettlementDB(t)
	s := setupSettle(t, db)

	mapping := &models.ChannelAccountingMapping{
		CompanyID: s.companyID, ChannelAccountID: s.channelAcctID,
		FeeExpenseAccountID: &s.feeAcctID, RefundAccountID: &s.refundAcctID,
		ClearingAccountID: &s.clearingAcctID,
	}

	feeAcct := SuggestAccountForLineType(mapping, models.SettlementLineFee)
	if feeAcct == nil || *feeAcct != s.feeAcctID {
		t.Error("Fee should map to fee expense account")
	}

	refundAcct := SuggestAccountForLineType(mapping, models.SettlementLineRefund)
	if refundAcct == nil || *refundAcct != s.refundAcctID {
		t.Error("Refund should map to refund account")
	}

	payoutAcct := SuggestAccountForLineType(mapping, models.SettlementLinePayout)
	if payoutAcct == nil || *payoutAcct != s.clearingAcctID {
		t.Error("Payout should map to clearing account")
	}

	saleAcct := SuggestAccountForLineType(mapping, models.SettlementLineSale)
	if saleAcct == nil || *saleAcct != s.clearingAcctID {
		t.Error("Sale should map to clearing account")
	}
}

func TestSuggestAccountForLineType_NilMapping(t *testing.T) {
	result := SuggestAccountForLineType(nil, models.SettlementLineFee)
	if result != nil {
		t.Error("Nil mapping should return nil")
	}
}

func TestSettlement_AutoMapsFromAccountingMapping(t *testing.T) {
	db := testSettlementDB(t)
	s := setupSettle(t, db)

	// Save accounting mapping first.
	SaveAccountingMapping(db, &models.ChannelAccountingMapping{
		CompanyID: s.companyID, ChannelAccountID: s.channelAcctID,
		FeeExpenseAccountID: &s.feeAcctID, ClearingAccountID: &s.clearingAcctID,
	})

	// Create settlement — lines should auto-map.
	settlement := models.ChannelSettlement{
		CompanyID: s.companyID, ChannelAccountID: s.channelAcctID,
		GrossAmount: decimal.NewFromInt(500), RawPayload: datatypes.JSON("{}"),
	}
	lines := []models.ChannelSettlementLine{
		{LineType: models.SettlementLineFee, Amount: decimal.NewFromInt(-50), RawPayload: datatypes.JSON("{}")},
		{LineType: models.SettlementLineSale, Amount: decimal.NewFromInt(500), RawPayload: datatypes.JSON("{}")},
	}

	CreateSettlementWithLines(db, &settlement, lines)

	savedLines, _ := GetSettlementLines(db, s.companyID, settlement.ID)
	for _, l := range savedLines {
		if l.MappedAccountID == nil {
			t.Errorf("Line type %s should be auto-mapped", l.LineType)
		}
	}
}

// ── Workflow status tests ────────────────────────────────────────────────────

func TestDeriveOrderWorkflowStatus(t *testing.T) {
	// Converted.
	invID := uint(1)
	status := DeriveOrderWorkflowStatus(
		models.ChannelOrder{ConvertedInvoiceID: &invID},
		nil,
	)
	if status != OrderWorkflowConverted {
		t.Errorf("Expected converted, got %s", status)
	}

	// Ready.
	status = DeriveOrderWorkflowStatus(
		models.ChannelOrder{},
		[]models.ChannelOrderLine{{MappingStatus: models.MappingStatusMappedExact}},
	)
	if status != OrderWorkflowReady {
		t.Errorf("Expected ready, got %s", status)
	}

	// Blocked (unmapped line).
	status = DeriveOrderWorkflowStatus(
		models.ChannelOrder{},
		[]models.ChannelOrderLine{{MappingStatus: models.MappingStatusUnmapped}},
	)
	if status != OrderWorkflowBlocked {
		t.Errorf("Expected blocked, got %s", status)
	}

	// Blocked (no lines).
	status = DeriveOrderWorkflowStatus(models.ChannelOrder{}, nil)
	if status != OrderWorkflowBlocked {
		t.Errorf("Expected blocked for empty lines, got %s", status)
	}
}

// ── Mapping uniqueness NULL-safe test ────────────────────────────────────────

func TestMappingUniqueness_NullMarketplace(t *testing.T) {
	db := testSettlementDB(t)
	co := models.Company{Name: "Uniq Co", IsActive: true}
	db.Create(&co)
	acct := models.Account{CompanyID: co.ID, Code: "4000", Name: "Rev", RootAccountType: models.RootRevenue, DetailAccountType: "revenue", IsActive: true}
	db.Create(&acct)
	item := models.ProductService{CompanyID: co.ID, Name: "W", Type: models.ProductServiceTypeInventory, RevenueAccountID: acct.ID, IsActive: true}
	item.ApplyTypeDefaults()
	db.Create(&item)
	ch := models.SalesChannelAccount{CompanyID: co.ID, ChannelType: models.ChannelTypeAmazon, DisplayName: "A", AuthStatus: models.ChannelAuthPending, IsActive: true}
	db.Create(&ch)

	// First mapping with nil marketplace.
	err := CreateItemMapping(db, &models.ItemChannelMapping{
		CompanyID: co.ID, ItemID: item.ID, ChannelAccountID: ch.ID,
		ChannelType: models.ChannelTypeAmazon, ExternalSKU: "SKU-NULL", IsActive: true,
	})
	if err != nil {
		t.Fatalf("First mapping failed: %v", err)
	}

	// Second mapping with same nil marketplace should be rejected.
	err = CreateItemMapping(db, &models.ItemChannelMapping{
		CompanyID: co.ID, ItemID: item.ID, ChannelAccountID: ch.ID,
		ChannelType: models.ChannelTypeAmazon, ExternalSKU: "SKU-NULL", IsActive: true,
	})
	if err == nil {
		t.Fatal("Expected duplicate error for null marketplace")
	}
}
