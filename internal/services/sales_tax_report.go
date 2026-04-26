// 遵循project_guide.md
package services

import (
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// ── Sales Tax Report types ────────────────────────────────────────────────────

// SalesTaxSummaryRow is one row in the SALES & PURCHASES section.
// One row per active tax code. All amounts are in the company's base currency.
type SalesTaxSummaryRow struct {
	TaxCodeID             uint
	TaxCodeCode           string          // e.g. "GST"
	TaxCodeName           string          // e.g. "Goods & Services Tax (5%)"
	SalesTaxAccountID     uint            // for the drill-down link
	SalesSubjectToTax     decimal.Decimal // Σ invoice_line.line_net
	TaxAmountOnSales      decimal.Decimal // Σ invoice_line.line_tax
	PurchasesSubjectToTax decimal.Decimal // Σ bill_line.line_net
	TaxAmountOnPurchases  decimal.Decimal // Σ bill_line.line_tax
	NetTaxOwing           decimal.Decimal // TaxOnSales − TaxOnPurchases
}

// SalesTaxBalanceRow is one row in the PAYMENTS & BALANCES OWING section.
type SalesTaxBalanceRow struct {
	TaxCodeID         uint
	TaxCodeCode       string
	TaxCodeName       string
	SalesTaxAccountID uint
	StartingBalance   decimal.Decimal // GL credit-normal balance before period start
	NetTaxOwing       decimal.Decimal // same as SalesTaxSummaryRow.NetTaxOwing
	LessPayments      decimal.Decimal // residual GL movements (tax remittances etc.)
	EndingBalance     decimal.Decimal // GL credit-normal balance at end of period
}

// SalesTaxSummaryTotals holds column totals for the SALES & PURCHASES table.
type SalesTaxSummaryTotals struct {
	SalesSubjectToTax     decimal.Decimal
	TaxAmountOnSales      decimal.Decimal
	PurchasesSubjectToTax decimal.Decimal
	TaxAmountOnPurchases  decimal.Decimal
	NetTaxOwing           decimal.Decimal
}

// SalesTaxBalanceTotals holds column totals for the PAYMENTS & BALANCES table.
type SalesTaxBalanceTotals struct {
	StartingBalance decimal.Decimal
	NetTaxOwing     decimal.Decimal
	LessPayments    decimal.Decimal
	EndingBalance   decimal.Decimal
}

// ── BuildSalesTaxReport ───────────────────────────────────────────────────────

// BuildSalesTaxReport builds both sections of the Sales Tax Report for the
// given company and date range. Uses accrual basis: invoices are included when
// status is issued/sent/partially_paid/paid/overdue; bills when posted/partially_paid/paid.
//
// Returns nil slices (not an error) when the company has no active tax codes.
func BuildSalesTaxReport(
	db *gorm.DB,
	companyID uint,
	fromDate, toDate time.Time,
) (
	summaryRows []SalesTaxSummaryRow,
	balanceRows []SalesTaxBalanceRow,
	summaryTotals SalesTaxSummaryTotals,
	balanceTotals SalesTaxBalanceTotals,
	err error,
) {
	// ── 1. Load all active tax codes ─────────────────────────────────────────
	type tcRow struct {
		ID                uint
		Code              string
		Name              string
		SalesTaxAccountID uint
	}
	var tcs []tcRow
	if err = db.Raw(`
		SELECT id, code, name, sales_tax_account_id
		FROM tax_codes
		WHERE company_id = ? AND is_active = true
		ORDER BY code ASC
	`, companyID).Scan(&tcs).Error; err != nil || len(tcs) == 0 {
		return
	}

	// Index by ID for quick lookup.
	rowMap := make(map[uint]int, len(tcs))
	summaryRows = make([]SalesTaxSummaryRow, len(tcs))
	for i, tc := range tcs {
		rowMap[tc.ID] = i
		summaryRows[i] = SalesTaxSummaryRow{
			TaxCodeID:         tc.ID,
			TaxCodeCode:       tc.Code,
			TaxCodeName:       tc.Name,
			SalesTaxAccountID: tc.SalesTaxAccountID,
		}
	}

	// ── 2. Sales aggregation (invoice_lines) ─────────────────────────────────
	type salesAgg struct {
		TaxCodeID uint
		SalesNet  decimal.Decimal
		SalesTax  decimal.Decimal
	}
	var salesAggs []salesAgg
	if err = db.Raw(`
		SELECT il.tax_code_id,
		       COALESCE(SUM(il.line_net), 0) AS sales_net,
		       COALESCE(SUM(il.line_tax), 0) AS sales_tax
		FROM invoice_lines il
		JOIN invoices inv ON inv.id = il.invoice_id
		WHERE inv.company_id = ?
		  AND il.tax_code_id IS NOT NULL
		  AND inv.status IN ('issued','sent','partially_paid','paid','overdue')
		  AND inv.invoice_date >= ?
		  AND inv.invoice_date <= ?
		GROUP BY il.tax_code_id
	`, companyID, fromDate, toDate).Scan(&salesAggs).Error; err != nil {
		return
	}
	for _, sa := range salesAggs {
		if idx, ok := rowMap[sa.TaxCodeID]; ok {
			summaryRows[idx].SalesSubjectToTax = sa.SalesNet
			summaryRows[idx].TaxAmountOnSales = sa.SalesTax
		}
	}

	// ── 3. Purchases aggregation (bill_lines) ─────────────────────────────────
	type purchaseAgg struct {
		TaxCodeID    uint
		PurchasesNet decimal.Decimal
		PurchasesTax decimal.Decimal
	}
	var purchaseAggs []purchaseAgg
	if err = db.Raw(`
		SELECT bl.tax_code_id,
		       COALESCE(SUM(bl.line_net), 0) AS purchases_net,
		       COALESCE(SUM(bl.line_tax), 0) AS purchases_tax
		FROM bill_lines bl
		JOIN bills b ON b.id = bl.bill_id
		WHERE b.company_id = ?
		  AND bl.tax_code_id IS NOT NULL
		  AND b.status IN ('posted','partially_paid','paid')
		  AND b.bill_date >= ?
		  AND b.bill_date <= ?
		GROUP BY bl.tax_code_id
	`, companyID, fromDate, toDate).Scan(&purchaseAggs).Error; err != nil {
		return
	}
	for _, pa := range purchaseAggs {
		if idx, ok := rowMap[pa.TaxCodeID]; ok {
			summaryRows[idx].PurchasesSubjectToTax = pa.PurchasesNet
			summaryRows[idx].TaxAmountOnPurchases = pa.PurchasesTax
		}
	}

	// ── 4. Compute NetTaxOwing + summary totals ───────────────────────────────
	for i := range summaryRows {
		r := &summaryRows[i]
		r.NetTaxOwing = r.TaxAmountOnSales.Sub(r.TaxAmountOnPurchases)
		summaryTotals.SalesSubjectToTax = summaryTotals.SalesSubjectToTax.Add(r.SalesSubjectToTax)
		summaryTotals.TaxAmountOnSales = summaryTotals.TaxAmountOnSales.Add(r.TaxAmountOnSales)
		summaryTotals.PurchasesSubjectToTax = summaryTotals.PurchasesSubjectToTax.Add(r.PurchasesSubjectToTax)
		summaryTotals.TaxAmountOnPurchases = summaryTotals.TaxAmountOnPurchases.Add(r.TaxAmountOnPurchases)
		summaryTotals.NetTaxOwing = summaryTotals.NetTaxOwing.Add(r.NetTaxOwing)
	}

	// ── 5. GL balances for each unique SalesTaxAccountID ─────────────────────
	accountIDs := make([]uint, 0, len(tcs))
	seen := make(map[uint]bool, len(tcs))
	for _, r := range summaryRows {
		if r.SalesTaxAccountID != 0 && !seen[r.SalesTaxAccountID] {
			seen[r.SalesTaxAccountID] = true
			accountIDs = append(accountIDs, r.SalesTaxAccountID)
		}
	}

	type glRow struct {
		AccountID      uint
		StartingDebit  decimal.Decimal
		StartingCredit decimal.Decimal
		EndingDebit    decimal.Decimal
		EndingCredit   decimal.Decimal
	}
	var glRows []glRow
	if len(accountIDs) > 0 {
		if err = db.Raw(`
			SELECT jl.account_id,
			       COALESCE(SUM(CASE WHEN je.entry_date <  ? THEN jl.debit  ELSE 0 END), 0) AS starting_debit,
			       COALESCE(SUM(CASE WHEN je.entry_date <  ? THEN jl.credit ELSE 0 END), 0) AS starting_credit,
			       COALESCE(SUM(CASE WHEN je.entry_date <= ? THEN jl.debit  ELSE 0 END), 0) AS ending_debit,
			       COALESCE(SUM(CASE WHEN je.entry_date <= ? THEN jl.credit ELSE 0 END), 0) AS ending_credit
			FROM journal_lines jl
			JOIN journal_entries je ON je.id = jl.journal_entry_id
			WHERE je.company_id = ?
			  AND `+reportableJournalEntryWhere+`
			  AND jl.account_id IN ?
			GROUP BY jl.account_id
		`, fromDate, fromDate, toDate, toDate, companyID, accountIDs).Scan(&glRows).Error; err != nil {
			return
		}
	}

	// Build balance map: accountID → (starting, ending) credit-normal.
	type balPair struct{ start, end decimal.Decimal }
	balMap := make(map[uint]balPair, len(glRows))
	for _, g := range glRows {
		// credit-normal: positive = owe money (normal for a tax liability)
		balMap[g.AccountID] = balPair{
			start: g.StartingCredit.Sub(g.StartingDebit),
			end:   g.EndingCredit.Sub(g.EndingDebit),
		}
	}

	// ── 6. Build balance rows ─────────────────────────────────────────────────
	balanceRows = make([]SalesTaxBalanceRow, len(summaryRows))
	for i, r := range summaryRows {
		bp := balMap[r.SalesTaxAccountID]
		// LessPayments = what was paid to the government during the period.
		// Residual = Starting + NetTaxOwing - Ending
		// (works when ITC uses the same account as the sales tax account)
		lessPayments := bp.start.Add(r.NetTaxOwing).Sub(bp.end)
		if lessPayments.IsNegative() {
			lessPayments = decimal.Zero // a refund received; do not show negative
		}
		balanceRows[i] = SalesTaxBalanceRow{
			TaxCodeID:         r.TaxCodeID,
			TaxCodeCode:       r.TaxCodeCode,
			TaxCodeName:       r.TaxCodeName,
			SalesTaxAccountID: r.SalesTaxAccountID,
			StartingBalance:   bp.start,
			NetTaxOwing:       r.NetTaxOwing,
			LessPayments:      lessPayments,
			EndingBalance:     bp.end,
		}
		balanceTotals.StartingBalance = balanceTotals.StartingBalance.Add(bp.start)
		balanceTotals.NetTaxOwing = balanceTotals.NetTaxOwing.Add(r.NetTaxOwing)
		balanceTotals.LessPayments = balanceTotals.LessPayments.Add(lessPayments)
		balanceTotals.EndingBalance = balanceTotals.EndingBalance.Add(bp.end)
	}

	return
}
