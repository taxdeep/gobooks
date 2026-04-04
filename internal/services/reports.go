// 遵循project_guide.md
package services

// reports.go — Reporting Layer for GoBooks.
//
// Architecture:
//   All accounting reports are built on a single parameterized account-balance
//   query (accountBalances). Reports differ only in:
//     - which account root types they include (AccountBalanceFilter.RootTypes)
//     - whether they aggregate over a period (FromDate+ToDate) or cumulatively (ToDate only)
//     - how they interpret the raw debit/credit sums (normalBalance rule)
//
// Primitives available for future reports (AR Aging, Clearing, etc.):
//   AccountBalanceFilter  — standard query input
//   RawAccountBalance     — standard query output row
//   normalBalance()       — debit/credit → signed natural balance
//   accountBalances()     — single truth source SQL (unexported engine function)
//
// Existing function signatures (TrialBalance, IncomeStatementReport,
// BalanceSheetReport) are preserved; handlers and templates require no changes.

import (
	"time"

	"gobooks/internal/models"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// ── Reporting Layer primitives ────────────────────────────────────────────────

// AccountBalanceFilter is the standard input for the account-balance query.
//
// Period report  (Trial Balance, Income Statement): set FromDate + ToDate.
// Cumulative report (Balance Sheet):                set ToDate only; zero FromDate = no lower bound.
// Company scoping is always enforced; it is never optional.
type AccountBalanceFilter struct {
	CompanyID uint

	// FromDate is the inclusive lower bound on journal entry date.
	// Zero value means no lower bound (cumulative from inception).
	FromDate time.Time

	// ToDate is the inclusive upper bound on journal entry date.
	// Zero value means no upper bound.
	ToDate time.Time

	// RootTypes restricts which GL accounts are returned.
	// nil or empty slice = all root types (used by Trial Balance).
	RootTypes []models.RootAccountType
}

// RawAccountBalance is one row returned by the core account-balance query.
// Debit and Credit are the raw period sums from journal_lines; each report
// interprets them through normalBalance() to get the meaningful figure.
type RawAccountBalance struct {
	Code   string
	Name   string
	Root   models.RootAccountType
	Detail models.DetailAccountType
	// Debit and Credit are the raw SUM(debit) / SUM(credit) for the period.
	// Do not display these directly — pass through normalBalance() first.
	Debit  decimal.Decimal
	Credit decimal.Decimal
}

// normalBalance returns the natural-sign balance for an account.
//
// Accounting convention:
//   Debit-normal  (asset, cost_of_sales, expense): positive = debit excess → Debit − Credit
//   Credit-normal (revenue, liability, equity):    positive = credit excess → Credit − Debit
//
// A positive result means the account has a balance in its normal direction.
// A negative result means the account has an abnormal balance.
func normalBalance(root models.RootAccountType, debit, credit decimal.Decimal) decimal.Decimal {
	switch root {
	case models.RootAsset, models.RootCostOfSales, models.RootExpense:
		return debit.Sub(credit)
	default: // revenue, liability, equity
		return credit.Sub(debit)
	}
}

// accountBalances is the single truth source SQL for GL account balance aggregation.
// It returns one RawAccountBalance per account matching the filter.
//
// Rules enforced here (not in callers):
//   - Only posted journal entries (status = 'posted') are included.
//   - company_id is always scoped on both the subquery and the outer accounts table.
//   - Date bounds are applied only when non-zero.
//   - Account type filtering is applied only when RootTypes is non-empty.
func accountBalances(db *gorm.DB, f AccountBalanceFilter) ([]RawAccountBalance, error) {
	// Build the inner subquery WHERE clause for entry_date + company scoping.
	innerWhere := "je.status = 'posted' AND je.company_id = ?"
	innerArgs := []any{f.CompanyID}
	if !f.FromDate.IsZero() {
		innerWhere += " AND je.entry_date >= ?"
		innerArgs = append(innerArgs, f.FromDate)
	}
	if !f.ToDate.IsZero() {
		// ToDate is inclusive: use < (ToDate + 1 day).
		innerWhere += " AND je.entry_date < ?"
		innerArgs = append(innerArgs, f.ToDate.AddDate(0, 0, 1))
	}

	// Build the outer WHERE clause for account type filtering + company scoping.
	outerWhere := "a.company_id = ?"
	outerArgs := []any{f.CompanyID}
	if len(f.RootTypes) > 0 {
		rootStrs := make([]string, len(f.RootTypes))
		for i, r := range f.RootTypes {
			rootStrs[i] = string(r)
		}
		outerWhere += " AND a.root_account_type IN ?"
		outerArgs = append(outerArgs, rootStrs)
	}

	allArgs := append(innerArgs, outerArgs...)

	query := `
SELECT
  a.code                AS code,
  a.name                AS name,
  a.root_account_type   AS root,
  a.detail_account_type AS detail,
  COALESCE(bal.debit,  0) AS debit,
  COALESCE(bal.credit, 0) AS credit
FROM accounts a
LEFT JOIN (
  SELECT jl.account_id,
    SUM(jl.debit)  AS debit,
    SUM(jl.credit) AS credit
  FROM journal_lines jl
  INNER JOIN journal_entries je ON je.id = jl.journal_entry_id
  WHERE ` + innerWhere + `
  GROUP BY jl.account_id
) bal ON bal.account_id = a.id
WHERE ` + outerWhere + `
ORDER BY a.code ASC
`
	var rows []RawAccountBalance
	err := db.Raw(query, allArgs...).Scan(&rows).Error
	return rows, err
}

// ── Trial Balance ─────────────────────────────────────────────────────────────

// TrialBalanceRow is one line in a Trial Balance report.
type TrialBalanceRow struct {
	Code           string
	Name           string
	Classification string
	Debit          decimal.Decimal
	Credit         decimal.Decimal
}

// TrialBalance returns balances per account for a date range (inclusive).
// All account types are included. Each account's net balance (Debit−Credit)
// is placed in the Debit column if positive, Credit column if negative.
func TrialBalance(db *gorm.DB, companyID uint, fromDate, toDate time.Time) ([]TrialBalanceRow, decimal.Decimal, decimal.Decimal, error) {
	rows, err := accountBalances(db, AccountBalanceFilter{
		CompanyID: companyID,
		FromDate:  fromDate,
		ToDate:    toDate,
		// No RootTypes filter — Trial Balance includes all account types.
	})
	if err != nil {
		return nil, decimal.Zero, decimal.Zero, err
	}

	out := make([]TrialBalanceRow, 0, len(rows))
	totalDebits := decimal.Zero
	totalCredits := decimal.Zero

	for _, r := range rows {
		// TB convention: place net (Debit−Credit) in the Debit column when ≥ 0,
		// in the Credit column when < 0. This is independent of account type.
		net := r.Debit.Sub(r.Credit)
		label := models.ClassificationDisplay(r.Root, r.Detail)
		row := TrialBalanceRow{
			Code:           r.Code,
			Name:           r.Name,
			Classification: label,
			Debit:          decimal.Zero,
			Credit:         decimal.Zero,
		}
		if net.GreaterThanOrEqual(decimal.Zero) {
			row.Debit = net
			totalDebits = totalDebits.Add(net)
		} else {
			row.Credit = net.Neg()
			totalCredits = totalCredits.Add(net.Neg())
		}
		out = append(out, row)
	}

	return out, totalDebits, totalCredits, nil
}

// ── Income Statement ──────────────────────────────────────────────────────────

// IncomeStatementLine is one line item in Income Statement sections.
type IncomeStatementLine struct {
	Code   string
	Name   string
	Amount decimal.Decimal
}

// IncomeStatement is the full Income Statement for a period.
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

// IncomeStatementReport builds a period Income Statement.
// Amounts use normalBalance(): positive = balance in the account's natural direction.
func IncomeStatementReport(db *gorm.DB, companyID uint, fromDate, toDate time.Time) (IncomeStatement, error) {
	rows, err := accountBalances(db, AccountBalanceFilter{
		CompanyID: companyID,
		FromDate:  fromDate,
		ToDate:    toDate,
		RootTypes: []models.RootAccountType{
			models.RootRevenue,
			models.RootCostOfSales,
			models.RootExpense,
		},
	})
	if err != nil {
		return IncomeStatement{}, err
	}

	report := IncomeStatement{FromDate: fromDate, ToDate: toDate}

	for _, r := range rows {
		amt := normalBalance(r.Root, r.Debit, r.Credit)
		switch r.Root {
		case models.RootRevenue:
			if !amt.IsZero() {
				report.Revenue = append(report.Revenue, IncomeStatementLine{Code: r.Code, Name: r.Name, Amount: amt})
			}
			report.TotalRevenue = report.TotalRevenue.Add(amt)
		case models.RootCostOfSales:
			if !amt.IsZero() {
				report.CostOfSales = append(report.CostOfSales, IncomeStatementLine{Code: r.Code, Name: r.Name, Amount: amt})
			}
			report.TotalCostOfSales = report.TotalCostOfSales.Add(amt)
		case models.RootExpense:
			if !amt.IsZero() {
				report.Expenses = append(report.Expenses, IncomeStatementLine{Code: r.Code, Name: r.Name, Amount: amt})
			}
			report.TotalExpenses = report.TotalExpenses.Add(amt)
		}
	}

	report.GrossProfit = report.TotalRevenue.Sub(report.TotalCostOfSales)
	report.NetIncome = report.GrossProfit.Sub(report.TotalExpenses)

	return report, nil
}

// ── Balance Sheet ─────────────────────────────────────────────────────────────

// BalanceSheetLine is one account line in a Balance Sheet section.
type BalanceSheetLine struct {
	Code   string
	Name   string
	Amount decimal.Decimal
}

// BalanceSheet is the full Balance Sheet as of a point in time.
type BalanceSheet struct {
	AsOf time.Time

	Assets      []BalanceSheetLine
	Liabilities []BalanceSheetLine
	Equity      []BalanceSheetLine

	TotalAssets      decimal.Decimal
	TotalLiabilities decimal.Decimal
	TotalEquity      decimal.Decimal
}

// BalanceSheetReport builds a cumulative Balance Sheet as-of a date (inclusive).
// Amounts use normalBalance(): positive = balance in the account's natural direction.
func BalanceSheetReport(db *gorm.DB, companyID uint, asOf time.Time) (BalanceSheet, error) {
	rows, err := accountBalances(db, AccountBalanceFilter{
		CompanyID: companyID,
		// FromDate is zero — Balance Sheet is cumulative from inception.
		ToDate: asOf,
		RootTypes: []models.RootAccountType{
			models.RootAsset,
			models.RootLiability,
			models.RootEquity,
		},
	})
	if err != nil {
		return BalanceSheet{}, err
	}

	report := BalanceSheet{AsOf: asOf}

	for _, r := range rows {
		amt := normalBalance(r.Root, r.Debit, r.Credit)
		switch r.Root {
		case models.RootAsset:
			if !amt.IsZero() {
				report.Assets = append(report.Assets, BalanceSheetLine{Code: r.Code, Name: r.Name, Amount: amt})
			}
			report.TotalAssets = report.TotalAssets.Add(amt)
		case models.RootLiability:
			if !amt.IsZero() {
				report.Liabilities = append(report.Liabilities, BalanceSheetLine{Code: r.Code, Name: r.Name, Amount: amt})
			}
			report.TotalLiabilities = report.TotalLiabilities.Add(amt)
		case models.RootEquity:
			if !amt.IsZero() {
				report.Equity = append(report.Equity, BalanceSheetLine{Code: r.Code, Name: r.Name, Amount: amt})
			}
			report.TotalEquity = report.TotalEquity.Add(amt)
		}
	}

	return report, nil
}
