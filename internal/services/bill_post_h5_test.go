// 遵循project_guide.md
package services

// bill_post_h5_test.go — Phase H slice H.5: Bill ↔ Receipt line-to-line
// matching + Purchase Price Variance (PPV).
//
// Approved matching semantics:
//   - one bill line → at most one receipt line
//   - one receipt line → may be referenced by multiple bill lines
//     over time (cumulative partial settlement)
//   - matched portion: Dr GR/IR at receipt unit cost; Dr/Cr PPV for
//     variance (bill_price − receipt_cost) signed
//   - unmatched portion (overflow beyond the receipt's remaining qty):
//     stays on the H.4 blind path (Dr GR/IR at bill_price)
//   - no reverse pointer on receipt_lines
//
// Each test constructs a full end-to-end scenario: seed receipt,
// post it (flag=true flow from H.3), then create a bill linking one
// of its lines and post the bill. Assertions span: no bill-derived
// inventory movements, JE debit/credit decomposition (GR/IR vs PPV),
// and cumulative match bookkeeping across two successive bills.

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ── H.5 fixture helpers ─────────────────────────────────────────────────────

// h5Fixture bundles the account IDs a matching test needs.
type h5Fixture struct {
	inv       invPostingSetup
	grirID    uint
	ppvID     uint
	warehouse uint
}

func seedH5Full(t *testing.T, db *gorm.DB) h5Fixture {
	t.Helper()
	s := setupInventoryPosting(t, db)

	// Flip receipt_required + seed GR/IR + PPV.
	grirID := seedH4Fixture(t, db, s)
	ppvAcct := models.Account{
		CompanyID:         s.companyID,
		Code:              "5900",
		Name:              "Purchase Price Variance",
		RootAccountType:   models.RootExpense,
		DetailAccountType: "operating_expense",
		IsActive:          true,
	}
	if err := db.Create(&ppvAcct).Error; err != nil {
		t.Fatalf("seed PPV account: %v", err)
	}
	if err := db.Model(&models.Company{}).
		Where("id = ?", s.companyID).
		Update("purchase_price_variance_account_id", ppvAcct.ID).Error; err != nil {
		t.Fatalf("wire PPV: %v", err)
	}

	// Warehouse for receipts.
	wh := models.Warehouse{CompanyID: s.companyID, Name: "Main", Code: "MAIN", IsActive: true}
	if err := db.Create(&wh).Error; err != nil {
		t.Fatalf("seed warehouse: %v", err)
	}

	// Migrate receipts tables (not part of the default invPostingDB set).
	if err := db.AutoMigrate(&models.Receipt{}, &models.ReceiptLine{}, &models.Warehouse{}); err != nil {
		t.Fatalf("migrate receipts: %v", err)
	}

	return h5Fixture{inv: s, grirID: grirID, ppvID: ppvAcct.ID, warehouse: wh.ID}
}

// postReceiptForItem creates and posts a Receipt with one line for
// the given product at (qty, unit_cost). Returns the receipt line ID
// — this is the pointer a bill line will set to engage matching.
func postReceiptForItem(t *testing.T, db *gorm.DB, fx h5Fixture, number string, itemID uint, qty, unitCost decimal.Decimal) uint {
	t.Helper()
	vendorIDCopy := fx.inv.vendorID
	receipt, err := CreateReceipt(db, CreateReceiptInput{
		CompanyID:   fx.inv.companyID,
		ReceiptNumber: number,
		VendorID:    &vendorIDCopy,
		WarehouseID: fx.warehouse,
		ReceiptDate: time.Now().UTC(),
		Lines: []CreateReceiptLineInput{
			{
				ProductServiceID: itemID,
				Description:      "rx line",
				Qty:              qty,
				UnitCost:         unitCost,
			},
		},
	})
	if err != nil {
		t.Fatalf("create receipt %s: %v", number, err)
	}
	if _, err := PostReceipt(db, fx.inv.companyID, receipt.ID, "tester", nil); err != nil {
		t.Fatalf("post receipt %s: %v", number, err)
	}
	var rls []models.ReceiptLine
	db.Where("receipt_id = ?", receipt.ID).Find(&rls)
	if len(rls) != 1 {
		t.Fatalf("receipt %s: got %d lines want 1", number, len(rls))
	}
	return rls[0].ID
}

