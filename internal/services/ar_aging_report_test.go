package services

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

func TestBuildARAgingReport_BucketsAndTotals(t *testing.T) {
	db := testReportsDB(t)
	companyID := seedARAgingCompany(t, db, "ARAging Co", "CAD")
	customerA := seedARAgingCustomer(t, db, companyID, "Customer A")
	customerB := seedARAgingCustomer(t, db, companyID, "Customer B")

	seedARAgingInvoice(t, db, companyID, customerA, "AR-CUR", "2026-04-01", "2026-04-20", models.InvoiceStatusIssued, "100.00", "100.00", "", "0.00")
	seedARAgingInvoice(t, db, companyID, customerA, "AR-1-30", "2026-03-15", "2026-03-20", models.InvoiceStatusIssued, "100.00", "100.00", "USD", "130.00")
	seedARAgingInvoice(t, db, companyID, customerA, "AR-31-60", "2026-02-20", "2026-02-27", models.InvoiceStatusSent, "70.00", "70.00", "", "0.00")
	seedARAgingInvoice(t, db, companyID, customerB, "AR-61-90", "2026-01-20", "2026-01-29", models.InvoiceStatusPartiallyPaid, "60.00", "60.00", "", "0.00")
	seedARAgingInvoice(t, db, companyID, customerB, "AR-91", "2025-12-20", "2025-12-29", models.InvoiceStatusOverdue, "50.00", "50.00", "", "0.00")
	seedARAgingInvoice(t, db, companyID, customerA, "AR-DRAFT", "2026-03-10", "2026-03-20", models.InvoiceStatusDraft, "999.00", "999.00", "", "0.00")
	seedARAgingInvoice(t, db, companyID, customerB, "AR-VOID", "2026-03-10", "2026-03-20", models.InvoiceStatusVoided, "888.00", "888.00", "", "0.00")
	seedARAgingInvoice(t, db, companyID, customerA, "AR-FUTURE", "2026-04-10", "2026-04-25", models.InvoiceStatusIssued, "40.00", "40.00", "", "0.00")
	seedARAgingInvoice(t, db, companyID, customerB, "AR-PAID", "2026-03-01", "2026-03-15", models.InvoiceStatusPaid, "25.00", "0.00", "", "0.00")

	report, err := BuildARAgingReport(db, companyID, time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if report.CurrencyCode != "CAD" {
		t.Fatalf("expected CAD report currency, got %q", report.CurrencyCode)
	}
	if len(report.Rows) != 2 {
		t.Fatalf("expected 2 customer rows, got %d", len(report.Rows))
	}

	rowA := report.Rows[0]
	if rowA.CustomerID != customerA || !rowA.Current.Equal(decimal.RequireFromString("100.00")) || !rowA.Days1To30.Equal(decimal.RequireFromString("130.00")) || !rowA.Days31To60.Equal(decimal.RequireFromString("70.00")) || !rowA.Total.Equal(decimal.RequireFromString("300.00")) {
		t.Fatalf("unexpected customer A row: %+v", rowA)
	}
	rowB := report.Rows[1]
	if rowB.CustomerID != customerB || !rowB.Days61To90.Equal(decimal.RequireFromString("60.00")) || !rowB.Days91Plus.Equal(decimal.RequireFromString("50.00")) || !rowB.Total.Equal(decimal.RequireFromString("110.00")) {
		t.Fatalf("unexpected customer B row: %+v", rowB)
	}

	if !report.Totals.Current.Equal(decimal.RequireFromString("100.00")) ||
		!report.Totals.Days1To30.Equal(decimal.RequireFromString("130.00")) ||
		!report.Totals.Days31To60.Equal(decimal.RequireFromString("70.00")) ||
		!report.Totals.Days61To90.Equal(decimal.RequireFromString("60.00")) ||
		!report.Totals.Days91Plus.Equal(decimal.RequireFromString("50.00")) ||
		!report.Totals.Total.Equal(decimal.RequireFromString("410.00")) {
		t.Fatalf("unexpected grand totals: %+v", report.Totals)
	}

	// Detail rows: Customer A has 3 outstanding invoices; Customer B has 2.
	// Draft (999) and Voided (888) and Paid (balance_due=0) must not appear.
	if len(rowA.DetailRows) != 3 {
		t.Fatalf("expected 3 detail rows for customer A, got %d", len(rowA.DetailRows))
	}
	if len(rowB.DetailRows) != 2 {
		t.Fatalf("expected 2 detail rows for customer B, got %d", len(rowB.DetailRows))
	}

	// Detail row bucket sums must equal customer summary bucket totals.
	var detailA ARAgingBucketTotals
	for _, d := range rowA.DetailRows {
		detailA.Current = detailA.Current.Add(d.Current)
		detailA.Days1To30 = detailA.Days1To30.Add(d.Days1To30)
		detailA.Days31To60 = detailA.Days31To60.Add(d.Days31To60)
		detailA.Days61To90 = detailA.Days61To90.Add(d.Days61To90)
		detailA.Days91Plus = detailA.Days91Plus.Add(d.Days91Plus)
		detailA.Total = detailA.Total.Add(d.BalanceDue)
	}
	if !detailA.Current.Equal(rowA.Current) || !detailA.Days1To30.Equal(rowA.Days1To30) ||
		!detailA.Days31To60.Equal(rowA.Days31To60) || !detailA.Total.Equal(rowA.Total) {
		t.Fatalf("customer A detail totals do not match summary: detail=%+v summary=%+v", detailA, rowA)
	}
}

func TestBuildARAgingReport_AsOfDateChangesBucketAndInclusion(t *testing.T) {
	db := testReportsDB(t)
	companyID := seedARAgingCompany(t, db, "ARAging AsOf Co", "CAD")
	customerID := seedARAgingCustomer(t, db, companyID, "Customer A")

	seedARAgingInvoice(t, db, companyID, customerID, "AR-SHIFT", "2026-03-15", "2026-03-31", models.InvoiceStatusIssued, "100.00", "100.00", "", "0.00")
	seedARAgingInvoice(t, db, companyID, customerID, "AR-NOT-YET", "2026-04-10", "2026-04-20", models.InvoiceStatusIssued, "20.00", "20.00", "", "0.00")

	reportMar31, err := BuildARAgingReport(db, companyID, time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(reportMar31.Rows) != 1 || !reportMar31.Rows[0].Current.Equal(decimal.RequireFromString("100.00")) || !reportMar31.Rows[0].Total.Equal(decimal.RequireFromString("100.00")) {
		t.Fatalf("expected invoice to stay current on due date, got %+v", reportMar31.Rows)
	}

	reportApr05, err := BuildARAgingReport(db, companyID, time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(reportApr05.Rows) != 1 || !reportApr05.Rows[0].Days1To30.Equal(decimal.RequireFromString("100.00")) || !reportApr05.Rows[0].Total.Equal(decimal.RequireFromString("100.00")) {
		t.Fatalf("expected invoice to move into 1-30 bucket, got %+v", reportApr05.Rows)
	}
}

func TestExportARAgingCSV(t *testing.T) {
	due := time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	report := ARAgingReport{
		AsOf:         time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC),
		CurrencyCode: "CAD",
		Rows: []ARAgingCustomerRow{
			{
				CustomerName: "Customer A",
				Current:      decimal.RequireFromString("100.00"),
				Days1To30:    decimal.RequireFromString("30.00"),
				Total:        decimal.RequireFromString("130.00"),
				DetailRows: []ARAgingDetailRow{
					{
						InvoiceNumber: "INV-001",
						InvoiceDate:   time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
						DueDate:       nil,
						Terms:         "",
						BalanceDue:    decimal.RequireFromString("100.00"),
						Current:       decimal.RequireFromString("100.00"),
					},
					{
						InvoiceNumber: "INV-002",
						InvoiceDate:   time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
						DueDate:       &due,
						Terms:         "Net30",
						BalanceDue:    decimal.RequireFromString("30.00"),
						Days1To30:     decimal.RequireFromString("30.00"),
					},
				},
			},
		},
		Totals: ARAgingBucketTotals{
			Current:   decimal.RequireFromString("100.00"),
			Days1To30: decimal.RequireFromString("30.00"),
			Total:     decimal.RequireFromString("130.00"),
		},
	}

	var buf bytes.Buffer
	if err := ExportARAgingCSV(report, &buf); err != nil {
		t.Fatal(err)
	}
	got := buf.String()

	// Header, metadata, and totals row use the 10-column structure.
	for _, want := range []string{
		"A/R Aging",
		"As of: 2026-04-05",
		"Amounts shown in company base currency: CAD",
		// 10-column header
		"Customer/Invoice,Invoice Date,Due Date,Terms,Current,1-30,31-60,61-90,91+,Balance Due",
		// customer summary row: date/terms columns blank, last column is Total
		"Customer A,,,,100.00,30.00,,,,130.00",
		// detail row with nil DueDate: DueDate column blank, Terms blank
		// Go csv quotes fields that begin with a space, so leading indent is quoted.
		`"  INV-001",2026-04-01,,,100.00,,,,,100.00`,
		// detail row with DueDate and Terms
		`"  INV-002",2026-03-15,2026-03-25,Net30,,30.00,,,,30.00`,
		// totals row: date/terms columns blank
		"Totals,,,,100.00,30.00,,,,130.00",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected CSV to contain %q\nfull output:\n%s", want, got)
		}
	}

	// Every multi-field row must have exactly 10 columns (real CSV parser handles quoted fields).
	cr := csv.NewReader(bytes.NewBufferString(got))
	cr.FieldsPerRecord = -1 // allow variable field counts so metadata rows don't fail parsing
	csvRows, err := cr.ReadAll()
	if err != nil {
		t.Fatalf("failed to parse CSV output: %v", err)
	}
	for _, row := range csvRows {
		if len(row) == 1 {
			continue // metadata / blank separator rows
		}
		if len(row) != 10 {
			t.Fatalf("expected 10 fields per row, got %d: %v", len(row), row)
		}
	}
}

func TestARAgingDetailRows_BucketConsistencyAndTotals(t *testing.T) {
	db := testReportsDB(t)
	companyID := seedARAgingCompany(t, db, "Detail Co", "CAD")
	custID := seedARAgingCustomer(t, db, companyID, "Detail Customer")

	// One invoice in each bucket (due dates relative to as-of 2026-04-05).
	seedARAgingInvoice(t, db, companyID, custID, "D-CUR", "2026-04-01", "2026-04-20", models.InvoiceStatusIssued, "100.00", "100.00", "", "0.00")
	seedARAgingInvoice(t, db, companyID, custID, "D-130", "2026-03-15", "2026-03-25", models.InvoiceStatusIssued, "30.00", "30.00", "", "0.00")
	seedARAgingInvoice(t, db, companyID, custID, "D-3160", "2026-02-20", "2026-02-27", models.InvoiceStatusSent, "70.00", "70.00", "", "0.00")
	seedARAgingInvoice(t, db, companyID, custID, "D-6190", "2026-01-20", "2026-01-29", models.InvoiceStatusPartiallyPaid, "60.00", "60.00", "", "0.00")
	seedARAgingInvoice(t, db, companyID, custID, "D-91P", "2025-12-20", "2025-12-29", models.InvoiceStatusOverdue, "50.00", "50.00", "", "0.00")
	// Fully paid — must not appear in detail rows.
	seedARAgingInvoice(t, db, companyID, custID, "D-PAID", "2026-03-01", "2026-03-15", models.InvoiceStatusPaid, "25.00", "0.00", "", "0.00")
	// Draft — must not appear in detail rows.
	seedARAgingInvoice(t, db, companyID, custID, "D-DRAFT", "2026-03-10", "2026-03-20", models.InvoiceStatusDraft, "99.00", "99.00", "", "0.00")

	asOf := time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)
	report, err := BuildARAgingReport(db, companyID, asOf)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Rows) != 1 {
		t.Fatalf("expected 1 customer row, got %d", len(report.Rows))
	}
	row := report.Rows[0]
	if len(row.DetailRows) != 5 {
		t.Fatalf("expected 5 detail rows, got %d: %+v", len(row.DetailRows), row.DetailRows)
	}

	// Each detail row falls in exactly one bucket.
	bucketNames := map[string]string{}
	for _, d := range row.DetailRows {
		nonZero := 0
		if !d.Current.IsZero() {
			nonZero++
			bucketNames[d.InvoiceNumber] = "Current"
		}
		if !d.Days1To30.IsZero() {
			nonZero++
			bucketNames[d.InvoiceNumber] = "1-30"
		}
		if !d.Days31To60.IsZero() {
			nonZero++
			bucketNames[d.InvoiceNumber] = "31-60"
		}
		if !d.Days61To90.IsZero() {
			nonZero++
			bucketNames[d.InvoiceNumber] = "61-90"
		}
		if !d.Days91Plus.IsZero() {
			nonZero++
			bucketNames[d.InvoiceNumber] = "91+"
		}
		if nonZero != 1 {
			t.Fatalf("detail row %q: expected exactly 1 non-zero bucket, got %d", d.InvoiceNumber, nonZero)
		}
		// BalanceDue == the single bucket value.
		bucketSum := d.Current.Add(d.Days1To30).Add(d.Days31To60).Add(d.Days61To90).Add(d.Days91Plus)
		if !bucketSum.Equal(d.BalanceDue) {
			t.Fatalf("detail row %q: bucket sum %s != BalanceDue %s", d.InvoiceNumber, bucketSum, d.BalanceDue)
		}
	}

	// Verify each invoice landed in the expected bucket.
	want := map[string]string{"D-CUR": "Current", "D-130": "1-30", "D-3160": "31-60", "D-6190": "61-90", "D-91P": "91+"}
	for inv, wantBucket := range want {
		if got := bucketNames[inv]; got != wantBucket {
			t.Fatalf("invoice %q: expected bucket %q, got %q", inv, wantBucket, got)
		}
	}

	// Detail totals must equal summary totals.
	var detailTotals ARAgingBucketTotals
	for _, d := range row.DetailRows {
		detailTotals.Current = detailTotals.Current.Add(d.Current)
		detailTotals.Days1To30 = detailTotals.Days1To30.Add(d.Days1To30)
		detailTotals.Days31To60 = detailTotals.Days31To60.Add(d.Days31To60)
		detailTotals.Days61To90 = detailTotals.Days61To90.Add(d.Days61To90)
		detailTotals.Days91Plus = detailTotals.Days91Plus.Add(d.Days91Plus)
		detailTotals.Total = detailTotals.Total.Add(d.BalanceDue)
	}
	if !detailTotals.Current.Equal(row.Current) ||
		!detailTotals.Days1To30.Equal(row.Days1To30) ||
		!detailTotals.Days31To60.Equal(row.Days31To60) ||
		!detailTotals.Days61To90.Equal(row.Days61To90) ||
		!detailTotals.Days91Plus.Equal(row.Days91Plus) ||
		!detailTotals.Total.Equal(row.Total) {
		t.Fatalf("detail totals do not match customer summary:\ndetail=%+v\nsummary=%+v", detailTotals, row)
	}
	// Grand total consistency.
	if !detailTotals.Total.Equal(report.Totals.Total) {
		t.Fatalf("all detail total %s != grand total %s", detailTotals.Total, report.Totals.Total)
	}
}

