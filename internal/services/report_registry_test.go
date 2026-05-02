// 遵循project_guide.md
package services

import "testing"

// TestReportRegistry_NoDuplicateKeys is the most important contract:
// report keys are persisted in report_favourites, so a duplicate or
// renamed key would orphan existing stars.
func TestReportRegistry_NoDuplicateKeys(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range AllReports() {
		if seen[r.Key] {
			t.Errorf("duplicate report Key %q in registry — keys must be globally unique", r.Key)
		}
		seen[r.Key] = true
	}
}

// TestReportRegistry_AllEntriesHaveCategory ensures every registry
// entry has a valid category, so the hub renderer never gets an
// "uncategorized" orphan.
func TestReportRegistry_AllEntriesHaveCategory(t *testing.T) {
	validCats := map[ReportCategory]bool{}
	for _, c := range Categories() {
		validCats[c] = true
	}
	for _, r := range AllReports() {
		if !validCats[r.Category] {
			t.Errorf("report %q has unknown category %q", r.Key, r.Category)
		}
	}
}

// TestReportRegistry_AllEntriesHaveHref guards against empty Href —
// a report card with no link is dead UI.
func TestReportRegistry_AllEntriesHaveHref(t *testing.T) {
	for _, r := range AllReports() {
		if r.Href == "" {
			t.Errorf("report %q has empty Href", r.Key)
		}
		if r.Title == "" {
			t.Errorf("report %q has empty Title", r.Key)
		}
	}
}

// TestReportByKey covers the validation guard used by the favourites
// toggle endpoint: known keys round-trip, unknown returns nil so the
// handler can render a clean error.
func TestReportByKey(t *testing.T) {
	if got := ReportByKey("balance-sheet"); got == nil || got.Key != "balance-sheet" {
		t.Errorf("ReportByKey(balance-sheet) = %v, want non-nil with matching key", got)
	}
	if got := ReportByKey("not-a-report"); got != nil {
		t.Errorf("ReportByKey(garbage) = %v, want nil", got)
	}
	if got := ReportByKey(""); got != nil {
		t.Errorf("ReportByKey() = %v, want nil for empty key", got)
	}
}

// TestCategories_StableOrder + TestReportsByCategory verify that the
// category order matches the registry's first-occurrence order — so
// the hub layout is predictable and stable.
func TestCategories_StableOrder(t *testing.T) {
	cats := Categories()
	if len(cats) == 0 {
		t.Fatal("Categories() returned empty list")
	}
	// First category should be financials per AllReports() order.
	if cats[0] != ReportCategoryFinancials {
		t.Errorf("Categories()[0] = %q, want financials (the registry's first category)", cats[0])
	}
}

func TestReportsByCategory_FiltersCorrectly(t *testing.T) {
	finReports := ReportsByCategory(ReportCategoryFinancials)
	if len(finReports) == 0 {
		t.Fatal("expected at least one report in financials category")
	}
	for _, r := range finReports {
		if r.Category != ReportCategoryFinancials {
			t.Errorf("ReportsByCategory(financials) returned %q from category %q", r.Key, r.Category)
		}
	}
}

func TestCoreReportsHaveOperationalMetadata(t *testing.T) {
	core := CoreReports()
	if len(core) < 4 {
		t.Fatalf("expected core report package, got %d reports", len(core))
	}
	for _, r := range core {
		if r.Mode == "" {
			t.Errorf("core report %q has empty Mode", r.Key)
		}
		if !r.Interactive {
			t.Errorf("core report %q should be marked interactive", r.Key)
		}
		if !r.DrillDown {
			t.Errorf("core report %q should expose drill-through", r.Key)
		}
	}
}
