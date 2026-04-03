// 遵循project_guide.md
package services

import (
	"time"

	"gobooks/internal/models"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// TrialBalanceRow is one line in a Trial Balance report.
type TrialBalanceRow struct {
	Code           string
	Name           string
	Classification string
	Debit          decimal.Decimal
	Credit         decimal.Decimal
}

// TrialBalance returns balances per account for a date range (inclusive).
func TrialBalance(db *gorm.DB, companyID uint, fromDate, toDate time.Time) ([]TrialBalanceRow, decimal.Decimal, decimal.Decimal, error) {
	type row struct {
		Code   string
		Name   string
		Root   string
		Detail string
		Debit  decimal.Decimal
		Credit decimal.Decimal
	}

	var sums []row
	err := db.Raw(
		`
SELECT
  a.code AS code,
  a.name AS name,
  a.root_account_type AS root,
  a.detail_account_type AS detail,
  COALESCE(bal.debit, 0)  AS debit,
  COALESCE(bal.credit, 0) AS credit
FROM accounts a
LEFT JOIN (
  SELECT jl.account_id,
    SUM(jl.debit)  AS debit,
    SUM(jl.credit) AS credit
  FROM journal_lines jl
  INNER JOIN journal_entries je ON je.id = jl.journal_entry_id
  WHERE je.entry_date >= ? AND je.entry_date < ?
    AND je.company_id = ?
    AND je.status = 'posted'
  GROUP BY jl.account_id
) bal ON bal.account_id = a.id
WHERE a.company_id = ?
GROUP BY a.code, a.name, a.root_account_type, a.detail_account_type
ORDER BY a.code ASC
`,
		fromDate,
		toDate.AddDate(0, 0, 1),
		companyID,
		companyID,
	).Scan(&sums).Error
	if err != nil {
		return nil, decimal.Zero, decimal.Zero, err
	}

	var out []TrialBalanceRow
	totalDebits := decimal.Zero
	totalCredits := decimal.Zero

	for _, s := range sums {
		net := s.Debit.Sub(s.Credit)
		root := models.RootAccountType(s.Root)
		detail := models.DetailAccountType(s.Detail)
		label := models.ClassificationDisplay(root, detail)
		r := TrialBalanceRow{Code: s.Code, Name: s.Name, Classification: label, Debit: decimal.Zero, Credit: decimal.Zero}
		if net.GreaterThanOrEqual(decimal.Zero) {
			r.Debit = net
			totalDebits = totalDebits.Add(net)
		} else {
			r.Credit = net.Neg()
			totalCredits = totalCredits.Add(net.Neg())
		}
		out = append(out, r)
	}

	return out, totalDebits, totalCredits, nil
}

// IncomeStatementLine is one line item in Income Statement sections.
type IncomeStatementLine struct {
	Code   string
	Name   string
	Amount decimal.Decimal
}

type IncomeStatement struct {
	FromDate time.Time
	ToDate   time.Time

	Revenue     []IncomeStatementLine
	CostOfSales []IncomeStatementLine
	Expenses    []IncomeStatementLine

	TotalRevenue     decimal.Decimal
	TotalCostOfSales decimal.Decimal
	TotalExpenses    decimal.Decimal

	GrossProfit decimal.Decimal
	NetIncome   decimal.Decimal
}

// IncomeStatement builds a simple income statement for a date range.
func IncomeStatementReport(db *gorm.DB, companyID uint, fromDate, toDate time.Time) (IncomeStatement, error) {
	report := IncomeStatement{FromDate: fromDate, ToDate: toDate}

	type row struct {
		Code   string
		Name   string
		Root   string
		Debit  decimal.Decimal
		Credit decimal.Decimal
	}
	var sums []row

	err := db.Raw(
		`
SELECT
  a.code AS code,
  a.name AS name,
  a.root_account_type AS root,
  COALESCE(bal.debit, 0)  AS debit,
  COALESCE(bal.credit, 0) AS credit
FROM accounts a
LEFT JOIN (
  SELECT jl.account_id,
    SUM(jl.debit)  AS debit,
    SUM(jl.credit) AS credit
  FROM journal_lines jl
  INNER JOIN journal_entries je ON je.id = jl.journal_entry_id
  WHERE je.entry_date >= ? AND je.entry_date < ?
    AND je.company_id = ?
    AND je.status = 'posted'
  GROUP BY jl.account_id
) bal ON bal.account_id = a.id
WHERE a.company_id = ? AND a.root_account_type IN ('revenue', 'cost_of_sales', 'expense')
GROUP BY a.code, a.name, a.root_account_type
ORDER BY a.code ASC
`,
		fromDate,
		toDate.AddDate(0, 0, 1),
		companyID,
		companyID,
	).Scan(&sums).Error
	if err != nil {
		return IncomeStatement{}, err
	}

	for _, s := range sums {
		root := models.RootAccountType(s.Root)
		switch root {
		case models.RootRevenue:
			amt := s.Credit.Sub(s.Debit)
			if !amt.IsZero() {
				report.Revenue = append(report.Revenue, IncomeStatementLine{Code: s.Code, Name: s.Name, Amount: amt})
			}
			report.TotalRevenue = report.TotalRevenue.Add(amt)
		case models.RootCostOfSales:
			amt := s.Debit.Sub(s.Credit)
			if !amt.IsZero() {
				report.CostOfSales = append(report.CostOfSales, IncomeStatementLine{Code: s.Code, Name: s.Name, Amount: amt})
			}
			report.TotalCostOfSales = report.TotalCostOfSales.Add(amt)
		case models.RootExpense:
			amt := s.Debit.Sub(s.Credit)
			if !amt.IsZero() {
				report.Expenses = append(report.Expenses, IncomeStatementLine{Code: s.Code, Name: s.Name, Amount: amt})
			}
			report.TotalExpenses = report.TotalExpenses.Add(amt)
		}
	}

	report.GrossProfit = report.TotalRevenue.Sub(report.TotalCostOfSales)
	report.NetIncome = report.GrossProfit.Sub(report.TotalExpenses)

	return report, nil
}

type BalanceSheetLine struct {
	Code   string
	Name   string
	Amount decimal.Decimal
}

type BalanceSheet struct {
	AsOf time.Time

	Assets      []BalanceSheetLine
	Liabilities []BalanceSheetLine
	Equity      []BalanceSheetLine

	TotalAssets      decimal.Decimal
	TotalLiabilities decimal.Decimal
	TotalEquity      decimal.Decimal
}

// BalanceSheet builds a simple balance sheet as-of a date (inclusive).
func BalanceSheetReport(db *gorm.DB, companyID uint, asOf time.Time) (BalanceSheet, error) {
	report := BalanceSheet{AsOf: asOf}

	type row struct {
		Code   string
		Name   string
		Root   string
		Debit  decimal.Decimal
		Credit decimal.Decimal
	}
	var sums []row

	err := db.Raw(
		`
SELECT
  a.code AS code,
  a.name AS name,
  a.root_account_type AS root,
  COALESCE(bal.debit, 0)  AS debit,
  COALESCE(bal.credit, 0) AS credit
FROM accounts a
LEFT JOIN (
  SELECT jl.account_id,
    SUM(jl.debit)  AS debit,
    SUM(jl.credit) AS credit
  FROM journal_lines jl
  INNER JOIN journal_entries je ON je.id = jl.journal_entry_id
  WHERE je.entry_date < ?
    AND je.company_id = ?
    AND je.status = 'posted'
  GROUP BY jl.account_id
) bal ON bal.account_id = a.id
WHERE a.company_id = ? AND a.root_account_type IN ('asset', 'liability', 'equity')
GROUP BY a.code, a.name, a.root_account_type
ORDER BY a.code ASC
`,
		asOf.AddDate(0, 0, 1),
		companyID,
		companyID,
	).Scan(&sums).Error
	if err != nil {
		return BalanceSheet{}, err
	}

	for _, s := range sums {
		root := models.RootAccountType(s.Root)
		switch root {
		case models.RootAsset:
			amt := s.Debit.Sub(s.Credit)
			if !amt.IsZero() {
				report.Assets = append(report.Assets, BalanceSheetLine{Code: s.Code, Name: s.Name, Amount: amt})
			}
			report.TotalAssets = report.TotalAssets.Add(amt)
		case models.RootLiability:
			amt := s.Credit.Sub(s.Debit)
			if !amt.IsZero() {
				report.Liabilities = append(report.Liabilities, BalanceSheetLine{Code: s.Code, Name: s.Name, Amount: amt})
			}
			report.TotalLiabilities = report.TotalLiabilities.Add(amt)
		case models.RootEquity:
			amt := s.Credit.Sub(s.Debit)
			if !amt.IsZero() {
				report.Equity = append(report.Equity, BalanceSheetLine{Code: s.Code, Name: s.Name, Amount: amt})
			}
			report.TotalEquity = report.TotalEquity.Add(amt)
		}
	}

	return report, nil
}
