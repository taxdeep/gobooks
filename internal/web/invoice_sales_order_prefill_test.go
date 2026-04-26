package web

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
	"gobooks/internal/web/templates/pages"
)

func testInvoiceSalesOrderPrefillDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.CustomerShippingAddress{},
		&models.Account{},
		&models.ProductService{},
		&models.SalesOrder{},
		&models.SalesOrderLine{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestPrefillInvoiceFromSalesOrderCarriesProductServiceLabel(t *testing.T) {
	db := testInvoiceSalesOrderPrefillDB(t)
	companyID := seedValidationCompany(t, db, "SO Invoice Prefill Co")
	customerID := seedValidationCustomer(t, db, companyID, "AR Customer")
	revenueID := seedValidationAccount(t, db, companyID, "4000", models.RootRevenue, models.DetailServiceRevenue)
	itemID := seedValidationProduct(t, db, companyID, revenueID, "Implementation Service")

	so := models.SalesOrder{
		CompanyID:    companyID,
		CustomerID:   customerID,
		OrderNumber:  "SO-ITEM-1",
		Status:       models.SalesOrderStatusConfirmed,
		OrderDate:    time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
		CurrencyCode: "CAD",
	}
	if err := db.Create(&so).Error; err != nil {
		t.Fatal(err)
	}
	line := models.SalesOrderLine{
		SalesOrderID:     so.ID,
		ProductServiceID: &itemID,
		Description:      "Implementation work",
		Quantity:         decimal.NewFromInt(3),
		UnitPrice:        decimal.NewFromInt(100),
	}
	if err := db.Create(&line).Error; err != nil {
		t.Fatal(err)
	}

	vm := pages.InvoiceEditorVM{}
	server := &Server{DB: db}
	server.prefillInvoiceFromSalesOrder(companyID, so.ID, &vm)

	if len(vm.Lines) != 1 {
		t.Fatalf("expected 1 prefilled invoice line; got %d", len(vm.Lines))
	}
	if vm.Lines[0].ProductServiceID != fmt.Sprintf("%d", itemID) {
		t.Fatalf("ProductServiceID = %q, want %d", vm.Lines[0].ProductServiceID, itemID)
	}
	if vm.Lines[0].ProductServiceLabel != "Implementation Service" {
		t.Fatalf("ProductServiceLabel = %q", vm.Lines[0].ProductServiceLabel)
	}
	if vm.Lines[0].Description != "Implementation work" {
		t.Fatalf("Description = %q", vm.Lines[0].Description)
	}
}
