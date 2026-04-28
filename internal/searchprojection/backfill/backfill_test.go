// 遵循project_guide.md
package backfill

import (
	"context"
	"strings"
	"testing"

	"balanciz/internal/searchprojection"
)

// TestAllFamilies_Coverage locks the 19-entity family set so a future
// producer addition can't silently skip the backfill registry.
func TestAllFamilies_Coverage(t *testing.T) {
	want := []Family{
		FamilyCustomer, FamilyVendor, FamilyProductService,
		FamilyInvoice, FamilyBill, FamilyQuote, FamilySalesOrder,
		FamilyPurchaseOrder, FamilyCustomerReceipt, FamilyExpense,
		FamilyJournalEntry, FamilyCreditNote, FamilyVendorCreditNote,
		FamilyARReturn, FamilyVendorReturn, FamilyARRefund, FamilyVendorRefund,
		FamilyCustomerDeposit, FamilyVendorPrepayment,
	}
	got := AllFamilies()
	if len(got) != len(want) {
		t.Fatalf("AllFamilies returned %d families, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("AllFamilies[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestParseFamily covers the CLI / admin-handler / audit-log entry
// points: known values round-trip, unknown values fail loudly.
func TestParseFamily(t *testing.T) {
	for _, fam := range AllFamilies() {
		got, ok := ParseFamily(string(fam))
		if !ok || got != fam {
			t.Errorf("ParseFamily(%q) = (%q, %v), want (%q, true)", fam, got, ok, fam)
		}
		// Case + whitespace tolerance.
		got2, ok2 := ParseFamily("  " + strings.ToUpper(string(fam)) + "  ")
		if !ok2 || got2 != fam {
			t.Errorf("ParseFamily upper/space %q failed: got (%q,%v)", fam, got2, ok2)
		}
	}
	if _, ok := ParseFamily("not_a_family"); ok {
		t.Errorf("ParseFamily('not_a_family') accepted; should reject")
	}
	if _, ok := ParseFamily(""); ok {
		t.Errorf("ParseFamily('') accepted; should reject")
	}
}

// TestRunFamily_UnknownFamilyReturnsError ensures the dispatcher
// surfaces unknown families as a per-family Err rather than panicking
// or silently no-op'ing — important because the admin route would
// otherwise look like it succeeded.
func TestRunFamily_UnknownFamilyReturnsError(t *testing.T) {
	res := RunFamily(context.Background(), nil, searchprojection.NoopProjector{}, Family("bogus"), Options{})
	if res.Err == nil {
		t.Fatalf("expected error for unknown family, got nil")
	}
	if !strings.Contains(res.Err.Error(), "bogus") {
		t.Errorf("error %q does not mention the unknown family name", res.Err)
	}
	if res.Family != Family("bogus") {
		t.Errorf("Family = %q, want bogus", res.Family)
	}
}

// TestResult_FirstErr returns the first non-nil per-family error or nil
// for a clean run. Locks the contract the admin handler relies on for
// audit logging.
func TestResult_FirstErr(t *testing.T) {
	clean := Result{Families: []FamilyResult{
		{Family: FamilyCustomer, Rows: 5},
		{Family: FamilyVendor, Rows: 3},
	}}
	if err := clean.FirstErr(); err != nil {
		t.Errorf("clean result FirstErr = %v, want nil", err)
	}

	fail := Result{Families: []FamilyResult{
		{Family: FamilyCustomer, Rows: 5},
		{Family: FamilyInvoice, Err: errSentinel("boom")},
		{Family: FamilyBill, Err: errSentinel("ignored — only first surfaces")},
	}}
	got := fail.FirstErr()
	if got == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(got.Error(), "invoice") {
		t.Errorf("FirstErr should wrap the first failed family, got %q", got)
	}
}

// TestResult_TotalRows sums Rows across all families.
func TestResult_TotalRows(t *testing.T) {
	res := Result{Families: []FamilyResult{
		{Family: FamilyCustomer, Rows: 5},
		{Family: FamilyVendor, Rows: 3},
		{Family: FamilyInvoice, Rows: 12},
	}}
	if got := res.TotalRows(); got != 20 {
		t.Errorf("TotalRows = %d, want 20", got)
	}
}

type errSentinel string

func (e errSentinel) Error() string { return string(e) }
