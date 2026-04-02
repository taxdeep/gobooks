// 遵循project_guide.md
package services

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gobooks/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testExportDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:export_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Account{},
		&models.Customer{},
		&models.ProductService{},
		&models.SalesChannelAccount{},
		&models.ItemChannelMapping{},
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

type exportSetup struct {
	companyID  uint
	channelID  uint
	clearingID uint
	feeID      uint
	revID      uint
	customerID uint
	itemID     uint
}

func setupExport(t *testing.T, db *gorm.DB) exportSetup {
	t.Helper()
	co := models.Company{Name: "Export Co", IsActive: true, BaseCurrencyCode: "CAD"}
	db.Create(&co)
	cust := models.Customer{CompanyID: co.ID, Name: "Cust", AddrStreet1: "1"}
	db.Create(&cust)
	clearing := models.Account{CompanyID: co.ID, Code: "1500", Name: "Clearing", RootAccountType: models.RootAsset, DetailAccountType: "other_current_asset", IsActive: true}
	db.Create(&clearing)
	fee := models.Account{CompanyID: co.ID, Code: "6500", Name: "Fees", RootAccountType: models.RootExpense, DetailAccountType: "operating_expense", IsActive: true}
	db.Create(&fee)
	rev := models.Account{CompanyID: co.ID, Code: "4000", Name: "Rev", RootAccountType: models.RootRevenue, DetailAccountType: "sales_revenue", IsActive: true}
	db.Create(&rev)
	item := models.ProductService{CompanyID: co.ID, Name: "Widget", Type: models.ProductServiceTypeService, RevenueAccountID: rev.ID, IsActive: true}
	item.ApplyTypeDefaults()
	db.Create(&item)
	ch := models.SalesChannelAccount{CompanyID: co.ID, ChannelType: models.ChannelTypeAmazon, DisplayName: "AMZ US", AuthStatus: models.ChannelAuthPending, IsActive: true}
	db.Create(&ch)
	SaveAccountingMapping(db, &models.ChannelAccountingMapping{
		CompanyID: co.ID, ChannelAccountID: ch.ID,
		ClearingAccountID: &clearing.ID, FeeExpenseAccountID: &fee.ID,
	})
	return exportSetup{companyID: co.ID, channelID: ch.ID, clearingID: clearing.ID, feeID: fee.ID, revID: rev.ID, customerID: cust.ID, itemID: item.ID}
}

// ── Clearing export tests ────────────────────────────────────────────────────

func TestExportClearingSummary_Columns(t *testing.T) {
	db := testExportDB(t)
	s := setupExport(t, db)
	_ = s

	var buf bytes.Buffer
	ExportClearingSummaryCSV(db, s.companyID, &buf)

	records, _ := csv.NewReader(&buf).ReadAll()
	if len(records) < 1 {
		t.Fatal("Expected at least header row")
	}
	header := records[0]
	if header[0] != "Channel Account" || header[4] != "Current Balance" {
		t.Errorf("Unexpected header: %v", header)
	}
}

func TestExportClearingMovements_ContainsRunningBalance(t *testing.T) {
	db := testExportDB(t)
	s := setupExport(t, db)

	// Post a channel-origin invoice to get clearing movements.
	order := models.ChannelOrder{CompanyID: s.companyID, ChannelAccountID: s.channelID, ExternalOrderID: "E-1", OrderStatus: "imported", RawPayload: datatypes.JSON("{}")}
	db.Create(&order)
	inv := models.Invoice{
		CompanyID: s.companyID, InvoiceNumber: "INV-EXP-1", CustomerID: s.customerID,
		InvoiceDate: time.Now(), ChannelOrderID: &order.ID, Status: models.InvoiceStatusDraft,
		Subtotal: decimal.NewFromInt(100), Amount: decimal.NewFromInt(100), BalanceDue: decimal.NewFromInt(100),
		CustomerNameSnapshot: "C",
	}
	db.Create(&inv)
	db.Create(&models.InvoiceLine{
		CompanyID: s.companyID, InvoiceID: inv.ID, SortOrder: 1,
		ProductServiceID: &s.itemID, Description: "W",
		Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(100),
		LineNet: decimal.NewFromInt(100), LineTotal: decimal.NewFromInt(100),
	})
	PostInvoice(db, s.companyID, inv.ID, "test", nil)

	var buf bytes.Buffer
	ExportClearingMovementsCSV(db, s.companyID, s.channelID, &buf)

	records, _ := csv.NewReader(&buf).ReadAll()
	if len(records) < 2 {
		t.Fatal("Expected header + at least 1 data row")
	}
	// Header should have "Running Balance".
	header := records[0]
	if header[7] != "Running Balance" {
		t.Errorf("Expected 'Running Balance' column, got %v", header)
	}
}

