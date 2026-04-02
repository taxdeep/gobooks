// 遵循project_guide.md
package services

// revaluation_test.go — Phase 5 unrealized FX revaluation tests.
//
// Coverage:
//   TestRevaluation_NoOpenForeignDocs      — no foreign docs → returns 0, nil (no run)
//   TestRevaluation_InvoiceRateRose        — USD inv at 1.37, revalue at 1.40 → adj +30, gain JE
//   TestRevaluation_InvoiceRateFell        — USD inv at 1.37, revalue at 1.30 → adj -70, loss JE
//   TestRevaluation_BillRateRose           — USD bill at 1.37, revalue at 1.42 → adj +50, loss JE
//   TestRevaluation_BillRateFell           — USD bill at 1.37, revalue at 1.30 → adj -70, gain JE
//   TestRevaluation_ReversalJEBalanced     — reversal JE is balanced and dated on ReversalDate
//   TestRevaluation_PartiallyPaidInvoice   — uses reduced BalanceDue/BalanceDueBase
//   TestRevaluation_BaseCurrencyDocSkipped — CAD invoice not included in revaluation
//   TestRevaluation_MixedDocsOneRun        — invoice + bill in same run; both JEs balanced
//
// Helpers from other test files (same package):
//   seedSettleCompany, seedSettleAccount, seedPostedInvoice, seedPostedBill — settle_fx_test.go
//   insertRate, fxRate, fxDate                                               — currency_service_test.go

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// testRevalDB opens an in-memory SQLite database with all tables needed for
// revaluation tests, including RevaluationRun and RevaluationLine.
func testRevalDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:reval_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Vendor{},
		&models.Account{},
		&models.Invoice{},
		&models.Bill{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.Currency{},
		&models.ExchangeRate{},
		&models.SettlementAllocation{},
		&models.RevaluationRun{},
		&models.RevaluationLine{},
		&models.AuditLog{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

// ── helpers ──────────────────────────────────────────────────────────────────

func revalDate(year, month, day int) time.Time {
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
}

// sumJELines sums all debit and credit amounts for a given journal entry.
func sumJELines(t *testing.T, db *gorm.DB, jeID uint) (debit, credit decimal.Decimal) {
	t.Helper()
	var lines []models.JournalLine
	db.Where("journal_entry_id = ?", jeID).Find(&lines)
	for _, l := range lines {
		debit = debit.Add(l.Debit)
		credit = credit.Add(l.Credit)
	}
	return
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestRevaluation_NoOpenForeignDocs: no foreign invoices or bills → returns (0, nil).
func TestRevaluation_NoOpenForeignDocs(t *testing.T) {
	db := testRevalDB(t)
	cid := seedSettleCompany(t, db, "CAD")

	// Seed a base-currency invoice (should be skipped).
	custID := seedCustomer(t, db, cid)
	seedPostedInvoice(t, db, cid, custID, "INV-BASE-001", "", "500.00", "500.00")

	runID, err := RunRevaluation(db, RunRevaluationInput{
		CompanyID:    cid,
		RunDate:      revalDate(2024, 12, 31),
		ReversalDate: revalDate(2025, 1, 1),
		Actor:        "test",
	})
	if err != nil {
		t.Fatalf("RunRevaluation: %v", err)
	}
	if runID != 0 {
		t.Errorf("expected runID=0 (nothing to revalue), got %d", runID)
	}
}

// TestRevaluation_InvoiceRateRose: USD inv $1000 posted at 1.37 (base 1370).
// Revalue at 1.40 → newBase 1400, adjustment +30, unrealized gain.
//
//   Revaluation JE:  DR AR 30,  CR FX 30
//   Reversal JE:     DR FX 30,  CR AR 30
func TestRevaluation_InvoiceRateRose(t *testing.T) {
	db := testRevalDB(t)
	cid := seedSettleCompany(t, db, "CAD")
	custID := seedCustomer(t, db, cid)

	arID := seedSettleAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	invID := seedPostedInvoice(t, db, cid, custID, "INV-FX-001", "USD", "1000.00", "1370.00")

	insertRate(t, db, nil, "USD", "CAD", fxRate(1.40), fxDate(2024, 12, 31))

	runDate := revalDate(2024, 12, 31)
	reversalDate := revalDate(2025, 1, 1)

	runID, err := RunRevaluation(db, RunRevaluationInput{
		CompanyID:    cid,
		RunDate:      runDate,
		ReversalDate: reversalDate,
		Actor:        "test",
	})
	if err != nil {
		t.Fatalf("RunRevaluation: %v", err)
	}
	if runID == 0 {
		t.Fatal("expected a revaluation run to be created")
	}

	// Check revaluation run.
	var run models.RevaluationRun
	db.First(&run, runID)
	if run.Status != models.RevaluationRunStatusPosted {
		t.Errorf("run status: want posted, got %s", run.Status)
	}
	if run.JournalEntryID == 0 {
		t.Error("run.JournalEntryID should be set")
	}
	if run.ReversalJEID == nil || *run.ReversalJEID == 0 {
		t.Error("run.ReversalJEID should be set")
	}

	// Check revaluation line.
	var line models.RevaluationLine
	db.Where("revaluation_run_id = ? AND document_id = ?", runID, invID).First(&line)
	if line.ID == 0 {
		t.Fatal("revaluation line not found")
	}
	if !line.Adjustment.Equal(decimal.RequireFromString("30.00")) {
		t.Errorf("Adjustment: want 30.00, got %s", line.Adjustment)
	}
	if !line.OldBase.Equal(decimal.RequireFromString("1370.00")) {
		t.Errorf("OldBase: want 1370.00, got %s", line.OldBase)
	}
	if !line.NewBase.Equal(decimal.RequireFromString("1400.00")) {
		t.Errorf("NewBase: want 1400.00, got %s", line.NewBase)
	}
	if line.AccountID != arID {
		t.Errorf("AccountID: want AR account %d, got %d", arID, line.AccountID)
	}

	// Revaluation JE must be balanced: DR AR 30, CR FX 30.
	dr, cr := sumJELines(t, db, run.JournalEntryID)
	if !dr.Equal(cr) {
		t.Errorf("revaluation JE imbalanced: debit %s ≠ credit %s", dr, cr)
	}
	if !dr.Equal(decimal.RequireFromString("30.00")) {
		t.Errorf("revaluation JE total: want 30.00, got %s", dr)
	}

	// Reversal JE must be balanced and dated on ReversalDate.
	var reversalJE models.JournalEntry
	db.First(&reversalJE, *run.ReversalJEID)
	if !reversalJE.EntryDate.Equal(reversalDate) {
		t.Errorf("reversal JE date: want %s, got %s", reversalDate, reversalJE.EntryDate)
	}
	rdr, rcr := sumJELines(t, db, *run.ReversalJEID)
	if !rdr.Equal(rcr) {
		t.Errorf("reversal JE imbalanced: debit %s ≠ credit %s", rdr, rcr)
	}
	if !rdr.Equal(decimal.RequireFromString("30.00")) {
		t.Errorf("reversal JE total: want 30.00, got %s", rdr)
	}

	// Verify revaluation does NOT touch the invoice's BalanceDue or BalanceDueBase.
	var inv models.Invoice
	db.First(&inv, invID)
	if !inv.BalanceDue.Equal(decimal.RequireFromString("1000.00")) {
		t.Errorf("BalanceDue must not change: want 1000.00, got %s", inv.BalanceDue)
	}
	if !inv.BalanceDueBase.Equal(decimal.RequireFromString("1370.00")) {
		t.Errorf("BalanceDueBase must not change: want 1370.00, got %s", inv.BalanceDueBase)
	}
}

// TestRevaluation_InvoiceRateFell: USD inv $1000 posted at 1.37, revalue at 1.30.
// adjustment = -70, unrealized loss.
//
//   Revaluation JE:  DR FX 70,  CR AR 70
func TestRevaluation_InvoiceRateFell(t *testing.T) {
	db := testRevalDB(t)
	cid := seedSettleCompany(t, db, "CAD")
	custID := seedCustomer(t, db, cid)

	seedSettleAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	invID := seedPostedInvoice(t, db, cid, custID, "INV-FX-002", "USD", "1000.00", "1370.00")

	insertRate(t, db, nil, "USD", "CAD", fxRate(1.30), fxDate(2024, 12, 31))

	runID, err := RunRevaluation(db, RunRevaluationInput{
		CompanyID:    cid,
		RunDate:      revalDate(2024, 12, 31),
		ReversalDate: revalDate(2025, 1, 1),
		Actor:        "test",
	})
	if err != nil {
		t.Fatalf("RunRevaluation: %v", err)
	}

	var line models.RevaluationLine
	db.Where("revaluation_run_id = ? AND document_id = ?", runID, invID).First(&line)
	if !line.Adjustment.Equal(decimal.RequireFromString("-70.00")) {
		t.Errorf("Adjustment: want -70.00, got %s", line.Adjustment)
	}

	var run models.RevaluationRun
	db.First(&run, runID)
	dr, cr := sumJELines(t, db, run.JournalEntryID)
	if !dr.Equal(cr) {
		t.Errorf("revaluation JE imbalanced: debit %s ≠ credit %s", dr, cr)
	}
	if !dr.Equal(decimal.RequireFromString("70.00")) {
		t.Errorf("revaluation JE total: want 70.00, got %s", dr)
	}

	// Verify reversal is balanced too.
	rdr, rcr := sumJELines(t, db, *run.ReversalJEID)
	if !rdr.Equal(rcr) {
		t.Errorf("reversal JE imbalanced: debit %s ≠ credit %s", rdr, rcr)
	}
}

// TestRevaluation_BillRateRose: USD bill $1000 posted at 1.37 (AP 1370).
// Revalue at 1.42 → newBase 1420, adjustment +50, unrealized loss (AP costs more).
//
//   Revaluation JE:  DR FX 50,  CR AP 50
func TestRevaluation_BillRateRose(t *testing.T) {
	db := testRevalDB(t)
	cid := seedSettleCompany(t, db, "CAD")
	vendorID := seedVendor(t, db, cid)

	apID := seedSettleAccount(t, db, cid, "2000", models.RootLiability, models.DetailAccountsPayable)
	billID := seedPostedBill(t, db, cid, vendorID, "BILL-FX-001", "USD", "1000.00", "1370.00")

	insertRate(t, db, nil, "USD", "CAD", fxRate(1.42), fxDate(2024, 12, 31))

	runID, err := RunRevaluation(db, RunRevaluationInput{
		CompanyID:    cid,
		RunDate:      revalDate(2024, 12, 31),
		ReversalDate: revalDate(2025, 1, 1),
		Actor:        "test",
	})
	if err != nil {
		t.Fatalf("RunRevaluation: %v", err)
	}

	var line models.RevaluationLine
	db.Where("revaluation_run_id = ? AND document_id = ?", runID, billID).First(&line)
	if !line.Adjustment.Equal(decimal.RequireFromString("50.00")) {
		t.Errorf("Adjustment: want 50.00, got %s", line.Adjustment)
	}
	if line.AccountID != apID {
		t.Errorf("AccountID: want AP account %d, got %d", apID, line.AccountID)
	}

	var run models.RevaluationRun
	db.First(&run, runID)
	dr, cr := sumJELines(t, db, run.JournalEntryID)
	if !dr.Equal(cr) {
		t.Errorf("revaluation JE imbalanced: debit %s ≠ credit %s", dr, cr)
	}
	if !dr.Equal(decimal.RequireFromString("50.00")) {
		t.Errorf("revaluation JE total: want 50.00, got %s", dr)
	}

	// Verify bill not changed.
	var bill models.Bill
	db.First(&bill, billID)
	if !bill.BalanceDue.Equal(decimal.RequireFromString("1000.00")) {
		t.Errorf("bill BalanceDue must not change: want 1000.00, got %s", bill.BalanceDue)
	}
	if !bill.BalanceDueBase.Equal(decimal.RequireFromString("1370.00")) {
		t.Errorf("bill BalanceDueBase must not change: want 1370.00, got %s", bill.BalanceDueBase)
	}
}

// TestRevaluation_BillRateFell: USD bill $1000 posted at 1.37, revalue at 1.30.
// adjustment = -70, unrealized gain (AP costs less).
//
//   Revaluation JE:  DR AP 70,  CR FX 70
func TestRevaluation_BillRateFell(t *testing.T) {
	db := testRevalDB(t)
	cid := seedSettleCompany(t, db, "CAD")
	vendorID := seedVendor(t, db, cid)

	seedSettleAccount(t, db, cid, "2000", models.RootLiability, models.DetailAccountsPayable)
	billID := seedPostedBill(t, db, cid, vendorID, "BILL-FX-002", "USD", "1000.00", "1370.00")

	insertRate(t, db, nil, "USD", "CAD", fxRate(1.30), fxDate(2024, 12, 31))

	runID, err := RunRevaluation(db, RunRevaluationInput{
		CompanyID:    cid,
		RunDate:      revalDate(2024, 12, 31),
		ReversalDate: revalDate(2025, 1, 1),
		Actor:        "test",
	})
	if err != nil {
		t.Fatalf("RunRevaluation: %v", err)
	}

	var line models.RevaluationLine
	db.Where("revaluation_run_id = ? AND document_id = ?", runID, billID).First(&line)
	if !line.Adjustment.Equal(decimal.RequireFromString("-70.00")) {
		t.Errorf("Adjustment: want -70.00, got %s", line.Adjustment)
	}

	var run models.RevaluationRun
	db.First(&run, runID)
	dr, cr := sumJELines(t, db, run.JournalEntryID)
	if !dr.Equal(cr) {
		t.Errorf("revaluation JE imbalanced: debit %s ≠ credit %s", dr, cr)
	}
	if !dr.Equal(decimal.RequireFromString("70.00")) {
		t.Errorf("revaluation JE total: want 70.00, got %s", dr)
	}
}

// TestRevaluation_ReversalJEBalanced: explicit check that reversal JE has
// opposite amounts from revaluation JE and is dated on ReversalDate.
func TestRevaluation_ReversalJEBalanced(t *testing.T) {
	db := testRevalDB(t)
	cid := seedSettleCompany(t, db, "CAD")
	custID := seedCustomer(t, db, cid)

	seedSettleAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	seedPostedInvoice(t, db, cid, custID, "INV-FX-003", "USD", "2000.00", "2740.00")

	insertRate(t, db, nil, "USD", "CAD", fxRate(1.40), fxDate(2024, 12, 31))

	runDate := revalDate(2024, 12, 31)
	reversalDate := revalDate(2025, 1, 1)
	runID, err := RunRevaluation(db, RunRevaluationInput{
		CompanyID:    cid,
		RunDate:      runDate,
		ReversalDate: reversalDate,
		Actor:        "test",
	})
	if err != nil {
		t.Fatalf("RunRevaluation: %v", err)
	}

	var run models.RevaluationRun
	db.First(&run, runID)

	// adj = 2000×1.40 − 2740 = 2800 − 2740 = 60
	dr, cr := sumJELines(t, db, run.JournalEntryID)
	if !dr.Equal(cr) {
		t.Errorf("revaluation JE imbalanced: DR %s ≠ CR %s", dr, cr)
	}

	rdr, rcr := sumJELines(t, db, *run.ReversalJEID)
	if !rdr.Equal(rcr) {
		t.Errorf("reversal JE imbalanced: DR %s ≠ CR %s", rdr, rcr)
	}
	if !rdr.Equal(dr) {
		t.Errorf("reversal JE total %s ≠ revaluation JE total %s", rdr, dr)
	}

	// Reversal JE must have opposite line directions.
	var revalLines, reversalLines []models.JournalLine
	db.Where("journal_entry_id = ?", run.JournalEntryID).Order("account_id").Find(&revalLines)
	db.Where("journal_entry_id = ?", *run.ReversalJEID).Order("account_id").Find(&reversalLines)
	if len(revalLines) != len(reversalLines) {
		t.Errorf("line count mismatch: reval %d, reversal %d", len(revalLines), len(reversalLines))
	} else {
		for i := range revalLines {
			if !revalLines[i].Debit.Equal(reversalLines[i].Credit) ||
				!revalLines[i].Credit.Equal(reversalLines[i].Debit) {
				t.Errorf("line %d not mirrored: reval DR=%s CR=%s, reversal DR=%s CR=%s",
					i, revalLines[i].Debit, revalLines[i].Credit,
					reversalLines[i].Debit, reversalLines[i].Credit)
			}
		}
	}

	// Reversal JE must be dated on ReversalDate.
	var reversalJE models.JournalEntry
	db.First(&reversalJE, *run.ReversalJEID)
	if !reversalJE.EntryDate.Equal(reversalDate) {
		t.Errorf("reversal JE date: want %s, got %s", reversalDate, reversalJE.EntryDate)
	}
}

// TestRevaluation_PartiallyPaidInvoice: invoice partially settled; revaluation
// uses the reduced BalanceDue / BalanceDueBase, not the original amount.
//
// Setup: USD 1000 inv posted at 1.37 (base 1370). Partially paid 400 USD @ 1.35
// (base released 548.00, leaving balance_due=600, balance_due_base=822.00).
// Revalue at 1.40 → newBase = 600×1.40 = 840, adj = 840 − 822 = 18.00.
func TestRevaluation_PartiallyPaidInvoice(t *testing.T) {
	db := testRevalDB(t)
	cid := seedSettleCompany(t, db, "CAD")
	custID := seedCustomer(t, db, cid)

	seedSettleAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)

	// Manually create an invoice that is partially paid.
	// balance_due=600 (remaining doc-currency), balance_due_base=822.00 (remaining carrying).
	inv := models.Invoice{
		CompanyID:      cid,
		InvoiceNumber:  "INV-PARTIAL-001",
		CustomerID:     custID,
		InvoiceDate:    revalDate(2024, 6, 1),
		Status:         models.InvoiceStatusPartiallyPaid,
		CurrencyCode:   "USD",
		ExchangeRate:   fxRate(1.37),
		Amount:         decimal.RequireFromString("1000.00"),
		Subtotal:       decimal.RequireFromString("1000.00"),
		TaxTotal:       decimal.Zero,
		AmountBase:     decimal.RequireFromString("1370.00"),
		SubtotalBase:   decimal.RequireFromString("1370.00"),
		TaxTotalBase:   decimal.Zero,
		BalanceDue:     decimal.RequireFromString("600.00"),
		BalanceDueBase: decimal.RequireFromString("822.00"),
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}

	insertRate(t, db, nil, "USD", "CAD", fxRate(1.40), fxDate(2024, 12, 31))

	runID, err := RunRevaluation(db, RunRevaluationInput{
		CompanyID:    cid,
		RunDate:      revalDate(2024, 12, 31),
		ReversalDate: revalDate(2025, 1, 1),
		Actor:        "test",
	})
	if err != nil {
		t.Fatalf("RunRevaluation: %v", err)
	}
	if runID == 0 {
		t.Fatal("expected run to be created")
	}

	var line models.RevaluationLine
	db.Where("revaluation_run_id = ? AND document_id = ?", runID, inv.ID).First(&line)
	// newBase = 600 × 1.40 = 840; adj = 840 - 822 = 18
	if !line.OldBase.Equal(decimal.RequireFromString("822.00")) {
		t.Errorf("OldBase: want 822.00 (BalanceDueBase), got %s", line.OldBase)
	}
	if !line.NewBase.Equal(decimal.RequireFromString("840.00")) {
		t.Errorf("NewBase: want 840.00, got %s", line.NewBase)
	}
	if !line.Adjustment.Equal(decimal.RequireFromString("18.00")) {
		t.Errorf("Adjustment: want 18.00, got %s", line.Adjustment)
	}
}

// TestRevaluation_BaseCurrencyDocSkipped: CAD invoices and bills are never revalued.
func TestRevaluation_BaseCurrencyDocSkipped(t *testing.T) {
	db := testRevalDB(t)
	cid := seedSettleCompany(t, db, "CAD")
	custID := seedCustomer(t, db, cid)
	vendorID := seedVendor(t, db, cid)

	seedSettleAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	seedSettleAccount(t, db, cid, "2000", models.RootLiability, models.DetailAccountsPayable)

	// Base-currency documents (no currency_code → treated as CAD).
	seedPostedInvoice(t, db, cid, custID, "INV-CAD-001", "", "500.00", "500.00")
	seedPostedBill(t, db, cid, vendorID, "BILL-CAD-001", "", "300.00", "300.00")

	// Also a foreign invoice but with zero adjustment (rate unchanged at 1.37).
	seedPostedInvoice(t, db, cid, custID, "INV-USD-001", "USD", "1000.00", "1370.00")
	insertRate(t, db, nil, "USD", "CAD", fxRate(1.37), fxDate(2024, 12, 31))

	runID, err := RunRevaluation(db, RunRevaluationInput{
		CompanyID:    cid,
		RunDate:      revalDate(2024, 12, 31),
		ReversalDate: revalDate(2025, 1, 1),
		Actor:        "test",
	})
	if err != nil {
		t.Fatalf("RunRevaluation: %v", err)
	}
	// All adjustments are zero → no run created.
	if runID != 0 {
		t.Errorf("expected runID=0 (all adjustments zero), got %d", runID)
	}
}

// TestRevaluation_MixedDocsOneRun: one USD invoice + one USD bill in the same run.
// Both revaluation and reversal JEs must be balanced.
func TestRevaluation_MixedDocsOneRun(t *testing.T) {
	db := testRevalDB(t)
	cid := seedSettleCompany(t, db, "CAD")
	custID := seedCustomer(t, db, cid)
	vendorID := seedVendor(t, db, cid)

	seedSettleAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	seedSettleAccount(t, db, cid, "2000", models.RootLiability, models.DetailAccountsPayable)

	// Invoice: USD 1000 @ 1.37 → base 1370; revalue @ 1.40 → adj +30 (gain)
	seedPostedInvoice(t, db, cid, custID, "INV-MIX-001", "USD", "1000.00", "1370.00")
	// Bill:    USD 500  @ 1.37 → base 685;  revalue @ 1.40 → adj +15 (loss for AP)
	seedPostedBill(t, db, cid, vendorID, "BILL-MIX-001", "USD", "500.00", "685.00")

	insertRate(t, db, nil, "USD", "CAD", fxRate(1.40), fxDate(2024, 12, 31))

	runID, err := RunRevaluation(db, RunRevaluationInput{
		CompanyID:    cid,
		RunDate:      revalDate(2024, 12, 31),
		ReversalDate: revalDate(2025, 1, 1),
		Actor:        "test",
	})
	if err != nil {
		t.Fatalf("RunRevaluation: %v", err)
	}
	if runID == 0 {
		t.Fatal("expected run to be created")
	}

	// Both revaluation and reversal JEs must be balanced.
	var run models.RevaluationRun
	db.First(&run, runID)

	dr, cr := sumJELines(t, db, run.JournalEntryID)
	if !dr.Equal(cr) {
		t.Errorf("revaluation JE imbalanced: DR %s ≠ CR %s", dr, cr)
	}

	rdr, rcr := sumJELines(t, db, *run.ReversalJEID)
	if !rdr.Equal(rcr) {
		t.Errorf("reversal JE imbalanced: DR %s ≠ CR %s", rdr, rcr)
	}

	// Verify two revaluation lines were created.
	var lineCount int64
	db.Model(&models.RevaluationLine{}).Where("revaluation_run_id = ?", runID).Count(&lineCount)
	if lineCount != 2 {
		t.Errorf("expected 2 revaluation lines, got %d", lineCount)
	}

	// Invoice adj: +30; Bill adj: 500×1.40−685 = 700−685 = +15.
	// Net FX effect: +30 (gain on AR) − +15 (loss on AP) = net balance = 15 net credit to FX.
	// Total JE amounts: AR DR 30 + FX DR 15 = 45; AP CR 15 + FX CR 30 = 45. Balanced. ✓
	if !dr.Equal(decimal.RequireFromString("45.00")) {
		t.Errorf("revaluation JE total: want 45.00, got %s", dr)
	}
}
