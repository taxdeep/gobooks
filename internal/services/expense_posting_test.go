// 遵循project_guide.md
package services

// expense_posting_test.go — IN.2 contract tests.
//
// Locks the five Rule #4 invariants for the Expense path:
//
//  1. Legacy mode + stock-item line → inventory_movements row lands
//     with source_type='expense', quantity_delta=+qty, and the JE
//     debits Inventory (routed via ProductService.InventoryAccountID)
//     while crediting the PaymentAccount.
//
//  2. Legacy mode + pure-expense line → JE only (Dr ExpenseAccount /
//     Cr PaymentAccount); NO inventory_movements row. The amount-only
//     fallback preserves Q1 behavior.
//
//  3. Controlled mode (receipt_required=true) + stock-item line →
//     PostExpense REJECTS with ErrExpenseStockItemRequiresReceipt.
//     No JE, no inventory movement, expense stays in status='draft'.
//     Q2 invariant enforced.
//
//  4. Void after a stock-item post → reverses inventory movements +
//     reverses JE (original marked reversed, reversal JE posted, new
//     ledger entries). Expense status flips to voided.
//
//  5. Post without PaymentAccountID → ErrExpensePaymentAccountRequiredForPost.
//     The JE needs a credit target.

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"balanciz/internal/models"
)

func testExpenseIN2DB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:expense_in2_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Vendor{},
		&models.Customer{},
		&models.Account{},
		&models.TaxCode{},
		&models.ProductService{},
		&models.Warehouse{},
		&models.Expense{},
		&models.ExpenseLine{},
		&models.Task{},
		&models.TaskInvoiceSource{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.AuditLog{},
		&models.InventoryMovement{},
		&models.InventoryBalance{},
		&models.InventoryCostLayer{},
		&models.InventoryLot{},
		&models.InventorySerialUnit{},
		&models.InventoryLayerConsumption{},
		&models.InventoryTrackingConsumption{},
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

type expenseIN2Fixture struct {
	CompanyID          uint
	ItemID             uint
	WarehouseID        uint
	ExpenseAccountID   uint
	InventoryAccountID uint
	PaymentAccountID   uint
}

func seedExpenseIN2Fixture(t *testing.T, db *gorm.DB) expenseIN2Fixture {
	t.Helper()
	co := models.Company{Name: "in2-co", IsActive: true, BaseCurrencyCode: "CAD"}
	if err := db.Create(&co).Error; err != nil {
		t.Fatalf("seed company: %v", err)
	}

	expAcct := models.Account{CompanyID: co.ID, Code: "6100", Name: "Office Expense",
		RootAccountType: models.RootExpense, DetailAccountType: "operating_expense", IsActive: true}
	invAcct := models.Account{CompanyID: co.ID, Code: "1300", Name: "Inventory Asset",
		RootAccountType: models.RootAsset, DetailAccountType: models.DetailInventory, IsActive: true}
	payAcct := models.Account{CompanyID: co.ID, Code: "1200", Name: "Credit Card",
		RootAccountType: models.RootAsset, DetailAccountType: "credit_card", IsActive: true}
	revAcct := models.Account{CompanyID: co.ID, Code: "4000", Name: "Revenue",
		RootAccountType: models.RootRevenue, DetailAccountType: "sales_revenue", IsActive: true}
	for _, a := range []*models.Account{&expAcct, &invAcct, &payAcct, &revAcct} {
		if err := db.Create(a).Error; err != nil {
			t.Fatalf("seed account %s: %v", a.Code, err)
		}
	}

	wh := models.Warehouse{CompanyID: co.ID, Name: "Main", Code: "MAIN",
		IsActive: true, IsDefault: true}
	if err := db.Create(&wh).Error; err != nil {
		t.Fatalf("seed warehouse: %v", err)
	}

	item := models.ProductService{
		CompanyID: co.ID, Name: "Coke Can",
		Type:               models.ProductServiceTypeInventory,
		RevenueAccountID:   revAcct.ID,
		InventoryAccountID: &invAcct.ID,
		IsActive:           true,
	}
	item.ApplyTypeDefaults()
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("seed item: %v", err)
	}

	return expenseIN2Fixture{
		CompanyID:          co.ID,
		ItemID:             item.ID,
		WarehouseID:        wh.ID,
		ExpenseAccountID:   expAcct.ID,
		InventoryAccountID: invAcct.ID,
		PaymentAccountID:   payAcct.ID,
	}
}

