package services

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/xuri/excelize/v2"
)

func TestExportAccountTransactionsCSV(t *testing.T) {
	report := sampleAccountTransactionsReport()
	var buf bytes.Buffer
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	if err := ExportAccountTransactionsCSV(report, from, to, &buf); err != nil {
		t.Fatalf("export csv: %v", err)
	}
	body := buf.String()
	for _, want := range []string{
		"Account Transactions",
		"21000 - GST/HST Payable",
		"2026-01-01 to 2026-03-31",
		"Date,Type,No.,Name,Description,Debit,Credit,Balance",
		"2026-01-15,Invoice,INV-001,WALMART CANADA,Tax on Goods,,125.00,125.00",
		",Totals and Ending Balance,,,,10.00,125.00,115.00",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected CSV to contain %q\nfull body:\n%s", want, body)
		}
	}

	cr := csv.NewReader(bytes.NewBufferString(body))
	cr.FieldsPerRecord = -1
	rows, err := cr.ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	for _, row := range rows {
		if len(row) == 0 || len(row) == 1 || (len(row) == 2 && row[0] != "Date") {
			continue
		}
		if len(row) != 8 {
			t.Fatalf("expected 8 fields in data/header row, got %d: %v", len(row), row)
		}
	}
}

func TestExportAccountTransactionsXLSX(t *testing.T) {
	report := sampleAccountTransactionsReport()
	var buf bytes.Buffer
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)

	if err := ExportAccountTransactionsXLSX(report, from, to, &buf); err != nil {
		t.Fatalf("export xlsx: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected non-empty xlsx")
	}

	f, err := excelize.OpenReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("open xlsx: %v", err)
	}
	defer f.Close()

	const sheet = "Account Transactions"
	assertCell(t, f, sheet, "A1", "Account Transactions")
	assertCell(t, f, sheet, "B2", "21000 - GST/HST Payable")
	assertCell(t, f, sheet, "B3", "2026-01-01 to 2026-03-31")
	assertCell(t, f, sheet, "A5", "Date")
	assertCell(t, f, sheet, "C7", "INV-001")
	assertCell(t, f, sheet, "D7", "WALMART CANADA")
	assertCell(t, f, sheet, "H8", "115.00")
}

func TestAccountTransactionsExportFilenameIsSafe(t *testing.T) {
	report := &AccountTransactionsReport{AccountCode: "21/000", AccountName: `GST "Payable"`}
	got := AccountTransactionsExportFilename(report, "xlsx")
	if strings.ContainsAny(got, `/\";:`) {
		t.Fatalf("filename contains unsafe characters: %q", got)
	}
	if !strings.HasPrefix(got, "AccountTransactions-") || !strings.HasSuffix(got, ".xlsx") {
		t.Fatalf("unexpected filename: %q", got)
	}
}

func sampleAccountTransactionsReport() *AccountTransactionsReport {
	return &AccountTransactionsReport{
		AccountCode:     "21000",
		AccountName:     "GST/HST Payable",
		AccountRootType: "liability",
		DetailType:      "sales_tax_payable",
		StartingBalance: decimal.Zero,
		Rows: []AccountTransactionRow{
			{
				Date:                 "2026-01-15",
				TransactionTypeLabel: "Invoice",
				DocumentNumber:       "INV-001",
				CounterpartyName:     "WALMART CANADA",
				Description:          "Tax on Goods",
				Credit:               decimal.RequireFromString("125.00"),
				Balance:              decimal.RequireFromString("125.00"),
			},
			{
				Date:                 "2026-01-20",
				TransactionTypeLabel: "Journal Entry",
				DocumentNumber:       "JE001",
				CounterpartyName:     "CBSA",
				Description:          "Adjustment",
				Debit:                decimal.RequireFromString("10.00"),
				Balance:              decimal.RequireFromString("115.00"),
			},
		},
		TotalDebits:   decimal.RequireFromString("10.00"),
		TotalCredits:  decimal.RequireFromString("125.00"),
		EndingBalance: decimal.RequireFromString("115.00"),
	}
}

func assertCell(t *testing.T, f *excelize.File, sheet, cell, want string) {
	t.Helper()
	got, err := f.GetCellValue(sheet, cell)
	if err != nil {
		t.Fatalf("get cell %s: %v", cell, err)
	}
	if got != want {
		t.Fatalf("%s = %q, want %q", cell, got, want)
	}
}
