// 遵循project_guide.md
package services

import (
	"testing"
	"time"
)

func rpDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic("bad date in test: " + s)
	}
	return t.UTC()
}

// ── ParseFiscalYearEnd ────────────────────────────────────────────────────────

func TestParseFiscalYearEnd(t *testing.T) {
	cases := []struct {
		input string
		wantM time.Month
		wantD int
	}{
		{"12-31", time.December, 31},
		{"03-31", time.March, 31},
		{"06-30", time.June, 30},
		{"",     time.December, 31}, // default
		{"bad",  time.December, 31}, // default
		{"13-01", time.December, 31}, // month out of range
	}
	for _, tc := range cases {
		m, day := ParseFiscalYearEnd(tc.input)
		if m != tc.wantM || day != tc.wantD {
			t.Errorf("ParseFiscalYearEnd(%q) = (%v, %d), want (%v, %d)",
				tc.input, m, day, tc.wantM, tc.wantD)
		}
	}
}

// ── lastMonthRange ────────────────────────────────────────────────────────────

func TestLastMonthRange(t *testing.T) {
	cases := []struct {
		today, wantFrom, wantTo string
	}{
		// Normal mid-month
		{"2026-04-03", "2026-03-01", "2026-03-31"},
		// January → December of prior year
		{"2026-01-15", "2025-12-01", "2025-12-31"},
		// March → February (non-leap year)
		{"2025-03-10", "2025-02-01", "2025-02-28"},
		// March → February (leap year)
		{"2024-03-01", "2024-02-01", "2024-02-29"},
	}
	for _, tc := range cases {
		got := lastMonthRange(rpDate(tc.today))
		if got.From.Format("2006-01-02") != tc.wantFrom {
			t.Errorf("lastMonthRange(%s).From = %s, want %s",
				tc.today, got.From.Format("2006-01-02"), tc.wantFrom)
		}
		if got.To.Format("2006-01-02") != tc.wantTo {
			t.Errorf("lastMonthRange(%s).To = %s, want %s",
				tc.today, got.To.Format("2006-01-02"), tc.wantTo)
		}
	}
}

// ── currentFiscalYearStart ────────────────────────────────────────────────────

func TestCurrentFiscalYearStart(t *testing.T) {
	cases := []struct {
		fyEnd string
		today string
		want  string
	}{
		// Calendar year (Dec 31): before year end
		{"12-31", "2026-04-03", "2026-01-01"},
		// Calendar year: after year end (shouldn't happen on Dec 31 unless today > Dec 31)
		// Actually Dec 31 FY end: today can never be after Dec 31 in same year without being Jan 1+
		// Let's test Jan 1 after calendar year end:
		{"12-31", "2026-01-01", "2026-01-01"}, // Jan 1 ≤ Dec 31 of same year → start = Jan 1 this year

		// Mar 31 FY end, today before FY end: FY started Apr 1 last year
		{"03-31", "2026-01-15", "2025-04-01"},
		// Mar 31 FY end, today after FY end: FY started Apr 1 this year
		{"03-31", "2026-05-01", "2026-04-01"},
		// Mar 31 FY end, today exactly on FY end
		{"03-31", "2026-03-31", "2025-04-01"},
		// Jun 30 FY end, today after FY end
		{"06-30", "2026-08-01", "2026-07-01"},
		// Jun 30 FY end, today before FY end
		{"06-30", "2026-04-03", "2025-07-01"},
	}
	for _, tc := range cases {
		got := currentFiscalYearStart(tc.fyEnd, rpDate(tc.today))
		if got.Format("2006-01-02") != tc.want {
			t.Errorf("currentFiscalYearStart(%q, %s) = %s, want %s",
				tc.fyEnd, tc.today, got.Format("2006-01-02"), tc.want)
		}
	}
}

// ── ComputeReportPeriod ───────────────────────────────────────────────────────

func TestComputeReportPeriod_LastMonth(t *testing.T) {
	p := ComputeReportPeriod(PresetLastMonth, "12-31", rpDate("2026-04-03"))
	if p.From.Format("2006-01-02") != "2026-03-01" {
		t.Errorf("From: want 2026-03-01, got %s", p.From.Format("2006-01-02"))
	}
	if p.To.Format("2006-01-02") != "2026-03-31" {
		t.Errorf("To: want 2026-03-31, got %s", p.To.Format("2006-01-02"))
	}
}

