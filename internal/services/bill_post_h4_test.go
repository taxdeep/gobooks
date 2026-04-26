// 遵循project_guide.md
package services

// bill_post_h4_test.go — Phase H slice H.4: Bill-side decoupling from
// inventory under companies.receipt_required=true.
//
// Scope lock: under flag=true, stock-backed Bill lines (a) do NOT
// form inventory and (b) debit the company's GR/IR clearing account
// instead of the per-item Inventory-Asset account. Non-stock lines
// remain on the Expense path. Under flag=false, byte-identical to
// pre-H.4 behavior (Phase G legacy + Phase E0 correctness all stand).

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// seedH4Fixture flips receipt_required=true and seeds a GR/IR
// clearing account (liability) wired to the company. Returns the
// GR/IR account ID for the assertions below.
func seedH4Fixture(t *testing.T, db *gorm.DB, s invPostingSetup) uint {
	t.Helper()
	grir := models.Account{
		CompanyID:         s.companyID,
		Code:              "2105",
		Name:              "GR/IR Clearing",
		RootAccountType:   models.RootLiability,
		DetailAccountType: models.DetailOtherCurrentLiability,
		IsActive:          true,
	}
	if err := db.Create(&grir).Error; err != nil {
		t.Fatalf("seed GR/IR account: %v", err)
	}
	if err := db.Model(&models.Company{}).
		Where("id = ?", s.companyID).
		Updates(map[string]any{
			"receipt_required":          true,
			"gr_ir_clearing_account_id": grir.ID,
		}).Error; err != nil {
		t.Fatalf("flip flag + wire GR/IR: %v", err)
	}
	return grir.ID
}

// buildBillWithLines creates and persists a draft bill + lines. The
// caller owns line construction so tests can express stock / non-stock
// / mixed layouts inline.
func buildBillWithLines(t *testing.T, db *gorm.DB, s invPostingSetup, number string, lines []models.BillLine) models.Bill {
	t.Helper()
	bill := models.Bill{
		CompanyID:    s.companyID,
		BillNumber:   number,
		VendorID:     s.vendorID,
		BillDate:     time.Now().UTC(),
		Status:       models.BillStatusDraft,
		CurrencyCode: "",
		ExchangeRate: decimal.NewFromInt(1),
	}
	// Aggregate from lines so the bill balance + subtotal are
	// consistent with what BuildBillFragments will compute.
	subtotal := decimal.Zero
	for _, l := range lines {
		subtotal = subtotal.Add(l.LineNet)
	}
	bill.Subtotal = subtotal
	bill.Amount = subtotal
	bill.BalanceDue = subtotal
	if err := db.Create(&bill).Error; err != nil {
		t.Fatalf("create bill: %v", err)
	}
	for i := range lines {
		lines[i].CompanyID = s.companyID
		lines[i].BillID = bill.ID
		if lines[i].SortOrder == 0 {
			lines[i].SortOrder = uint(i + 1)
		}
		if err := db.Create(&lines[i]).Error; err != nil {
			t.Fatalf("create bill line %d: %v", i, err)
		}
	}
	return bill
}

// countMovementsForBill returns the number of inventory_movements rows
// sourced from a given bill. Under H.4 flag=true this must stay at 0.
func countMovementsForBill(t *testing.T, db *gorm.DB, companyID, billID uint) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ? AND source_id = ?",
			companyID, "bill", billID).
		Count(&n).Error; err != nil {
		t.Fatalf("count movements: %v", err)
	}
	return n
}

// sumJournalDebitByAccount returns the sum of Debit across all journal
// lines for the given (company, account, journal_entry_id).
func sumJournalDebitByAccount(t *testing.T, db *gorm.DB, companyID, accountID, jeID uint) decimal.Decimal {
	t.Helper()
	var rows []models.JournalLine
	if err := db.Where("company_id = ? AND account_id = ? AND journal_entry_id = ?",
		companyID, accountID, jeID).Find(&rows).Error; err != nil {
		t.Fatalf("load JE lines: %v", err)
	}
	total := decimal.Zero
	for _, r := range rows {
		total = total.Add(r.Debit)
	}
	return total
}

// ── Scenario 1: flag=true + pure stock bill ──────────────────────────────────

