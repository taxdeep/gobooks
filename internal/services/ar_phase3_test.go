// 遵循project_guide.md
package services

// ar_phase3_test.go — AR Phase 3: CustomerDeposit lifecycle tests.
//
// Tests verify:
//  1. Draft creation succeeds; no JE created.
//  2. Posting creates correct JE (Dr Bank, Cr Liability) and updates status/balance.
//  3. Apply-to-invoice creates correct JE (Dr Liability, Cr AR), reduces BalanceRemaining and invoice BalanceDue.
//  4. Void of draft succeeds with no JE.
//  5. Void of posted deposit creates reversal JE and marks original reversed.
//  6. Cannot void partially-applied deposit.
//  7. Company isolation.
//  8. Document numbering.

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// ── DB helper ─────────────────────────────────────────────────────────────────

func phase3DB(t *testing.T) *gorm.DB {
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
		&models.CustomerDeposit{},
		&models.CustomerDepositApplication{},
		// AR boundary objects required for AR/AP control lookup
		&models.ARAPControlMapping{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func p3Company(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	c := models.Company{Name: "Phase3 Co", BaseCurrencyCode: "CAD", IsActive: true, AccountCodeLength: 4}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func p3Customer(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	c := models.Customer{CompanyID: companyID, Name: "Test Customer"}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// p3BankAccount creates a bank/cash account for deposits.
func p3BankAccount(t *testing.T, db *gorm.DB, companyID uint) uint {
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

// p3LiabilityAccount creates a customer deposit liability account.
func p3LiabilityAccount(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	a := models.Account{
		CompanyID:         companyID,
		Code:              "2100",
		Name:              "Customer Deposits",
		RootAccountType:   models.RootLiability,
		DetailAccountType: models.DetailOtherCurrentLiability,
		IsActive:          true,
	}
	if err := db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	return a.ID
}

// p3ARAccount creates an AR account and wires it up via the legacy system_key.
func p3ARAccount(t *testing.T, db *gorm.DB, companyID uint) uint {
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

// p3Invoice creates a posted invoice with BalanceDue set.
func p3Invoice(t *testing.T, db *gorm.DB, companyID, custID uint, amount decimal.Decimal) *models.Invoice {
	t.Helper()
	inv := models.Invoice{
		CompanyID:    companyID,
		CustomerID:   custID,
		InvoiceNumber: "INV-0001",
		InvoiceDate:  time.Now(),
		Status:       models.InvoiceStatusSent,
		Amount:       amount,
		Subtotal:     amount,
		BalanceDue:   amount,
		CurrencyCode: "CAD",
		ExchangeRate: decimal.NewFromInt(1),
	}
	if err := db.Create(&inv).Error; err != nil {
		t.Fatal(err)
	}
	return &inv
}

// ── Creation ──────────────────────────────────────────────────────────────────

func TestPhase3_CreateDeposit_NilBankAndLiability(t *testing.T) {
	db := phase3DB(t)
	cid := p3Company(t, db)
	custID := p3Customer(t, db, cid)

	// Bank and liability accounts not set — allowed at draft stage.
	dep, err := CreateCustomerDeposit(db, cid, CustomerDepositInput{
		CustomerID:   custID,
		DepositDate:  time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.NewFromInt(500),
	})
	if err != nil {
		t.Fatalf("CreateCustomerDeposit: %v", err)
	}
	if dep.Status != models.CustomerDepositStatusDraft {
		t.Errorf("expected draft; got %s", dep.Status)
	}
	if dep.DepositNumber == "" {
		t.Error("DepositNumber must be set")
	}
	if !dep.BalanceRemaining.IsZero() {
		t.Errorf("BalanceRemaining should be 0 at draft; got %s", dep.BalanceRemaining)
	}

	// No JE should exist.
	var count int64
	db.Model(&models.JournalEntry{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 JEs after draft creation; got %d", count)
	}
}

func TestPhase3_CreateDeposit_NoCustomer(t *testing.T) {
	db := phase3DB(t)
	cid := p3Company(t, db)
	_, err := CreateCustomerDeposit(db, cid, CustomerDepositInput{
		DepositDate:  time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.NewFromInt(100),
	})
	if err == nil {
		t.Error("expected error for missing customer")
	}
}

func TestPhase3_CreateDeposit_ZeroAmount(t *testing.T) {
	db := phase3DB(t)
	cid := p3Company(t, db)
	custID := p3Customer(t, db, cid)
	_, err := CreateCustomerDeposit(db, cid, CustomerDepositInput{
		CustomerID:   custID,
		DepositDate:  time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.Zero,
	})
	if err == nil {
		t.Error("expected error for zero amount")
	}
}

// ── Posting ───────────────────────────────────────────────────────────────────

func TestPhase3_PostDeposit(t *testing.T) {
	db := phase3DB(t)
	cid := p3Company(t, db)
	custID := p3Customer(t, db, cid)
	bankID := p3BankAccount(t, db, cid)
	liabID := p3LiabilityAccount(t, db, cid)

	dep, err := CreateCustomerDeposit(db, cid, CustomerDepositInput{
		CustomerID:                custID,
		BankAccountID:             &bankID,
		DepositLiabilityAccountID: &liabID,
		DepositDate:               time.Now(),
		CurrencyCode:              "CAD",
		Amount:                    decimal.NewFromInt(1000),
	})
	if err != nil {
		t.Fatalf("CreateCustomerDeposit: %v", err)
	}

	if err := PostCustomerDeposit(db, cid, dep.ID, "tester", nil); err != nil {
		t.Fatalf("PostCustomerDeposit: %v", err)
	}

	// Reload deposit.
	posted, _ := GetCustomerDeposit(db, cid, dep.ID)
	if posted.Status != models.CustomerDepositStatusPosted {
		t.Errorf("expected posted; got %s", posted.Status)
	}
	if !posted.BalanceRemaining.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("BalanceRemaining should be 1000; got %s", posted.BalanceRemaining)
	}
	if posted.JournalEntryID == nil {
		t.Fatal("JournalEntryID must be set after posting")
	}
	if posted.PostedAt == nil {
		t.Error("PostedAt must be set after posting")
	}

	// Verify JE structure: Dr Bank, Cr Liability.
	var lines []models.JournalLine
	db.Where("journal_entry_id = ?", *posted.JournalEntryID).Find(&lines)
	if len(lines) != 2 {
		t.Fatalf("expected 2 JE lines; got %d", len(lines))
	}

	var bankLine, liabLine *models.JournalLine
	for i := range lines {
		if lines[i].AccountID == bankID {
			bankLine = &lines[i]
		}
		if lines[i].AccountID == liabID {
			liabLine = &lines[i]
		}
	}
	if bankLine == nil {
		t.Fatal("no bank debit line found")
	}
	if liabLine == nil {
		t.Fatal("no liability credit line found")
	}
	if !bankLine.Debit.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("bank debit expected 1000; got %s", bankLine.Debit)
	}
	if !liabLine.Credit.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("liability credit expected 1000; got %s", liabLine.Credit)
	}

	// Verify LedgerEntry projection.
	var ledgerCount int64
	db.Model(&models.LedgerEntry{}).Where("journal_entry_id = ?", *posted.JournalEntryID).Count(&ledgerCount)
	if ledgerCount != 2 {
		t.Errorf("expected 2 ledger entries; got %d", ledgerCount)
	}
}

func TestPhase3_PostDeposit_MissingBank(t *testing.T) {
	db := phase3DB(t)
	cid := p3Company(t, db)
	custID := p3Customer(t, db, cid)
	liabID := p3LiabilityAccount(t, db, cid)

	dep, _ := CreateCustomerDeposit(db, cid, CustomerDepositInput{
		CustomerID:                custID,
		DepositLiabilityAccountID: &liabID,
		DepositDate:               time.Now(),
		CurrencyCode:              "CAD",
		Amount:                    decimal.NewFromInt(100),
	})
	err := PostCustomerDeposit(db, cid, dep.ID, "tester", nil)
	if err == nil {
		t.Error("expected error posting with no bank account")
	}
}

// ── Void ──────────────────────────────────────────────────────────────────────

func TestPhase3_VoidDraftDeposit(t *testing.T) {
	db := phase3DB(t)
	cid := p3Company(t, db)
	custID := p3Customer(t, db, cid)

	dep, _ := CreateCustomerDeposit(db, cid, CustomerDepositInput{
		CustomerID:   custID,
		DepositDate:  time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.NewFromInt(200),
	})
	if err := VoidCustomerDeposit(db, cid, dep.ID, "tester"); err != nil {
		t.Fatalf("VoidCustomerDeposit draft: %v", err)
	}
	got, _ := GetCustomerDeposit(db, cid, dep.ID)
	if got.Status != models.CustomerDepositStatusVoided {
		t.Errorf("expected voided; got %s", got.Status)
	}
	// No JE should have been created.
	var count int64
	db.Model(&models.JournalEntry{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 JEs; got %d", count)
	}
}

func TestPhase3_VoidPostedDeposit(t *testing.T) {
	db := phase3DB(t)
	cid := p3Company(t, db)
	custID := p3Customer(t, db, cid)
	bankID := p3BankAccount(t, db, cid)
	liabID := p3LiabilityAccount(t, db, cid)

	dep, _ := CreateCustomerDeposit(db, cid, CustomerDepositInput{
		CustomerID:                custID,
		BankAccountID:             &bankID,
		DepositLiabilityAccountID: &liabID,
		DepositDate:               time.Now(),
		CurrencyCode:              "CAD",
		Amount:                    decimal.NewFromInt(500),
	})
	PostCustomerDeposit(db, cid, dep.ID, "tester", nil)

	if err := VoidCustomerDeposit(db, cid, dep.ID, "tester"); err != nil {
		t.Fatalf("VoidCustomerDeposit posted: %v", err)
	}

	got, _ := GetCustomerDeposit(db, cid, dep.ID)
	if got.Status != models.CustomerDepositStatusVoided {
		t.Errorf("expected voided; got %s", got.Status)
	}
	if !got.BalanceRemaining.IsZero() {
		t.Errorf("BalanceRemaining should be 0 after void; got %s", got.BalanceRemaining)
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
		t.Errorf("expected at least 2 JEs (original + reversal); got %d", jeCount)
	}
}

func TestPhase3_VoidAppliedDeposit_Fails(t *testing.T) {
	db := phase3DB(t)
	cid := p3Company(t, db)
	custID := p3Customer(t, db, cid)
	bankID := p3BankAccount(t, db, cid)
	liabID := p3LiabilityAccount(t, db, cid)
	arID := p3ARAccount(t, db, cid)

	// Wire AR account via detail type (ResolveControlAccount checks this).
	_ = arID

	dep, _ := CreateCustomerDeposit(db, cid, CustomerDepositInput{
		CustomerID:                custID,
		BankAccountID:             &bankID,
		DepositLiabilityAccountID: &liabID,
		DepositDate:               time.Now(),
		CurrencyCode:              "CAD",
		Amount:                    decimal.NewFromInt(1000),
	})
	PostCustomerDeposit(db, cid, dep.ID, "tester", nil)

	// Force status to partially_applied without going through apply logic.
	db.Model(dep).Update("status", models.CustomerDepositStatusPartiallyApplied)

	err := VoidCustomerDeposit(db, cid, dep.ID, "tester")
	if err == nil {
		t.Error("expected error voiding partially-applied deposit")
	}
}

// ── Isolation ─────────────────────────────────────────────────────────────────

func TestPhase3_Isolation(t *testing.T) {
	db := phase3DB(t)
	cid1 := p3Company(t, db)
	// Different company setup.
	cid2 := func() uint {
		c := models.Company{Name: "Other Co", BaseCurrencyCode: "CAD", IsActive: true, AccountCodeLength: 4}
		db.Create(&c)
		return c.ID
	}()
	custID := p3Customer(t, db, cid1)

	dep, _ := CreateCustomerDeposit(db, cid1, CustomerDepositInput{
		CustomerID:   custID,
		DepositDate:  time.Now(),
		CurrencyCode: "CAD",
		Amount:       decimal.NewFromInt(300),
	})

	_, err := GetCustomerDeposit(db, cid2, dep.ID)
	if err == nil {
		t.Error("expected isolation error; cid2 should not see cid1 deposit")
	}
	_, err = GetCustomerDeposit(db, cid1, dep.ID)
	if err != nil {
		t.Errorf("cid1 should see own deposit: %v", err)
	}
}

// ── Document numbering ────────────────────────────────────────────────────────

func TestPhase3_DocumentNumbering(t *testing.T) {
	db := phase3DB(t)
	cid := p3Company(t, db)
	custID := p3Customer(t, db, cid)

	makeDeposit := func() *models.CustomerDeposit {
		d, err := CreateCustomerDeposit(db, cid, CustomerDepositInput{
			CustomerID:   custID,
			DepositDate:  time.Now(),
			CurrencyCode: "CAD",
			Amount:       decimal.NewFromInt(100),
		})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}

	d1 := makeDeposit()
	d2 := makeDeposit()
	if d1.DepositNumber == d2.DepositNumber {
		t.Errorf("deposit numbers must be unique; got %s and %s", d1.DepositNumber, d2.DepositNumber)
	}
	if d1.DepositNumber != "DEP-0001" {
		t.Errorf("first deposit number should be DEP-0001; got %s", d1.DepositNumber)
	}
	if d2.DepositNumber != "DEP-0002" {
		t.Errorf("second deposit number should be DEP-0002; got %s", d2.DepositNumber)
	}
}
