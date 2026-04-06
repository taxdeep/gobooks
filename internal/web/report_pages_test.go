package web

import (
	"bytes"
	"encoding/csv"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"gobooks/internal/models"
	"gobooks/internal/services"
)

func TestResolveAsOfDateAt_UsesPresetEndDateForBalanceSheet(t *testing.T) {
	ref := time.Date(2026, time.April, 3, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		name       string
		preset     string
		fyEnd      string
		asOfRaw    string
		wantPreset string
		wantAsOf   string
	}{
		{
			name:       "last month uses prior month end",
			preset:     string(services.PresetLastMonth),
			fyEnd:      "12-31",
			wantPreset: string(services.PresetLastMonth),
			wantAsOf:   "2026-03-31",
		},
		{
			name:       "year to date uses today",
			preset:     string(services.PresetYearToDate),
			fyEnd:      "12-31",
			wantPreset: string(services.PresetYearToDate),
			wantAsOf:   "2026-04-03",
		},
		{
			name:       "last fiscal year uses prior fiscal year end",
			preset:     string(services.PresetLastFiscalYear),
			fyEnd:      "03-31",
			wantPreset: string(services.PresetLastFiscalYear),
			wantAsOf:   "2026-03-31",
		},
		{
			name:       "custom explicit date stays explicit",
			preset:     "",
			fyEnd:      "12-31",
			asOfRaw:    "2026-02-14",
			wantPreset: string(services.PresetCustom),
			wantAsOf:   "2026-02-14",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotPreset, gotAsOf := resolveAsOfDateAt(tc.preset, tc.asOfRaw, tc.fyEnd, ref)
			if gotPreset != tc.wantPreset {
				t.Fatalf("preset: want %q, got %q", tc.wantPreset, gotPreset)
			}
			if gotAsOf != tc.wantAsOf {
				t.Fatalf("asOf: want %q, got %q", tc.wantAsOf, gotAsOf)
			}
		})
	}
}

func TestReportPages_UseToolbarAndPrintHideReportNavArea(t *testing.T) {
	db := testErrorFeedbackDB(t)
	server := &Server{DB: db}
	user := seedErrorFeedbackUser(t, db)
	companyID := seedValidationCompany(t, db, "Reports Toolbar Co")
	if err := db.Exec(`UPDATE companies SET fiscal_year_end = ? WHERE id = ?`, "03-31", companyID).Error; err != nil {
		t.Fatal(err)
	}

	app := errorFeedbackApp(server, user, companyID)
	app.Get("/reports/balance-sheet", server.handleBalanceSheet)
	app.Get("/reports/journal-entries", server.handleJournalEntryReport)

	balanceResp := performRequest(t, app, "/reports/balance-sheet?period=last_fiscal_year", "")
	if balanceResp.StatusCode != http.StatusOK {
		t.Fatalf("balance sheet: expected %d, got %d", http.StatusOK, balanceResp.StatusCode)
	}
	balanceBody := readResponseBody(t, balanceResp)
	wantAsOf := services.ComputeReportPeriod(services.PresetLastFiscalYear, "03-31", time.Now()).To.Format("2006-01-02")
	if !strings.Contains(balanceBody, `data-mode="asof"`) {
		t.Fatalf("expected balance sheet toolbar to run in asof mode, got %q", balanceBody)
	}
	if !strings.Contains(balanceBody, `data-as-of="`+wantAsOf+`"`) {
		t.Fatalf("expected balance sheet toolbar as-of %q, got %q", wantAsOf, balanceBody)
	}
	if !strings.Contains(balanceBody, "For as-of reports, presets choose the report date.") {
		t.Fatalf("expected balance sheet as-of helper text, got %q", balanceBody)
	}

	journalResp := performRequest(t, app, "/reports/journal-entries", "")
	if journalResp.StatusCode != http.StatusOK {
		t.Fatalf("journal report: expected %d, got %d", http.StatusOK, journalResp.StatusCode)
	}
	journalBody := readResponseBody(t, journalResp)
	if !strings.Contains(journalBody, `class="report-toolbar-form`) {
		t.Fatalf("expected journal report to use unified toolbar, got %q", journalBody)
	}
	if !strings.Contains(journalBody, "Report Period") {
		t.Fatalf("expected journal report toolbar label, got %q", journalBody)
	}
	if !strings.Contains(journalBody, `name="period"`) {
		t.Fatalf("expected journal report toolbar period field, got %q", journalBody)
	}
	if strings.Contains(journalBody, `>Export CSV</a>`) {
		t.Fatalf("expected journal report toolbar to hide CSV export link when no export URL, got %q", journalBody)
	}
	if !strings.Contains(journalBody, ".report-nav-area {") {
		t.Fatalf("expected print styles to hide report-nav-area, got %q", journalBody)
	}
}