// ── Settlement export tests ──────────────────────────────────────────────────

func TestExportSettlementsList_Columns(t *testing.T) {
	db := testExportDB(t)
	s := setupExport(t, db)

	now := time.Now()
	CreateSettlementWithLines(db, &models.ChannelSettlement{
		CompanyID: s.companyID, ChannelAccountID: s.channelID,
		ExternalSettlementID: "SET-EXP", SettlementDate: &now,
		RawPayload: datatypes.JSON("{}"),
	}, []models.ChannelSettlementLine{
		{LineType: models.SettlementLineFee, Amount: decimal.NewFromInt(50), RawPayload: datatypes.JSON("{}")},
	})

	var buf bytes.Buffer
	ExportSettlementsListCSV(db, s.companyID, &buf)

	records, _ := csv.NewReader(&buf).ReadAll()
	if len(records) < 2 {
		t.Fatal("Expected header + 1 data row")
	}
	header := records[0]
	if header[9] != "Fee Status" || header[11] != "Payout Status" {
		t.Errorf("Unexpected columns: %v", header)
	}
	// Data row should show "Not Posted".
	if records[1][9] != "Not Posted" {
		t.Errorf("Expected 'Not Posted', got '%s'", records[1][9])
	}
}

func TestExportSettlementLines_ContainsMappedAccount(t *testing.T) {
	db := testExportDB(t)
	s := setupExport(t, db)

	now := time.Now()
	settlement := models.ChannelSettlement{
		CompanyID: s.companyID, ChannelAccountID: s.channelID,
		SettlementDate: &now, RawPayload: datatypes.JSON("{}"),
	}
	CreateSettlementWithLines(db, &settlement, []models.ChannelSettlementLine{
		{LineType: models.SettlementLineFee, Description: "FBA fee", Amount: decimal.NewFromInt(25), RawPayload: datatypes.JSON("{}")},
	})

	var buf bytes.Buffer
	ExportSettlementLinesCSV(db, s.companyID, settlement.ID, &buf)

	records, _ := csv.NewReader(&buf).ReadAll()
	if len(records) < 2 {
		t.Fatal("Expected header + 1 line")
	}
	// Mapped account should be non-empty (auto-mapped from fee mapping).
	if records[1][7] == "" {
		t.Error("Expected mapped account to be filled (auto-mapped)")
	}
}

// ── Channel order export tests ───────────────────────────────────────────────

func TestExportChannelOrders_ContainsWorkflowStatus(t *testing.T) {
	db := testExportDB(t)
	s := setupExport(t, db)

	// Create mapping.
	CreateItemMapping(db, &models.ItemChannelMapping{
		CompanyID: s.companyID, ItemID: s.itemID, ChannelAccountID: s.channelID,
		ChannelType: models.ChannelTypeAmazon, ExternalSKU: "SKU-1", IsActive: true,
	})

	order := models.ChannelOrder{
		CompanyID: s.companyID, ChannelAccountID: s.channelID,
		ExternalOrderID: "ORD-EXP", OrderStatus: "imported",
		RawPayload: datatypes.JSON("{}"), ImportedAt: time.Now(),
	}
	CreateChannelOrderWithLines(db, &order, []models.ChannelOrderLine{
		{ExternalSKU: "SKU-1", Quantity: decimal.NewFromInt(1), RawPayload: datatypes.JSON("{}")},
	})

	var buf bytes.Buffer
	ExportChannelOrdersListCSV(db, s.companyID, &buf)

	records, _ := csv.NewReader(&buf).ReadAll()
	if len(records) < 2 {
		t.Fatal("Expected header + 1 row")
	}
	header := records[0]
	if header[5] != "Workflow Status" {
		t.Errorf("Expected 'Workflow Status' column, got %v", header)
	}
	if records[1][5] != "ready" {
		t.Errorf("Expected 'ready' workflow status, got '%s'", records[1][5])
	}
}