func createDraftStockExpense(t *testing.T, db *gorm.DB, fx expenseIN2Fixture) uint {
	t.Helper()
	itemID := fx.ItemID
	expAcctID := fx.InventoryAccountID // stock line's "Category" defaults to inv acct
	payAcctID := fx.PaymentAccountID
	whID := fx.WarehouseID
	exp, err := CreateExpense(db, ExpenseInput{
		CompanyID:        fx.CompanyID,
		ExpenseDate:      time.Now().UTC(),
		Description:      "12 cans of Coke",
		CurrencyCode:     "CAD",
		Amount:           decimal.NewFromInt(20), // matches sum of LineTotal
		ExpenseAccountID: &expAcctID,
		PaymentAccountID: &payAcctID,
		PaymentMethod:    models.PaymentMethodCreditCard,
		WarehouseID:      &whID,
		Lines: []ExpenseLineInput{
			{
				Description:      "Coke Can",
				ProductServiceID: &itemID,
				ExpenseAccountID: &expAcctID,
				Qty:              decimal.NewFromInt(12),
				UnitPrice:        decimal.RequireFromString("1.67"),
				Amount:           decimal.NewFromFloat(20.04), // qty × unit_price, rounded
				LineTotal:        decimal.NewFromFloat(20.04),
			},
		},
	})
	if err != nil {
		t.Fatalf("create stock expense: %v", err)
	}
	return exp.ID
}

// ── Scenario 1: legacy stock line forms inventory ───────────────────────────

