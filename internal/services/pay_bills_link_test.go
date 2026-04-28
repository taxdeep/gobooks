package services

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

func testPayBillsDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:pay_bills_link_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Vendor{},
		&models.Account{},
		&models.Bill{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.SettlementAllocation{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedPayBillsCompany(t *testing.T, db *gorm.DB, name string) uint {
	t.Helper()

	row := models.Company{
		Name:                    name,
		EntityType:              models.EntityTypeIncorporated,
		BusinessType:            models.BusinessTypeRetail,
		Industry:                models.IndustryRetail,
		IncorporatedDate:        "2024-01-01",
		FiscalYearEnd:           "12-31",
		BusinessNumber:          "123456789",
		AddressLine:             "123 Main",
		City:                    "Vancouver",
		Province:                "BC",
		PostalCode:              "V6B1A1",
		Country:                 "CA",
		AccountCodeLength:       4,
		AccountCodeLengthLocked: true,
		IsActive:                true,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row.ID
}

func seedPayBillsAccount(t *testing.T, db *gorm.DB, companyID uint, code string, root models.RootAccountType, detail models.DetailAccountType) uint {
	t.Helper()

	row := models.Account{
		CompanyID:         companyID,
		Code:              code,
		Name:              code,
		RootAccountType:   root,
		DetailAccountType: detail,
		IsActive:          true,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row.ID
}

func TestRecordPayBillsMarksLinkedBillPaid(t *testing.T) {
	db := testPayBillsDB(t)
	companyID := seedPayBillsCompany(t, db, "Acme")
	vendor := models.Vendor{CompanyID: companyID, Name: "Vendor A"}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatal(err)
	}

	bankID := seedPayBillsAccount(t, db, companyID, "1000", models.RootAsset, models.DetailBank)
	apID := seedPayBillsAccount(t, db, companyID, "2000", models.RootLiability, models.DetailAccountsPayable)
	bill := models.Bill{
		CompanyID:  companyID,
		BillNumber: "BILL001",
		VendorID:   vendor.ID,
		BillDate:   time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC),
		Status:     models.BillStatusPosted,
		Amount:     decimal.RequireFromString("100.00"),
		Subtotal:   decimal.RequireFromString("100.00"),
		TaxTotal:   decimal.Zero,
	}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatal(err)
	}

	jeID, err := RecordPayBills(db, PayBillsInput{
		CompanyID:     companyID,
		EntryDate:     time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC),
		BankAccountID: bankID,
		APAccountID:   apID,
		Bills:         []BillPayment{{BillID: bill.ID, Amount: decimal.RequireFromString("100.00")}},
		Memo:          "Payment",
	})
	if err != nil {
		t.Fatal(err)
	}
	if jeID == 0 {
		t.Fatal("expected journal entry id")
	}

	var updated models.Bill
	if err := db.First(&updated, bill.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != models.BillStatusPaid {
		t.Fatalf("expected bill to be paid, got %s", updated.Status)
	}
}
