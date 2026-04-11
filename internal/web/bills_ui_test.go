package web

import (
	"context"
	"strings"
	"testing"

	"gobooks/internal/web/templates/pages"
)

func TestBills_UsesDateFilterInputsWithSanitizer(t *testing.T) {
	vm := pages.BillsVM{
		HasCompany: true,
		FilterFrom: "2026-01-04",
		FilterTo:   "2026-01-31",
	}

	var sb strings.Builder
	if err := pages.Bills(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render bills page: %v", err)
	}
	html := sb.String()

	for _, want := range []string{
		`name="from"`,
		`name="to"`,
		`data-date-filter-input`,
		`inputmode="numeric"`,
		`maxlength="10"`,
		`/static/date_filter_input.js?v=1`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected bills HTML to contain %q", want)
		}
	}
}
