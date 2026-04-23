package web

import (
	"context"
	"strings"
	"testing"

	"gobooks/internal/web/templates/pages"
)

// TestBills_FilterBarWiresNativeDateInputs locks the new (post-Sales-Orders
// alignment) filter contract:
//
//   - From / To use native <input type="date"> (browser handles validation)
//   - Vendor uses SmartPicker, not a preloaded <select>
//   - Apply Filter + Reset are present
//
// Replaces the old TestBills_UsesDateFilterInputsWithSanitizer which
// asserted the now-removed JS-driven text-input sanitizer pattern.
func TestBills_FilterBarWiresNativeDateInputs(t *testing.T) {
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
		`type="date"`,
		`value="2026-01-04"`,
		`value="2026-01-31"`,
		`Apply Filter`,
		`Reset`,
		// Vendor SmartPicker root attribute — proves the picker replaced
		// the legacy <select name="vendor_id">.
		`data-entity="vendor"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected bills HTML to contain %q", want)
		}
	}

	// Negative checks: the legacy sanitizer-driven artifacts must be gone.
	for _, gone := range []string{
		`data-date-filter-input`,
		`/static/date_filter_input.js`,
	} {
		if strings.Contains(html, gone) {
			t.Errorf("expected bills HTML to no longer contain legacy artifact %q", gone)
		}
	}
}