// buildBillWithMatchedLine creates a bill with a single stock line
// pointing at the given receipt line. Posts it through the full H.5
// flow.
func buildAndPostMatchedBill(t *testing.T, db *gorm.DB, fx h5Fixture, number string, itemID, receiptLineID uint, qty, unitPrice decimal.Decimal) models.Bill {
	t.Helper()
	expenseAcctID := fx.inv.expenseID
	lineNet := qty.Mul(unitPrice)
	bill := buildBillWithLines(t, db, fx.inv, number, []models.BillLine{
		{
			ProductServiceID: &itemID,
			Description:      "bill line",
			Qty:              qty,
			UnitPrice:        unitPrice,
			ExpenseAccountID: &expenseAcctID,
			LineNet:          lineNet,
			LineTotal:        lineNet,
			ReceiptLineID:    &receiptLineID,
		},
	})
	// Re-persist the line with receipt_line_id since buildBillWithLines
	// might not copy it via Create (struct copy below ensures it).
	db.Model(&models.BillLine{}).
		Where("bill_id = ?", bill.ID).
		Update("receipt_line_id", receiptLineID)

	if err := PostBill(db, fx.inv.companyID, bill.ID, "tester", nil); err != nil {
		t.Fatalf("PostBill %s: %v", number, err)
	}
	return bill
}

// ── Happy paths: exact match with signed PPV ───────────────────────────────

// Receipt: 10 widgets @ $5 (GR/IR credit $50)
// Bill:    10 widgets @ $6 → full match + unfavorable variance
// Expected JE:
//   Dr GR/IR  50  (matched_qty × receipt_cost)
//   Dr PPV    10  (matched_qty × (6−5))
//   Cr AP     60  (line_net)
// No bill-derived inventory movements.
func TestPostBill_H5_ExactMatch_UnfavorableVariance(t *testing.T) {
	db := testInventoryPostingDB(t)
	fx := seedH5Full(t, db)

	rlID := postReceiptForItem(t, db, fx, "RCPT-H5-U", fx.inv.stockItemID,
		decimal.NewFromInt(10), decimal.NewFromFloat(5.00))

	bill := buildAndPostMatchedBill(t, db, fx, "BILL-H5-U", fx.inv.stockItemID, rlID,
		decimal.NewFromInt(10), decimal.NewFromFloat(6.00))

	if n := countMovementsForBill(t, db, fx.inv.companyID, bill.ID); n != 0 {
		t.Fatalf("bill movements: got %d want 0", n)
	}
	var posted models.Bill
	db.First(&posted, bill.ID)
	jeID := *posted.JournalEntryID

	grirDebit := sumJournalDebitByAccount(t, db, fx.inv.companyID, fx.grirID, jeID)
	ppvDebit := sumJournalDebitByAccount(t, db, fx.inv.companyID, fx.ppvID, jeID)
	if !grirDebit.Equal(decimal.NewFromInt(50)) {
		t.Fatalf("GR/IR debit: got %s want 50 (10 × receipt_cost 5)", grirDebit)
	}
	if !ppvDebit.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("PPV debit: got %s want 10 (10 × (6−5) unfavorable)", ppvDebit)
	}
}

