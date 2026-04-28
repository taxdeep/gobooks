// 遵循project_guide.md
package services

// inventory_posting_insufficient_test.go — locks the operator-
// facing message when an invoice post tries to issue more stock
// than is on hand.

import (
	"errors"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

func insufficientStockTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&id="+uniqueTestTag()),
		&gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.ProductService{},
		&models.Account{},
		&models.Warehouse{},
		&models.InventoryBalance{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestWrapInsufficientStockErr_IncludesProductNameAndOnHand(t *testing.T) {
	db := insufficientStockTestDB(t)

	co := models.Company{Name: "StockErr Co", BaseCurrencyCode: "CAD", IsActive: true}
	if err := db.Create(&co).Error; err != nil {
		t.Fatal(err)
	}
	rev := models.Account{CompanyID: co.ID, Code: "4000", Name: "Rev",
		RootAccountType: models.RootRevenue, DetailAccountType: "sales_revenue", IsActive: true}
	db.Create(&rev)
	item := models.ProductService{CompanyID: co.ID, Name: "Computer 1",
		Type: models.ProductServiceTypeInventory, RevenueAccountID: rev.ID, IsActive: true}
	item.ApplyTypeDefaults()
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	// On-hand = 5 (at NULL warehouse = legacy path).
	bal := models.InventoryBalance{
		CompanyID:      co.ID,
		ItemID:         item.ID,
		LocationType:   models.LocationTypeInternal,
		QuantityOnHand: decimal.NewFromInt(5),
		AverageCost:    decimal.NewFromInt(10),
	}
	db.Create(&bal)

	// Requested 8, available 5.
	err := wrapInsufficientStockErr(db, co.ID, item.ID, nil, decimal.NewFromInt(8))
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, ErrInsufficientStock) {
		t.Fatalf("error should wrap ErrInsufficientStock, got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{
		"Computer 1",     // product name
		"5",              // on-hand
		"8",              // requested
		"on hand",        // template phrase
		"not enough stock", // operator cue
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %s", want, msg)
		}
	}
}

func TestWrapInsufficientStockErr_MissingProduct_FallsBackToID(t *testing.T) {
	db := insufficientStockTestDB(t)
	co := models.Company{Name: "StockErr Co 2", BaseCurrencyCode: "CAD", IsActive: true}
	db.Create(&co)
	// No product row seeded — wrap should fall back to "item #<id>".
	err := wrapInsufficientStockErr(db, co.ID, 99999, nil, decimal.NewFromInt(3))
	if err == nil || !errors.Is(err, ErrInsufficientStock) {
		t.Fatalf("expected ErrInsufficientStock wrap, got %v", err)
	}
	if !strings.Contains(err.Error(), "item #99999") {
		t.Errorf("expected fallback 'item #99999' in %q", err.Error())
	}
}

func TestWrapInsufficientStockErr_MissingBalance_FallsBackToZero(t *testing.T) {
	db := insufficientStockTestDB(t)
	co := models.Company{Name: "StockErr Co 3", BaseCurrencyCode: "CAD", IsActive: true}
	db.Create(&co)
	rev := models.Account{CompanyID: co.ID, Code: "4000", Name: "Rev",
		RootAccountType: models.RootRevenue, DetailAccountType: "sales_revenue", IsActive: true}
	db.Create(&rev)
	item := models.ProductService{CompanyID: co.ID, Name: "Phantom Widget",
		Type: models.ProductServiceTypeInventory, RevenueAccountID: rev.ID, IsActive: true}
	item.ApplyTypeDefaults()
	db.Create(&item)
	// No balance row — on-hand falls back to zero.
	err := wrapInsufficientStockErr(db, co.ID, item.ID, nil, decimal.NewFromInt(3))
	if err == nil || !errors.Is(err, ErrInsufficientStock) {
		t.Fatalf("expected ErrInsufficientStock wrap, got %v", err)
	}
	if !strings.Contains(err.Error(), "Phantom Widget") {
		t.Errorf("product name missing: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "0 on hand") {
		t.Errorf("zero fallback missing: %s", err.Error())
	}
}
