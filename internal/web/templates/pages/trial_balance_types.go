// 遵循project_guide.md
package pages

import (
	"time"

	"gobooks/internal/services"
)

// ── Report Toolbar VM ─────────────────────────────────────────────────────────

// ReportToolbarHiddenInput is one Name/Value pair the report toolbar must
// re-emit as `<input type="hidden">` so the value survives GET form submit.
type ReportToolbarHiddenInput struct {
	Name  string
	Value string
}

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
	// Export URLs are base URLs without query params. Empty values are hidden.
	// The toolbar appends the current date params plus HiddenInputs.
	CSVExportURL   string
	ExcelExportURL string
	PDFExportURL   string
	// FormAction is the GET action for the report form (e.g. "/reports/income-statement").
	FormAction string
	// HiddenInputs carries extra `<input type="hidden">` pairs that must
	// survive form submission. Use this for context the toolbar itself
	// doesn't know about — e.g. Account Transactions stamps account_id
	// here so the GET form roundtrip doesn't drop it (browsers REPLACE
	// the action's query string with form inputs on submit, so query
	// params on FormAction alone are silently lost).
	HiddenInputs []ReportToolbarHiddenInput
	// Mode is "period" (shows From + To) or "asof" (shows As Of only).
	Mode string
	// Source is "cache", "recomputed", or "mixed" when the rendered page can
	// tell the user where the current result came from.
	Source string
	// FreshnessLabel gives the user a human-readable freshness hint.
	FreshnessLabel string
}

// ── Report page VMs ───────────────────────────────────────────────────────────

type TrialBalanceVM struct {
	HasCompany bool

	From string
	To   string
	// FromTime / ToTime are the parsed dates used by the templ to build
	// per-row drill URLs into Account Transactions. Same period as the
	// From / To string fields above; populated by the handler.
	FromTime time.Time
	ToTime   time.Time

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

	Report services.IncomeStatement

	FormError string

	Toolbar ReportToolbarVM
}

type BalanceSheetVM struct {
	HasCompany bool

	AsOf string

	Report services.BalanceSheet

	FormError string

	// AsOfTime is the parsed time.Time for use in templates.
	AsOfTime time.Time

	Toolbar ReportToolbarVM
}

type ARAgingVM struct {
	HasCompany bool

	AsOf string

	Report services.ARAgingReport

	FormError string

	Toolbar ReportToolbarVM
}

func reportSourceLabel(source string) string {
	switch source {
	case "cache":
		return "Source: cache"
	case "recomputed":
		return "Source: recomputed"
	case "mixed":
		return "Source: mixed"
	default:
		return ""
	}
}
