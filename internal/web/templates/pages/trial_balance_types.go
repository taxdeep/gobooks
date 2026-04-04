// 遵循project_guide.md
package pages

import (
	"time"

	"gobooks/internal/services"
)

// ── Report Toolbar VM ─────────────────────────────────────────────────────────

// ReportToolbarVM carries all data needed by the unified report toolbar component.
type ReportToolbarVM struct {
	// Preset is the currently active period preset (one of the PresetXxx constants
	// in services/report_periods.go, or "custom").
	Preset string
	// From and To are YYYY-MM-DD strings for period-range reports (P&L, Trial Balance).
	From string
	To   string
	// AsOf is a YYYY-MM-DD string for point-in-time reports (Balance Sheet).
	AsOf string
	// FiscalYearEnd is "MM-DD" (e.g. "12-31"), embedded so Alpine.js can compute
	// period dates on the client side without an extra round-trip.
	FiscalYearEnd string
	// CompanyName and ReportTitle appear in the print-only header.
	CompanyName string
	ReportTitle string
	// CSVExportURL is the base URL for CSV export (without query params).
	// Empty = Export CSV button is hidden.
	CSVExportURL string
	// FormAction is the GET action for the report form (e.g. "/reports/income-statement").
	FormAction string
	// Mode is "period" (shows From + To) or "asof" (shows As Of only).
	Mode string
}

// ── Report page VMs ───────────────────────────────────────────────────────────

type TrialBalanceVM struct {
	HasCompany bool

	From string
	To   string

	ActiveTab string

	Rows []services.TrialBalanceRow

	TotalDebits  string
	TotalCredits string

	FormError string

	Toolbar ReportToolbarVM
}

type IncomeStatementVM struct {
	HasCompany bool

	From string
	To   string

	ActiveTab string

	Report services.IncomeStatement

	FormError string

	Toolbar ReportToolbarVM
}

type BalanceSheetVM struct {
	HasCompany bool

	AsOf string

	ActiveTab string

	Report services.BalanceSheet

	FormError string

	// AsOfTime is the parsed time.Time for use in templates.
	AsOfTime time.Time

	Toolbar ReportToolbarVM
}
