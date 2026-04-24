// 遵循project_guide.md
package services

import (
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// CounterpartySummaryRow is one line in a "by Customer" / "by Vendor"
// summary report. Generic shape so the same templ component renders
// both Sales by Customer and Expense by Vendor without duplication.
type CounterpartySummaryRow struct {
	CounterpartyID   uint            // customer_id or vendor_id
	CounterpartyName string
	DocumentCount    int             // invoices / bills+expenses count
	TotalAmount      decimal.Decimal // sum of document amounts
	AverageAmount    decimal.Decimal // TotalAmount / DocumentCount
}

// CounterpartySummaryReport groups the rows + grand totals.
type CounterpartySummaryReport struct {
	FromDate    time.Time
	ToDate      time.Time
	Rows        []CounterpartySummaryRow
	GrandTotal  decimal.Decimal
	GrandCount  int
}

// BuildSalesByCustomerReport aggregates posted (non-voided) invoices
// by customer for the period. Returns rows sorted by TotalAmount
// descending so the highest-revenue customers appear first.
//
// "Sales" definition here = invoice.Amount sum. Doesn't subtract
// credit notes or returns — that's a separate "Net Sales" report
// that needs explicit accounting input on whether returns reduce
// gross sales (Canadian/US convention) or are tracked separately.
func BuildSalesByCustomerReport(db *gorm.DB, companyID uint, fromDate, toDate time.Time) (*CounterpartySummaryReport, error) {
	type row struct {
		CustomerID uint
		Name       string
		Cnt        int
		Total      decimal.Decimal
	}
	var rows []row
	if err := db.Raw(`
		SELECT inv.customer_id   AS customer_id,
		       cust.name         AS name,
		       COUNT(*)          AS cnt,
		       SUM(inv.amount)   AS total
		FROM invoices inv
		LEFT JOIN customers cust ON cust.id = inv.customer_id
		WHERE inv.company_id = ?
		  AND inv.status NOT IN ('draft', 'voided')
		  AND inv.invoice_date >= ?
		  AND inv.invoice_date <= ?
		GROUP BY inv.customer_id, cust.name
		ORDER BY total DESC, cust.name ASC
	`, companyID, fromDate, toDate).Scan(&rows).Error; err != nil {
		return nil, err
	}

	report := &CounterpartySummaryReport{FromDate: fromDate, ToDate: toDate}
	for _, r := range rows {
		summary := CounterpartySummaryRow{
			CounterpartyID:   r.CustomerID,
			CounterpartyName: r.Name,
			DocumentCount:    r.Cnt,
			TotalAmount:      r.Total,
		}
		if r.Cnt > 0 {
			summary.AverageAmount = r.Total.DivRound(decimal.NewFromInt(int64(r.Cnt)), 2)
		}
		report.Rows = append(report.Rows, summary)
		report.GrandTotal = report.GrandTotal.Add(r.Total)
		report.GrandCount += r.Cnt
	}
	return report, nil
}

// BuildExpenseByVendorReport aggregates posted bills + posted expenses
// by vendor for the period. Bills and expenses are summed together
// because they're conceptually the same thing for "what did I spend
// at this vendor" purposes — both are AP-side outflows.
func BuildExpenseByVendorReport(db *gorm.DB, companyID uint, fromDate, toDate time.Time) (*CounterpartySummaryReport, error) {
	type row struct {
		VendorID uint
		Name     string
		Cnt      int
		Total    decimal.Decimal
	}

	// Use UNION ALL so the bills + expenses contribute to the same
	// vendor's row. The outer GROUP BY collapses them into one
	// CounterpartySummaryRow per vendor.
	var rows []row
	if err := db.Raw(`
		SELECT v.id                AS vendor_id,
		       v.name              AS name,
		       SUM(s.cnt)          AS cnt,
		       SUM(s.amount)       AS total
		FROM (
			SELECT vendor_id, COUNT(*) AS cnt, SUM(amount) AS amount
			FROM bills
			WHERE company_id = ?
			  AND status NOT IN ('draft', 'voided')
			  AND bill_date >= ? AND bill_date <= ?
			GROUP BY vendor_id

			UNION ALL

			SELECT vendor_id, COUNT(*) AS cnt, SUM(amount) AS amount
			FROM expenses
			WHERE company_id = ?
			  AND status NOT IN ('draft', 'voided')
			  AND expense_date >= ? AND expense_date <= ?
			GROUP BY vendor_id
		) s
		LEFT JOIN vendors v ON v.id = s.vendor_id
		GROUP BY v.id, v.name
		ORDER BY total DESC, v.name ASC
	`, companyID, fromDate, toDate, companyID, fromDate, toDate).Scan(&rows).Error; err != nil {
		return nil, err
	}

	report := &CounterpartySummaryReport{FromDate: fromDate, ToDate: toDate}
	for _, r := range rows {
		summary := CounterpartySummaryRow{
			CounterpartyID:   r.VendorID,
			CounterpartyName: r.Name,
			DocumentCount:    r.Cnt,
			TotalAmount:      r.Total,
		}
		if r.Cnt > 0 {
			summary.AverageAmount = r.Total.DivRound(decimal.NewFromInt(int64(r.Cnt)), 2)
		}
		report.Rows = append(report.Rows, summary)
		report.GrandTotal = report.GrandTotal.Add(r.Total)
		report.GrandCount += r.Cnt
	}
	return report, nil
}
