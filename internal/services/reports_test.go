package services

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

func testReportsDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:reports_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Account{}, &models.JournalEntry{}, &models.JournalLine{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedAccountWithBalance(t *testing.T, db *gorm.DB, companyID uint, code, name string, root models.RootAccountType, detail models.DetailAccountType, debit, credit string) uint {
	t.Helper()
	acc := models.Account{
		CompanyID:         companyID,
		Code:              code,
		Name:              name,
		RootAccountType:   root,
		DetailAccountType: detail,
		IsActive:          true,
	}
	if err := db.Create(&acc).Error; err != nil {
		t.Fatal(err)
	}

	entry := models.JournalEntry{
		CompanyID: companyID,
		EntryDate: time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
		JournalNo: "JE-001",
		Status:    models.JournalEntryStatusPosted,
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}

	line := models.JournalLine{
		CompanyID:      companyID,
		JournalEntryID: entry.ID,
		AccountID:      acc.ID,
		Debit:          decimal.RequireFromString(debit),
		Credit:         decimal.RequireFromString(credit),
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}
	return acc.ID
}

func TestTrialBalance_filtersByCompany(t *testing.T) {
	db := testReportsDB(t)
	seedAccountWithBalance(t, db, 1, "1000", "Cash A", models.RootAsset, models.DetailBank, "100.00", "0.00")
	seedAccountWithBalance(t, db, 2, "1000", "Cash B", models.RootAsset, models.DetailBank, "250.00", "0.00")

	rows, debits, credits, err := TrialBalance(db, 1, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row for company 1, got %d", len(rows))
	}
	if rows[0].Name != "Cash A" {
		t.Fatalf("expected company 1 account, got %q", rows[0].Name)
	}
	if !debits.Equal(decimal.RequireFromString("100.00")) || !credits.Equal(decimal.Zero) {
		t.Fatalf("unexpected totals: debits=%s credits=%s", debits, credits)
	}
}

func TestIncomeStatementReport_filtersByCompany(t *testing.T) {
	db := testReportsDB(t)
	seedAccountWithBalance(t, db, 1, "4000", "Revenue A", models.RootRevenue, models.DetailServiceRevenue, "0.00", "300.00")
	seedAccountWithBalance(t, db, 2, "4000", "Revenue B", models.RootRevenue, models.DetailServiceRevenue, "0.00", "900.00")

	report, err := IncomeStatementReport(db, 1, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Revenue) != 1 {
		t.Fatalf("expected 1 revenue line, got %d", len(report.Revenue))
	}
	if report.Revenue[0].Name != "Revenue A" {
		t.Fatalf("expected company 1 revenue, got %q", report.Revenue[0].Name)
	}
	if !report.TotalRevenue.Equal(decimal.RequireFromString("300.00")) {
		t.Fatalf("unexpected total revenue: %s", report.TotalRevenue)
	}
}

func TestBalanceSheetReport_filtersByCompany(t *testing.T) {
	db := testReportsDB(t)
	seedAccountWithBalance(t, db, 1, "2000", "Payable A", models.RootLiability, models.DetailAccountsPayable, "0.00", "125.00")
	seedAccountWithBalance(t, db, 2, "2000", "Payable B", models.RootLiability, models.DetailAccountsPayable, "0.00", "400.00")

	report, err := BalanceSheetReport(db, 1, time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Liabilities) != 1 {
		t.Fatalf("expected 1 liability line, got %d", len(report.Liabilities))
	}
	if report.Liabilities[0].Name != "Payable A" {
		t.Fatalf("expected company 1 liability, got %q", report.Liabilities[0].Name)
	}
	if !report.TotalLiabilities.Equal(decimal.RequireFromString("125.00")) {
		t.Fatalf("unexpected total liabilities: %s", report.TotalLiabilities)
	}
}

func TestJournalEntryReport_filtersByDateAndCompany(t *testing.T) {
	db := testReportsDB(t)
	seedAccountWithBalance(t, db, 1, "1000", "Cash A", models.RootAsset, models.DetailBank, "100.00", "0.00")
	seedAccountWithBalance(t, db, 2, "1000", "Cash B", models.RootAsset, models.DetailBank, "50.00", "0.00")

	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	rep1, err := JournalEntryReport(db, 1, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep1) != 1 {
		t.Fatalf("company 1: want 1 entry, got %d", len(rep1))
	}
	if rep1[0].JournalNo != "JE-001" || len(rep1[0].Lines) != 1 {
		t.Fatalf("unexpected entry: %+v", rep1[0])
	}
	if rep1[0].Lines[0].AccountCode != "1000" || rep1[0].Lines[0].AccountName != "Cash A" {
		t.Fatalf("unexpected line: %+v", rep1[0].Lines[0])
	}

	rep1empty, err := JournalEntryReport(db, 1, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep1empty) != 0 {
		t.Fatalf("expected no entries in April for company 1, got %d", len(rep1empty))
	}

	rep2, err := JournalEntryReport(db, 2, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep2) != 1 || rep2[0].Lines[0].AccountName != "Cash B" {
		t.Fatalf("company 2 report mismatch: %+v", rep2)
	}
}