// Receipt: 10 widgets @ $8 (GR/IR credit $80)
// Bill:    10 widgets @ $5 → full match + favorable variance (bill < receipt)
// Expected JE:
//   Dr GR/IR  80  (matched × receipt_cost)
//   Cr PPV    30  (matched × (5−8) → credit for favorable direction)
//   Cr AP     50  (line_net)
func TestPostBill_H5_ExactMatch_FavorableVariance(t *testing.T) {
	db := testInventoryPostingDB(t)
	fx := seedH5Full(t, db)

	rlID := postReceiptForItem(t, db, fx, "RCPT-H5-F", fx.inv.stockItemID,
		decimal.NewFromInt(10), decimal.NewFromFloat(8.00))

	bill := buildAndPostMatchedBill(t, db, fx, "BILL-H5-F", fx.inv.stockItemID, rlID,
		decimal.NewFromInt(10), decimal.NewFromFloat(5.00))

	var posted models.Bill
	db.First(&posted, bill.ID)
	jeID := *posted.JournalEntryID

	grirDebit := sumJournalDebitByAccount(t, db, fx.inv.companyID, fx.grirID, jeID)
	if !grirDebit.Equal(decimal.NewFromInt(80)) {
		t.Fatalf("GR/IR debit: got %s want 80 (matched × receipt_cost)", grirDebit)
	}

	// PPV credit should be 30 (favorable).
	var ppvLines []models.JournalLine
	db.Where("company_id = ? AND account_id = ? AND journal_entry_id = ?",
		fx.inv.companyID, fx.ppvID, jeID).Find(&ppvLines)
	ppvNet := decimal.Zero
	for _, l := range ppvLines {
		ppvNet = ppvNet.Add(l.Debit).Sub(l.Credit)
	}
	if !ppvNet.Equal(decimal.NewFromInt(-30)) {
		t.Fatalf("PPV net: got %s want -30 (Cr 30 favorable)", ppvNet)
	}
}

// Receipt: 10 widgets @ $5
// Bill:    10 widgets @ $5 → full match, zero variance
// Expected JE:
//   Dr GR/IR  50
//   Cr AP     50
// No PPV fragment at all — zero-variance short-circuit.
func TestPostBill_H5_ExactMatch_ZeroVariance_NoPPVFragment(t *testing.T) {
	db := testInventoryPostingDB(t)
	fx := seedH5Full(t, db)

	rlID := postReceiptForItem(t, db, fx, "RCPT-H5-Z", fx.inv.stockItemID,
		decimal.NewFromInt(10), decimal.NewFromFloat(5.00))
	bill := buildAndPostMatchedBill(t, db, fx, "BILL-H5-Z", fx.inv.stockItemID, rlID,
		decimal.NewFromInt(10), decimal.NewFromFloat(5.00))

	var posted models.Bill
	db.First(&posted, bill.ID)
	var ppvLines int64
	db.Model(&models.JournalLine{}).
		Where("company_id = ? AND account_id = ? AND journal_entry_id = ?",
			fx.inv.companyID, fx.ppvID, *posted.JournalEntryID).
		Count(&ppvLines)
	if ppvLines != 0 {
		t.Fatalf("PPV lines: got %d want 0 (zero-variance must not emit PPV)", ppvLines)
	}
}

// ── Partial & cumulative matching ────────────────────────────────────────────

// Receipt: 10 widgets @ $5 (GR/IR credit $50)
// Bill 1:   6 widgets @ $6 — partial match (6 matched, 4 still available on receipt)
//   Dr GR/IR  30  (6 × 5)
//   Dr PPV     6  (6 × 1)
//   Cr AP     36
// Bill 2:   4 widgets @ $7 — matches remaining 4
//   Dr GR/IR  20  (4 × 5)
//   Dr PPV     8  (4 × 2)
//   Cr AP     28
// Net effect on receipt: fully matched, GR/IR cleared to zero
// (receipt credited 50; bills debited 30+20=50).
func TestPostBill_H5_CumulativePartialMatches_ClearGRIR(t *testing.T) {
	db := testInventoryPostingDB(t)
	fx := seedH5Full(t, db)

	rlID := postReceiptForItem(t, db, fx, "RCPT-H5-C", fx.inv.stockItemID,
		decimal.NewFromInt(10), decimal.NewFromFloat(5.00))

	// Bill 1: 6 units @ $6 — partial, 6 of 10 matched.
	b1 := buildAndPostMatchedBill(t, db, fx, "BILL-H5-C-1", fx.inv.stockItemID, rlID,
		decimal.NewFromInt(6), decimal.NewFromFloat(6.00))
	var p1 models.Bill
	db.First(&p1, b1.ID)
	je1 := *p1.JournalEntryID
	if v := sumJournalDebitByAccount(t, db, fx.inv.companyID, fx.grirID, je1); !v.Equal(decimal.NewFromInt(30)) {
		t.Fatalf("bill1 GR/IR debit: got %s want 30", v)
	}
	if v := sumJournalDebitByAccount(t, db, fx.inv.companyID, fx.ppvID, je1); !v.Equal(decimal.NewFromInt(6)) {
		t.Fatalf("bill1 PPV debit: got %s want 6", v)
	}

	// Bill 2: 4 units @ $7 — picks up remaining 4 of 10.
	b2 := buildAndPostMatchedBill(t, db, fx, "BILL-H5-C-2", fx.inv.stockItemID, rlID,
		decimal.NewFromInt(4), decimal.NewFromFloat(7.00))
	var p2 models.Bill
	db.First(&p2, b2.ID)
	je2 := *p2.JournalEntryID
	if v := sumJournalDebitByAccount(t, db, fx.inv.companyID, fx.grirID, je2); !v.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("bill2 GR/IR debit: got %s want 20 (4 × 5 cumulative)", v)
	}
	if v := sumJournalDebitByAccount(t, db, fx.inv.companyID, fx.ppvID, je2); !v.Equal(decimal.NewFromInt(8)) {
		t.Fatalf("bill2 PPV debit: got %s want 8 (4 × 2 variance)", v)
	}

	// Net GR/IR across receipt + both bills = 0 (receipt credited 50, bills debited 50).
	var allGRIR []models.JournalLine
	db.Where("company_id = ? AND account_id = ?", fx.inv.companyID, fx.grirID).
		Find(&allGRIR)
	netGRIR := decimal.Zero
	for _, l := range allGRIR {
		netGRIR = netGRIR.Add(l.Debit).Sub(l.Credit)
	}
	if !netGRIR.IsZero() {
		t.Fatalf("net GR/IR after two matching bills: got %s want 0", netGRIR)
	}
}

