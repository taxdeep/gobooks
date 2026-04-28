// 遵循project_guide.md
package pages

import (
	"context"
	"strings"
	"testing"
)

// TestReportToolbar_HiddenInputsSurviveGetSubmit locks the regression
// fix for "Account Transactions: Run Report jumps back to Sales Tax
// Report":
//
//   - Account Transactions stamps account_id on the toolbar via
//     HiddenInputs because GET form submit replaces the action's query
//     string with form-input values. If account_id isn't a hidden
//     <input>, the resulting URL is /reports/account-transactions?from=…
//     &to=… (account_id missing) → handler redirects back to
//     /reports/sales-tax.
//
// This test renders the toolbar with a hidden account_id and asserts
// the input is in the HTML. Belongs in the templates package because
// it's a render-shape contract, not a handler contract.
func TestReportToolbar_HiddenInputsSurviveGetSubmit(t *testing.T) {
	vm := ReportToolbarVM{
		Preset:      "custom",
		From:        "2026-01-01",
		To:          "2026-03-31",
		FormAction:  "/reports/account-transactions",
		ReportTitle: "Account Transactions",
		CompanyName: "Test Co",
		Mode:        "period",
		HiddenInputs: []ReportToolbarHiddenInput{
			{Name: "account_id", Value: "71"},
		},
	}

	var sb strings.Builder
	if err := ReportToolbar(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := sb.String()

	if !strings.Contains(html, `name="account_id"`) {
		t.Fatal("rendered toolbar missing hidden account_id input — Run Report would lose the param on submit")
	}
	if !strings.Contains(html, `value="71"`) {
		t.Fatal("rendered toolbar missing account_id value=71")
	}
	// Sanity: the FormAction must NOT carry the account_id in its query
	// string — that's the bug we're guarding against. Rely on the
	// hidden input as the single source of truth instead.
	if strings.Contains(html, `action="/reports/account-transactions?account_id=`) {
		t.Error("FormAction still carries account_id in query string — relying on this is the original bug")
	}
}

// TestReportToolbar_NoHiddenInputs verifies the empty-list case (the
// vast majority of report pages) doesn't emit stray inputs that would
// confuse the form serialisation.
func TestReportToolbar_NoHiddenInputs(t *testing.T) {
	vm := ReportToolbarVM{
		Preset:     "year_to_date",
		FormAction: "/reports/income-statement",
		Mode:       "period",
	}

	var sb strings.Builder
	if err := ReportToolbar(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := sb.String()

	if strings.Contains(html, `name="account_id"`) {
		t.Error("toolbar rendered an unexpected account_id input when none was requested")
	}
}

func TestReportToolbar_ExportDropdownCarriesHiddenQuery(t *testing.T) {
	vm := ReportToolbarVM{
		Preset:         "custom",
		From:           "2026-01-01",
		To:             "2026-03-31",
		FormAction:     "/reports/account-transactions",
		ReportTitle:    "Account Transactions",
		CompanyName:    "Test Co",
		Mode:           "period",
		CSVExportURL:   "/reports/account-transactions/export.csv",
		ExcelExportURL: "/reports/account-transactions/export.xlsx",
		PDFExportURL:   "/reports/account-transactions/export.pdf",
		HiddenInputs: []ReportToolbarHiddenInput{
			{Name: "account_id", Value: "71"},
		},
	}

	var sb strings.Builder
	if err := ReportToolbar(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := sb.String()

	for _, want := range []string{
		`data-csv-base="/reports/account-transactions/export.csv"`,
		`data-excel-base="/reports/account-transactions/export.xlsx"`,
		`data-pdf-base="/reports/account-transactions/export.pdf"`,
		`data-hidden-query="account_id=71"`,
		"Export CSV",
		"Export Excel",
		"Export PDF",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected toolbar to contain %q", want)
		}
	}
}
