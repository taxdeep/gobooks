// 遵循project_guide.md
package main

import (
	"reflect"
	"testing"
)

// diffIDSets is the heart of the reconciler — it decides what to repair
// vs what to delete. Cover the merge logic exhaustively because a bug
// here would either drop search rows for live entities or leave orphans
// indefinitely.

func TestDiffIDSets_AllAligned(t *testing.T) {
	missing, orphans := diffIDSets([]uint{1, 2, 3}, []uint{1, 2, 3})
	if len(missing) != 0 || len(orphans) != 0 {
		t.Errorf("aligned sets should produce no diff; got missing=%v orphans=%v", missing, orphans)
	}
}

func TestDiffIDSets_AllMissing(t *testing.T) {
	missing, orphans := diffIDSets([]uint{1, 2, 3}, []uint{})
	if !reflect.DeepEqual(missing, []uint{1, 2, 3}) {
		t.Errorf("missing = %v, want [1 2 3]", missing)
	}
	if len(orphans) != 0 {
		t.Errorf("orphans = %v, want empty", orphans)
	}
}

func TestDiffIDSets_AllOrphans(t *testing.T) {
	missing, orphans := diffIDSets([]uint{}, []uint{1, 2, 3})
	if len(missing) != 0 {
		t.Errorf("missing = %v, want empty", missing)
	}
	if !reflect.DeepEqual(orphans, []uint{1, 2, 3}) {
		t.Errorf("orphans = %v, want [1 2 3]", orphans)
	}
}

func TestDiffIDSets_Interleaved(t *testing.T) {
	// business has 1, 3, 5 (3 rows); projection has 2, 3, 4 (3 rows)
	// expected: missing=[1,5], orphans=[2,4]
	missing, orphans := diffIDSets([]uint{1, 3, 5}, []uint{2, 3, 4})
	if !reflect.DeepEqual(missing, []uint{1, 5}) {
		t.Errorf("missing = %v, want [1 5]", missing)
	}
	if !reflect.DeepEqual(orphans, []uint{2, 4}) {
		t.Errorf("orphans = %v, want [2 4]", orphans)
	}
}

func TestDiffIDSets_BusinessLongerTail(t *testing.T) {
	missing, orphans := diffIDSets([]uint{1, 2, 3, 4, 5}, []uint{1, 2, 3})
	if !reflect.DeepEqual(missing, []uint{4, 5}) {
		t.Errorf("missing = %v, want [4 5]", missing)
	}
	if len(orphans) != 0 {
		t.Errorf("orphans = %v, want empty", orphans)
	}
}

func TestDiffIDSets_ProjectionLongerTail(t *testing.T) {
	missing, orphans := diffIDSets([]uint{1, 2, 3}, []uint{1, 2, 3, 4, 5})
	if len(missing) != 0 {
		t.Errorf("missing = %v, want empty", missing)
	}
	if !reflect.DeepEqual(orphans, []uint{4, 5}) {
		t.Errorf("orphans = %v, want [4 5]", orphans)
	}
}

func TestDiffIDSets_BothEmpty(t *testing.T) {
	missing, orphans := diffIDSets(nil, nil)
	if len(missing) != 0 || len(orphans) != 0 {
		t.Errorf("both-empty diff should be empty; got missing=%v orphans=%v", missing, orphans)
	}
}

// HasDrift drives the process exit code — a single missing OR a single
// orphan is enough to signal cron failure.
func TestReconcileResult_HasDrift(t *testing.T) {
	cases := []struct {
		name string
		r    reconcileResult
		want bool
	}{
		{"clean", reconcileResult{Missing: 0, Orphans: 0}, false},
		{"missing only", reconcileResult{Missing: 3, Orphans: 0}, true},
		{"orphans only", reconcileResult{Missing: 0, Orphans: 1}, true},
		{"both", reconcileResult{Missing: 5, Orphans: 2}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.HasDrift(); got != tc.want {
				t.Errorf("HasDrift = %v, want %v", got, tc.want)
			}
		})
	}
}

// filterFamilies decides which entity types to scan based on the -only
// flag. Empty / "all" returns everything; valid name returns one slot;
// invalid name returns nil so the CLI can exit with a clear error.
func TestFilterFamilies(t *testing.T) {
	if got := filterFamilies(allFamilies, "all"); len(got) != len(allFamilies) {
		t.Errorf("all → %d families, want %d", len(got), len(allFamilies))
	}
	if got := filterFamilies(allFamilies, ""); len(got) != len(allFamilies) {
		t.Errorf("empty → %d families, want %d (treat as all)", len(got), len(allFamilies))
	}
	if got := filterFamilies(allFamilies, "invoice"); len(got) != 1 || got[0].entityType != "invoice" {
		t.Errorf("invoice → %v, want single invoice slot", got)
	}
	if got := filterFamilies(allFamilies, "elasticsearch"); got != nil {
		t.Errorf("unknown → %v, want nil so CLI fails clearly", got)
	}
}

// Sanity: the registry must list all 19 entity types Phase 1-5.5 ship,
// and no extras. If a producer is added/removed without updating the
// reconciler registry, this catches it before prod drift accumulates.
func TestAllFamiliesRegistry_HasExpectedTypes(t *testing.T) {
	want := map[string]bool{
		// Phase 1 + 2 + 3
		"customer":         true,
		"vendor":           true,
		"product_service":  true,
		"invoice":          true,
		"bill":             true,
		"quote":            true,
		"sales_order":      true,
		"purchase_order":   true,
		"customer_receipt": true,
		"expense":          true,
		// Phase 5.4 + 5.5
		"journal_entry":      true,
		"credit_note":        true,
		"vendor_credit_note": true,
		"ar_return":          true,
		"vendor_return":      true,
		"ar_refund":          true,
		"vendor_refund":      true,
		"customer_deposit":   true,
		"vendor_prepayment":  true,
	}
	got := map[string]bool{}
	for _, fam := range allFamilies {
		got[fam.entityType] = true
	}
	for k := range want {
		if !got[k] {
			t.Errorf("registry missing entity type %q (added a producer without updating allFamilies?)", k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("registry has unexpected entity type %q (test out of date or producer dropped?)", k)
		}
	}
}