func TestPostExpense_IN2_LegacyStockLineFormsInventoryAndJE(t *testing.T) {
	db := testExpenseIN2DB(t)
	fx := seedExpenseIN2Fixture(t, db)
	expenseID := createDraftStockExpense(t, db, fx)

	posted, err := PostExpense(db, fx.CompanyID, expenseID, "admin@test", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if posted.Status != models.ExpenseStatusPosted {
		t.Fatalf("status: got %q want posted", posted.Status)
	}
	if posted.JournalEntryID == nil {
		t.Fatal("expected JournalEntryID linked")
	}

	// (1) inventory_movements row.
	var movs []models.InventoryMovement
	if err := db.Where("company_id = ? AND source_type = ? AND source_id = ?",
		fx.CompanyID, "expense", expenseID).Find(&movs).Error; err != nil {
		t.Fatalf("load movements: %v", err)
	}
	if len(movs) != 1 {
		t.Fatalf("inventory_movements: got %d want 1 (Rule #4 chain)", len(movs))
	}
	if !movs[0].QuantityDelta.Equal(decimal.NewFromInt(12)) {
		t.Fatalf("qty_delta: got %s want +12", movs[0].QuantityDelta)
	}

	// (2) Inventory balance reflects receipt.
	var bal models.InventoryBalance
	if err := db.Where("company_id = ? AND item_id = ?",
		fx.CompanyID, fx.ItemID).First(&bal).Error; err != nil {
		t.Fatalf("load balance: %v", err)
	}
	if !bal.QuantityOnHand.Equal(decimal.NewFromInt(12)) {
		t.Fatalf("on_hand: got %s want 12", bal.QuantityOnHand)
	}

	// (3) JE: Dr Inventory, Cr Payment.
	var je models.JournalEntry
	if err := db.Preload("Lines").First(&je, *posted.JournalEntryID).Error; err != nil {
		t.Fatalf("load JE: %v", err)
	}
	if je.SourceType != models.LedgerSourceExpense || je.SourceID != expenseID {
		t.Fatalf("JE source linkage: got %s/%d", je.SourceType, je.SourceID)
	}
	var invDebit, payCredit decimal.Decimal
	for _, l := range je.Lines {
		switch l.AccountID {
		case fx.InventoryAccountID:
			invDebit = invDebit.Add(l.Debit)
		case fx.PaymentAccountID:
			payCredit = payCredit.Add(l.Credit)
		}
	}
	// 12 × 1.67 = 20.04 (authoritative cost from inventory module).
	want := decimal.NewFromFloat(20.04)
	if !invDebit.Equal(want) {
		t.Fatalf("Dr Inventory: got %s want %s", invDebit, want)
	}
	if !payCredit.Equal(want) {
		t.Fatalf("Cr Payment: got %s want %s", payCredit, want)
	}
}

// ── Scenario 2: pure-expense line posts normally, zero inventory ────────────

func TestPostExpense_IN2_PureExpenseLinePostsNoInventory(t *testing.T) {
	db := testExpenseIN2DB(t)
	fx := seedExpenseIN2Fixture(t, db)

	expAcctID := fx.ExpenseAccountID
	payAcctID := fx.PaymentAccountID
	exp, err := CreateExpense(db, ExpenseInput{
		CompanyID:        fx.CompanyID,
		ExpenseDate:      time.Now().UTC(),
		Description:      "Gas",
		CurrencyCode:     "CAD",
		Amount:           decimal.NewFromInt(35),
		ExpenseAccountID: &expAcctID,
		PaymentAccountID: &payAcctID,
		PaymentMethod:    models.PaymentMethodCreditCard,
		Lines: []ExpenseLineInput{{
			Description:      "Gas",
			ExpenseAccountID: &expAcctID,
			Amount:           decimal.NewFromInt(35),
			LineTotal:        decimal.NewFromInt(35),
		}},
	})
	if err != nil {
		t.Fatalf("create pure expense: %v", err)
	}
	posted, err := PostExpense(db, fx.CompanyID, exp.ID, "admin@test", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if posted.JournalEntryID == nil {
		t.Fatal("expected JE for pure expense")
	}
	// Zero inventory movements.
	var mvCount int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ? AND source_id = ?",
			fx.CompanyID, "expense", exp.ID).
		Count(&mvCount)
	if mvCount != 0 {
		t.Fatalf("pure expense must not form movements; got %d", mvCount)
	}
	// Dr Expense, Cr Payment at 35.00.
	var je models.JournalEntry
	db.Preload("Lines").First(&je, *posted.JournalEntryID)
	var expDebit, payCredit decimal.Decimal
	for _, l := range je.Lines {
		switch l.AccountID {
		case fx.ExpenseAccountID:
			expDebit = expDebit.Add(l.Debit)
		case fx.PaymentAccountID:
			payCredit = payCredit.Add(l.Credit)
		}
	}
	if !expDebit.Equal(decimal.NewFromInt(35)) {
		t.Fatalf("Dr Expense: got %s want 35", expDebit)
	}
	if !payCredit.Equal(decimal.NewFromInt(35)) {
		t.Fatalf("Cr Payment: got %s want 35", payCredit)
	}
}

// ── Scenario 3: controlled mode rejects stock line ──────────────────────────

func TestPostExpense_IN2_ControlledModeRejectsStockLine(t *testing.T) {
	db := testExpenseIN2DB(t)
	fx := seedExpenseIN2Fixture(t, db)

	// Flip receipt_required=true.
	if err := db.Model(&models.Company{}).
		Where("id = ?", fx.CompanyID).
		Update("receipt_required", true).Error; err != nil {
		t.Fatalf("flip controlled mode: %v", err)
	}

	expenseID := createDraftStockExpense(t, db, fx)
	_, err := PostExpense(db, fx.CompanyID, expenseID, "admin@test", nil)
	if err == nil {
		t.Fatalf("expected ErrExpenseStockItemRequiresReceipt")
	}
	if !isErr(err, ErrExpenseStockItemRequiresReceipt) {
		t.Fatalf("got %v want ErrExpenseStockItemRequiresReceipt", err)
	}

	// No JE, no movement, expense stays draft.
	var mvCount int64
	db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ? AND source_id = ?",
			fx.CompanyID, "expense", expenseID).
		Count(&mvCount)
	if mvCount != 0 {
		t.Fatalf("controlled rejection must not leave movements; got %d", mvCount)
	}
	var stillDraft models.Expense
	db.First(&stillDraft, expenseID)
	if stillDraft.Status != models.ExpenseStatusDraft {
		t.Fatalf("status after rejection: got %q want draft", stillDraft.Status)
	}
}