func TestARAgingDetailRows_PartialPaymentAndSorting(t *testing.T) {
	db := testReportsDB(t)
	companyID := seedARAgingCompany(t, db, "Partial Co", "CAD")
	custID := seedARAgingCustomer(t, db, companyID, "Partial Customer")

	// Partially paid: amount=200, balanceDue=80 → still outstanding.
	seedARAgingInvoice(t, db, companyID, custID, "P-PARTIAL", "2026-03-01", "2026-03-20", models.InvoiceStatusPartiallyPaid, "200.00", "80.00", "", "0.00")
	// Fully paid: amount=100, balanceDue=0 → must not appear.
	seedARAgingInvoice(t, db, companyID, custID, "P-PAID", "2026-03-05", "2026-03-25", models.InvoiceStatusPaid, "100.00", "0.00", "", "0.00")
	// Current: due in future.
	seedARAgingInvoice(t, db, companyID, custID, "P-CUR", "2026-04-01", "2026-04-20", models.InvoiceStatusIssued, "50.00", "50.00", "", "0.00")

	asOf := time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)
	report, err := BuildARAgingReport(db, companyID, asOf)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Rows) != 1 {
		t.Fatalf("expected 1 customer row, got %d", len(report.Rows))
	}
	row := report.Rows[0]

	// Fully paid must not appear.
	for _, d := range row.DetailRows {
		if d.InvoiceNumber == "P-PAID" {
			t.Fatalf("fully paid invoice P-PAID must not appear in detail rows")
		}
	}
	if len(row.DetailRows) != 2 {
		t.Fatalf("expected 2 detail rows (partially paid + current), got %d", len(row.DetailRows))
	}

	// Partially paid detail row must show the remaining balance, not the full amount.
	var partialDetail *ARAgingDetailRow
	for i := range row.DetailRows {
		if row.DetailRows[i].InvoiceNumber == "P-PARTIAL" {
			partialDetail = &row.DetailRows[i]
			break
		}
	}
	if partialDetail == nil {
		t.Fatal("expected P-PARTIAL in detail rows")
	}
	if !partialDetail.BalanceDue.Equal(decimal.RequireFromString("80.00")) {
		t.Fatalf("P-PARTIAL balance due: expected 80.00, got %s", partialDetail.BalanceDue)
	}

	// Sorting: P-PARTIAL due 2026-03-20 < P-CUR due 2026-04-20.
	if row.DetailRows[0].InvoiceNumber != "P-PARTIAL" {
		t.Fatalf("expected P-PARTIAL first (earlier due date), got %q", row.DetailRows[0].InvoiceNumber)
	}

	// Summary total must equal sum of detail BalanceDue.
	var detailTotal decimal.Decimal
	for _, d := range row.DetailRows {
		detailTotal = detailTotal.Add(d.BalanceDue)
	}
	if !detailTotal.Equal(row.Total) {
		t.Fatalf("detail total %s != customer summary total %s", detailTotal, row.Total)
	}
}

