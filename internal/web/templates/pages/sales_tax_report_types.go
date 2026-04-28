// 遵循project_guide.md
package pages

import "balanciz/internal/services"

// SalesTaxReportVM is the view-model for Reports → Sales Tax Report.
type SalesTaxReportVM struct {
	HasCompany bool

	// Filter state (echoed back to template).
	Preset     string // "year_to_date", "last_month", etc.
	DateFrom   string // "2026-01-01"
	DateTo     string // "2026-04-13"

	// Human-readable date range label for the table header.
	DateLabel string // "Jan 01, 2026 to Apr 13, 2026"

	// SALES & PURCHASES section.
	SummaryRows   []services.SalesTaxSummaryRow
	SummaryTotals services.SalesTaxSummaryTotals

	// PAYMENTS & BALANCES OWING section.
	BalanceRows   []services.SalesTaxBalanceRow
	BalanceTotals services.SalesTaxBalanceTotals

	FormError string

	Toolbar ReportToolbarVM
}

// AccountTransactionsVM is the view-model for the per-account ledger drill-down.
type AccountTransactionsVM struct {
	HasCompany bool

	// Filter state.
	Preset   string
	DateFrom string
	DateTo   string

	// Loaded report (nil on error or before first run).
	Report *services.AccountTransactionsReport

	// Available accounts for the selector dropdown.
	// Only populated when the account_id param is provided.
	AccountID uint

	FormError string

	Toolbar ReportToolbarVM
}
