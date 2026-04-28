package db

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

func testDocumentIndexDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:doc_index_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Company{}, &models.Customer{}, &models.Vendor{}, &models.Invoice{}, &models.Bill{}); err != nil {
		t.Fatal(err)
	}
	if err := ensureDocumentNumberIndexes(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedDocumentIndexCompany(t *testing.T, db *gorm.DB, name string) uint {
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

func TestEnsureDocumentNumberIndexesRejectsCaseInsensitiveInvoiceDuplicates(t *testing.T) {
	db := testDocumentIndexDB(t)
	companyID := seedDocumentIndexCompany(t, db, "Acme")
	customer := models.Customer{CompanyID: companyID, Name: "Customer A"}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatal(err)
	}

	first := models.Invoice{
		CompanyID:     companyID,
		InvoiceNumber: "IN001",
		CustomerID:    customer.ID,
		InvoiceDate:   time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC),
		Amount:        decimal.RequireFromString("100.00"),
	}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}

	second := models.Invoice{
		CompanyID:     companyID,
		InvoiceNumber: "in001",
		CustomerID:    customer.ID,
		InvoiceDate:   time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC),
		Amount:        decimal.RequireFromString("100.00"),
	}
	if err := db.Create(&second).Error; err == nil {
		t.Fatal("expected case-insensitive invoice duplicate to fail")
	}
}

func TestEnsureDocumentNumberIndexesRejectsCaseInsensitiveBillDuplicatesPerVendor(t *testing.T) {
	db := testDocumentIndexDB(t)
	companyID := seedDocumentIndexCompany(t, db, "Acme")
	vendor := models.Vendor{CompanyID: companyID, Name: "Vendor A"}
	if err := db.Create(&vendor).Error; err != nil {
		t.Fatal(err)
	}

	first := models.Bill{
		CompanyID:  companyID,
		BillNumber: "BILL001",
		VendorID:   vendor.ID,
		BillDate:   time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC),
		Amount:     decimal.RequireFromString("100.00"),
	}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}

	second := models.Bill{
		CompanyID:  companyID,
		BillNumber: "bill001",
		VendorID:   vendor.ID,
		BillDate:   time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC),
		Amount:     decimal.RequireFromString("100.00"),
	}
	if err := db.Create(&second).Error; err == nil {
		t.Fatal("expected case-insensitive bill duplicate to fail")
	}
}