func TestARAgingDetailRows_NilDueDateHandledSafely(t *testing.T) {
	db := testReportsDB(t)
	companyID := seedARAgingCompany(t, db, "NilDue Co", "CAD")
	custID := seedARAgingCustomer(t, db, companyID, "NilDue Customer")

	// Invoice with no due date — must land in Current bucket, not panic.
	invoice := models.Invoice{
		CompanyID:      companyID,
		CustomerID:     custID,
		InvoiceNumber:  "N-NODUE",
		InvoiceDate:    time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		DueDate:        nil,
		Status:         models.InvoiceStatusIssued,
		Amount:         decimal.RequireFromString("75.00"),
		BalanceDue:     decimal.RequireFromString("75.00"),
		AmountBase:     decimal.RequireFromString("75.00"),
		BalanceDueBase: decimal.RequireFromString("75.00"),
		CurrencyCode:   "CAD",
	}
	if err := db.Create(&invoice).Error; err != nil {
		t.Fatal(err)
	}

	report, err := BuildARAgingReport(db, companyID, time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Rows) != 1 || len(report.Rows[0].DetailRows) != 1 {
		t.Fatalf("expected 1 row with 1 detail, got rows=%d", len(report.Rows))
	}
	d := report.Rows[0].DetailRows[0]
	if !d.Current.Equal(decimal.RequireFromString("75.00")) {
		t.Fatalf("no-due-date invoice must land in Current, got current=%s 1-30=%s", d.Current, d.Days1To30)
	}
	if d.DueDate != nil {
		t.Fatalf("expected nil DueDate on detail row, got %v", d.DueDate)
	}
}