// Under receipt_required=true with only stock-backed lines, PostBill
// must (a) skip CreatePurchaseMovements entirely (no inventory_movements
// rows produced by the bill) and (b) route the full line-net debit to
// the GR/IR clearing account (not the per-item Inventory Asset).
// Credit stays with AP — Bill remains the financial claim.
func TestPostBill_H4_FlagOn_StockOnly_DebitsGRIR_NoMovements(t *testing.T) {
	db := testInventoryPostingDB(t)
	s := setupInventoryPosting(t, db)
	grirID := seedH4Fixture(t, db, s)

	// One stock line: 10 widgets × $20 = $200.
	bill := buildBillWithLines(t, db, s, "BILL-H4-STOCK", []models.BillLine{
		{
			ProductServiceID: &s.stockItemID,
			Description:      "Widget",
			Qty:              decimal.NewFromInt(10),
			UnitPrice:        decimal.NewFromInt(20),
			ExpenseAccountID: &s.expenseID,
			LineNet:          decimal.NewFromInt(200),
			LineTotal:        decimal.NewFromInt(200),
		},
	})
	if err := PostBill(db, s.companyID, bill.ID, "tester", nil); err != nil {
		t.Fatalf("PostBill: %v", err)
	}

	// (a) No inventory movements from this bill.
	if n := countMovementsForBill(t, db, s.companyID, bill.ID); n != 0 {
		t.Fatalf("movements for bill: got %d want 0 (H.4 flag=true must skip CreatePurchaseMovements)", n)
	}
	// Inventory balances must still be zero for this item.
	var bal models.InventoryBalance
	err := db.Where("company_id = ? AND item_id = ?", s.companyID, s.stockItemID).
		First(&bal).Error
	if err == nil && !bal.QuantityOnHand.IsZero() {
		t.Fatalf("on_hand: got %s want 0 (bill must not form inventory under flag=true)", bal.QuantityOnHand)
	}

	// (b) Debit landed on GR/IR for the full $200 line-net.
	var posted models.Bill
	db.First(&posted, bill.ID)
	if posted.JournalEntryID == nil {
		t.Fatalf("JE not linked on posted bill")
	}
	grirDebit := sumJournalDebitByAccount(t, db, s.companyID, grirID, *posted.JournalEntryID)
	if !grirDebit.Equal(decimal.NewFromInt(200)) {
		t.Fatalf("GR/IR debit: got %s want 200", grirDebit)
	}
	// Neither Inventory-Asset nor the Expense account received a debit.
	if v := sumJournalDebitByAccount(t, db, s.companyID, s.invAssetID, *posted.JournalEntryID); !v.IsZero() {
		t.Fatalf("Inventory-Asset debit: got %s want 0 (stock lines under flag=true must not hit InventoryAsset)", v)
	}
	if v := sumJournalDebitByAccount(t, db, s.companyID, s.expenseID, *posted.JournalEntryID); !v.IsZero() {
		t.Fatalf("Expense debit: got %s want 0 (stock lines under flag=true must redirect to GR/IR, not stay on Expense)", v)
	}
}

// ── Scenario 2: flag=true + pure non-stock bill ──────────────────────────────

// Under flag=true, Bill lines that are NOT stock-backed must follow
// the legacy expense/AP path unchanged. This guards against an
// over-broad H.4 rewrite that would swap every Bill debit to GR/IR.
// GR/IR need not be configured for service-only bills (no stock
// lines → the H.4 guard does not engage).
func TestPostBill_H4_FlagOn_NonStockOnly_PreservesExpensePath(t *testing.T) {
	db := testInventoryPostingDB(t)
	s := setupInventoryPosting(t, db)
	// Flip flag but do NOT configure GR/IR — service-only bill must
	// not require it.
	if err := db.Model(&models.Company{}).
		Where("id = ?", s.companyID).
		Update("receipt_required", true).Error; err != nil {
		t.Fatalf("flip flag: %v", err)
	}

	bill := buildBillWithLines(t, db, s, "BILL-H4-SVC", []models.BillLine{
		{
			ProductServiceID: &s.svcItemID,
			Description:      "Consulting hours",
			Qty:              decimal.NewFromInt(5),
			UnitPrice:        decimal.NewFromInt(100),
			ExpenseAccountID: &s.expenseID,
			LineNet:          decimal.NewFromInt(500),
			LineTotal:        decimal.NewFromInt(500),
		},
	})
	if err := PostBill(db, s.companyID, bill.ID, "tester", nil); err != nil {
		t.Fatalf("PostBill: %v", err)
	}
	var posted models.Bill
	db.First(&posted, bill.ID)
	if posted.JournalEntryID == nil {
		t.Fatalf("JE not linked")
	}
	expenseDebit := sumJournalDebitByAccount(t, db, s.companyID, s.expenseID, *posted.JournalEntryID)
	if !expenseDebit.Equal(decimal.NewFromInt(500)) {
		t.Fatalf("Expense debit: got %s want 500 (non-stock must stay on Expense path)", expenseDebit)
	}
	// Movements: zero, as expected for service-only bills on any path.
	if n := countMovementsForBill(t, db, s.companyID, bill.ID); n != 0 {
		t.Fatalf("movements: got %d want 0", n)
	}
}

// ── Scenario 3: flag=true + mixed bill (stock + non-stock) ───────────────────