// ── Over-match: excess bill qty goes to blind GR/IR ─────────────────────────

// Receipt: 10 widgets @ $5 (GR/IR credit $50)
// Bill:    12 widgets @ $7
//   Matched portion:   10 × 5 = 50 → Dr GR/IR; 10 × (7−5) = 20 → Dr PPV
//   Unmatched portion:  2 × 7 = 14 → Dr GR/IR (blind, H.4 style)
//   AP credit = 12 × 7 = 84
//   Total debits = 50 + 20 + 14 = 84 ✓
func TestPostBill_H5_OverMatch_OverflowToBlindGRIR(t *testing.T) {
	db := testInventoryPostingDB(t)
	fx := seedH5Full(t, db)

	rlID := postReceiptForItem(t, db, fx, "RCPT-H5-O", fx.inv.stockItemID,
		decimal.NewFromInt(10), decimal.NewFromFloat(5.00))
	bill := buildAndPostMatchedBill(t, db, fx, "BILL-H5-O", fx.inv.stockItemID, rlID,
		decimal.NewFromInt(12), decimal.NewFromFloat(7.00))

	var posted models.Bill
	db.First(&posted, bill.ID)
	jeID := *posted.JournalEntryID

	// GR/IR total debit = 50 (matched) + 14 (blind) = 64.
	if v := sumJournalDebitByAccount(t, db, fx.inv.companyID, fx.grirID, jeID); !v.Equal(decimal.NewFromInt(64)) {
		t.Fatalf("GR/IR debit: got %s want 64 (50 match + 14 blind)", v)
	}
	// PPV only on matched portion.
	if v := sumJournalDebitByAccount(t, db, fx.inv.companyID, fx.ppvID, jeID); !v.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("PPV debit: got %s want 20 (10 × 2 on matched only)", v)
	}
}

// ── Guards ──────────────────────────────────────────────────────────────────

