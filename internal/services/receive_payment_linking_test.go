package services

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

func testReceivePaymentDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:receive_payment_link_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Account{},
		&models.Invoice{},
		&models.JournalEntry{},
		&models.JournalLine{},
		&models.LedgerEntry{},
		&models.SettlementAllocation{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedReceivePaymentCompany(t *testing.T, db *gorm.DB, name string) uint {
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

func seedReceivePaymentAccount(t *testing.T, db *gorm.DB, companyID uint, code string, root models.RootAccountType, detail models.DetailAccountType) uint {
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

func TestRecordReceivePaymentShowsActionableMessageForPartialLinkedInvoice(t *testing.T) {
	db := testReceivePaymentDB(t)
	companyID := seedReceivePaymentCompany(t, db, "Acme")
	customer := models.Customer{CompanyID: companyID, Name: "Customer A"}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatal(err)
	}

	bankID := seedReceivePaymentAccount(t, db, companyID, "1000", models.RootAsset, models.DetailBank)
	arID := seedReceivePaymentAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)
	invoice := models.Invoice{
		CompanyID:     companyID,
		InvoiceNumber: "IN001",
		CustomerID:    customer.ID,
		InvoiceDate:   time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC),
		Status:        models.InvoiceStatusSent,
		Amount:        decimal.RequireFromString("100.00"),
		Subtotal:      decimal.RequireFromString("100.00"),
		TaxTotal:      decimal.Zero,
		BalanceDue:    decimal.RequireFromString("100.00"),
	}
	if err := db.Create(&invoice).Error; err != nil {
		t.Fatal(err)
	}

	_, err := RecordReceivePayment(db, ReceivePaymentInput{
		CompanyID:     companyID,
		CustomerID:    customer.ID,
		EntryDate:     time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC),
		BankAccountID: bankID,
		ARAccountID:   arID,
		InvoiceID:     &invoice.ID,
		Amount:        decimal.RequireFromString("50.00"),
	})
	if err == nil {
		t.Fatal("expected linked partial payment to be rejected")
	}
	if !strings.Contains(err.Error(), "leave the invoice blank") {
		t.Fatalf("expected actionable message, got %v", err)
	}
	if !strings.Contains(err.Error(), "remaining balance due (100.00)") {
		t.Fatalf("expected remaining balance in message, got %v", err)
	}
}

func TestRecordReceivePaymentAllowsFullSettlementForOverdueInvoiceBalance(t *testing.T) {
	db := testReceivePaymentDB(t)
	companyID := seedReceivePaymentCompany(t, db, "Acme")
	customer := models.Customer{CompanyID: companyID, Name: "Customer A"}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatal(err)
	}

	bankID := seedReceivePaymentAccount(t, db, companyID, "1000", models.RootAsset, models.DetailBank)
	arID := seedReceivePaymentAccount(t, db, companyID, "1100", models.RootAsset, models.DetailAccountsReceivable)
	invoice := models.Invoice{
		CompanyID:     companyID,
		InvoiceNumber: "IN002",
		CustomerID:    customer.ID,
		InvoiceDate:   time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Status:        models.InvoiceStatusOverdue,
		Amount:        decimal.RequireFromString("100.00"),
		Subtotal:      decimal.RequireFromString("100.00"),
		TaxTotal:      decimal.Zero,
		BalanceDue:    decimal.RequireFromString("25.00"),
	}
	if err := db.Create(&invoice).Error; err != nil {
		t.Fatal(err)
	}

	jeID, err := RecordReceivePayment(db, ReceivePaymentInput{
		CompanyID:     companyID,
		CustomerID:    customer.ID,
		EntryDate:     time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC),
		BankAccountID: bankID,
		ARAccountID:   arID,
		InvoiceID:     &invoice.ID,
		Amount:        decimal.RequireFromString("25.00"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if jeID == 0 {
		t.Fatal("expected journal entry id")
	}

	var updated models.Invoice
	if err := db.First(&updated, invoice.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != models.InvoiceStatusPaid {
		t.Fatalf("expected invoice to be paid, got %s", updated.Status)
	}
	if !updated.BalanceDue.IsZero() {
		t.Fatalf("expected balance due to be zero, got %s", updated.BalanceDue)
	}
}
