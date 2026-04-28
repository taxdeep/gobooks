// 遵循project_guide.md
package services

// ar_phase6_test.go — AR Phase 6: ARWriteOff lifecycle + ARAging + CustomerStatement tests.
//
// ARWriteOff tests:
//  1. Create draft — no JE.
//  2. Post — creates Dr Expense / Cr AR JE + 2 ledger entries; reduces Invoice.BalanceDue.
//  3. Post without AR account → ErrWriteOffNoARAcct.
//  4. Post without expense account → ErrWriteOffNoExpenseAcct.
//  5. Void draft.
//  6. Cannot void posted.
//  7. Reverse posted — creates reversal JE; restores Invoice.BalanceDue.
//  8. Cannot reverse non-posted.
//  9. Company isolation.
// 10. Document numbering (WOF-0001, WOF-0002).
//
// ARAging tests:
// 11. Empty DB returns empty aging.
// 12. Overdue invoice appears in correct bucket.
// 13. Invoice not yet due appears in Current bucket.
// 14. Multiple customers, multiple buckets.
//
// CustomerStatement tests:
// 15. Empty period returns zero balances.
// 16. Invoice in period adds debit line.

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── DB helper ─────────────────────────────────────────────────────────────────

func phase6DB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Account{},
		&models.TaxCode{},
		&models.ProductService{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.ARWriteOff{},
		&models.ARAPControlMapping{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func p6Company(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	c := models.Company{Name: "Phase6 Co", BaseCurrencyCode: "CAD", IsActive: true, AccountCodeLength: 4}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func p6Customer(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	c := models.Customer{CompanyID: companyID, Name: "Test Customer P6"}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func p6Invoice(t *testing.T, db *gorm.DB, companyID, custID uint, amount decimal.Decimal, daysOverdue int) *models.Invoice {
	t.Helper()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	due := today.AddDate(0, 0, -daysOverdue)
	inv := models.Invoice{
		CompanyID:     companyID,
		CustomerID:    custID,
		InvoiceNumber: "INV-6001",
		InvoiceDate:   due.AddDate(0, 0, -30), // issued 30d before due
		DueDate:       &due,
		Status:        models.InvoiceStatusSent,
		Amount:        amount,
		Subtotal:      amount,
		BalanceDue:    amount,
		CurrencyCode:  "CAD",
		ExchangeRate:  decimal.NewFromInt(1),
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}
	return &inv
}

func p6ARAccount(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	a := models.Account{
		CompanyID:         companyID,
		Code:              "1100",
		Name:              "Accounts Receivable",
		RootAccountType:   models.RootAsset,
		DetailAccountType: models.DetailAccountsReceivable,
		IsActive:          true,
	}
	if err := db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	return a.ID
}

func p6ExpenseAccount(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	a := models.Account{
		CompanyID:         companyID,
		Code:              "6100",
		Name:              "Bad Debt Expense",
		RootAccountType:   models.RootExpense,
		DetailAccountType: models.DetailOperatingExpense,
		IsActive:          true,
	}
	if err := db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	return a.ID
}

// ── ARWriteOff tests ──────────────────────────────────────────────────────────

func TestPhase6_CreateWriteOff(t *testing.T) {
	db := phase6DB(t)
	cid := p6Company(t, db)
	custID := p6Customer(t, db, cid)

	wo, err := CreateARWriteOff(db, cid, ARWriteOffInput{
		CustomerID:   custID,
		WriteOffDate: time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.NewFromInt(150),
		Reason:       "Customer bankrupt",
	})
	if err != nil {
		t.Fatalf("CreateARWriteOff: %v", err)
	}
	if wo.Status != models.ARWriteOffStatusDraft {
		t.Errorf("expected draft; got %s", wo.Status)
	}
	if wo.WriteOffNumber == "" {
		t.Error("WriteOffNumber must be set")
	}

	// No JE.
	var jeCount int64
	db.Model(&models.JournalEntry{}).Count(&jeCount)
	if jeCount != 0 {
		t.Errorf("expected 0 JEs after draft creation; got %d", jeCount)
	}
}

func TestPhase6_PostWriteOff(t *testing.T) {
	db := phase6DB(t)
	cid := p6Company(t, db)
	custID := p6Customer(t, db, cid)
	arID := p6ARAccount(t, db, cid)
	expID := p6ExpenseAccount(t, db, cid)
	inv := p6Invoice(t, db, cid, custID, decimal.NewFromInt(200), 60)

	invIDPtr := inv.ID
	wo, _ := CreateARWriteOff(db, cid, ARWriteOffInput{
		CustomerID:       custID,
		InvoiceID:        &invIDPtr,
		ARAccountID:      &arID,
		ExpenseAccountID: &expID,
		WriteOffDate:     time.Now(),
		CurrencyCode:     "CAD",
		ExchangeRate:     decimal.NewFromInt(1),
		Amount:           decimal.NewFromInt(200),
		Reason:           "Uncollectible",
	})

	if err := PostARWriteOff(db, cid, wo.ID, "tester", nil); err != nil {
		t.Fatalf("PostARWriteOff: %v", err)
	}

	// Verify status.
	updated, _ := GetARWriteOff(db, cid, wo.ID)
	if updated.Status != models.ARWriteOffStatusPosted {
		t.Errorf("expected posted; got %s", updated.Status)
	}
	if updated.JournalEntryID == nil {
		t.Fatal("JournalEntryID must be set after posting")
	}

	// Verify JE lines: Dr Expense / Cr AR.
	var lines []models.JournalLine
	db.Where("journal_entry_id = ?", *updated.JournalEntryID).Find(&lines)
	if len(lines) != 2 {
		t.Fatalf("expected 2 JE lines; got %d", len(lines))
	}

	var drLine, crLine *models.JournalLine
	for i := range lines {
		if lines[i].Debit.IsPositive() {
			drLine = &lines[i]
		}
		if lines[i].Credit.IsPositive() {
			crLine = &lines[i]
		}
	}
	if drLine == nil || crLine == nil {
		t.Fatal("must have one debit and one credit line")
	}
	if drLine.AccountID != expID {
		t.Errorf("Dr line should be expense account; got %d", drLine.AccountID)
	}
	if crLine.AccountID != arID {
		t.Errorf("Cr line should be AR account; got %d", crLine.AccountID)
	}
	if !drLine.Debit.Equal(decimal.NewFromInt(200)) {
		t.Errorf("Dr amount should be 200; got %s", drLine.Debit)
	}

	// Verify 2 ledger entries.
	var leCount int64
	db.Model(&models.LedgerEntry{}).Where("journal_entry_id = ?", *updated.JournalEntryID).Count(&leCount)
	if leCount != 2 {
		t.Errorf("expected 2 ledger entries; got %d", leCount)
	}

	// Verify Invoice.BalanceDue reduced to 0 (GREATEST(200-200, 0)).
	var updatedInv models.Invoice
	db.First(&updatedInv, inv.ID)
	if !updatedInv.BalanceDue.IsZero() {
		t.Errorf("Invoice.BalanceDue should be 0; got %s", updatedInv.BalanceDue)
	}
}

func TestPhase6_PostWriteOff_NoARAcct(t *testing.T) {
	db := phase6DB(t)
	cid := p6Company(t, db)
	custID := p6Customer(t, db, cid)
	expID := p6ExpenseAccount(t, db, cid)

	wo, _ := CreateARWriteOff(db, cid, ARWriteOffInput{
		CustomerID:       custID,
		ExpenseAccountID: &expID,
		WriteOffDate:     time.Now(),
		CurrencyCode:     "CAD",
		Amount:           decimal.NewFromInt(100),
	})

	err := PostARWriteOff(db, cid, wo.ID, "tester", nil)
	if err != ErrWriteOffNoARAcct {
		t.Errorf("expected ErrWriteOffNoARAcct; got %v", err)
	}
}

func TestPhase6_PostWriteOff_NoExpenseAcct(t *testing.T) {
	db := phase6DB(t)
	cid := p6Company(t, db)
	custID := p6Customer(t, db, cid)
	arID := p6ARAccount(t, db, cid)

	wo, _ := CreateARWriteOff(db, cid, ARWriteOffInput{
		CustomerID:   custID,
		ARAccountID:  &arID,
		WriteOffDate: time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.NewFromInt(100),
	})

	err := PostARWriteOff(db, cid, wo.ID, "tester", nil)
	if err != ErrWriteOffNoExpenseAcct {
		t.Errorf("expected ErrWriteOffNoExpenseAcct; got %v", err)
	}
}

func TestPhase6_VoidDraftWriteOff(t *testing.T) {
	db := phase6DB(t)
	cid := p6Company(t, db)
	custID := p6Customer(t, db, cid)

	wo, _ := CreateARWriteOff(db, cid, ARWriteOffInput{
		CustomerID:   custID,
		WriteOffDate: time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.NewFromInt(50),
	})

	if err := VoidARWriteOff(db, cid, wo.ID); err != nil {
		t.Fatalf("VoidARWriteOff: %v", err)
	}
	updated, _ := GetARWriteOff(db, cid, wo.ID)
	if updated.Status != models.ARWriteOffStatusVoided {
		t.Errorf("expected voided; got %s", updated.Status)
	}

	// No JE.
	var jeCount int64
	db.Model(&models.JournalEntry{}).Count(&jeCount)
	if jeCount != 0 {
		t.Errorf("expected 0 JEs after void; got %d", jeCount)
	}
}

func TestPhase6_VoidPostedWriteOff_Fails(t *testing.T) {
	db := phase6DB(t)
	cid := p6Company(t, db)
	custID := p6Customer(t, db, cid)
	arID := p6ARAccount(t, db, cid)
	expID := p6ExpenseAccount(t, db, cid)

	wo, _ := CreateARWriteOff(db, cid, ARWriteOffInput{
		CustomerID:       custID,
		ARAccountID:      &arID,
		ExpenseAccountID: &expID,
		WriteOffDate:     time.Now(),
		CurrencyCode:     "CAD",
		Amount:           decimal.NewFromInt(50),
	})
	PostARWriteOff(db, cid, wo.ID, "tester", nil)

	err := VoidARWriteOff(db, cid, wo.ID)
	if err == nil {
		t.Fatal("expected error voiding a posted write-off")
	}
}

func TestPhase6_ReversePostedWriteOff(t *testing.T) {
	db := phase6DB(t)
	cid := p6Company(t, db)
	custID := p6Customer(t, db, cid)
	arID := p6ARAccount(t, db, cid)
	expID := p6ExpenseAccount(t, db, cid)
	inv := p6Invoice(t, db, cid, custID, decimal.NewFromInt(100), 30)

	invIDPtr := inv.ID
	wo, _ := CreateARWriteOff(db, cid, ARWriteOffInput{
		CustomerID:       custID,
		InvoiceID:        &invIDPtr,
		ARAccountID:      &arID,
		ExpenseAccountID: &expID,
		WriteOffDate:     time.Now(),
		CurrencyCode:     "CAD",
		ExchangeRate:     decimal.NewFromInt(1),
		Amount:           decimal.NewFromInt(100),
	})
	PostARWriteOff(db, cid, wo.ID, "tester", nil)

	// Invoice.BalanceDue should now be 0.
	var invAfterPost models.Invoice
	db.First(&invAfterPost, inv.ID)
	if !invAfterPost.BalanceDue.IsZero() {
		t.Errorf("expected BalanceDue=0 after post; got %s", invAfterPost.BalanceDue)
	}

	if err := ReverseARWriteOff(db, cid, wo.ID, "tester", nil); err != nil {
		t.Fatalf("ReverseARWriteOff: %v", err)
	}

	updated, _ := GetARWriteOff(db, cid, wo.ID)
	if updated.Status != models.ARWriteOffStatusReversed {
		t.Errorf("expected reversed; got %s", updated.Status)
	}

	// Invoice.BalanceDue should be restored to 100.
	var invAfterRev models.Invoice
	db.First(&invAfterRev, inv.ID)
	if !invAfterRev.BalanceDue.Equal(decimal.NewFromInt(100)) {
		t.Errorf("expected BalanceDue restored to 100; got %s", invAfterRev.BalanceDue)
	}

	// 2 JEs: original + reversal.
	var jeCount int64
	db.Model(&models.JournalEntry{}).Where("company_id = ?", cid).Count(&jeCount)
	if jeCount != 2 {
		t.Errorf("expected 2 JEs after reversal; got %d", jeCount)
	}
}

func TestPhase6_ReverseDraftWriteOff_Fails(t *testing.T) {
	db := phase6DB(t)
	cid := p6Company(t, db)
	custID := p6Customer(t, db, cid)

	wo, _ := CreateARWriteOff(db, cid, ARWriteOffInput{
		CustomerID:   custID,
		WriteOffDate: time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.NewFromInt(50),
	})

	err := ReverseARWriteOff(db, cid, wo.ID, "tester", nil)
	if err == nil {
		t.Fatal("expected error reversing a draft write-off")
	}
}

func TestPhase6_WriteOffIsolation(t *testing.T) {
	db := phase6DB(t)
	cid1 := p6Company(t, db)
	cid2 := p6Company(t, db)
	custID := p6Customer(t, db, cid1)

	wo, _ := CreateARWriteOff(db, cid1, ARWriteOffInput{
		CustomerID:   custID,
		WriteOffDate: time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.NewFromInt(50),
	})

	_, err := GetARWriteOff(db, cid2, wo.ID)
	if err != ErrWriteOffNotFound {
		t.Errorf("expected ErrWriteOffNotFound for wrong company; got %v", err)
	}
}

func TestPhase6_WriteOffDocumentNumbering(t *testing.T) {
	db := phase6DB(t)
	cid := p6Company(t, db)
	custID := p6Customer(t, db, cid)

	w1, _ := CreateARWriteOff(db, cid, ARWriteOffInput{
		CustomerID:   custID,
		WriteOffDate: time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.NewFromInt(50),
	})
	w2, _ := CreateARWriteOff(db, cid, ARWriteOffInput{
		CustomerID:   custID,
		WriteOffDate: time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.NewFromInt(75),
	})
	if w1.WriteOffNumber != "WOF-0001" {
		t.Errorf("expected WOF-0001; got %s", w1.WriteOffNumber)
	}
	if w2.WriteOffNumber != "WOF-0002" {
		t.Errorf("expected WOF-0002; got %s", w2.WriteOffNumber)
	}
}

// ── ARAging tests ─────────────────────────────────────────────────────────────

func TestPhase6_ARAging_Empty(t *testing.T) {
	db := phase6DB(t)
	cid := p6Company(t, db)

	aging, err := GetARAging(db, cid, time.Now())
	if err != nil {
		t.Fatalf("GetARAging: %v", err)
	}
	if len(aging.Lines) != 0 {
		t.Errorf("expected 0 lines; got %d", len(aging.Lines))
	}
	if !aging.GrandTotal.IsZero() {
		t.Errorf("expected zero grand total; got %s", aging.GrandTotal)
	}
}

func TestPhase6_ARAging_Buckets(t *testing.T) {
	db := phase6DB(t)
	cid := p6Company(t, db)
	custID := p6Customer(t, db, cid)

	// Create invoices with different overdue amounts.
	p6Invoice(t, db, cid, custID, decimal.NewFromInt(100), 0)   // current (due today)
	p6Invoice(t, db, cid, custID, decimal.NewFromInt(200), 15)  // 1-30 days
	p6Invoice(t, db, cid, custID, decimal.NewFromInt(300), 45)  // 31-60 days
	p6Invoice(t, db, cid, custID, decimal.NewFromInt(400), 75)  // 61-90 days
	p6Invoice(t, db, cid, custID, decimal.NewFromInt(500), 100) // 90+ days

	aging, err := GetARAging(db, cid, time.Now())
	if err != nil {
		t.Fatalf("GetARAging: %v", err)
	}
	if len(aging.Lines) != 1 {
		t.Fatalf("expected 1 customer line; got %d", len(aging.Lines))
	}

	line := aging.Lines[0]
	if !line.Current.Equal(decimal.NewFromInt(100)) {
		t.Errorf("Current should be 100; got %s", line.Current)
	}
	if !line.Days1_30.Equal(decimal.NewFromInt(200)) {
		t.Errorf("Days1_30 should be 200; got %s", line.Days1_30)
	}
	if !line.Days31_60.Equal(decimal.NewFromInt(300)) {
		t.Errorf("Days31_60 should be 300; got %s", line.Days31_60)
	}
	if !line.Days61_90.Equal(decimal.NewFromInt(400)) {
		t.Errorf("Days61_90 should be 400; got %s", line.Days61_90)
	}
	if !line.Days90Plus.Equal(decimal.NewFromInt(500)) {
		t.Errorf("Days90Plus should be 500; got %s", line.Days90Plus)
	}
	if !line.Total.Equal(decimal.NewFromInt(1500)) {
		t.Errorf("Total should be 1500; got %s", line.Total)
	}
	if !aging.GrandTotal.Equal(decimal.NewFromInt(1500)) {
		t.Errorf("GrandTotal should be 1500; got %s", aging.GrandTotal)
	}
}

func TestPhase6_ARAging_PaidInvoicesExcluded(t *testing.T) {
	db := phase6DB(t)
	cid := p6Company(t, db)
	custID := p6Customer(t, db, cid)

	// Create an overdue invoice then mark it paid.
	inv := p6Invoice(t, db, cid, custID, decimal.NewFromInt(100), 30)
	db.Model(inv).Updates(map[string]any{
		"balance_due": decimal.Zero,
		"status":      string(models.InvoiceStatusPaid),
	})

	aging, err := GetARAging(db, cid, time.Now())
	if err != nil {
		t.Fatalf("GetARAging: %v", err)
	}
	if len(aging.Lines) != 0 {
		t.Errorf("paid invoice should be excluded; got %d lines", len(aging.Lines))
	}
}

// ── CustomerStatement tests ───────────────────────────────────────────────────

func stmtDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Account{},
		&models.Invoice{},
		&models.InvoiceLine{},
		&models.CustomerReceipt{},
		&models.ARRefund{},
		&models.ARWriteOff{},
		&models.CreditNote{},
		&models.CreditNoteLine{},
		&models.CreditNoteApplication{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestPhase6_CustomerStatement_Empty(t *testing.T) {
	db := stmtDB(t)
	var c models.Company
	db.Create(&c)
	var cust models.Customer
	cust.CompanyID = c.ID
	db.Create(&cust)

	from := time.Now().AddDate(0, -1, 0)
	to := time.Now()
	stmt, err := GetCustomerStatement(db, c.ID, cust.ID, from, to)
	if err != nil {
		t.Fatalf("GetCustomerStatement: %v", err)
	}
	if !stmt.OpeningBalance.IsZero() {
		t.Errorf("expected zero opening balance; got %s", stmt.OpeningBalance)
	}
	if len(stmt.Lines) != 0 {
		t.Errorf("expected 0 lines; got %d", len(stmt.Lines))
	}
	if !stmt.ClosingBalance.IsZero() {
		t.Errorf("expected zero closing balance; got %s", stmt.ClosingBalance)
	}
}

func TestPhase6_CustomerStatement_InvoiceInPeriod(t *testing.T) {
	db := stmtDB(t)
	var co models.Company
	db.Create(&co)
	cust := models.Customer{CompanyID: co.ID, Name: "Test"}
	db.Create(&cust)

	from := time.Now().AddDate(0, -1, 0)
	to := time.Now()
	invDate := time.Now().AddDate(0, 0, -5)

	inv := models.Invoice{
		CompanyID:     co.ID,
		CustomerID:    cust.ID,
		InvoiceNumber: "INV-S001",
		InvoiceDate:   invDate,
		Status:        models.InvoiceStatusSent,
		Amount:        decimal.NewFromInt(500),
		Subtotal:      decimal.NewFromInt(500),
		BalanceDue:    decimal.NewFromInt(500),
		CurrencyCode:  "CAD",
		ExchangeRate:  decimal.NewFromInt(1),
	}
	db.Create(&inv)

	stmt, err := GetCustomerStatement(db, co.ID, cust.ID, from, to)
	if err != nil {
		t.Fatalf("GetCustomerStatement: %v", err)
	}
	if len(stmt.Lines) != 1 {
		t.Fatalf("expected 1 line; got %d", len(stmt.Lines))
	}
	if stmt.Lines[0].Type != StatementLineInvoice {
		t.Errorf("expected invoice line; got %s", stmt.Lines[0].Type)
	}
	if !stmt.Lines[0].Debit.Equal(decimal.NewFromInt(500)) {
		t.Errorf("expected debit 500; got %s", stmt.Lines[0].Debit)
	}
	if !stmt.ClosingBalance.Equal(decimal.NewFromInt(500)) {
		t.Errorf("expected closing balance 500; got %s", stmt.ClosingBalance)
	}
}