func TestComputeReportPeriod_YearToDate_CalendarFY(t *testing.T) {
	// Calendar FY (Dec 31), today Apr 3 → YTD from Jan 1 to Apr 3
	p := ComputeReportPeriod(PresetYearToDate, "12-31", rpDate("2026-04-03"))
	if p.From.Format("2006-01-02") != "2026-01-01" {
		t.Errorf("From: want 2026-01-01, got %s", p.From.Format("2006-01-02"))
	}
	if p.To.Format("2006-01-02") != "2026-04-03" {
		t.Errorf("To: want 2026-04-03, got %s", p.To.Format("2006-01-02"))
	}
}

func TestComputeReportPeriod_YearToDate_NonCalendarFY(t *testing.T) {
	// FY ends Mar 31; today Jan 15 2026 → current FY started Apr 1 2025
	p := ComputeReportPeriod(PresetYearToDate, "03-31", rpDate("2026-01-15"))
	if p.From.Format("2006-01-02") != "2025-04-01" {
		t.Errorf("From: want 2025-04-01, got %s", p.From.Format("2006-01-02"))
	}
	if p.To.Format("2006-01-02") != "2026-01-15" {
		t.Errorf("To: want 2026-01-15, got %s", p.To.Format("2006-01-02"))
	}
}

func TestComputeReportPeriod_LastFiscalYear_CalendarFY(t *testing.T) {
	// Calendar FY; today Apr 3 2026 → last FY = Jan 1 2025 to Dec 31 2025
	p := ComputeReportPeriod(PresetLastFiscalYear, "12-31", rpDate("2026-04-03"))
	if p.From.Format("2006-01-02") != "2025-01-01" {
		t.Errorf("From: want 2025-01-01, got %s", p.From.Format("2006-01-02"))
	}
	if p.To.Format("2006-01-02") != "2025-12-31" {
		t.Errorf("To: want 2025-12-31, got %s", p.To.Format("2006-01-02"))
	}
}

func TestComputeReportPeriod_LastFiscalYear_NonCalendarFY(t *testing.T) {
	// FY ends Mar 31; today May 1 2026 → last FY = Apr 1 2025 to Mar 31 2026
	p := ComputeReportPeriod(PresetLastFiscalYear, "03-31", rpDate("2026-05-01"))
	if p.From.Format("2006-01-02") != "2025-04-01" {
		t.Errorf("From: want 2025-04-01, got %s", p.From.Format("2006-01-02"))
	}
	if p.To.Format("2006-01-02") != "2026-03-31" {
		t.Errorf("To: want 2026-03-31, got %s", p.To.Format("2006-01-02"))
	}
}

func TestComputeReportPeriod_Custom_ReturnsZero(t *testing.T) {
	p := ComputeReportPeriod(PresetCustom, "12-31", rpDate("2026-04-03"))
	if !p.From.IsZero() || !p.To.IsZero() {
		t.Errorf("Custom preset should return zero ReportPeriod, got From=%v To=%v", p.From, p.To)
	}
}

// ── PresetFromDates ───────────────────────────────────────────────────────────

func TestPresetFromDates_MatchesLastMonth(t *testing.T) {
	today := rpDate("2026-04-03")
	p := ComputeReportPeriod(PresetLastMonth, "12-31", today)
	got := PresetFromDates(p.From, p.To, "12-31", today)
	if got != PresetLastMonth {
		t.Errorf("want PresetLastMonth, got %q", got)
	}
}

func TestPresetFromDates_MatchesYTD(t *testing.T) {
	today := rpDate("2026-04-03")
	p := ComputeReportPeriod(PresetYearToDate, "12-31", today)
	got := PresetFromDates(p.From, p.To, "12-31", today)
	if got != PresetYearToDate {
		t.Errorf("want PresetYearToDate, got %q", got)
	}
}

func TestPresetFromDates_NoMatch_ReturnsCustom(t *testing.T) {
	today := rpDate("2026-04-03")
	got := PresetFromDates(rpDate("2020-01-01"), rpDate("2020-06-30"), "12-31", today)
	if got != PresetCustom {
		t.Errorf("want PresetCustom, got %q", got)
	}
}
