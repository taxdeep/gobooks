// 遵循project_guide.md
package services

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// customer_deposit_post_test.go — locks the auto-routing behaviour added
// 2026-04-24: PostCustomerDeposit resolves the system Customer Deposits
// liability account when the operator didn't pick one, and writes the
// resolved id back onto the deposit row.

func testDepositPostDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:dep_post_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Account{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.CustomerDeposit{},
		&models.CustomerDepositApplication{},
		&models.NumberingSetting{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestPostCustomerDeposit_AutoResolvesSystemAccount confirms the manual
// flow doesn't need the operator to pick a liability account — the
// system Customer Deposits account is created on first post and reused
// thereafter.
func TestPostCustomerDeposit_AutoResolvesSystemAccount(t *testing.T) {
	db := testDepositPostDB(t)
	c := models.Company{Name: "Auto Liab Co", AccountCodeLength: 4, IsActive: true, BaseCurrencyCode: "CAD"}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	cust := models.Customer{CompanyID: c.ID, Name: "Auto Liab Cust"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}
	bank := models.Account{
		CompanyID: c.ID, Code: "1000", Name: "Bank",
		RootAccountType: models.RootAsset, DetailAccountType: models.DetailBank, IsActive: true,
	}
	if err := db.Create(&bank).Error; err != nil {
		t.Fatal(err)
	}

	dep, err := CreateCustomerDeposit(db, c.ID, CustomerDepositInput{
		CustomerID:    cust.ID,
		BankAccountID: &bank.ID,
		// DepositLiabilityAccountID intentionally nil — the new
		// design auto-routes to the system account at post time.
		DepositDate: time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC),
		Amount:      decimal.RequireFromString("250.00"),
	})
	if err != nil {
		t.Fatalf("CreateCustomerDeposit: %v", err)
	}
	if dep.DepositNumber != "DEP0001" {
		t.Errorf("dep.DepositNumber = %q, want DEP0001", dep.DepositNumber)
	}
	if dep.Source != models.DepositSourceManual {
		t.Errorf("dep.Source = %q, want manual", dep.Source)
	}

	// Post — should auto-resolve liability account, write JE, and
	// update the deposit row's DepositLiabilityAccountID.
	if err := PostCustomerDeposit(db, c.ID, dep.ID, "actor@test", nil); err != nil {
		t.Fatalf("PostCustomerDeposit: %v", err)
	}

	var reloaded models.CustomerDeposit
	if err := db.First(&reloaded, dep.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != models.CustomerDepositStatusPosted {
		t.Errorf("status = %q, want posted", reloaded.Status)
	}
	if reloaded.DepositLiabilityAccountID == nil || *reloaded.DepositLiabilityAccountID == 0 {
		t.Fatal("DepositLiabilityAccountID was not back-filled after post")
	}

	// The auto-resolved account must be the system Customer Deposits row.
	var custDepAcc models.Account
	if err := db.Where("company_id = ? AND system_key = ?", c.ID, "customer_deposits").First(&custDepAcc).Error; err != nil {
		t.Fatalf("system Customer Deposits account not created: %v", err)
	}
	if *reloaded.DepositLiabilityAccountID != custDepAcc.ID {
		t.Errorf("liability id = %d, want system account id %d",
			*reloaded.DepositLiabilityAccountID, custDepAcc.ID)
	}

	// JE: Bank DR 250 / Customer Deposits CR 250.
	var bankDebit, custDepCredit decimal.Decimal
	db.Model(&models.JournalLine{}).
		Where("company_id = ? AND account_id = ?", c.ID, bank.ID).
		Select("COALESCE(SUM(debit),0)").
		Scan(&bankDebit)
	db.Model(&models.JournalLine{}).
		Where("company_id = ? AND account_id = ?", c.ID, custDepAcc.ID).
		Select("COALESCE(SUM(credit),0)").
		Scan(&custDepCredit)
	want := decimal.RequireFromString("250.00")
	if !bankDebit.Equal(want) {
		t.Errorf("bank DR = %s, want %s", bankDebit, want)
	}
	if !custDepCredit.Equal(want) {
		t.Errorf("Customer Deposits CR = %s, want %s", custDepCredit, want)
	}

	// Second deposit reuses the same system account (no duplicate
	// liability account per company).
	dep2, err := CreateCustomerDeposit(db, c.ID, CustomerDepositInput{
		CustomerID:    cust.ID,
		BankAccountID: &bank.ID,
		DepositDate:   time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		Amount:        decimal.RequireFromString("80.00"),
	})
	if err != nil {
		t.Fatalf("CreateCustomerDeposit (2nd): %v", err)
	}
	if dep2.DepositNumber != "DEP0002" {
		t.Errorf("second dep number = %q, want DEP0002 (numbering must persist)", dep2.DepositNumber)
	}
	if err := PostCustomerDeposit(db, c.ID, dep2.ID, "actor@test", nil); err != nil {
		t.Fatalf("PostCustomerDeposit (2nd): %v", err)
	}
	var count int64
	db.Model(&models.Account{}).
		Where("company_id = ? AND system_key = ?", c.ID, "customer_deposits").
		Count(&count)
	if count != 1 {
		t.Errorf("expected exactly 1 Customer Deposits account, got %d", count)
	}
}