// ── Scenario 4: void reverses inventory + JE ────────────────────────────────

func TestVoidExpense_IN2_ReversesInventoryAndJE(t *testing.T) {
	db := testExpenseIN2DB(t)
	fx := seedExpenseIN2Fixture(t, db)
	expenseID := createDraftStockExpense(t, db, fx)

	posted, err := PostExpense(db, fx.CompanyID, expenseID, "admin@test", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	origJEID := *posted.JournalEntryID

	voided, err := VoidExpense(db, fx.CompanyID, expenseID, "admin@test", nil)
	if err != nil {
		t.Fatalf("void: %v", err)
	}
	if voided.Status != models.ExpenseStatusVoided {
		t.Fatalf("status: got %q want voided", voided.Status)
	}

	// Original JE flipped to reversed.
	var origJE models.JournalEntry
	db.First(&origJE, origJEID)
	if origJE.Status != models.JournalEntryStatusReversed {
		t.Fatalf("orig JE status: got %q want reversed", origJE.Status)
	}
	// Reversal JE exists.
	var revJEs []models.JournalEntry
	db.Where("reversed_from_id = ?", origJEID).Find(&revJEs)
	if len(revJEs) != 1 {
		t.Fatalf("reversal JEs: got %d want 1", len(revJEs))
	}
	// Reversal movement exists.
	var revMvs []models.InventoryMovement
	db.Where("company_id = ? AND source_type = ?",
		fx.CompanyID, "expense_reversal").Find(&revMvs)
	if len(revMvs) != 1 {
		t.Fatalf("reversal movements: got %d want 1", len(revMvs))
	}
	// Balance back to 0.
	var bal models.InventoryBalance
	db.Where("company_id = ? AND item_id = ?", fx.CompanyID, fx.ItemID).First(&bal)
	if !bal.QuantityOnHand.IsZero() {
		t.Fatalf("on_hand after void: got %s want 0", bal.QuantityOnHand)
	}
}

// ── Scenario 5: post without PaymentAccountID rejects ───────────────────────

func TestPostExpense_IN2_PaymentAccountRequiredForPost(t *testing.T) {
	db := testExpenseIN2DB(t)
	fx := seedExpenseIN2Fixture(t, db)

	expAcctID := fx.ExpenseAccountID
	exp, err := CreateExpense(db, ExpenseInput{
		CompanyID:        fx.CompanyID,
		ExpenseDate:      time.Now().UTC(),
		Description:      "Unpaid expense",
		CurrencyCode:     "CAD",
		Amount:           decimal.NewFromInt(10),
		ExpenseAccountID: &expAcctID,
		// Deliberately omit PaymentAccountID.
		Lines: []ExpenseLineInput{{
			Description:      "Unpaid",
			ExpenseAccountID: &expAcctID,
			Amount:           decimal.NewFromInt(10),
			LineTotal:        decimal.NewFromInt(10),
		}},
	})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	_, err = PostExpense(db, fx.CompanyID, exp.ID, "admin@test", nil)
	if !isErr(err, ErrExpensePaymentAccountRequiredForPost) {
		t.Fatalf("got %v want ErrExpensePaymentAccountRequiredForPost", err)
	}
}