// PPV unconfigured + matching engaged → fail loud, tx rollback.
func TestPostBill_H5_MatchingWithoutPPVConfigured_FailsLoud(t *testing.T) {
	db := testInventoryPostingDB(t)
	fx := seedH5Full(t, db)
	// Clear PPV after seedH5Full wired it.
	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.inv.companyID).
		Update("purchase_price_variance_account_id", nil).Error; err != nil {
		t.Fatalf("clear PPV: %v", err)
	}
	rlID := postReceiptForItem(t, db, fx, "RCPT-H5-NP", fx.inv.stockItemID,
		decimal.NewFromInt(10), decimal.NewFromFloat(5.00))

	expenseAcctID := fx.inv.expenseID
	b := buildBillWithLines(t, db, fx.inv, "BILL-H5-NP", []models.BillLine{
		{
			ProductServiceID: &fx.inv.stockItemID,
			Description:      "line",
			Qty:              decimal.NewFromInt(10),
			UnitPrice:        decimal.NewFromFloat(5.00),
			ExpenseAccountID: &expenseAcctID,
			LineNet:          decimal.NewFromInt(50),
			LineTotal:        decimal.NewFromInt(50),
			ReceiptLineID:    &rlID,
		},
	})
	db.Model(&models.BillLine{}).Where("bill_id = ?", b.ID).Update("receipt_line_id", rlID)

	err := PostBill(db, fx.inv.companyID, b.ID, "tester", nil)
	if !isErr(err, ErrPPVAccountNotConfigured) {
		t.Fatalf("got %v want ErrPPVAccountNotConfigured", err)
	}
	var unchanged models.Bill
	db.First(&unchanged, b.ID)
	if unchanged.Status != models.BillStatusDraft {
		t.Fatalf("status: got %q want draft (tx must roll back)", unchanged.Status)
	}
}

// Cross-company receipt_line_id → rejected.
func TestPostBill_H5_CrossCompanyReceiptRef_Rejected(t *testing.T) {
	db := testInventoryPostingDB(t)
	fx := seedH5Full(t, db)

	// Seed a second company with its own receipt.
	other := models.Company{Name: "other-co", IsActive: true, BaseCurrencyCode: "CAD"}
	db.Create(&other)
	otherWh := models.Warehouse{CompanyID: other.ID, Name: "OWH", Code: "OWH", IsActive: true}
	db.Create(&otherWh)
	otherItem := models.ProductService{
		CompanyID: other.ID, Name: "OI",
		Type: models.ProductServiceTypeInventory, IsActive: true,
	}
	otherItem.ApplyTypeDefaults()
	db.Create(&otherItem)
	rcpt := models.Receipt{
		CompanyID: other.ID, WarehouseID: otherWh.ID,
		ReceiptDate: time.Now().UTC(), Status: models.ReceiptStatusPosted,
	}
	db.Create(&rcpt)
	foreignRL := models.ReceiptLine{
		CompanyID: other.ID, ReceiptID: rcpt.ID,
		ProductServiceID: otherItem.ID, Qty: decimal.NewFromInt(10),
		UnitCost: decimal.NewFromFloat(5.00),
	}
	db.Create(&foreignRL)

	expenseAcctID := fx.inv.expenseID
	b := buildBillWithLines(t, db, fx.inv, "BILL-H5-CROSS", []models.BillLine{
		{
			ProductServiceID: &fx.inv.stockItemID,
			Description:      "line",
			Qty:              decimal.NewFromInt(5),
			UnitPrice:        decimal.NewFromFloat(6.00),
			ExpenseAccountID: &expenseAcctID,
			LineNet:          decimal.NewFromInt(30),
			LineTotal:        decimal.NewFromInt(30),
			ReceiptLineID:    &foreignRL.ID,
		},
	})
	db.Model(&models.BillLine{}).Where("bill_id = ?", b.ID).Update("receipt_line_id", foreignRL.ID)

	err := PostBill(db, fx.inv.companyID, b.ID, "tester", nil)
	if !isErr(err, ErrBillLineReceiptRefInvalid) {
		t.Fatalf("got %v want ErrBillLineReceiptRefInvalid", err)
	}
}