func seedARAgingCompany(t *testing.T, db *gorm.DB, name, baseCurrency string) uint {
	t.Helper()
	company := models.Company{
		Name:                    name,
		EntityType:              models.EntityTypeIncorporated,
		BusinessType:            models.BusinessTypeRetail,
		Industry:                models.IndustryRetail,
		IncorporatedDate:        "2024-01-01",
		FiscalYearEnd:           "12-31",
		BusinessNumber:          name + "-BN",
		AddressLine:             "123 Main",
		City:                    "Vancouver",
		Province:                "BC",
		PostalCode:              "V6B1A1",
		Country:                 "CA",
		AccountCodeLength:       4,
		AccountCodeLengthLocked: true,
		BaseCurrencyCode:        baseCurrency,
	}
	if err := db.Create(&company).Error; err != nil {
		t.Fatal(err)
	}
	return company.ID
}

func seedARAgingCustomer(t *testing.T, db *gorm.DB, companyID uint, name string) uint {
	t.Helper()
	customer := models.Customer{CompanyID: companyID, Name: name}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatal(err)
	}
	return customer.ID
}

func seedARAgingInvoice(t *testing.T, db *gorm.DB, companyID, customerID uint, invoiceNumber, invoiceDate, dueDate string, status models.InvoiceStatus, amount, balanceDue, currencyCode, balanceDueBase string) {
	t.Helper()
	invoiceDateValue, err := time.Parse("2006-01-02", invoiceDate)
	if err != nil {
		t.Fatal(err)
	}
	dueDateValue, err := time.Parse("2006-01-02", dueDate)
	if err != nil {
		t.Fatal(err)
	}
	invoice := models.Invoice{
		CompanyID:      companyID,
		CustomerID:     customerID,
		InvoiceNumber:  invoiceNumber,
		InvoiceDate:    invoiceDateValue,
		DueDate:        &dueDateValue,
		Status:         status,
		Amount:         decimal.RequireFromString(amount),
		BalanceDue:     decimal.RequireFromString(balanceDue),
		CurrencyCode:   currencyCode,
		AmountBase:     decimal.RequireFromString(amount),
		BalanceDueBase: decimal.RequireFromString(balanceDueBase),
	}
	if invoice.CurrencyCode == "" {
		invoice.AmountBase = invoice.Amount
		invoice.BalanceDueBase = invoice.BalanceDue
	} else if invoice.BalanceDueBase.GreaterThan(decimal.Zero) {
		invoice.AmountBase = invoice.BalanceDueBase
	}
	if err := db.Create(&invoice).Error; err != nil {
		t.Fatal(err)
	}
}
