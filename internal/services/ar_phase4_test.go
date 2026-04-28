// 遵循project_guide.md
package services

// ar_phase4_test.go — AR Phase 4: CustomerReceipt + PaymentApplication lifecycle tests.
//
// Tests verify:
//  1. Draft creation succeeds; no JE created.
//  2. Confirmation creates correct JE (Dr Bank, Cr AR) and sets status/UnappliedAmount.
//  3. Apply-to-invoice creates PaymentApplication, reduces Invoice.BalanceDue,
//     reduces UnappliedAmount, updates statuses.
//  4. Unapply reverses the application, restores Invoice.BalanceDue.
//  5. Reverse of confirmed (unapplied) receipt creates reversal JE.
//  6. Cannot reverse a partially-applied receipt (must unapply first).
//  7. Void of draft succeeds, no JE.
//  8. Company isolation.
//  9. Document numbering.

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── DB helper ─────────────────────────────────────────────────────────────────

func phase4DB(t *testing.T) *gorm.DB {
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
		&models.CustomerReceipt{},
		&models.PaymentApplication{},
		&models.ARAPControlMapping{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func p4Company(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	c := models.Company{Name: "Phase4 Co", BaseCurrencyCode: "CAD", IsActive: true, AccountCodeLength: 4}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func p4Customer(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	c := models.Customer{CompanyID: companyID, Name: "Test Customer"}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func p4BankAccount(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	a := models.Account{
		CompanyID:         companyID,
		Code:              "1010",
		Name:              "Bank",
		RootAccountType:   models.RootAsset,
		DetailAccountType: models.DetailBank,
		IsActive:          true,
	}
	if err := db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	return a.ID
}

func p4ARAccount(t *testing.T, db *gorm.DB, companyID uint) uint {
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

func p4Invoice(t *testing.T, db *gorm.DB, companyID, custID uint, amount decimal.Decimal) *models.Invoice {
	t.Helper()
	inv := models.Invoice{
		CompanyID:     companyID,
		CustomerID:    custID,
		InvoiceNumber: "INV-0001",
		InvoiceDate:   time.Now(),
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

// ── Creation ──────────────────────────────────────────────────────────────────

func TestPhase4_CreateReceipt(t *testing.T) {
	db := phase4DB(t)
	cid := p4Company(t, db)
	custID := p4Customer(t, db, cid)

	rcpt, err := CreateCustomerReceipt(db, cid, CustomerReceiptInput{
		CustomerID:   custID,
		ReceiptDate:  time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.NewFromInt(500),
	})
	if err != nil {
		t.Fatalf("CreateCustomerReceipt: %v", err)
	}
	if rcpt.Status != models.CustomerReceiptStatusDraft {
		t.Errorf("expected draft; got %s", rcpt.Status)
	}
	if rcpt.ReceiptNumber == "" {
		t.Error("ReceiptNumber must be set")
	}
	if !rcpt.UnappliedAmount.IsZero() {
		t.Errorf("UnappliedAmount should be 0 at draft; got %s", rcpt.UnappliedAmount)
	}
	// No JE.
	var count int64
	db.Model(&models.JournalEntry{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 JEs after draft creation; got %d", count)
	}
}

func TestPhase4_CreateReceipt_NoCustomer(t *testing.T) {
	db := phase4DB(t)
	cid := p4Company(t, db)
	_, err := CreateCustomerReceipt(db, cid, CustomerReceiptInput{
		ReceiptDate:  time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.NewFromInt(100),
	})
	if err == nil {
		t.Error("expected error for missing customer")
	}
}

func TestPhase4_CreateReceipt_ZeroAmount(t *testing.T) {
	db := phase4DB(t)
	cid := p4Company(t, db)
	custID := p4Customer(t, db, cid)
	_, err := CreateCustomerReceipt(db, cid, CustomerReceiptInput{
		CustomerID:   custID,
		ReceiptDate:  time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.Zero,
	})
	if err == nil {
		t.Error("expected error for zero amount")
	}
}

// ── Confirm ───────────────────────────────────────────────────────────────────

func TestPhase4_ConfirmReceipt(t *testing.T) {
	db := phase4DB(t)
	cid := p4Company(t, db)
	custID := p4Customer(t, db, cid)
	bankID := p4BankAccount(t, db, cid)
	_ = p4ARAccount(t, db, cid)

	rcpt, err := CreateCustomerReceipt(db, cid, CustomerReceiptInput{
		CustomerID:    custID,
		BankAccountID: &bankID,
		ReceiptDate:   time.Now(),
		CurrencyCode:  "CAD",
		Amount:        decimal.NewFromInt(1000),
	})
	if err != nil {
		t.Fatalf("CreateCustomerReceipt: %v", err)
	}

	if err := ConfirmCustomerReceipt(db, cid, rcpt.ID, "tester", nil); err != nil {
		t.Fatalf("ConfirmCustomerReceipt: %v", err)
	}

	confirmed, _ := GetCustomerReceipt(db, cid, rcpt.ID)
	if confirmed.Status != models.CustomerReceiptStatusConfirmed {
		t.Errorf("expected confirmed; got %s", confirmed.Status)
	}
	if !confirmed.UnappliedAmount.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("UnappliedAmount should be 1000; got %s", confirmed.UnappliedAmount)
	}
	if confirmed.JournalEntryID == nil {
		t.Fatal("JournalEntryID must be set after confirming")
	}
	if confirmed.ConfirmedAt == nil {
		t.Error("ConfirmedAt must be set after confirming")
	}

	// Verify JE structure: Dr Bank Cr AR.
	var lines []models.JournalLine
	db.Where("journal_entry_id = ?", *confirmed.JournalEntryID).Find(&lines)
	if len(lines) != 2 {
		t.Fatalf("expected 2 JE lines; got %d", len(lines))
	}

	var bankLine, arLine *models.JournalLine
	for i := range lines {
		if lines[i].AccountID == bankID {
			bankLine = &lines[i]
		} else {
			arLine = &lines[i]
		}
	}
	if bankLine == nil || !bankLine.Debit.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("bank debit expected 1000; got %+v", bankLine)
	}
	if arLine == nil || !arLine.Credit.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("AR credit expected 1000; got %+v", arLine)
	}

	// Verify LedgerEntry projection.
	var ledgerCount int64
	db.Model(&models.LedgerEntry{}).Where("journal_entry_id = ?", *confirmed.JournalEntryID).Count(&ledgerCount)
	if ledgerCount != 2 {
		t.Errorf("expected 2 ledger entries; got %d", ledgerCount)
	}
}

func TestPhase4_ConfirmReceipt_MissingBank(t *testing.T) {
	db := phase4DB(t)
	cid := p4Company(t, db)
	custID := p4Customer(t, db, cid)
	_ = p4ARAccount(t, db, cid)

	rcpt, _ := CreateCustomerReceipt(db, cid, CustomerReceiptInput{
		CustomerID:   custID,
		ReceiptDate:  time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.NewFromInt(100),
	})
	err := ConfirmCustomerReceipt(db, cid, rcpt.ID, "tester", nil)
	if err == nil {
		t.Error("expected error confirming with no bank account")
	}
}

// ── Apply ─────────────────────────────────────────────────────────────────────

func TestPhase4_ApplyReceiptToInvoice(t *testing.T) {
	db := phase4DB(t)
	cid := p4Company(t, db)
	custID := p4Customer(t, db, cid)
	bankID := p4BankAccount(t, db, cid)
	_ = p4ARAccount(t, db, cid)
	inv := p4Invoice(t, db, cid, custID, decimal.NewFromInt(800))

	rcpt, _ := CreateCustomerReceipt(db, cid, CustomerReceiptInput{
		CustomerID:    custID,
		BankAccountID: &bankID,
		ReceiptDate:   time.Now(),
		CurrencyCode:  "CAD",
		Amount:        decimal.NewFromInt(1000),
	})
	ConfirmCustomerReceipt(db, cid, rcpt.ID, "tester", nil)

	err := ApplyReceiptToInvoice(db, cid, ApplyReceiptInput{
		ReceiptID:     rcpt.ID,
		InvoiceID:     inv.ID,
		AmountApplied: decimal.NewFromInt(800),
		Actor:         "tester",
	})
	if err != nil {
		t.Fatalf("ApplyReceiptToInvoice: %v", err)
	}

	// Reload receipt.
	updated, _ := GetCustomerReceipt(db, cid, rcpt.ID)
	if updated.Status != models.CustomerReceiptStatusPartiallyApplied {
		t.Errorf("expected partially_applied; got %s", updated.Status)
	}
	if !updated.UnappliedAmount.Equal(decimal.NewFromInt(200)) {
		t.Errorf("UnappliedAmount should be 200; got %s", updated.UnappliedAmount)
	}

	// Reload invoice.
	var updatedInv models.Invoice
	db.First(&updatedInv, inv.ID)
	if !updatedInv.BalanceDue.IsZero() {
		t.Errorf("Invoice BalanceDue should be 0; got %s", updatedInv.BalanceDue)
	}

	// Verify PaymentApplication was created.
	var appCount int64
	db.Model(&models.PaymentApplication{}).Where("customer_receipt_id = ? AND invoice_id = ?", rcpt.ID, inv.ID).Count(&appCount)
	if appCount != 1 {
		t.Errorf("expected 1 payment application; got %d", appCount)
	}

	// No new JE should have been created by apply (only the confirm JE).
	var jeCount int64
	db.Model(&models.JournalEntry{}).Where("company_id = ?", cid).Count(&jeCount)
	if jeCount != 1 {
		t.Errorf("expected exactly 1 JE (from confirm only); got %d", jeCount)
	}
}

func TestPhase4_ApplyReceipt_ExceedsBalance(t *testing.T) {
	db := phase4DB(t)
	cid := p4Company(t, db)
	custID := p4Customer(t, db, cid)
	bankID := p4BankAccount(t, db, cid)
	_ = p4ARAccount(t, db, cid)
	inv := p4Invoice(t, db, cid, custID, decimal.NewFromInt(100))

	rcpt, _ := CreateCustomerReceipt(db, cid, CustomerReceiptInput{
		CustomerID:    custID,
		BankAccountID: &bankID,
		ReceiptDate:   time.Now(),
		CurrencyCode:  "CAD",
		Amount:        decimal.NewFromInt(50),
	})
	ConfirmCustomerReceipt(db, cid, rcpt.ID, "tester", nil)

	err := ApplyReceiptToInvoice(db, cid, ApplyReceiptInput{
		ReceiptID:     rcpt.ID,
		InvoiceID:     inv.ID,
		AmountApplied: decimal.NewFromInt(75), // exceeds unapplied amount of 50
		Actor:         "tester",
	})
	if err == nil {
		t.Error("expected error for apply exceeding unapplied amount")
	}
}

// ── Unapply ───────────────────────────────────────────────────────────────────

func TestPhase4_UnapplyReceipt(t *testing.T) {
	db := phase4DB(t)
	cid := p4Company(t, db)
	custID := p4Customer(t, db, cid)
	bankID := p4BankAccount(t, db, cid)
	_ = p4ARAccount(t, db, cid)
	inv := p4Invoice(t, db, cid, custID, decimal.NewFromInt(500))

	rcpt, _ := CreateCustomerReceipt(db, cid, CustomerReceiptInput{
		CustomerID:    custID,
		BankAccountID: &bankID,
		ReceiptDate:   time.Now(),
		CurrencyCode:  "CAD",
		Amount:        decimal.NewFromInt(500),
	})
	ConfirmCustomerReceipt(db, cid, rcpt.ID, "tester", nil)
	ApplyReceiptToInvoice(db, cid, ApplyReceiptInput{
		ReceiptID:     rcpt.ID,
		InvoiceID:     inv.ID,
		AmountApplied: decimal.NewFromInt(500),
		Actor:         "tester",
	})

	// Find the application.
	var app models.PaymentApplication
	db.Where("customer_receipt_id = ? AND invoice_id = ?", rcpt.ID, inv.ID).First(&app)

	if err := UnapplyReceipt(db, cid, app.ID, "tester"); err != nil {
		t.Fatalf("UnapplyReceipt: %v", err)
	}

	// Receipt should be confirmed (unapplied) again.
	updated, _ := GetCustomerReceipt(db, cid, rcpt.ID)
	if updated.Status != models.CustomerReceiptStatusConfirmed {
		t.Errorf("expected confirmed after unapply; got %s", updated.Status)
	}
	if !updated.UnappliedAmount.Equal(decimal.NewFromInt(500)) {
		t.Errorf("UnappliedAmount should be restored to 500; got %s", updated.UnappliedAmount)
	}

	// Invoice BalanceDue should be restored.
	var updatedInv models.Invoice
	db.First(&updatedInv, inv.ID)
	if !updatedInv.BalanceDue.Equal(decimal.NewFromInt(500)) {
		t.Errorf("Invoice BalanceDue should be restored to 500; got %s", updatedInv.BalanceDue)
	}
}

// ── Reverse ───────────────────────────────────────────────────────────────────

func TestPhase4_ReverseReceipt(t *testing.T) {
	db := phase4DB(t)
	cid := p4Company(t, db)
	custID := p4Customer(t, db, cid)
	bankID := p4BankAccount(t, db, cid)
	_ = p4ARAccount(t, db, cid)

	rcpt, _ := CreateCustomerReceipt(db, cid, CustomerReceiptInput{
		CustomerID:    custID,
		BankAccountID: &bankID,
		ReceiptDate:   time.Now(),
		CurrencyCode:  "CAD",
		Amount:        decimal.NewFromInt(300),
	})
	ConfirmCustomerReceipt(db, cid, rcpt.ID, "tester", nil)

	if err := ReverseCustomerReceipt(db, cid, rcpt.ID, "tester"); err != nil {
		t.Fatalf("ReverseCustomerReceipt: %v", err)
	}

	got, _ := GetCustomerReceipt(db, cid, rcpt.ID)
	if got.Status != models.CustomerReceiptStatusReversed {
		t.Errorf("expected reversed; got %s", got.Status)
	}
	if !got.UnappliedAmount.IsZero() {
		t.Errorf("UnappliedAmount should be 0 after reversal; got %s", got.UnappliedAmount)
	}

	// Original JE should be marked reversed.
	var je models.JournalEntry
	db.First(&je, got.JournalEntryID)
	if je.Status != models.JournalEntryStatusReversed {
		t.Errorf("original JE should be reversed; got %s", je.Status)
	}

	// A reversal JE should now exist.
	var jeCount int64
	db.Model(&models.JournalEntry{}).Where("company_id = ?", cid).Count(&jeCount)
	if jeCount < 2 {
		t.Errorf("expected at least 2 JEs; got %d", jeCount)
	}
}

func TestPhase4_ReverseApplied_Fails(t *testing.T) {
	db := phase4DB(t)
	cid := p4Company(t, db)
	custID := p4Customer(t, db, cid)
	bankID := p4BankAccount(t, db, cid)
	_ = p4ARAccount(t, db, cid)
	inv := p4Invoice(t, db, cid, custID, decimal.NewFromInt(200))

	rcpt, _ := CreateCustomerReceipt(db, cid, CustomerReceiptInput{
		CustomerID:    custID,
		BankAccountID: &bankID,
		ReceiptDate:   time.Now(),
		CurrencyCode:  "CAD",
		Amount:        decimal.NewFromInt(200),
	})
	ConfirmCustomerReceipt(db, cid, rcpt.ID, "tester", nil)
	ApplyReceiptToInvoice(db, cid, ApplyReceiptInput{
		ReceiptID:     rcpt.ID,
		InvoiceID:     inv.ID,
		AmountApplied: decimal.NewFromInt(200),
		Actor:         "tester",
	})

	// Status is now fully_applied — cannot reverse.
	err := ReverseCustomerReceipt(db, cid, rcpt.ID, "tester")
	if err == nil {
		t.Error("expected error reversing a fully-applied receipt")
	}
}

// ── Void ──────────────────────────────────────────────────────────────────────

func TestPhase4_VoidDraftReceipt(t *testing.T) {
	db := phase4DB(t)
	cid := p4Company(t, db)
	custID := p4Customer(t, db, cid)

	rcpt, _ := CreateCustomerReceipt(db, cid, CustomerReceiptInput{
		CustomerID:   custID,
		ReceiptDate:  time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.NewFromInt(200),
	})
	if err := VoidCustomerReceipt(db, cid, rcpt.ID); err != nil {
		t.Fatalf("VoidCustomerReceipt: %v", err)
	}
	got, _ := GetCustomerReceipt(db, cid, rcpt.ID)
	if got.Status != models.CustomerReceiptStatusVoided {
		t.Errorf("expected voided; got %s", got.Status)
	}
	// No JE created.
	var count int64
	db.Model(&models.JournalEntry{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 JEs; got %d", count)
	}
}

// ── Isolation ─────────────────────────────────────────────────────────────────

func TestPhase4_Isolation(t *testing.T) {
	db := phase4DB(t)
	cid1 := p4Company(t, db)
	cid2 := func() uint {
		c := models.Company{Name: "Other Co", BaseCurrencyCode: "CAD", IsActive: true, AccountCodeLength: 4}
		db.Create(&c)
		return c.ID
	}()
	custID := p4Customer(t, db, cid1)

	rcpt, _ := CreateCustomerReceipt(db, cid1, CustomerReceiptInput{
		CustomerID:   custID,
		ReceiptDate:  time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.NewFromInt(300),
	})

	_, err := GetCustomerReceipt(db, cid2, rcpt.ID)
	if err == nil {
		t.Error("expected isolation error; cid2 should not see cid1 receipt")
	}
	_, err = GetCustomerReceipt(db, cid1, rcpt.ID)
	if err != nil {
		t.Errorf("cid1 should see own receipt: %v", err)
	}
}

// ── Document numbering ────────────────────────────────────────────────────────

func TestPhase4_DocumentNumbering(t *testing.T) {
	db := phase4DB(t)
	cid := p4Company(t, db)
	custID := p4Customer(t, db, cid)

	make4 := func() *models.CustomerReceipt {
		r, err := CreateCustomerReceipt(db, cid, CustomerReceiptInput{
			CustomerID:   custID,
			ReceiptDate:  time.Now(),
			CurrencyCode: "CAD",
			Amount:       decimal.NewFromInt(100),
		})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	r1 := make4()
	r2 := make4()
	if r1.ReceiptNumber == r2.ReceiptNumber {
		t.Errorf("receipt numbers must be unique; got %s and %s", r1.ReceiptNumber, r2.ReceiptNumber)
	}
	if r1.ReceiptNumber != "RCT-0001" {
		t.Errorf("first receipt number should be RCT-0001; got %s", r1.ReceiptNumber)
	}
	if r2.ReceiptNumber != "RCT-0002" {
		t.Errorf("second receipt number should be RCT-0002; got %s", r2.ReceiptNumber)
	}
}