// Draft (non-posted) receipt reference → rejected.
func TestPostBill_H5_DraftReceiptRef_Rejected(t *testing.T) {
	db := testInventoryPostingDB(t)
	fx := seedH5Full(t, db)

	// Create receipt but DO NOT post it — stays in draft.
	vid := fx.inv.vendorID
	drft, err := CreateReceipt(db, CreateReceiptInput{
		CompanyID:   fx.inv.companyID,
		ReceiptNumber: "RCPT-DRAFT",
		VendorID:    &vid,
		WarehouseID: fx.warehouse,
		ReceiptDate: time.Now().UTC(),
		Lines: []CreateReceiptLineInput{
			{ProductServiceID: fx.inv.stockItemID, Qty: decimal.NewFromInt(10), UnitCost: decimal.NewFromFloat(5.00)},
		},
	})
	if err != nil {
		t.Fatalf("create draft receipt: %v", err)
	}
	var rls []models.ReceiptLine
	db.Where("receipt_id = ?", drft.ID).Find(&rls)
	rlID := rls[0].ID

	expenseAcctID := fx.inv.expenseID
	b := buildBillWithLines(t, db, fx.inv, "BILL-H5-DRFT", []models.BillLine{
		{
			ProductServiceID: &fx.inv.stockItemID,
			Description:      "line",
			Qty:              decimal.NewFromInt(5),
			UnitPrice:        decimal.NewFromFloat(6.00),
			ExpenseAccountID: &expenseAcctID,
			LineNet:          decimal.NewFromInt(30),
			LineTotal:        decimal.NewFromInt(30),
			ReceiptLineID:    &rlID,
		},
	})
	db.Model(&models.BillLine{}).Where("bill_id = ?", b.ID).Update("receipt_line_id", rlID)

	err = PostBill(db, fx.inv.companyID, b.ID, "tester", nil)
	if !isErr(err, ErrBillLineReceiptRefInvalid) {
		t.Fatalf("got %v want ErrBillLineReceiptRefInvalid (receipt is draft)", err)
	}
}

// ── Legacy path still byte-identical ────────────────────────────────────────

// flag=false bills ignore receipt_line_id entirely: H.5 matching never
// engages, Bill-forms-inventory legacy path proceeds untouched.
func TestPostBill_H5_FlagOff_WithReceiptRef_IgnoresMatching(t *testing.T) {
	db := testInventoryPostingDB(t)
	// No H4/H5 fixture — default flag=false + no GR/IR/PPV.
	s := setupInventoryPosting(t, db)
	// Migrate receipt tables so the FK can resolve.
	db.AutoMigrate(&models.Receipt{}, &models.ReceiptLine{}, &models.Warehouse{})
	wh := models.Warehouse{CompanyID: s.companyID, Name: "M", Code: "M", IsActive: true}
	db.Create(&wh)
	// Seed a posted receipt line directly (bypass flag=true post guard).
	rcpt := models.Receipt{
		CompanyID: s.companyID, WarehouseID: wh.ID,
		ReceiptDate: time.Now().UTC(), Status: models.ReceiptStatusPosted,
	}
	db.Create(&rcpt)
	rl := models.ReceiptLine{
		CompanyID: s.companyID, ReceiptID: rcpt.ID,
		ProductServiceID: s.stockItemID, Qty: decimal.NewFromInt(10),
		UnitCost: decimal.NewFromFloat(5.00),
	}
	db.Create(&rl)

	expenseAcctID := s.expenseID
	b := buildBillWithLines(t, db, s, "BILL-H5-OFF-REF", []models.BillLine{
		{
			ProductServiceID: &s.stockItemID,
			Description:      "line",
			Qty:              decimal.NewFromInt(5),
			UnitPrice:        decimal.NewFromFloat(6.00),
			ExpenseAccountID: &expenseAcctID,
			LineNet:          decimal.NewFromInt(30),
			LineTotal:        decimal.NewFromInt(30),
			ReceiptLineID:    &rl.ID, // set but inert under flag=false
		},
	})
	db.Model(&models.BillLine{}).Where("bill_id = ?", b.ID).Update("receipt_line_id", rl.ID)

	if err := PostBill(db, s.companyID, b.ID, "tester", nil); err != nil {
		t.Fatalf("PostBill: %v", err)
	}
	// Legacy path: inventory movement created.
	if n := countMovementsForBill(t, db, s.companyID, b.ID); n != 1 {
		t.Fatalf("movements: got %d want 1 (legacy path)", n)
	}
	var posted models.Bill
	db.First(&posted, b.ID)
	// Inventory Asset debited (not GR/IR — flag=false).
	if v := sumJournalDebitByAccount(t, db, s.companyID, s.invAssetID, *posted.JournalEntryID); !v.Equal(decimal.NewFromInt(30)) {
		t.Fatalf("inventory asset debit: got %s want 30", v)
	}
}
