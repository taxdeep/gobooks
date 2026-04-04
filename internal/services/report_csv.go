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