// The key H.4 line-type dispatch test: a single bill with both stock
// and non-stock lines splits by line type — stock → GR/IR, non-stock →
// Expense. Lines use DIFFERENT expense accounts (standard chart of
// accounts) so the pre-aggregation redirect behaves cleanly.
func TestPostBill_H4_FlagOn_MixedBill_StockToGRIR_NonStockToExpense(t *testing.T) {
	db := testInventoryPostingDB(t)
	s := setupInventoryPosting(t, db)
	grirID := seedH4Fixture(t, db, s)

	// Seed a second expense account so stock / non-stock lines map
	// to DISTINCT expense accounts — matches real CoA practice.
	stockExpense := models.Account{
		CompanyID: s.companyID, Code: "6100", Name: "Inventory Purchases",
		RootAccountType: models.RootExpense, DetailAccountType: "operating_expense",
		IsActive: true,
	}
	db.Create(&stockExpense)

	bill := buildBillWithLines(t, db, s, "BILL-H4-MIX", []models.BillLine{
		{
			ProductServiceID: &s.stockItemID,
			Description:      "Widget",
			Qty:              decimal.NewFromInt(10),
			UnitPrice:        decimal.NewFromInt(20),
			ExpenseAccountID: &stockExpense.ID,
			LineNet:          decimal.NewFromInt(200),
			LineTotal:        decimal.NewFromInt(200),
		},
		{
			ProductServiceID: &s.svcItemID,
			Description:      "Delivery labour",
			Qty:              decimal.NewFromInt(1),
			UnitPrice:        decimal.NewFromInt(50),
			ExpenseAccountID: &s.expenseID,
			LineNet:          decimal.NewFromInt(50),
			LineTotal:        decimal.NewFromInt(50),
		},
	})
	if err := PostBill(db, s.companyID, bill.ID, "tester", nil); err != nil {
		t.Fatalf("PostBill: %v", err)
	}

	if n := countMovementsForBill(t, db, s.companyID, bill.ID); n != 0 {
		t.Fatalf("movements: got %d want 0 (no bill should form inventory under flag=true)", n)
	}
	var posted models.Bill
	db.First(&posted, bill.ID)
	jeID := *posted.JournalEntryID

	// Stock portion (200) → GR/IR.
	if v := sumJournalDebitByAccount(t, db, s.companyID, grirID, jeID); !v.Equal(decimal.NewFromInt(200)) {
		t.Fatalf("GR/IR debit: got %s want 200 (stock line only)", v)
	}
	// Non-stock portion (50) stays on the original expense account.
	if v := sumJournalDebitByAccount(t, db, s.companyID, s.expenseID, jeID); !v.Equal(decimal.NewFromInt(50)) {
		t.Fatalf("Expense debit: got %s want 50 (non-stock line only)", v)
	}
	// The stock line's expense account received no debit — it was
	// redirected to GR/IR.
	if v := sumJournalDebitByAccount(t, db, s.companyID, stockExpense.ID, jeID); !v.IsZero() {
		t.Fatalf("Stock-line expense debit: got %s want 0 (should have redirected to GR/IR)", v)
	}
}

// ── Scenario 4: flag=false must stay byte-identical ──────────────────────────
// Locked by the pre-existing PostBill test suite (inventory_posting_test.go,
// inventory_reversal_test.go, invoice_post_currency_test.go, etc.). A
// focused sentinel test here verifies the exact H.4-relevant behavior
// under flag=false: stock debit lands on Inventory-Asset and
// CreatePurchaseMovements fires.

func TestPostBill_H4_FlagOff_StockBill_LegacyPathIntact(t *testing.T) {
	db := testInventoryPostingDB(t)
	s := setupInventoryPosting(t, db)
	// Do NOT flip receipt_required. Default is false.

	bill := buildBillWithLines(t, db, s, "BILL-H4-OFF", []models.BillLine{
		{
			ProductServiceID: &s.stockItemID,
			Description:      "Widget",
			Qty:              decimal.NewFromInt(10),
			UnitPrice:        decimal.NewFromInt(20),
			ExpenseAccountID: &s.expenseID,
			LineNet:          decimal.NewFromInt(200),
			LineTotal:        decimal.NewFromInt(200),
		},
	})
	if err := PostBill(db, s.companyID, bill.ID, "tester", nil); err != nil {
		t.Fatalf("PostBill: %v", err)
	}
	// flag=false: movement created, inventory asset debited.
	if n := countMovementsForBill(t, db, s.companyID, bill.ID); n != 1 {
		t.Fatalf("movements: got %d want 1 (legacy path under flag=false MUST create the movement)", n)
	}
	var posted models.Bill
	db.First(&posted, bill.ID)
	jeID := *posted.JournalEntryID
	if v := sumJournalDebitByAccount(t, db, s.companyID, s.invAssetID, jeID); !v.Equal(decimal.NewFromInt(200)) {
		t.Fatalf("Inventory-Asset debit: got %s want 200 (legacy redirect)", v)
	}
}

