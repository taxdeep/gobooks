// 遵循project_guide.md
package services

import (
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// CashFlowSourceRow groups cash account movements by their originating
// source_type (Bill / Invoice / Customer Receipt / Manual JE / etc.) so
// the operator can answer "where did my cash come from" and "where did
// it go" at a glance.
type CashFlowSourceRow struct {
	SourceType  string // raw journal_entries.source_type ("" for manual)
	SourceLabel string // human label via transactionTypeLabel
	Inflow      decimal.Decimal // total DR to cash accounts (money in)
	Outflow     decimal.Decimal // total CR to cash accounts (money out)
	Net         decimal.Decimal // Inflow − Outflow
}

// CashAccountSummary is one cash/bank account's period activity.
type CashAccountSummary struct {
	AccountID       uint
	AccountCode     string
	AccountName     string
	OpeningBalance  decimal.Decimal
	TotalInflow     decimal.Decimal
	TotalOutflow    decimal.Decimal
	ClosingBalance  decimal.Decimal
}

// CashFlowReport is the operator-friendly cash flow summary. NOT a
// GAAP indirect-method statement of cash flows — this is the more
// useful "where did the cash actually move" view that small business
// operators reach for. Intentionally honest about its scope.
//
// Composition:
//   - Per-account opening / inflow / outflow / closing for every
//     bank-detail-type account (one row per cash account).
//   - Aggregate inflows + outflows grouped by source_type so the
//     operator sees "Customer Receipts: $X in, Bill Payments: $Y out".
//   - Net change in cash + opening / closing totals for the company.
type CashFlowReport struct {
	FromDate time.Time
	ToDate   time.Time

	Accounts []CashAccountSummary

	InflowBySource  []CashFlowSourceRow
	OutflowBySource []CashFlowSourceRow

	OpeningCash decimal.Decimal
	TotalInflow decimal.Decimal
	TotalOutflow decimal.Decimal
	NetChange   decimal.Decimal // Inflow − Outflow
	ClosingCash decimal.Decimal
}

// BuildCashFlowReport assembles the cash flow summary. Strategy:
//
//  1. Find every bank-detail-type account in the company. Treat all
//     of them collectively as "cash".
//  2. For each, compute opening balance (sum of JL through fromDate)
//     + period DR/CR + closing balance.
//  3. For the period, group the cash-account JEs by source_type and
//     sum DR / CR per group. DR to cash = inflow, CR from cash =
//     outflow.
//
// All sums use posted JEs only — drafts and voided JEs don't move cash.
func BuildCashFlowReport(db *gorm.DB, companyID uint, fromDate, toDate time.Time) (*CashFlowReport, error) {
	report := &CashFlowReport{FromDate: fromDate, ToDate: toDate}

	// ── 1. Identify cash accounts ─────────────────────────────────────
	type cashAcc struct {
		ID   uint
		Code string
		Name string
	}
	var cashAccounts []cashAcc
	if err := db.Raw(`
		SELECT id, code, name
		FROM accounts
		WHERE company_id = ?
		  AND root_account_type = ?
		  AND detail_account_type = ?
		ORDER BY code ASC, id ASC
	`, companyID, string(models.RootAsset), string(models.DetailBank)).Scan(&cashAccounts).Error; err != nil {
		return nil, err
	}

	if len(cashAccounts) == 0 {
		// No bank-type accounts → empty report. Caller renders a
		// "no cash accounts" hint rather than zeros everywhere.
		return report, nil
	}
	cashIDs := make([]uint, 0, len(cashAccounts))
	for _, a := range cashAccounts {
		cashIDs = append(cashIDs, a.ID)
	}

	// ── 2. Per-account opening + period sums ──────────────────────────
	type sumRow struct {
		AccountID    uint
		Debit        decimal.Decimal
		Credit       decimal.Decimal
	}
	openSums := map[uint]decimal.Decimal{}
	periodSums := map[uint]struct{ Debit, Credit decimal.Decimal }{}

	// Opening balance — all posted JEs strictly before fromDate.
	if !fromDate.IsZero() {
		var rows []sumRow
		if err := db.Raw(`
			SELECT jl.account_id   AS account_id,
			       SUM(jl.debit)   AS debit,
			       SUM(jl.credit)  AS credit
			FROM journal_lines jl
			JOIN journal_entries je ON je.id = jl.journal_entry_id
			WHERE jl.account_id IN ?
			  AND je.company_id = ?
			  AND je.status = 'posted'
			  AND je.entry_date < ?
			GROUP BY jl.account_id
		`, cashIDs, companyID, fromDate).Scan(&rows).Error; err == nil {
			for _, r := range rows {
				// Cash is debit-normal: balance = DR − CR.
				openSums[r.AccountID] = r.Debit.Sub(r.Credit)
			}
		}
	}

	// Period activity.
	{
		var rows []sumRow
		if err := db.Raw(`
			SELECT jl.account_id   AS account_id,
			       SUM(jl.debit)   AS debit,
			       SUM(jl.credit)  AS credit
			FROM journal_lines jl
			JOIN journal_entries je ON je.id = jl.journal_entry_id
			WHERE jl.account_id IN ?
			  AND je.company_id = ?
			  AND je.status = 'posted'
			  AND je.entry_date >= ?
			  AND je.entry_date <= ?
			GROUP BY jl.account_id
		`, cashIDs, companyID, fromDate, toDate).Scan(&rows).Error; err == nil {
			for _, r := range rows {
				periodSums[r.AccountID] = struct{ Debit, Credit decimal.Decimal }{r.Debit, r.Credit}
			}
		}
	}

	for _, a := range cashAccounts {
		opening := openSums[a.ID]
		period := periodSums[a.ID]
		summary := CashAccountSummary{
			AccountID:      a.ID,
			AccountCode:    a.Code,
			AccountName:    a.Name,
			OpeningBalance: opening,
			TotalInflow:    period.Debit,
			TotalOutflow:   period.Credit,
			ClosingBalance: opening.Add(period.Debit).Sub(period.Credit),
		}
		report.Accounts = append(report.Accounts, summary)
		report.OpeningCash = report.OpeningCash.Add(opening)
		report.TotalInflow = report.TotalInflow.Add(period.Debit)
		report.TotalOutflow = report.TotalOutflow.Add(period.Credit)
	}
	report.NetChange = report.TotalInflow.Sub(report.TotalOutflow)
	report.ClosingCash = report.OpeningCash.Add(report.NetChange)

	// ── 3. Period activity grouped by source_type ────────────────────
	type srcRow struct {
		SourceType string
		Debit      decimal.Decimal
		Credit     decimal.Decimal
	}
	var srcRows []srcRow
	if err := db.Raw(`
		SELECT je.source_type   AS source_type,
		       SUM(jl.debit)    AS debit,
		       SUM(jl.credit)   AS credit
		FROM journal_lines jl
		JOIN journal_entries je ON je.id = jl.journal_entry_id
		WHERE jl.account_id IN ?
		  AND je.company_id = ?
		  AND je.status = 'posted'
		  AND je.entry_date >= ?
		  AND je.entry_date <= ?
		GROUP BY je.source_type
	`, cashIDs, companyID, fromDate, toDate).Scan(&srcRows).Error; err != nil {
		return nil, err
	}

	for _, r := range srcRows {
		row := CashFlowSourceRow{
			SourceType:  r.SourceType,
			SourceLabel: transactionTypeLabel(r.SourceType),
			Inflow:      r.Debit,
			Outflow:     r.Credit,
			Net:         r.Debit.Sub(r.Credit),
		}
		// A source_type can produce both inflows AND outflows (e.g.
		// internal transfers). Place the row in the section where its
		// dominant direction is — keeps the two tables clean while still
		// reporting the full Net column.
		if r.Debit.GreaterThan(r.Credit) {
			report.InflowBySource = append(report.InflowBySource, row)
		} else {
			report.OutflowBySource = append(report.OutflowBySource, row)
		}
	}

	return report, nil
}
