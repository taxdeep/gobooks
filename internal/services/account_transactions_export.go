package services

import (
	"context"
	"encoding/csv"
	"fmt"
	"html"
	"io"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"github.com/xuri/excelize/v2"

	"gobooks/internal/services/pdf"
)

// AccountTransactionsExportFilename returns an ASCII-safe filename for report exports.
func AccountTransactionsExportFilename(report *AccountTransactionsReport, ext string) string {
	ext = strings.TrimPrefix(strings.TrimSpace(ext), ".")
	if ext == "" {
		ext = "dat"
	}
	segment := "unknown"
	if report != nil {
		segment = sanitizePDFFilenameSegment(report.AccountCode + "-" + report.AccountName)
	}
	return "AccountTransactions-" + segment + "." + ext
}

// ExportAccountTransactionsCSV writes Account Transactions in Excel-compatible CSV form.
func ExportAccountTransactionsCSV(report *AccountTransactionsReport, from, to time.Time, w io.Writer) error {
	if report == nil {
		return fmt.Errorf("account transactions report is required")
	}
	cw := csv.NewWriter(w)
	defer cw.Flush()

	_ = cw.Write([]string{"Account Transactions"})
	_ = cw.Write([]string{"Account", report.AccountCode + " - " + report.AccountName})
	_ = cw.Write([]string{"Period", from.Format("2006-01-02") + " to " + to.Format("2006-01-02")})
	_ = cw.Write(nil)
	_ = cw.Write([]string{"Date", "Type", "No.", "Name", "Description", "Debit", "Credit", "Balance"})
	_ = cw.Write([]string{"", "Starting Balance", "", "", "", "", "", csvMoney(report.StartingBalance)})
	for _, row := range report.Rows {
		_ = cw.Write([]string{
			row.Date,
			row.TransactionTypeLabel,
			row.DocumentNumber,
			row.CounterpartyName,
			row.Description,
			csvMoneyBlankZero(row.Debit),
			csvMoneyBlankZero(row.Credit),
			csvMoney(row.Balance),
		})
	}
	_ = cw.Write([]string{
		"", "Totals and Ending Balance", "", "", "",
		csvMoneyBlankZero(report.TotalDebits),
		csvMoneyBlankZero(report.TotalCredits),
		csvMoney(report.EndingBalance),
	})
	_ = cw.Write([]string{
		"", "Balance Change", "", "", "", "", "",
		csvMoney(report.EndingBalance.Sub(report.StartingBalance)),
	})

	return cw.Error()
}

