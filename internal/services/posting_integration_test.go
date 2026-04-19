// 遵循project_guide.md
package services

// posting_integration_test.go — DB-backed integration tests for the posting
// engine (invoice post/void, bill post, journal reversal).
//
// All tests use an isolated SQLite in-memory database provisioned per-test
// function (via testPostingDB) so tests are fully independent. SQLite does not
// support SELECT FOR UPDATE, so the Phase-6 row lock is a no-op; correctness
// under sequential access is still fully exercised.
//
// Coverage matrix:
//
//   Invoice posting
//     TestPostInvoice_CreatesJEAndLedger               — JE + lines + ledger + source fields
//     TestPostInvoice_SameRevenueAccountMergesLines     — aggregation visible in journal_lines count
//     TestPostInvoice_SameTaxAccountMergesLines         — tax merges into single journal_line
//
//   Bill posting
//     TestPostBill_FullRecoverableTaxCreatesITCLine     — 3 JE lines: expense, ITC, AP
//     TestPostBill_NonRecoverableTaxEmbeddedInExpense   — 2 JE lines: expense (w/ tax), AP
//     TestPostBill_DuplicatePostingRejected             — second PostBill call rejected
//
//   Lifecycle: void
//     TestVoidInvoice_CreatesReversalAndMarkOriginalReversed
//       — reversal JE created, original JE status=reversed,
//         original ledger entries status=reversed, reversal ledger entries active
//
//   Lifecycle: reverse
//     TestReverseJournalEntry_CreatesReversingJE
//       — original JE status=reversed, reversal JE correct source fields
//
//   Company consistency
//     TestPostInvoice_CrossCompanyTaxCodeRejected       — tax code from wrong company

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ── DB helper ─────────────────────────────────────────────────────────────────

// testPostingDB creates an isolated in-memory SQLite DB with all models needed
// for posting engine integration tests.
func testPostingDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:posting_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Vendor{},
		&models.Account{},
		&models.TaxCode{},
		&models.ProductService{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.Bill{},
		&models.BillLine{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.TaskInvoiceSource{},
		&models.AuditLog{},
		&models.InventoryMovement{},
		&models.InventoryBalance{},
		&models.InventoryCostLayer{},
		&models.InventoryLayerConsumption{},
		&models.PaymentTransaction{},    // required by VoidInvoice payment-transaction guard
		&models.SettlementAllocation{},  // required by VoidInvoice settlement-allocation guard
		&models.CreditNoteApplication{}, // required by VoidInvoice credit-application reversal
		&models.APCreditApplication{},   // required by VoidBill credit-application reversal
	); err != nil {
		t.Fatal(err)
	}
	return db
}

// ── Seed helpers ──────────────────────────────────────────────────────────────

func seedVendor(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	v := models.Vendor{CompanyID: companyID, Name: "Test Vendor"}
	if err := db.Create(&v).Error; err != nil {
		t.Fatal(err)
	}
	return v.ID
}

