// 遵循project_guide.md
package services

import (
	"encoding/csv"
	"fmt"
	"io"
	"time"

	"github.com/shopspring/decimal"
)

// csvMoney formats a decimal as a fixed-2 string; returns "" when zero.
func csvMoney(d decimal.Decimal) string { return d.StringFixed(2) }
func csvMoneyBlankZero(d decimal.Decimal) string {
	if d.IsZero() {
		return ""
	}
	return d.StringFixed(2)
}

// ── Trial Balance ─────────────────────────────────────────────────────────────

// ExportTrialBalanceCSV writes a trial balance report as CSV to w.
// Columns: Code, Name, Classification, Debit, Credit
func ExportTrialBalanceCSV(
	from, to time.Time,
	rows []TrialBalanceRow,
	totalDebits, totalCredits decimal.Decimal,
	w io.Writer,
) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	_ = cw.Write([]string{"Trial Balance"})
	_ = cw.Write([]string{fmt.Sprintf("Period: %s to %s",
		from.Format("2006-01-02"), to.Format("2006-01-02"))})
	_ = cw.Write(nil) // blank separator
	_ = cw.Write([]string{"Code", "Name", "Classification", "Debit", "Credit"})
	for _, r := range rows {
		_ = cw.Write([]string{
			r.Code, r.Name, r.Classification,
			csvMoneyBlankZero(r.Debit), csvMoneyBlankZero(r.Credit),
		})
	}
	// Totals row
	_ = cw.Write([]string{"", "Totals", "", csvMoney(totalDebits), csvMoney(totalCredits)})

	return cw.Error()
}

// ── Income Statement ──────────────────────────────────────────────────────────

// ExportIncomeStatementCSV writes an income statement as CSV to w.
// Columns: Section, Code, Name, Amount
func ExportIncomeStatementCSV(report IncomeStatement, w io.Writer) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	_ = cw.Write([]string{"Income Statement"})
	_ = cw.Write([]string{fmt.Sprintf("Period: %s to %s",
		report.FromDate.Format("2006-01-02"), report.ToDate.Format("2006-01-02"))})
	_ = cw.Write(nil)
	_ = cw.Write([]string{"Section", "Code", "Name", "Amount"})

	writeSection := func(title string, lines []IncomeStatementLine, total decimal.Decimal) {
		_ = cw.Write([]string{title, "", title, ""})
		for _, l := range lines {
			_ = cw.Write([]string{title, l.Code, l.Name, csvMoneyBlankZero(l.Amount)})
		}
		_ = cw.Write([]string{"", "", "Total " + title, csvMoney(total)})
		_ = cw.Write(nil)
	}

	writeSection("Revenue", report.Revenue, report.TotalRevenue)
	writeSection("Cost of Sales", report.CostOfSales, report.TotalCostOfSales)
	_ = cw.Write([]string{"", "", "Gross Profit", csvMoney(report.GrossProfit)})
	_ = cw.Write(nil)
	writeSection("Expenses", report.Expenses, report.TotalExpenses)
	_ = cw.Write([]string{"", "", "Net Income", csvMoney(report.NetIncome)})

	return cw.Error()
}

// ── Balance Sheet ─────────────────────────────────────────────────────────────

// ExportBalanceSheetCSV writes a balance sheet as CSV to w.
// Columns: Section, Code, Name, Amount
func ExportBalanceSheetCSV(report BalanceSheet, w io.Writer) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	_ = cw.Write([]string{"Balance Sheet"})
	_ = cw.Write([]string{"As of: " + report.AsOf.Format("2006-01-02")})
	_ = cw.Write(nil)
	_ = cw.Write([]string{"Section", "Code", "Name", "Amount"})

	writeSection := func(title string, lines []BalanceSheetLine, total decimal.Decimal) {
		_ = cw.Write([]string{title, "", title, ""})
		for _, l := range lines {
			_ = cw.Write([]string{title, l.Code, l.Name, csvMoneyBlankZero(l.Amount)})
		}
		_ = cw.Write([]string{"", "", "Total " + title, csvMoney(total)})
		_ = cw.Write(nil)
	}

	writeSection("Assets", report.Assets, report.TotalAssets)
	writeSection("Liabilities", report.Liabilities, report.TotalLiabilities)
	writeSection("Equity", report.Equity, report.TotalEquity)
	_ = cw.Write([]string{"", "", "Total Liabilities + Equity",
		csvMoney(report.TotalLiabilities.Add(report.TotalEquity))})

	return cw.Error()
}

// ExportARAgingCSV writes an A/R Aging report as CSV to w.
// Every data row (header, customer summary, invoice detail, totals) has exactly 10 columns:
//
//	Customer/Invoice | Invoice Date | Due Date | Terms | Current | 1-30 | 31-60 | 61-90 | 91+ | Balance Due
//
// Customer summary rows leave Invoice Date / Due Date / Terms blank.
// Invoice detail rows are indented ("  INV-001") and follow their customer summary row.
// Totals row leaves Invoice Date / Due Date / Terms blank.
func ExportARAgingCSV(report ARAgingReport, w io.Writer) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	_ = cw.Write([]string{"A/R Aging"})
	_ = cw.Write([]string{"As of: " + report.AsOf.Format("2006-01-02")})
	if report.CurrencyCode != "" {
		_ = cw.Write([]string{"Amounts shown in company base currency: " + report.CurrencyCode})
	}
	_ = cw.Write(nil)
	_ = cw.Write([]string{"Customer/Invoice", "Invoice Date", "Due Date", "Terms", "Current", "1-30", "31-60", "61-90", "91+", "Balance Due"})
	for _, row := range report.Rows {
		_ = cw.Write([]string{
			row.CustomerName, "", "", "",
			csvMoneyBlankZero(row.Current),
			csvMoneyBlankZero(row.Days1To30),
			csvMoneyBlankZero(row.Days31To60),
			csvMoneyBlankZero(row.Days61To90),
			csvMoneyBlankZero(row.Days91Plus),
			csvMoney(row.Total),
		})
		for _, d := range row.DetailRows {
			dueDateStr := ""
			if d.DueDate != nil {
				dueDateStr = d.DueDate.Format("2006-01-02")
			}
			_ = cw.Write([]string{
				"  " + d.InvoiceNumber,
				d.InvoiceDate.Format("2006-01-02"),
				dueDateStr,
				d.Terms,
				csvMoneyBlankZero(d.Current),
				csvMoneyBlankZero(d.Days1To30),
				csvMoneyBlankZero(d.Days31To60),
				csvMoneyBlankZero(d.Days61To90),
				csvMoneyBlankZero(d.Days91Plus),
				csvMoney(d.BalanceDue),
			})
		}
	}
	_ = cw.Write([]string{
		"Totals", "", "", "",
		csvMoneyBlankZero(report.Totals.Current),
		csvMoneyBlankZero(report.Totals.Days1To30),
		csvMoneyBlankZero(report.Totals.Days31To60),
		csvMoneyBlankZero(report.Totals.Days61To90),
		csvMoneyBlankZero(report.Totals.Days91Plus),
		csvMoney(report.Totals.Total),
	})

	return cw.Error()
}