func TestExportChannelOrderLines_ContainsMappingStatus(t *testing.T) {
	db := testExportDB(t)
	s := setupExport(t, db)

	order := models.ChannelOrder{
		CompanyID: s.companyID, ChannelAccountID: s.channelID,
		ExternalOrderID: "ORD-LINES", OrderStatus: "imported",
		RawPayload: datatypes.JSON("{}"), ImportedAt: time.Now(),
	}
	CreateChannelOrderWithLines(db, &order, []models.ChannelOrderLine{
		{ExternalSKU: "UNKNOWN", Quantity: decimal.NewFromInt(2), RawPayload: datatypes.JSON("{}")},
	})

	var buf bytes.Buffer
	ExportChannelOrderLinesCSV(db, s.companyID, order.ID, &buf)

	records, _ := csv.NewReader(&buf).ReadAll()
	if len(records) < 2 {
		t.Fatal("Expected header + 1 line")
	}
	if records[1][8] != "unmapped" {
		t.Errorf("Expected 'unmapped', got '%s'", records[1][8])
	}
}

// ── Company isolation test ───────────────────────────────────────────────────

func TestExportCSV_CompanyIsolation(t *testing.T) {
	db := testExportDB(t)
	s := setupExport(t, db)

	// Create data for company s.
	now := time.Now()
	CreateSettlementWithLines(db, &models.ChannelSettlement{
		CompanyID: s.companyID, ChannelAccountID: s.channelID,
		SettlementDate: &now, RawPayload: datatypes.JSON("{}"),
	}, []models.ChannelSettlementLine{
		{LineType: models.SettlementLineFee, Amount: decimal.NewFromInt(10), RawPayload: datatypes.JSON("{}")},
	})

	// Export for a different company should return only header.
	otherCo := models.Company{Name: "Other", IsActive: true}
	db.Create(&otherCo)

	var buf bytes.Buffer
	ExportSettlementsListCSV(db, otherCo.ID, &buf)

	records, _ := csv.NewReader(&buf).ReadAll()
	if len(records) != 1 {
		t.Errorf("Other company should see only header row, got %d rows", len(records))
	}
}

// ── CSV escaping test ────────────────────────────────────────────────────────

func TestCSV_Escaping(t *testing.T) {
	// Verify csv.Writer handles commas and quotes correctly.
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.Write([]string{"Name", "Value"})
	w.Write([]string{"Test, with comma", `Has "quotes"`})
	w.Flush()

	content := buf.String()
	if !strings.Contains(content, `"Test, with comma"`) {
		t.Error("CSV should quote fields with commas")
	}
	if !strings.Contains(content, `"Has ""quotes"""`) {
		t.Error("CSV should escape quotes with double-quote")
	}
}

// ── Audit label tests ────────────────────────────────────────────────────────

func TestSettlementStatusLabel(t *testing.T) {
	// Not posted.
	s := models.ChannelSettlement{}
	if SettlementStatusLabel(s) != "Not Posted" {
		t.Error("Expected 'Not Posted'")
	}

	// Posted.
	jeID := uint(1)
	s.PostedJournalEntryID = &jeID
	if SettlementStatusLabel(s) != "Fee Posted" {
		t.Error("Expected 'Fee Posted'")
	}

	// Reversed.
	revID := uint(2)
	s.PostedReversalJEID = &revID
	if SettlementStatusLabel(s) != "Fee Reversed" {
		t.Error("Expected 'Fee Reversed'")
	}
}

func TestSettlementPayoutLabel(t *testing.T) {
	s := models.ChannelSettlement{}
	if SettlementPayoutLabel(s) != "No Payout" {
		t.Error("Expected 'No Payout'")
	}

	jeID := uint(1)
	s.PayoutJournalEntryID = &jeID
	if SettlementPayoutLabel(s) != "Payout Recorded" {
		t.Error("Expected 'Payout Recorded'")
	}

	revID := uint(2)
	s.PayoutReversalJEID = &revID
	if SettlementPayoutLabel(s) != "Payout Reversed" {
		t.Error("Expected 'Payout Reversed'")
	}
}

func TestOrderBlockerReason(t *testing.T) {
	// Unmapped.
	lines := []models.ChannelOrderLine{
		{ExternalSKU: "SKU-X", MappingStatus: models.MappingStatusUnmapped},
	}
	reason := OrderBlockerReason(lines)
	if !strings.Contains(reason, "SKU-X") {
		t.Errorf("Expected blocker mentioning SKU, got: %s", reason)
	}

	// All mapped.
	lines = []models.ChannelOrderLine{
		{ExternalSKU: "A", MappingStatus: models.MappingStatusMappedExact},
	}
	if OrderBlockerReason(lines) != "" {
		t.Error("Expected no blocker for mapped lines")
	}

	// Empty.
	if OrderBlockerReason(nil) != "No lines" {
		t.Error("Expected 'No lines' for empty")
	}
}