// ExportAccountTransactionsXLSX writes Account Transactions as a native Excel workbook.
func ExportAccountTransactionsXLSX(report *AccountTransactionsReport, from, to time.Time, w io.Writer) error {
	if report == nil {
		return fmt.Errorf("account transactions report is required")
	}
	f := excelize.NewFile()
	defer f.Close()

	defaultSheet := f.GetSheetName(0)
	const sheet = "Account Transactions"
	if err := f.SetSheetName(defaultSheet, sheet); err != nil {
		return err
	}

	titleStyle, _ := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true, Size: 16, Color: "111827"},
	})
	metaStyle, _ := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Color: "4B5563"},
	})
	headerStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "374151"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"E5E7EB"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center"},
	})
	moneyStyle, _ := f.NewStyle(&excelize.Style{
		NumFmt:    4,
		Alignment: &excelize.Alignment{Horizontal: "right"},
	})
	totalStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"F3F4F6"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "right"},
		NumFmt:    4,
	})
	totalLabelStyle, _ := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true},
		Fill: excelize.Fill{Type: "pattern", Color: []string{"F3F4F6"}, Pattern: 1},
	})

	_ = f.MergeCell(sheet, "A1", "H1")
	_ = f.SetCellValue(sheet, "A1", "Account Transactions")
	_ = f.SetCellStyle(sheet, "A1", "A1", titleStyle)
	_ = f.SetCellValue(sheet, "A2", "Account")
	_ = f.SetCellValue(sheet, "B2", report.AccountCode+" - "+report.AccountName)
	_ = f.SetCellValue(sheet, "A3", "Period")
	_ = f.SetCellValue(sheet, "B3", from.Format("2006-01-02")+" to "+to.Format("2006-01-02"))
	_ = f.SetCellStyle(sheet, "A2", "B3", metaStyle)

	headers := []string{"Date", "Type", "No.", "Name", "Description", "Debit", "Credit", "Balance"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 5)
		_ = f.SetCellValue(sheet, cell, h)
	}
	_ = f.SetCellStyle(sheet, "A5", "H5", headerStyle)

	rowNum := 6
	_ = f.SetCellValue(sheet, "A6", "Starting Balance")
	_ = f.MergeCell(sheet, "A6", "G6")
	setDecimalCell(f, sheet, "H6", report.StartingBalance)
	_ = f.SetCellStyle(sheet, "A6", "G6", totalLabelStyle)
	_ = f.SetCellStyle(sheet, "H6", "H6", totalStyle)
	rowNum++

	for _, row := range report.Rows {
		values := []any{
			row.Date,
			row.TransactionTypeLabel,
			row.DocumentNumber,
			row.CounterpartyName,
			row.Description,
		}
		for col, value := range values {
			cell, _ := excelize.CoordinatesToCellName(col+1, rowNum)
			_ = f.SetCellValue(sheet, cell, value)
		}
		setDecimalCellBlankZero(f, sheet, coord(6, rowNum), row.Debit)
		setDecimalCellBlankZero(f, sheet, coord(7, rowNum), row.Credit)
		setDecimalCell(f, sheet, coord(8, rowNum), row.Balance)
		_ = f.SetCellStyle(sheet, coord(6, rowNum), coord(8, rowNum), moneyStyle)
		rowNum++
	}

	_ = f.SetCellValue(sheet, coord(1, rowNum), "Totals and Ending Balance")
	_ = f.MergeCell(sheet, coord(1, rowNum), coord(5, rowNum))
	setDecimalCell(f, sheet, coord(6, rowNum), report.TotalDebits)
	setDecimalCell(f, sheet, coord(7, rowNum), report.TotalCredits)
	setDecimalCell(f, sheet, coord(8, rowNum), report.EndingBalance)
	_ = f.SetCellStyle(sheet, coord(1, rowNum), coord(5, rowNum), totalLabelStyle)
	_ = f.SetCellStyle(sheet, coord(6, rowNum), coord(8, rowNum), totalStyle)
	rowNum++

	_ = f.SetCellValue(sheet, coord(1, rowNum), "Balance Change")
	_ = f.MergeCell(sheet, coord(1, rowNum), coord(7, rowNum))
	setDecimalCell(f, sheet, coord(8, rowNum), report.EndingBalance.Sub(report.StartingBalance))
	_ = f.SetCellStyle(sheet, coord(1, rowNum), coord(7, rowNum), totalLabelStyle)
	_ = f.SetCellStyle(sheet, coord(8, rowNum), coord(8, rowNum), totalStyle)

	_ = f.SetColWidth(sheet, "A", "A", 14)
	_ = f.SetColWidth(sheet, "B", "B", 18)
	_ = f.SetColWidth(sheet, "C", "C", 18)
	_ = f.SetColWidth(sheet, "D", "D", 26)
	_ = f.SetColWidth(sheet, "E", "E", 42)
	_ = f.SetColWidth(sheet, "F", "H", 14)
	_ = f.SetPanes(sheet, &excelize.Panes{Freeze: true, YSplit: 5, TopLeftCell: "A6", ActivePane: "bottomLeft"})

	return f.Write(w)
}

// RenderAccountTransactionsPDF renders Account Transactions through the shared chromedp engine.
func RenderAccountTransactionsPDF(ctx context.Context, report *AccountTransactionsReport, from, to time.Time) ([]byte, error) {
	if report == nil {
		return nil, fmt.Errorf("account transactions report is required")
	}
	return pdf.RenderPDF(ctx, accountTransactionsPDFHTML(report, from, to))
}

