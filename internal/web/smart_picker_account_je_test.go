// 遵循project_guide.md
package web

import (
	"fmt"
	"testing"

	"gobooks/internal/models"
)

// TestAccountProvider_JournalEntryContext_ReturnsAllRoots locks the new
// "journal_entry_account" context: the JE list page's account filter
// must see every active company account, not just expense roots.
//
// Existing expense_form_category callers are tested separately in
// smart_picker_handler_test.go — this file only covers the JE-specific
// context.
func TestAccountProvider_JournalEntryContext_ReturnsAllRoots(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "JE Account Picker Co")

	expID := seedSPAccount(t, db, companyID, "6100", "Office Supplies", models.RootExpense, true)
	revID := seedSPAccount(t, db, companyID, "4100", "Sales Revenue", models.RootRevenue, true)
	bankID := seedSPAccount(t, db, companyID, "1010", "Bank Checking", models.RootAsset, true)
	// Inactive — must not appear
	seedSPAccount(t, db, companyID, "6300", "Retired Expense", models.RootExpense, false)
	// Other company — must not appear
	otherID := seedCompany(t, db, "JE Account Picker Other Co")
	seedSPAccount(t, db, otherID, "6100", "Cross-Tenant Expense", models.RootExpense, true)

	var p ExpenseAccountProvider
	ctx := SmartPickerContext{
		CompanyID: companyID,
		Context:   "journal_entry_account",
		Limit:     50,
	}
	result, err := p.Search(db, ctx, "")
	if err != nil {
		t.Fatal(err)
	}

	gotIDs := collectIDs(result.Candidates)
	for _, want := range []uint{expID, revID, bankID} {
		key := fmt.Sprintf("%d", want)
		if !gotIDs[key] {
			t.Errorf("expected account ID %d in JE-context results, missing", want)
		}
	}
	if len(result.Candidates) != 3 {
		t.Errorf("expected 3 active company accounts, got %d: %+v", len(result.Candidates), result.Candidates)
	}
}

// TestAccountProvider_ExpenseContextStillExpenseOnly verifies the
// branch logic preserves the legacy behaviour for the original context
// — adding the JE branch must not accidentally widen what existing
// expense-form callers see.
func TestAccountProvider_ExpenseContextStillExpenseOnly(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "Expense-Only Backcompat Co")

	expID := seedSPAccount(t, db, companyID, "6100", "Office Supplies", models.RootExpense, true)
	seedSPAccount(t, db, companyID, "4100", "Sales Revenue", models.RootRevenue, true)
	seedSPAccount(t, db, companyID, "1010", "Bank Checking", models.RootAsset, true)

	var p ExpenseAccountProvider
	ctx := SmartPickerContext{
		CompanyID: companyID,
		Context:   "expense_form_category",
		Limit:     50,
	}
	result, err := p.Search(db, ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("expected 1 expense account, got %d: %+v", len(result.Candidates), result.Candidates)
	}
	if result.Candidates[0].ID != fmt.Sprintf("%d", expID) {
		t.Errorf("expected ID %d, got %s", expID, result.Candidates[0].ID)
	}
}

// TestAccountProvider_GetByID_JournalEntryContext verifies the same
// scope policy applies on rehydrate — picking a non-expense account
// in the JE context must round-trip cleanly.
func TestAccountProvider_GetByID_JournalEntryContext(t *testing.T) {
	db := testRouteDB(t)
	companyID := seedCompany(t, db, "JE GetByID Co")
	revID := seedSPAccount(t, db, companyID, "4100", "Sales Revenue", models.RootRevenue, true)

	var p ExpenseAccountProvider
	ctx := SmartPickerContext{
		CompanyID: companyID,
		Context:   "journal_entry_account",
	}
	item, err := p.GetByID(db, ctx, fmt.Sprintf("%d", revID))
	if err != nil {
		t.Fatal(err)
	}
	if item == nil {
		t.Fatal("expected revenue account in JE context, got nil")
	}
	if item.Primary != "Sales Revenue" || item.Secondary != "4100" {
		t.Errorf("unexpected item: %+v", item)
	}

	// Same revenue ID in expense_form_category context must NOT round-trip
	// — that context only accepts expense roots.
	ctxExp := SmartPickerContext{CompanyID: companyID, Context: "expense_form_category"}
	itemExp, err := p.GetByID(db, ctxExp, fmt.Sprintf("%d", revID))
	if err != nil {
		t.Fatal(err)
	}
	if itemExp != nil {
		t.Errorf("expected nil for revenue account in expense context, got %+v", itemExp)
	}
}