func seedCustomer(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	c := models.Customer{CompanyID: companyID, Name: "Test Customer"}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func seedTaxCode(t *testing.T, db *gorm.DB, companyID uint, rate string, scope models.TaxScope, mode models.TaxRecoveryMode, recoveryPct string, salesAcctID uint, purchaseAcctID *uint) uint {
	t.Helper()
	tc := models.TaxCode{
		CompanyID:         companyID,
		Name:              "Tax-" + rate,
		Rate:              d(rate),
		Scope:             scope,
		RecoveryMode:      mode,
		RecoveryRate:      d(recoveryPct),
		SalesTaxAccountID: salesAcctID,
	}
	if purchaseAcctID != nil {
		tc.PurchaseRecoverableAccountID = purchaseAcctID
	}
	if err := db.Create(&tc).Error; err != nil {
		t.Fatal(err)
	}
	return tc.ID
}

func seedProductService(t *testing.T, db *gorm.DB, companyID, revenueAcctID uint) uint {
	t.Helper()
	p := models.ProductService{
		CompanyID:        companyID,
		Name:             "Service",
		Type:             models.ProductServiceTypeService,
		RevenueAccountID: revenueAcctID,
		IsActive:         true,
	}
	if err := db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	return p.ID
}

// seedInvoice creates a draft invoice with the given lines and returns its ID.
// amount must be the sum of all line nets + taxes.
func seedInvoice(t *testing.T, db *gorm.DB, companyID, customerID uint, amount string, lines []models.InvoiceLine) uint {
	t.Helper()
	inv := models.Invoice{
		CompanyID:     companyID,
		InvoiceNumber: fmt.Sprintf("INV-%03d", companyID),
		CustomerID:    customerID,
		InvoiceDate:   time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Status:        models.InvoiceStatusDraft,
		Amount:        d(amount),
		Subtotal:      d(amount),
		TaxTotal:      decimal.Zero,
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}
	for i := range lines {
		lines[i].CompanyID = companyID
		lines[i].InvoiceID = inv.ID
		lines[i].SortOrder = uint(i + 1)
		if err := db.Create(&lines[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	return inv.ID
}

func seedBill(t *testing.T, db *gorm.DB, companyID, vendorID uint, amount string, lines []models.BillLine) uint {
	t.Helper()
	bill := models.Bill{
		CompanyID:  companyID,
		BillNumber: fmt.Sprintf("BILL-%03d", companyID),
		VendorID:   vendorID,
		BillDate:   time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Status:     models.BillStatusDraft,
		Amount:     d(amount),
	}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatal(err)
	}
	for i := range lines {
		lines[i].CompanyID = companyID
		lines[i].BillID = bill.ID
		lines[i].SortOrder = uint(i + 1)
		if err := db.Create(&lines[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	return bill.ID
}

// ── Invoice posting ───────────────────────────────────────────────────────────

func TestPostInvoice_CreatesJEAndLedger(t *testing.T) {
	db := testPostingDB(t)
	cid := seedInvoicePostCompany(t, db, "Posting Co")
	custID := seedCustomer(t, db, cid)
	arID := seedInvoicePostAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	revID := seedInvoicePostAccount(t, db, cid, "4000", models.RootRevenue, models.DetailServiceRevenue)
	psID := seedProductService(t, db, cid, revID)

	_ = arID // used by PostInvoice's AR account lookup
	invID := seedInvoice(t, db, cid, custID, "1000.00", []models.InvoiceLine{
		{ProductServiceID: &psID, Description: "Consulting", Qty: d("1"), UnitPrice: d("1000"), LineNet: d("1000.00"), LineTotal: d("1000.00")},
	})

	if err := PostInvoice(db, cid, invID, "tester", nil); err != nil {
		t.Fatalf("PostInvoice: %v", err)
	}

	// Invoice must be 'issued' (posted).
	var inv models.Invoice
	db.First(&inv, invID)
	if inv.Status != models.InvoiceStatusIssued {
		t.Errorf("invoice status = %q, want 'issued'", inv.Status)
	}
	if inv.JournalEntryID == nil {
		t.Fatal("invoice.journal_entry_id is nil after posting")
	}

	// JE must exist with correct source fields and status.
	var je models.JournalEntry
	db.First(&je, *inv.JournalEntryID)
	if je.Status != models.JournalEntryStatusPosted {
		t.Errorf("JE status = %q, want 'posted'", je.Status)
	}
	if je.SourceType != models.LedgerSourceInvoice {
		t.Errorf("JE source_type = %q, want 'invoice'", je.SourceType)
	}
	if je.SourceID != invID {
		t.Errorf("JE source_id = %d, want %d", je.SourceID, invID)
	}

	// JE lines must be balanced: 1 AR debit + 1 revenue credit.
	var lines []models.JournalLine
	db.Where("journal_entry_id = ?", je.ID).Find(&lines)
	if len(lines) != 2 {
		t.Errorf("journal_lines count = %d, want 2", len(lines))
	}
	var totalDebit, totalCredit decimal.Decimal
	for _, l := range lines {
		totalDebit = totalDebit.Add(l.Debit)
		totalCredit = totalCredit.Add(l.Credit)
	}
	if !totalDebit.Equal(d("1000.00")) || !totalCredit.Equal(d("1000.00")) {
		t.Errorf("JE balance: DR=%s CR=%s", totalDebit, totalCredit)
	}

	// Ledger entries must be active, one per journal line.
	var ledger []models.LedgerEntry
	db.Where("journal_entry_id = ? AND status = ?", je.ID, models.LedgerEntryStatusActive).Find(&ledger)
	if len(ledger) != len(lines) {
		t.Errorf("ledger_entries count = %d, want %d", len(ledger), len(lines))
	}
}

func TestPostInvoice_SameRevenueAccountMergesLines(t *testing.T) {
	// Two lines pointing to the same revenue account → after aggregation the JE
	// should have exactly 2 rows: one AR debit and one merged revenue credit.
	db := testPostingDB(t)
	cid := seedInvoicePostCompany(t, db, "MergeRev Co")
	custID := seedCustomer(t, db, cid)
	_ = seedInvoicePostAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	revID := seedInvoicePostAccount(t, db, cid, "4000", models.RootRevenue, models.DetailServiceRevenue)
	psID := seedProductService(t, db, cid, revID)

	invID := seedInvoice(t, db, cid, custID, "1000.00", []models.InvoiceLine{
		{ProductServiceID: &psID, Description: "Line A", Qty: d("1"), UnitPrice: d("600"), LineNet: d("600.00"), LineTotal: d("600.00")},
		{ProductServiceID: &psID, Description: "Line B", Qty: d("1"), UnitPrice: d("400"), LineNet: d("400.00"), LineTotal: d("400.00")},
	})

	if err := PostInvoice(db, cid, invID, "tester", nil); err != nil {
		t.Fatalf("PostInvoice: %v", err)
	}

	var inv models.Invoice
	db.First(&inv, invID)

	var lines []models.JournalLine
	db.Where("journal_entry_id = ?", *inv.JournalEntryID).Find(&lines)

	// Same revenue account → merged → 1 AR + 1 revenue = 2 lines total.
	if len(lines) != 2 {
		t.Errorf("journal_lines = %d, want 2 (AR + merged revenue)", len(lines))
	}

	// Merged revenue credit must equal the sum of both line nets.
	var revCredit decimal.Decimal
	for _, l := range lines {
		if l.AccountID == revID {
			revCredit = l.Credit
		}
	}
	if !revCredit.Equal(d("1000.00")) {
		t.Errorf("merged revenue credit = %s, want 1000.00", revCredit)
	}
}

func TestPostInvoice_SameTaxAccountMergesLines(t *testing.T) {
	// Two lines, same revenue account, same 5% GST code.
	// After posting: 3 journal lines — 1 AR, 1 merged revenue, 1 merged tax.
	db := testPostingDB(t)
	cid := seedInvoicePostCompany(t, db, "MergeTax Co")
	custID := seedCustomer(t, db, cid)
	_ = seedInvoicePostAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	revID := seedInvoicePostAccount(t, db, cid, "4000", models.RootRevenue, models.DetailServiceRevenue)
	taxPayableID := seedInvoicePostAccount(t, db, cid, "2300", models.RootLiability, models.DetailSalesTaxPayable)
	psID := seedProductService(t, db, cid, revID)
	tcID := seedTaxCode(t, db, cid, "0.05", models.TaxScopeSales, models.TaxRecoveryNone, "0", taxPayableID, nil)

	invID := seedInvoice(t, db, cid, custID, "1050.00", []models.InvoiceLine{
		{ProductServiceID: &psID, TaxCodeID: &tcID, Description: "Line A", Qty: d("1"), UnitPrice: d("500"), LineNet: d("500.00"), LineTax: d("25.00"), LineTotal: d("525.00")},
		{ProductServiceID: &psID, TaxCodeID: &tcID, Description: "Line B", Qty: d("1"), UnitPrice: d("500"), LineNet: d("500.00"), LineTax: d("25.00"), LineTotal: d("525.00")},
	})
	// Adjust the invoice amount to include tax total.
	db.Model(&models.Invoice{}).Where("id = ?", invID).Updates(map[string]any{
		"amount":    d("1050.00"),
		"subtotal":  d("1000.00"),
		"tax_total": d("50.00"),
	})

	if err := PostInvoice(db, cid, invID, "tester", nil); err != nil {
		t.Fatalf("PostInvoice: %v", err)
	}

	var inv models.Invoice
	db.First(&inv, invID)

	var lines []models.JournalLine
	db.Where("journal_entry_id = ?", *inv.JournalEntryID).Find(&lines)

	// AR + merged revenue + merged tax = 3.
	if len(lines) != 3 {
		t.Errorf("journal_lines = %d, want 3 (AR + revenue + merged tax)", len(lines))
	}

	var taxCredit decimal.Decimal
	for _, l := range lines {
		if l.AccountID == taxPayableID {
			taxCredit = l.Credit
		}
	}
	if !taxCredit.Equal(d("50.00")) {
		t.Errorf("merged tax credit = %s, want 50.00", taxCredit)
	}
}

// ── Bill posting ──────────────────────────────────────────────────────────────

func TestPostBill_FullRecoverableTaxCreatesITCLine(t *testing.T) {
	// $1 000 net, 13% HST full recovery → 3 JE lines: DR Expense 1000, DR ITC 130, CR AP 1130.
	db := testPostingDB(t)
	cid := seedInvoicePostCompany(t, db, "BillFullRec Co")
	vendorID := seedVendor(t, db, cid)
	apID := seedInvoicePostAccount(t, db, cid, "2000", models.RootLiability, models.DetailAccountsPayable)
	expID := seedInvoicePostAccount(t, db, cid, "6100", models.RootExpense, models.DetailOperatingExpense)
	itcID := seedInvoicePostAccount(t, db, cid, "1320", models.RootAsset, models.DetailOtherCurrentAsset)
	itcIDPtr := &itcID
	tcID := seedTaxCode(t, db, cid, "0.13", models.TaxScopePurchase, models.TaxRecoveryFull, "100", 0, itcIDPtr)

	_ = apID
	billID := seedBill(t, db, cid, vendorID, "1130.00", []models.BillLine{
		{ExpenseAccountID: &expID, TaxCodeID: &tcID, Description: "Office Rent", Qty: d("1"), UnitPrice: d("1000"), LineNet: d("1000.00"), LineTax: d("130.00"), LineTotal: d("1130.00")},
	})

	if err := PostBill(db, cid, billID, "tester", nil); err != nil {
		t.Fatalf("PostBill: %v", err)
	}

	var bill models.Bill
	db.First(&bill, billID)
	if bill.Status != models.BillStatusPosted {
		t.Errorf("bill status = %q, want 'posted'", bill.Status)
	}

	var lines []models.JournalLine
	db.Where("journal_entry_id = ?", *bill.JournalEntryID).Find(&lines)

	// DR Expense + DR ITC + CR AP = 3 lines.
	if len(lines) != 3 {
		t.Errorf("journal_lines = %d, want 3 (expense + ITC + AP)", len(lines))
	}

	amounts := make(map[uint]decimal.Decimal)
	for _, l := range lines {
		if l.Debit.IsPositive() {
			amounts[l.AccountID] = l.Debit
		} else {
			amounts[l.AccountID] = l.Credit
		}
	}
	if !amounts[expID].Equal(d("1000.00")) {
		t.Errorf("expense debit = %s, want 1000.00", amounts[expID])
	}
	if !amounts[itcID].Equal(d("130.00")) {
		t.Errorf("ITC debit = %s, want 130.00", amounts[itcID])
	}
	if !amounts[apID].Equal(d("1130.00")) {
		t.Errorf("AP credit = %s, want 1130.00", amounts[apID])
	}
}

func TestPostBill_NonRecoverableTaxEmbeddedInExpense(t *testing.T) {
	// $1 000 net, 13% non-recoverable → 2 JE lines: DR Expense 1130, CR AP 1130.
	db := testPostingDB(t)
	cid := seedInvoicePostCompany(t, db, "BillNonRec Co")
	vendorID := seedVendor(t, db, cid)
	apID := seedInvoicePostAccount(t, db, cid, "2000", models.RootLiability, models.DetailAccountsPayable)
	expID := seedInvoicePostAccount(t, db, cid, "6100", models.RootExpense, models.DetailOperatingExpense)
	tcID := seedTaxCode(t, db, cid, "0.13", models.TaxScopePurchase, models.TaxRecoveryNone, "0", 0, nil)

	_ = apID
	billID := seedBill(t, db, cid, vendorID, "1130.00", []models.BillLine{
		{ExpenseAccountID: &expID, TaxCodeID: &tcID, Description: "Supplies", Qty: d("1"), UnitPrice: d("1000"), LineNet: d("1000.00"), LineTax: d("130.00"), LineTotal: d("1130.00")},
	})

	if err := PostBill(db, cid, billID, "tester", nil); err != nil {
		t.Fatalf("PostBill: %v", err)
	}

	var bill models.Bill
	db.First(&bill, billID)

	var lines []models.JournalLine
	db.Where("journal_entry_id = ?", *bill.JournalEntryID).Find(&lines)

	// No ITC line → only 2 lines.
	if len(lines) != 2 {
		t.Errorf("journal_lines = %d, want 2 (expense+tax embedded + AP)", len(lines))
	}

	for _, l := range lines {
		if l.AccountID == expID && !l.Debit.Equal(d("1130.00")) {
			t.Errorf("expense debit = %s, want 1130.00 (includes embedded tax)", l.Debit)
		}
	}
}

func TestPostBill_DuplicatePostingRejected(t *testing.T) {
	db := testPostingDB(t)
	cid := seedInvoicePostCompany(t, db, "BillDup Co")
	vendorID := seedVendor(t, db, cid)
	_ = seedInvoicePostAccount(t, db, cid, "2000", models.RootLiability, models.DetailAccountsPayable)
	expID := seedInvoicePostAccount(t, db, cid, "6100", models.RootExpense, models.DetailOperatingExpense)

	billID := seedBill(t, db, cid, vendorID, "500.00", []models.BillLine{
		{ExpenseAccountID: &expID, Description: "Service fee", Qty: d("1"), UnitPrice: d("500"), LineNet: d("500.00"), LineTotal: d("500.00")},
	})

	if err := PostBill(db, cid, billID, "tester", nil); err != nil {
		t.Fatalf("first PostBill failed: %v", err)
	}

	// Second attempt must be rejected.
	err := PostBill(db, cid, billID, "tester", nil)
	if err == nil {
		t.Fatal("expected error on duplicate PostBill, got nil")
	}
	if !errors.Is(err, ErrBillNotDraft) && !errors.Is(err, ErrAlreadyPosted) {
		t.Fatalf("expected ErrBillNotDraft or ErrAlreadyPosted, got: %v", err)
	}
}

// ── Lifecycle: void ───────────────────────────────────────────────────────────

func TestVoidInvoice_CreatesReversalAndMarkOriginalReversed(t *testing.T) {
	db := testPostingDB(t)
	cid := seedInvoicePostCompany(t, db, "Void Co")
	custID := seedCustomer(t, db, cid)
	_ = seedInvoicePostAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	revID := seedInvoicePostAccount(t, db, cid, "4000", models.RootRevenue, models.DetailServiceRevenue)
	psID := seedProductService(t, db, cid, revID)

	invID := seedInvoice(t, db, cid, custID, "1000.00", []models.InvoiceLine{
		{ProductServiceID: &psID, Description: "Service", Qty: d("1"), UnitPrice: d("1000"), LineNet: d("1000.00"), LineTotal: d("1000.00")},
	})

	// Post first.
	if err := PostInvoice(db, cid, invID, "tester", nil); err != nil {
		t.Fatalf("PostInvoice: %v", err)
	}
	var invAfterPost models.Invoice
	db.First(&invAfterPost, invID)
	origJEID := *invAfterPost.JournalEntryID

	// Void.
	if err := VoidInvoice(db, cid, invID, "tester", nil); err != nil {
		t.Fatalf("VoidInvoice: %v", err)
	}

	// Invoice status must be voided.
	var inv models.Invoice
	db.First(&inv, invID)
	if inv.Status != models.InvoiceStatusVoided {
		t.Errorf("invoice status = %q, want 'voided'", inv.Status)
	}

	// Original JE must be reversed.
	var origJE models.JournalEntry
	db.First(&origJE, origJEID)
	if origJE.Status != models.JournalEntryStatusReversed {
		t.Errorf("original JE status = %q, want 'reversed'", origJE.Status)
	}

	// Reversal JE must exist: source_type='reversal', source_id=invID, status='posted'.
	var revJE models.JournalEntry
	if err := db.Where("reversed_from_id = ? AND company_id = ?", origJEID, cid).First(&revJE).Error; err != nil {
		t.Fatalf("reversal JE not found: %v", err)
	}
	if revJE.Status != models.JournalEntryStatusPosted {
		t.Errorf("reversal JE status = %q, want 'posted'", revJE.Status)
	}
	if revJE.SourceType != models.LedgerSourceReversal {
		t.Errorf("reversal JE source_type = %q, want 'reversal'", revJE.SourceType)
	}
	if revJE.SourceID != invID {
		t.Errorf("reversal JE source_id = %d, want %d", revJE.SourceID, invID)
	}

	// Original ledger entries must all be reversed.
	var activeOrigLedger int64
	db.Model(&models.LedgerEntry{}).
		Where("journal_entry_id = ? AND status = ?", origJEID, models.LedgerEntryStatusActive).
		Count(&activeOrigLedger)
	if activeOrigLedger != 0 {
		t.Errorf("original JE still has %d active ledger entries, want 0", activeOrigLedger)
	}

	// Reversal JE ledger entries must be active.
	var activeRevLedger int64
	db.Model(&models.LedgerEntry{}).
		Where("journal_entry_id = ? AND status = ?", revJE.ID, models.LedgerEntryStatusActive).
		Count(&activeRevLedger)
	if activeRevLedger == 0 {
		t.Error("reversal JE has 0 active ledger entries, want > 0")
	}

	// Reversal lines must be the mirror image: debit/credit swapped.
	var origLines, revLines []models.JournalLine
	db.Where("journal_entry_id = ?", origJEID).Find(&origLines)
	db.Where("journal_entry_id = ?", revJE.ID).Find(&revLines)
	if len(origLines) != len(revLines) {
		t.Errorf("orig lines %d vs reversal lines %d", len(origLines), len(revLines))
	}
	origByAcct := make(map[uint]models.JournalLine)
	for _, l := range origLines {
		origByAcct[l.AccountID] = l
	}
	for _, rv := range revLines {
		orig, ok := origByAcct[rv.AccountID]
		if !ok {
			continue
		}
		if !rv.Debit.Equal(orig.Credit) || !rv.Credit.Equal(orig.Debit) {
			t.Errorf("account %d: orig DR=%s CR=%s → rev DR=%s CR=%s",
				rv.AccountID, orig.Debit, orig.Credit, rv.Debit, rv.Credit)
		}
	}
}

// ── Lifecycle: reverse ────────────────────────────────────────────────────────

func TestReverseJournalEntry_CreatesReversingJE(t *testing.T) {
	db := testPostingDB(t)
	cid := seedInvoicePostCompany(t, db, "Reverse Co")
	acct1 := seedInvoicePostAccount(t, db, cid, "1100", models.RootAsset, models.DetailAccountsReceivable)
	acct2 := seedInvoicePostAccount(t, db, cid, "4000", models.RootRevenue, models.DetailServiceRevenue)

	// Insert a manual JE directly (no source document).
	je := models.JournalEntry{
		CompanyID: cid,
		EntryDate: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		JournalNo: "MAN-001",
		Status:    models.JournalEntryStatusPosted,
	}
	if err := db.Create(&je).Error; err != nil {
		t.Fatal(err)
	}
	manualLines := []models.JournalLine{
		{CompanyID: cid, JournalEntryID: je.ID, AccountID: acct1, Debit: d("1000.00"), Credit: decimal.Zero},
		{CompanyID: cid, JournalEntryID: je.ID, AccountID: acct2, Debit: decimal.Zero, Credit: d("1000.00")},
	}
	if err := db.Create(&manualLines).Error; err != nil {
		t.Fatal(err)
	}

	// Reverse within a transaction (ReverseJournalEntry requires tx).
	var reversalID uint
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		reversalID, err = ReverseJournalEntry(tx, cid, je.ID,
			time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC))
		return err
	}); err != nil {
		t.Fatalf("ReverseJournalEntry: %v", err)
	}

	// Original JE must now be reversed.
	var orig models.JournalEntry
	db.First(&orig, je.ID)
	if orig.Status != models.JournalEntryStatusReversed {
		t.Errorf("original JE status = %q, want 'reversed'", orig.Status)
	}

	// Reversal JE must have correct source fields.
	var rev models.JournalEntry
	db.First(&rev, reversalID)
	if rev.Status != models.JournalEntryStatusPosted {
		t.Errorf("reversal JE status = %q, want 'posted'", rev.Status)
	}
	if rev.SourceType != models.LedgerSourceReversal {
		t.Errorf("reversal JE source_type = %q, want 'reversal'", rev.SourceType)
	}
	if rev.SourceID != je.ID {
		t.Errorf("reversal JE source_id = %d, want %d (original JE ID)", rev.SourceID, je.ID)
	}
	if rev.ReversedFromID == nil || *rev.ReversedFromID != je.ID {
		t.Errorf("reversal JE reversed_from_id wrong")
	}

	// Reversal JE must have active ledger entries (projected by ReverseJournalEntry).
	var ledgerCount int64
	db.Model(&models.LedgerEntry{}).
		Where("journal_entry_id = ? AND status = ?", rev.ID, models.LedgerEntryStatusActive).
		Count(&ledgerCount)
	if ledgerCount == 0 {
		t.Error("reversal JE has no active ledger entries")
	}

	// Attempting to reverse the same JE again must fail.
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := ReverseJournalEntry(tx, cid, je.ID, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
		return err
	}); err == nil {
		t.Error("expected error when reversing an already-reversed JE, got nil")
	}
}

// ── Company consistency ───────────────────────────────────────────────────────

func TestPostInvoice_CrossCompanyTaxCodeRejected(t *testing.T) {
	db := testPostingDB(t)

	// Company A: customer, AR account, revenue account, product service.
	cidA := seedInvoicePostCompany(t, db, "Company A")
	custID := seedCustomer(t, db, cidA)
	_ = seedInvoicePostAccount(t, db, cidA, "1100", models.RootAsset, models.DetailAccountsReceivable)
	revID := seedInvoicePostAccount(t, db, cidA, "4000", models.RootRevenue, models.DetailServiceRevenue)
	psID := seedProductService(t, db, cidA, revID)

	// Company B: tax code that belongs to B (wrong company for the invoice).
	cidB := seedInvoicePostCompany(t, db, "Company B")
	taxPayableB := seedInvoicePostAccount(t, db, cidB, "2300", models.RootLiability, models.DetailSalesTaxPayable)
	tcIDB := seedTaxCode(t, db, cidB, "0.05", models.TaxScopeSales, models.TaxRecoveryNone, "0", taxPayableB, nil)

	// Invoice for A, but line uses tax code from B.
	invID := seedInvoice(t, db, cidA, custID, "1050.00", []models.InvoiceLine{
		{ProductServiceID: &psID, TaxCodeID: &tcIDB, Description: "Service", Qty: d("1"), UnitPrice: d("1000"), LineNet: d("1000.00"), LineTax: d("50.00"), LineTotal: d("1050.00")},
	})

	err := PostInvoice(db, cidA, invID, "tester", nil)
	if err == nil {
		t.Fatal("expected error for cross-company tax code, got nil")
	}
	// The error must mention that the tax code is invalid for this company.
	const wantSubstr = "tax code is not valid for this company"
	if !containsStr(err.Error(), wantSubstr) {
		t.Errorf("error %q does not contain %q", err.Error(), wantSubstr)
	}
}

// containsStr reports whether s contains substr (replaces strings.Contains to
// avoid a second import just for test helpers).
func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && func() bool {
		for i := 0; i <= len(s)-len(substr); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	}()
}
