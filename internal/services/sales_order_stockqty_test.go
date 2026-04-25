// 遵循project_guide.md
package services

import (
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// sales_order_stockqty_test.go — locks the integer-quantity rule for
// stock-tracked inventory items on SO line entry. Service / non-inventory /
// other-charge items continue to accept fractional qty so consulting hours
// (1.5h) still work.

func stockQtyDB(t *testing.T) *gorm.DB {
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
		&models.SalesOrder{},
		&models.SalesOrderLine{},
		&models.NumberingSetting{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

type stockQtyFixture struct {
	CompanyID    uint
	CustomerID   uint
	StockItemID  uint
	ServiceItemID uint
}

func seedStockQtyFixture(t *testing.T, db *gorm.DB) stockQtyFixture {
	t.Helper()
	co := models.Company{Name: "Stock Qty Co", BaseCurrencyCode: "CAD", IsActive: true, AccountCodeLength: 4}
	if err := db.Create(&co).Error; err != nil {
		t.Fatal(err)
	}
	cust := models.Customer{CompanyID: co.ID, Name: "Cust"}
	if err := db.Create(&cust).Error; err != nil {
		t.Fatal(err)
	}
	rev := models.Account{
		CompanyID: co.ID, Code: "4000", Name: "Rev",
		RootAccountType: models.RootRevenue, DetailAccountType: "sales_revenue", IsActive: true,
	}
	if err := db.Create(&rev).Error; err != nil {
		t.Fatal(err)
	}
	stock := models.ProductService{
		CompanyID:        co.ID,
		Name:             "Watermelon",
		Type:             models.ProductServiceTypeInventory,
		RevenueAccountID: rev.ID,
		IsActive:         true,
	}
	stock.ApplyTypeDefaults()
	if err := db.Create(&stock).Error; err != nil {
		t.Fatal(err)
	}
	svc := models.ProductService{
		CompanyID:        co.ID,
		Name:             "Consulting",
		Type:             models.ProductServiceTypeService,
		RevenueAccountID: rev.ID,
		IsActive:         true,
	}
	svc.ApplyTypeDefaults()
	if err := db.Create(&svc).Error; err != nil {
		t.Fatal(err)
	}
	return stockQtyFixture{
		CompanyID:     co.ID,
		CustomerID:    cust.ID,
		StockItemID:   stock.ID,
		ServiceItemID: svc.ID,
	}
}

// TestCreateSalesOrder_RejectsFractionalStockItemQty locks the create-time
// guard: a stock-tracked inventory line must use whole-unit qty. 8.5 of a
// stock item makes no sense — slicing one watermelon into pieces is a BOM
// concern, not a line-item concern.
func TestCreateSalesOrder_RejectsFractionalStockItemQty(t *testing.T) {
	db := stockQtyDB(t)
	f := seedStockQtyFixture(t, db)

	_, err := CreateSalesOrder(db, f.CompanyID, SalesOrderInput{
		CustomerID: f.CustomerID,
		OrderDate:  time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
		Lines: []SalesOrderLineInput{
			{
				ProductServiceID: &f.StockItemID,
				Description:      "Watermelon",
				Quantity:         decimal.RequireFromString("8.5"),
				UnitPrice:        decimal.RequireFromString("10"),
			},
		},
	})
	if err == nil {
		t.Fatal("expected error for fractional stock item qty, got nil")
	}
	if !strings.Contains(err.Error(), "whole-unit quantities") {
		t.Errorf("error = %v, want guidance about whole-unit quantities", err)
	}
}

// TestCreateSalesOrder_AcceptsWholeStockQty confirms the guard doesn't
// false-positive on the legal whole-unit case.
func TestCreateSalesOrder_AcceptsWholeStockQty(t *testing.T) {
	db := stockQtyDB(t)
	f := seedStockQtyFixture(t, db)

	so, err := CreateSalesOrder(db, f.CompanyID, SalesOrderInput{
		CustomerID: f.CustomerID,
		OrderDate:  time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
		Lines: []SalesOrderLineInput{
			{
				ProductServiceID: &f.StockItemID,
				Description:      "Watermelon",
				Quantity:         decimal.NewFromInt(8),
				UnitPrice:        decimal.RequireFromString("10"),
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateSalesOrder: %v", err)
	}
	if so.ID == 0 {
		t.Error("expected SO to be created")
	}
}

// TestCreateSalesOrder_AcceptsFractionalServiceQty locks the negative case:
// a service item (not stock) must still accept 1.5h of consulting. The
// guard is scoped to IsStockItem, not all line items.
func TestCreateSalesOrder_AcceptsFractionalServiceQty(t *testing.T) {
	db := stockQtyDB(t)
	f := seedStockQtyFixture(t, db)

	so, err := CreateSalesOrder(db, f.CompanyID, SalesOrderInput{
		CustomerID: f.CustomerID,
		OrderDate:  time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
		Lines: []SalesOrderLineInput{
			{
				ProductServiceID: &f.ServiceItemID,
				Description:      "Consulting",
				Quantity:         decimal.RequireFromString("1.5"),
				UnitPrice:        decimal.RequireFromString("200"),
			},
		},
	})
	if err != nil {
		t.Fatalf("service item with fractional qty should succeed: %v", err)
	}
	if so.ID == 0 {
		t.Error("expected SO to be created")
	}
}

// TestUpdateSalesOrder_RejectsFractionalStockItemQty mirrors the create
// guard on the update path — the operator can't sneak a 8.5 in via Edit.
func TestUpdateSalesOrder_RejectsFractionalStockItemQty(t *testing.T) {
	db := stockQtyDB(t)
	f := seedStockQtyFixture(t, db)

	so, err := CreateSalesOrder(db, f.CompanyID, SalesOrderInput{
		CustomerID: f.CustomerID,
		OrderDate:  time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
		Lines: []SalesOrderLineInput{
			{
				ProductServiceID: &f.StockItemID,
				Description:      "Watermelon",
				Quantity:         decimal.NewFromInt(8),
				UnitPrice:        decimal.RequireFromString("10"),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = UpdateSalesOrder(db, f.CompanyID, so.ID, SalesOrderInput{
		CustomerID: f.CustomerID,
		OrderDate:  time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
		Lines: []SalesOrderLineInput{
			{
				ProductServiceID: &f.StockItemID,
				Description:      "Watermelon",
				Quantity:         decimal.RequireFromString("8.25"),
				UnitPrice:        decimal.RequireFromString("10"),
			},
		},
	})
	if err == nil {
		t.Fatal("expected error for fractional stock item qty on update, got nil")
	}
	if !strings.Contains(err.Error(), "whole-unit quantities") {
		t.Errorf("error = %v, want guidance about whole-unit quantities", err)
	}
}