// ── H.4 addition 1: GR/IR required when stock lines on bill + flag=on ────────

// Under receipt_required=true, if a stock-backed line is present on
// the bill, the company MUST have a GR/IR clearing account
// configured. Missing config fails PostBill with the same sentinel
// Receipt uses (H.3), so the setup requirement is symmetric across
// document types — no half-configured state where Receipt works but
// Bill silently mis-books.
func TestPostBill_H4_FlagOn_StockBill_MissingGRIR_FailsLoud(t *testing.T) {
	db := testInventoryPostingDB(t)
	s := setupInventoryPosting(t, db)
	// Flip flag but do NOT configure GR/IR.
	if err := db.Model(&models.Company{}).
		Where("id = ?", s.companyID).
		Update("receipt_required", true).Error; err != nil {
		t.Fatalf("flip flag: %v", err)
	}
	bill := buildBillWithLines(t, db, s, "BILL-H4-NOGRIR", []models.BillLine{
		{
			ProductServiceID: &s.stockItemID,
			Description:      "Widget",
			Qty:              decimal.NewFromInt(10),
			UnitPrice:        decimal.NewFromInt(20),
			ExpenseAccountID: &s.expenseID,
			LineNet:          decimal.NewFromInt(200),
			LineTotal:        decimal.NewFromInt(200),
		},
	})
	err := PostBill(db, s.companyID, bill.ID, "tester", nil)
	if !isErr(err, ErrGRIRAccountNotConfigured) {
		t.Fatalf("got %v want ErrGRIRAccountNotConfigured", err)
	}
	// Bill must remain in draft — tx rolled back.
	var unchanged models.Bill
	db.First(&unchanged, bill.ID)
	if unchanged.Status != models.BillStatusDraft {
		t.Fatalf("status: got %q want draft (tx must roll back on guard)", unchanged.Status)
	}
}

// ── H.4 addition 2: Void symmetry under flag=true ────────────────────────────

// A bill posted under flag=true must void cleanly: the reversal JE
// posts, and because no inventory movements were created at post
// time (CreatePurchaseMovements was skipped), no reversal movements
// are created either. The existing ReversePurchaseMovements helper
// loads zero originals and exits cleanly — this test locks that
// contract end-to-end rather than by inspection.
func TestVoidBill_H4_FlagOn_StockBill_ReversesJENoMovements(t *testing.T) {
	db := testInventoryPostingDB(t)
	s := setupInventoryPosting(t, db)
	grirID := seedH4Fixture(t, db, s)

	bill := buildBillWithLines(t, db, s, "BILL-H4-VOID", []models.BillLine{
		{
			ProductServiceID: &s.stockItemID,
			Description:      "Widget",
			Qty:              decimal.NewFromInt(10),
			UnitPrice:        decimal.NewFromInt(20),
			ExpenseAccountID: &s.expenseID,
			LineNet:          decimal.NewFromInt(200),
			LineTotal:        decimal.NewFromInt(200),
		},
	})
	if err := PostBill(db, s.companyID, bill.ID, "tester", nil); err != nil {
		t.Fatalf("PostBill: %v", err)
	}
	if err := VoidBill(db, s.companyID, bill.ID, "tester", nil); err != nil {
		t.Fatalf("VoidBill: %v", err)
	}
	// Reversal JE exists; original JE remains posted and is cancelled by the reversal.
	var voided models.Bill
	db.First(&voided, bill.ID)
	if voided.Status != models.BillStatusVoided {
		t.Fatalf("bill status: got %q want voided", voided.Status)
	}
	var origJE models.JournalEntry
	db.First(&origJE, *voided.JournalEntryID)
	if origJE.Status != models.JournalEntryStatusPosted {
		t.Fatalf("orig JE status: got %q want posted", origJE.Status)
	}
	// No bill movements existed → no reversal movements.
	var revCount int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ?", s.companyID, "bill_reversal").
		Count(&revCount)
	if revCount != 0 {
		t.Fatalf("reversal movements: got %d want 0 (no originals existed under flag=true)", revCount)
	}
	// Net GR/IR balance returns to zero: Dr 200 (void) - Cr 0 + original Dr 200 = net zero.
	// Simpler: count GR/IR journal_lines — should have two, summing to net zero.
	var grirLines []models.JournalLine
	db.Where("company_id = ? AND account_id = ?", s.companyID, grirID).Find(&grirLines)
	netDebit := decimal.Zero
	for _, l := range grirLines {
		netDebit = netDebit.Add(l.Debit).Sub(l.Credit)
	}
	if !netDebit.IsZero() {
		t.Fatalf("net GR/IR movement after post+void: got %s want 0", netDebit)
	}
}
