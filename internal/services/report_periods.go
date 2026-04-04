// 遵循project_guide.md
package services

import (
	"strconv"
	"strings"
	"time"
)

// ReportPreset identifies a named reporting period.
type ReportPreset string

const (
	PresetLastFiscalYear ReportPreset = "last_fiscal_year"
	PresetYearToDate     ReportPreset = "year_to_date"
	PresetLastMonth      ReportPreset = "last_month"
	PresetCustom         ReportPreset = "custom"
)

// ReportPeriod holds the resolved From / To dates for a reporting period.
// For point-in-time reports (Balance Sheet) use To as the "as of" date.
type ReportPeriod struct {
	From time.Time
	To   time.Time
}

// ParseFiscalYearEnd parses a "MM-DD" string into month and day.
// Returns (December, 31) on any parse failure.
func ParseFiscalYearEnd(mmdd string) (time.Month, int) {
	parts := strings.Split(strings.TrimSpace(mmdd), "-")
	if len(parts) != 2 {
		return time.December, 31
	}
	m, err1 := strconv.Atoi(parts[0])
	d, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || m < 1 || m > 12 || d < 1 || d > 31 {
		return time.December, 31
	}
	return time.Month(m), d
}

// ComputeReportPeriod returns the From/To date range for a named preset.
// fiscalYearEnd is "MM-DD" (e.g. "12-31"). today is the reference date.
// For PresetCustom the zero-value ReportPeriod is returned; callers must
// supply their own dates.
func ComputeReportPeriod(preset ReportPreset, fiscalYearEnd string, today time.Time) ReportPeriod {
	today = today.UTC().Truncate(24 * time.Hour)
	switch preset {
	case PresetLastMonth:
		return lastMonthRange(today)
	case PresetYearToDate:
		start := currentFiscalYearStart(fiscalYearEnd, today)
		return ReportPeriod{From: start, To: today}
	case PresetLastFiscalYear:
		return lastFiscalYearRange(fiscalYearEnd, today)
	default:
		return ReportPeriod{}
	}
}

// lastMonthRange returns the first and last calendar day of the month
// immediately before today.
func lastMonthRange(today time.Time) ReportPeriod {
	firstOfThisMonth := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	lastOfPrev := firstOfThisMonth.AddDate(0, 0, -1)
	firstOfPrev := time.Date(lastOfPrev.Year(), lastOfPrev.Month(), 1, 0, 0, 0, 0, time.UTC)
	return ReportPeriod{From: firstOfPrev, To: lastOfPrev}
}

// currentFiscalYearStart returns the first day of the fiscal year that
// contains today, based on the company's fiscal year end.
//
// Rule: the fiscal year ends on fyEndMonth/fyEndDay each calendar year.
//   - If today ≤ this calendar year's FY-end, the FY started the day after
//     last calendar year's FY-end.
//   - Otherwise the FY started the day after this calendar year's FY-end.
func currentFiscalYearStart(fiscalYearEnd string, today time.Time) time.Time {
	m, d := ParseFiscalYearEnd(fiscalYearEnd)
	fyEndThisYear := time.Date(today.Year(), m, d, 0, 0, 0, 0, time.UTC)
	if !today.After(fyEndThisYear) {
		// Still inside current FY (today ≤ FY end): FY started last year.
		fyEndPrevYear := time.Date(today.Year()-1, m, d, 0, 0, 0, 0, time.UTC)
		return fyEndPrevYear.AddDate(0, 0, 1)
	}
	// FY has ended: new FY started the day after this year's FY end.
	return fyEndThisYear.AddDate(0, 0, 1)
}

// lastFiscalYearRange returns the From/To for the completed fiscal year
// immediately before the current one.
func lastFiscalYearRange(fiscalYearEnd string, today time.Time) ReportPeriod {
	currentStart := currentFiscalYearStart(fiscalYearEnd, today)
	lastEnd := currentStart.AddDate(0, 0, -1) // last day of previous FY

	// lastEnd is a fiscal year end date. One calendar year earlier (same MM-DD)
	// gives the fiscal year end before that.
	m, d := ParseFiscalYearEnd(fiscalYearEnd)
	priorFYEnd := time.Date(lastEnd.Year()-1, m, d, 0, 0, 0, 0, time.UTC)
	lastStart := priorFYEnd.AddDate(0, 0, 1)
	return ReportPeriod{From: lastStart, To: lastEnd}
}

// PresetLabel returns the human-readable label for a preset.
func PresetLabel(p ReportPreset) string {
	switch p {
	case PresetLastFiscalYear:
		return "Last Fiscal Year"
	case PresetYearToDate:
		return "Year to Date"
	case PresetLastMonth:
		return "Last Month"
	default:
		return "Custom"
	}
}

// PresetFromDates identifies which preset (if any) matches from/to.
// Returns PresetCustom when no named preset matches.
func PresetFromDates(from, to time.Time, fiscalYearEnd string, today time.Time) ReportPreset {
	today = today.UTC().Truncate(24 * time.Hour)
	for _, p := range []ReportPreset{PresetLastMonth, PresetYearToDate, PresetLastFiscalYear} {
		result := ComputeReportPeriod(p, fiscalYearEnd, today)
		if result.From.Equal(from) && result.To.Equal(to) {
			return p
		}
	}
	return PresetCustom
}
