// 遵循project_guide.md
package services

import (
	"errors"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// product_service_uom_test.go — locks the U1 service contracts:
//   - ChangeStockUOM is allowed only when on-hand == 0; resets sell +
//     purchase UOMs to match the new stock unit; writes an audit row.
//   - SaveProductUOMs runs the model validator; rejects inconsistent
//     factors; writes an audit row.

func uomServiceDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Account{},
		&models.ProductService{},
		&models.InventoryBalance{},
		&models.InventoryCostLayer{},
		&models.AuditLog{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

type uomFixture struct {
	CompanyID   uint
	StockItemID uint
}

func seedUOMFixture(t *testing.T, db *gorm.DB) uomFixture {
	t.Helper()
	co := models.Company{Name: "UOM Co", BaseCurrencyCode: "CAD", IsActive: true, AccountCodeLength: 4}
	if err := db.Create(&co).Error; err != nil {
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
		CompanyID: co.ID, Name: "Bottle",
		Type: models.ProductServiceTypeInventory, RevenueAccountID: rev.ID, IsActive: true,
	}
	stock.ApplyTypeDefaults()
	if err := db.Create(&stock).Error; err != nil {
		t.Fatal(err)
	}
	return uomFixture{CompanyID: co.ID, StockItemID: stock.ID}
}

// ── SaveProductUOMs ─────────────────────────────────────────────────────────

func TestSaveProductUOMs_HappyPath(t *testing.T) {
	db := uomServiceDB(t)
	f := seedUOMFixture(t, db)

	if err := SaveProductUOMs(db, SaveProductUOMsInput{
		CompanyID:         f.CompanyID,
		ItemID:            f.StockItemID,
		SellUOM:           "BOTTLE",
		SellUOMFactor:     decimal.NewFromInt(1),
		PurchaseUOM:       "CASE",
		PurchaseUOMFactor: decimal.NewFromInt(24),
		Actor:             "ops@example.com",
	}); err != nil {
		t.Fatalf("SaveProductUOMs: %v", err)
	}

	var reloaded models.ProductService
	if err := db.First(&reloaded, f.StockItemID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.PurchaseUOM != "CASE" || !reloaded.PurchaseUOMFactor.Equal(decimal.NewFromInt(24)) {
		t.Errorf("update not persisted: %+v", reloaded)
	}

	var audit []models.AuditLog
	db.Where("action = ?", "product_service.uom.saved").Find(&audit)
	if len(audit) != 1 {
		t.Fatalf("expected 1 audit row, got %d", len(audit))
	}
}

func TestSaveProductUOMs_ValidatorRejects(t *testing.T) {
	db := uomServiceDB(t)
	f := seedUOMFixture(t, db)
	// Stock unit defaults to "EA" (from ApplyTypeDefaults). Try
	// "sell EA with factor 5" — must reject (factor != 1 when ==stock).
	err := SaveProductUOMs(db, SaveProductUOMsInput{
		CompanyID:         f.CompanyID,
		ItemID:            f.StockItemID,
		SellUOM:           "EA",
		SellUOMFactor:     decimal.NewFromInt(5),
		PurchaseUOM:       "EA",
		PurchaseUOMFactor: decimal.NewFromInt(1),
		Actor:             "ops",
	})
	if err == nil {
		t.Fatal("expected validator rejection, got nil")
	}
	if !strings.Contains(err.Error(), "sell UOM equals stock UOM") {
		t.Errorf("error = %v, want sell-equals-stock guidance", err)
	}
}

// ── ChangeStockUOM ──────────────────────────────────────────────────────────

func TestChangeStockUOM_HappyPath_ResetsFactorsAndAudits(t *testing.T) {
	db := uomServiceDB(t)
	f := seedUOMFixture(t, db)
	// Pre-set sell/purchase to non-defaults so we can verify the reset.
	db.Model(&models.ProductService{}).Where("id = ?", f.StockItemID).Updates(map[string]any{
		"sell_uom":            "PACK_6",
		"sell_uom_factor":     decimal.NewFromInt(6),
		"purchase_uom":        "CASE",
		"purchase_uom_factor": decimal.NewFromInt(24),
	})

	if err := ChangeStockUOM(db, ChangeStockUOMInput{
		CompanyID:   f.CompanyID,
		ItemID:      f.StockItemID,
		NewStockUOM: "BOTTLE",
		Actor:       "ops@example.com",
	}); err != nil {
		t.Fatalf("ChangeStockUOM: %v", err)
	}

	var reloaded models.ProductService
	db.First(&reloaded, f.StockItemID)
	if reloaded.StockUOM != "BOTTLE" {
		t.Errorf("StockUOM = %q, want BOTTLE", reloaded.StockUOM)
	}
	// Sell + Purchase reset to match the new stock unit (factor 1).
	if reloaded.SellUOM != "BOTTLE" || !reloaded.SellUOMFactor.Equal(decimal.NewFromInt(1)) {
		t.Errorf("Sell not reset: %s × %s", reloaded.SellUOM, reloaded.SellUOMFactor)
	}
	if reloaded.PurchaseUOM != "BOTTLE" || !reloaded.PurchaseUOMFactor.Equal(decimal.NewFromInt(1)) {
		t.Errorf("Purchase not reset: %s × %s", reloaded.PurchaseUOM, reloaded.PurchaseUOMFactor)
	}

	var audit []models.AuditLog
	db.Where("action = ?", "product_service.stock_uom.changed").Find(&audit)
	if len(audit) != 1 {
		t.Fatalf("expected 1 audit row, got %d", len(audit))
	}
}

func TestChangeStockUOM_RejectsWhenOnHandExists(t *testing.T) {
	db := uomServiceDB(t)
	f := seedUOMFixture(t, db)
	// Seed an on-hand row so the guard fires.
	if err := db.Create(&models.InventoryBalance{
		CompanyID: f.CompanyID, ItemID: f.StockItemID,
		QuantityOnHand: decimal.NewFromInt(10),
	}).Error; err != nil {
		t.Fatal(err)
	}

	err := ChangeStockUOM(db, ChangeStockUOMInput{
		CompanyID:   f.CompanyID,
		ItemID:      f.StockItemID,
		NewStockUOM: "BOTTLE",
		Actor:       "ops",
	})
	if err == nil {
		t.Fatal("expected on-hand guard rejection, got nil")
	}
	if !errors.Is(err, ErrStockUOMHasStock) {
		t.Errorf("expected ErrStockUOMHasStock, got %v", err)
	}
}

func TestChangeStockUOM_RejectsNonStockItem(t *testing.T) {
	db := uomServiceDB(t)
	co := models.Company{Name: "Co", BaseCurrencyCode: "CAD", IsActive: true, AccountCodeLength: 4}
	db.Create(&co)
	rev := models.Account{
		CompanyID: co.ID, Code: "4000", Name: "Rev",
		RootAccountType: models.RootRevenue, DetailAccountType: "sales_revenue", IsActive: true,
	}
	db.Create(&rev)
	svc := models.ProductService{
		CompanyID: co.ID, Name: "Consulting",
		Type: models.ProductServiceTypeService, RevenueAccountID: rev.ID, IsActive: true,
	}
	svc.ApplyTypeDefaults()
	db.Create(&svc)

	err := ChangeStockUOM(db, ChangeStockUOMInput{
		CompanyID:   co.ID,
		ItemID:      svc.ID,
		NewStockUOM: "HOUR",
		Actor:       "ops",
	})
	if err == nil {
		t.Fatal("expected non-stock rejection, got nil")
	}
	if !errors.Is(err, ErrStockUOMNotStockItem) {
		t.Errorf("expected ErrStockUOMNotStockItem, got %v", err)
	}
}

// TestChangeStockUOM_NoOpWhenSame — calling with the existing UOM
// returns nil without touching anything (avoids spurious audit row).
func TestChangeStockUOM_NoOpWhenSame(t *testing.T) {
	db := uomServiceDB(t)
	f := seedUOMFixture(t, db)
	// Default StockUOM is "EA" from ApplyTypeDefaults.
	if err := ChangeStockUOM(db, ChangeStockUOMInput{
		CompanyID: f.CompanyID, ItemID: f.StockItemID, NewStockUOM: "EA",
		Actor: "ops",
	}); err != nil {
		t.Fatalf("expected no-op, got %v", err)
	}
	var n int64
	db.Model(&models.AuditLog{}).Where("action = ?", "product_service.stock_uom.changed").Count(&n)
	if n != 0 {
		t.Errorf("no-op should not write audit row, got %d", n)
	}
}
