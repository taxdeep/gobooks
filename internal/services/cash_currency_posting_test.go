// 遵循project_guide.md
package services

import (
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

func cashCurrencyPostingDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Account{},
		&models.Currency{},
		&models.CompanyCurrency{},
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

func seedCashPostingCompany(t *testing.T, db *gorm.DB, multi bool) uint {
	t.Helper()
	co := models.Company{
		Name:                 "Cash FX Co",
		BaseCurrencyCode:     "CAD",
		MultiCurrencyEnabled: multi,
		AccountCodeLength:    4,
		IsActive:             true,
	}
	if err := db.Create(&co).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Currency{Code: "USD", Name: "US Dollar", IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	if multi {
		if err := db.Create(&models.CompanyCurrency{CompanyID: co.ID, CurrencyCode: "USD", IsActive: true}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return co.ID
}

func seedCashPostingCustomer(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	c := models.Customer{CompanyID: companyID, Name: "FX Customer"}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func seedCashPostingUSDAccount(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	usd := "USD"
	acc := models.Account{
		CompanyID:         companyID,
		Code:              "1020",
		Name:              "USD Cash",
		RootAccountType:   models.RootAsset,
		DetailAccountType: models.DetailBank,
		CurrencyMode:      models.CurrencyModeFixedForeign,
		CurrencyCode:      &usd,
		IsActive:          true,
	}
	if err := db.Create(&acc).Error; err != nil {
		t.Fatal(err)
	}
	return acc.ID
}

func seedCashPostingDepositLiability(t *testing.T, db *gorm.DB, companyID uint) uint {
	t.Helper()
	acc := models.Account{
		CompanyID:         companyID,
		Code:              "2100",
		Name:              "Customer Deposits",
		RootAccountType:   models.RootLiability,
		DetailAccountType: models.DetailOtherCurrentLiability,
		IsActive:          true,
	}
	if err := db.Create(&acc).Error; err != nil {
		t.Fatal(err)
	}
	return acc.ID
}

func TestCustomerDepositForeignCashWritesTransactionAndBaseAmounts(t *testing.T) {
	db := cashCurrencyPostingDB(t)
	companyID := seedCashPostingCompany(t, db, true)
	customerID := seedCashPostingCustomer(t, db, companyID)
	bankID := seedCashPostingUSDAccount(t, db, companyID)
	liabilityID := seedCashPostingDepositLiability(t, db, companyID)

	dep, err := CreateCustomerDeposit(db, companyID, CustomerDepositInput{
		CustomerID:                customerID,
		BankAccountID:             &bankID,
		DepositLiabilityAccountID: &liabilityID,
		DepositDate:               time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC),
		CurrencyCode:              "USD",
		ExchangeRate:              decimal.RequireFromString("1.30000000"),
		Amount:                    decimal.NewFromInt(100),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := PostCustomerDeposit(db, companyID, dep.ID, "tester", nil); err != nil {
		t.Fatalf("post deposit: %v", err)
	}

	var posted models.CustomerDeposit
	if err := db.First(&posted, dep.ID).Error; err != nil {
		t.Fatal(err)
	}
	if posted.JournalEntryID == nil {
		t.Fatal("posted deposit has no journal entry")
	}
	var je models.JournalEntry
	if err := db.Preload("Lines").First(&je, *posted.JournalEntryID).Error; err != nil {
		t.Fatal(err)
	}
	if je.TransactionCurrencyCode != "USD" {
		t.Fatalf("transaction currency: want USD, got %q", je.TransactionCurrencyCode)
	}
	if !je.ExchangeRate.Equal(decimal.RequireFromString("1.30000000")) {
		t.Fatalf("exchange rate: want 1.30000000, got %s", je.ExchangeRate)
	}

	var bankLine, liabilityLine models.JournalLine
	for _, line := range je.Lines {
		switch line.AccountID {
		case bankID:
			bankLine = line
		case liabilityID:
			liabilityLine = line
		}
	}
	if !bankLine.TxDebit.Equal(decimal.NewFromInt(100)) || !bankLine.Debit.Equal(decimal.NewFromInt(130)) {
		t.Fatalf("bank line amounts: tx=%s base=%s", bankLine.TxDebit, bankLine.Debit)
	}
	if !liabilityLine.TxCredit.Equal(decimal.NewFromInt(100)) || !liabilityLine.Credit.Equal(decimal.NewFromInt(130)) {
		t.Fatalf("liability line amounts: tx=%s base=%s", liabilityLine.TxCredit, liabilityLine.Credit)
	}
}

func TestCustomerDepositForeignCashRequiresMultiCurrency(t *testing.T) {
	db := cashCurrencyPostingDB(t)
	companyID := seedCashPostingCompany(t, db, false)
	customerID := seedCashPostingCustomer(t, db, companyID)
	bankID := seedCashPostingUSDAccount(t, db, companyID)
	liabilityID := seedCashPostingDepositLiability(t, db, companyID)

	dep, err := CreateCustomerDeposit(db, companyID, CustomerDepositInput{
		CustomerID:                customerID,
		BankAccountID:             &bankID,
		DepositLiabilityAccountID: &liabilityID,
		DepositDate:               time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC),
		CurrencyCode:              "USD",
		ExchangeRate:              decimal.RequireFromString("1.30000000"),
		Amount:                    decimal.NewFromInt(100),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = PostCustomerDeposit(db, companyID, dep.ID, "tester", nil)
	if err == nil || !strings.Contains(err.Error(), "multi-currency is not enabled") {
		t.Fatalf("expected multi-currency guard, got %v", err)
	}

	var count int64
	if err := db.Model(&models.JournalEntry{}).Where("company_id = ?", companyID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected no journal entry after rejected foreign cash post, got %d", count)
	}
}