func setDecimalCell(f *excelize.File, sheet, cell string, d decimal.Decimal) {
	v, _ := d.Float64()
	_ = f.SetCellValue(sheet, cell, v)
}

func setDecimalCellBlankZero(f *excelize.File, sheet, cell string, d decimal.Decimal) {
	if d.IsZero() {
		_ = f.SetCellValue(sheet, cell, nil)
		return
	}
	v, _ := d.Float64()
	_ = f.SetCellValue(sheet, cell, v)
}

func coord(col, row int) string {
	cell, _ := excelize.CoordinatesToCellName(col, row)
	return cell
}

func accountTransactionsPDFHTML(report *AccountTransactionsReport, from, to time.Time) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><meta charset="utf-8"><style>
@page{size:A4 landscape;margin:14mm}
body{font-family:Arial,Helvetica,sans-serif;color:#111827;font-size:10px}
h1{font-size:20px;margin:0 0 4px}
.muted{color:#6b7280}
.meta{margin:0 0 14px}
table{width:100%;border-collapse:collapse}
th{background:#f3f4f6;color:#374151;text-align:left;font-weight:700}
th,td{border-bottom:1px solid #e5e7eb;padding:6px 7px;vertical-align:top}
.num{text-align:right;font-variant-numeric:tabular-nums;white-space:nowrap}
.total td{background:#f9fafb;font-weight:700}
</style></head><body>`)
	b.WriteString(`<h1>Account Transactions</h1>`)
	b.WriteString(`<p class="meta"><strong>Account:</strong> ` + esc(report.AccountCode+" - "+report.AccountName) + `<br>`)
	b.WriteString(`<strong>Period:</strong> ` + from.Format("2006-01-02") + ` to ` + to.Format("2006-01-02") + `</p>`)
	b.WriteString(`<table><thead><tr>`)
	for _, h := range []string{"Date", "Type", "No.", "Name", "Description", "Debit", "Credit", "Balance"} {
		b.WriteString(`<th>` + h + `</th>`)
	}
	b.WriteString(`</tr></thead><tbody>`)
	b.WriteString(`<tr class="total"><td colspan="7">Starting Balance</td><td class="num">` + csvMoney(report.StartingBalance) + `</td></tr>`)
	for _, row := range report.Rows {
		b.WriteString(`<tr>`)
		b.WriteString(`<td>` + esc(row.Date) + `</td>`)
		b.WriteString(`<td>` + esc(row.TransactionTypeLabel) + `</td>`)
		b.WriteString(`<td>` + esc(row.DocumentNumber) + `</td>`)
		b.WriteString(`<td>` + emptyDash(row.CounterpartyName) + `</td>`)
		b.WriteString(`<td>` + emptyDash(row.Description) + `</td>`)
		b.WriteString(`<td class="num">` + csvMoneyBlankZero(row.Debit) + `</td>`)
		b.WriteString(`<td class="num">` + csvMoneyBlankZero(row.Credit) + `</td>`)
		b.WriteString(`<td class="num">` + csvMoney(row.Balance) + `</td>`)
		b.WriteString(`</tr>`)
	}
	b.WriteString(`<tr class="total"><td colspan="5">Totals and Ending Balance</td>`)
	b.WriteString(`<td class="num">` + csvMoneyBlankZero(report.TotalDebits) + `</td>`)
	b.WriteString(`<td class="num">` + csvMoneyBlankZero(report.TotalCredits) + `</td>`)
	b.WriteString(`<td class="num">` + csvMoney(report.EndingBalance) + `</td></tr>`)
	b.WriteString(`<tr class="total"><td colspan="7">Balance Change</td><td class="num">` + csvMoney(report.EndingBalance.Sub(report.StartingBalance)) + `</td></tr>`)
	b.WriteString(`</tbody></table></body></html>`)
	return b.String()
}

func esc(s string) string {
	return html.EscapeString(s)
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return `<span class="muted">-</span>`
	}
	return esc(s)
}