func TestARAgingReportPageAndCSVHappyPath(t *testing.T) {
	db := testErrorFeedbackDB(t)
	server := &Server{DB: db}
	user := seedErrorFeedbackUser(t, db)
	companyID := seedValidationCompany(t, db, "AR Aging Co")
	if err := db.Model(&models.Company{}).Where("id = ?", companyID).Updates(map[string]any{
		"fiscal_year_end":    "12-31",
		"base_currency_code": "CAD",
	}).Error; err != nil {
		t.Fatal(err)
	}
	customerA := seedValidationCustomer(t, db, companyID, "Aging Customer A")
	customerB := seedValidationCustomer(t, db, companyID, "Aging Customer B")

	for _, inv := range []models.Invoice{
		{
			CompanyID:      companyID,
			InvoiceNumber:  "AR-A-001",
			CustomerID:     customerA,
			InvoiceDate:    time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			DueDate:        datePtrWeb(t, "2026-04-20"),
			Status:         models.InvoiceStatusIssued,
			Amount:         decimal.RequireFromString("100.00"),
			BalanceDue:     decimal.RequireFromString("100.00"),
			AmountBase:     decimal.RequireFromString("100.00"),
			BalanceDueBase: decimal.RequireFromString("100.00"),
			CurrencyCode:   "CAD",
		},
		{
			CompanyID:      companyID,
			InvoiceNumber:  "AR-A-002",
			CustomerID:     customerA,
			InvoiceDate:    time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
			DueDate:        datePtrWeb(t, "2026-03-20"),
			Status:         models.InvoiceStatusIssued,
			Amount:         decimal.RequireFromString("75.00"),
			BalanceDue:     decimal.RequireFromString("75.00"),
			AmountBase:     decimal.RequireFromString("75.00"),
			BalanceDueBase: decimal.RequireFromString("75.00"),
			CurrencyCode:   "CAD",
		},
		{
			CompanyID:      companyID,
			InvoiceNumber:  "AR-B-001",
			CustomerID:     customerB,
			InvoiceDate:    time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC),
			DueDate:        datePtrWeb(t, "2025-12-31"),
			Status:         models.InvoiceStatusOverdue,
			Amount:         decimal.RequireFromString("50.00"),
			BalanceDue:     decimal.RequireFromString("50.00"),
			AmountBase:     decimal.RequireFromString("50.00"),
			BalanceDueBase: decimal.RequireFromString("50.00"),
			CurrencyCode:   "CAD",
		},
		{
			CompanyID:      companyID,
			InvoiceNumber:  "AR-DRAFT-001",
			CustomerID:     customerB,
			InvoiceDate:    time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			DueDate:        datePtrWeb(t, "2026-03-20"),
			Status:         models.InvoiceStatusDraft,
			Amount:         decimal.RequireFromString("999.00"),
			BalanceDue:     decimal.RequireFromString("999.00"),
			AmountBase:     decimal.RequireFromString("999.00"),
			BalanceDueBase: decimal.RequireFromString("999.00"),
			CurrencyCode:   "CAD",
		},
	} {
		invoice := inv
		if err := db.Create(&invoice).Error; err != nil {
			t.Fatal(err)
		}
	}

	app := errorFeedbackApp(server, user, companyID)
	app.Get("/reports/ar-aging", server.handleARAgingReport)
	app.Get("/reports/ar-aging/export.csv", server.handleExportARAgingCSV)

	pageResp := performRequest(t, app, "/reports/ar-aging?as_of=2026-04-05", "")
	if pageResp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, pageResp.StatusCode)
	}
	pageBody := readResponseBody(t, pageResp)
	for _, want := range []string{
		"A/R Aging",
		`data-mode="asof"`,
		`data-as-of="2026-04-05"`,
		"Export CSV",
		"Current",
		"1-30",
		"91+",
		"Aging Customer A",
		"Aging Customer B",
		"175.00",
		"50.00",
		"225.00",
		"All amounts are shown in company base currency:",
		// Updated text: detail rows are now rendered inline
		"Customer totals with invoice-level detail rows expanded below each customer.",
		// Invoice detail rows must appear in the HTML
		"AR-A-001",
		"AR-A-002",
		"AR-B-001",
	} {
		if !strings.Contains(pageBody, want) {
			t.Fatalf("expected AR aging page to contain %q", want)
		}
	}
	if strings.Contains(pageBody, "AR-DRAFT-001") {
		t.Fatalf("expected draft invoice to stay out of AR aging page")
	}

	csvResp := performRequest(t, app, "/reports/ar-aging/export.csv?as_of=2026-04-05", "")
	if csvResp.StatusCode != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, csvResp.StatusCode)
	}
	if got := csvResp.Header.Get("Content-Type"); !strings.Contains(got, "text/csv") {
		t.Fatalf("expected csv content type, got %q", got)
	}
	if got := csvResp.Header.Get("Content-Disposition"); !strings.Contains(got, "gobooks_ar_aging_") {
		t.Fatalf("expected csv filename header, got %q", got)
	}
	csvBody := readResponseBody(t, csvResp)
	for _, want := range []string{
		"A/R Aging",
		"As of: 2026-04-05",
		// 10-column header
		"Customer/Invoice,Invoice Date,Due Date,Terms,Current,1-30,31-60,61-90,91+,Balance Due",
		// customer summary rows (date/terms blank, 10 cols)
		"Aging Customer A,,,,100.00,75.00,,,,175.00",
		"Aging Customer B,,,,,,,,50.00,50.00",
		// totals row
		"Totals,,,,100.00,75.00,,,50.00,225.00",
		// invoice detail rows appear in CSV (Go csv quotes space-leading fields)
		`"  AR-A-001"`,
		`"  AR-A-002"`,
		`"  AR-B-001"`,
	} {
		if !strings.Contains(csvBody, want) {
			t.Fatalf("expected AR aging CSV to contain %q\nfull body:\n%s", want, csvBody)
		}
	}
	// Every multi-field row must have exactly 10 columns (parsed with a real CSV reader
	// so quoted fields containing spaces are handled correctly).
	cr := csv.NewReader(bytes.NewBufferString(csvBody))
	cr.FieldsPerRecord = -1 // allow variable field counts so metadata rows don't fail parsing
	csvRows, err := cr.ReadAll()
	if err != nil {
		t.Fatalf("failed to parse CSV response: %v", err)
	}
	for _, row := range csvRows {
		if len(row) == 1 {
			continue // metadata / blank separator lines
		}
		if len(row) != 10 {
			t.Fatalf("expected 10 fields per CSV row, got %d: %v", len(row), row)
		}
	}
}
