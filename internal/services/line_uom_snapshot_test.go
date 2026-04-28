// 遵循project_guide.md
package services

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// line_uom_snapshot_test.go — locks the U2 helper that resolves a line's
// UOM defaults from the linked product (or falls back safely when no
// product / non-stock product / unknown product).

func snapshotDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{}, &models.Account{}, &models.ProductService{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

type snapFix struct {
	CompanyID    uint
	StockItemID  uint
	ServiceID    uint
}

func seedSnapFix(t *testing.T, db *gorm.DB) snapFix {
	t.Helper()
	co := models.Company{Name: "Snap Co", BaseCurrencyCode: "CAD", IsActive: true, AccountCodeLength: 4}
	db.Create(&co)
	rev := models.Account{
		CompanyID: co.ID, Code: "4000", Name: "Rev",
		RootAccountType: models.RootRevenue, DetailAccountType: "sales_revenue", IsActive: true,
	}
	db.Create(&rev)
	stock := models.ProductService{
		CompanyID: co.ID, Name: "Bottle of water",
		Type: models.ProductServiceTypeInventory, RevenueAccountID: rev.ID, IsActive: true,
		StockUOM: "BOTTLE", SellUOM: "BOTTLE", SellUOMFactor: decimal.NewFromInt(1),
		PurchaseUOM: "CASE", PurchaseUOMFactor: decimal.NewFromInt(24),
	}
	stock.ApplyTypeDefaults()
	// ApplyTypeDefaults nukes our UOMs back to defaults; re-set after.
	stock.StockUOM = "BOTTLE"
	stock.SellUOM = "BOTTLE"
	stock.SellUOMFactor = decimal.NewFromInt(1)
	stock.PurchaseUOM = "CASE"
	stock.PurchaseUOMFactor = decimal.NewFromInt(24)
	db.Create(&stock)

	svc := models.ProductService{
		CompanyID: co.ID, Name: "Consulting",
		Type: models.ProductServiceTypeService, RevenueAccountID: rev.ID, IsActive: true,
	}
	svc.ApplyTypeDefaults()
	db.Create(&svc)

	return snapFix{CompanyID: co.ID, StockItemID: stock.ID, ServiceID: svc.ID}
}

func TestSnapshotLineUOM_FreeText(t *testing.T) {
	db := snapshotDB(t)
	got := SnapshotLineUOM(db, 1, nil, LineUOMSell, decimal.NewFromInt(5), "", decimal.Zero)
	if got.LineUOM != "EA" || !got.LineUOMFactor.Equal(decimal.NewFromInt(1)) {
		t.Errorf("free-text fallback wrong: %+v", got)
	}
	if !got.QtyInStockUOM.Equal(decimal.NewFromInt(5)) {
		t.Errorf("free-text QtyInStockUOM = %s, want 5", got.QtyInStockUOM)
	}
}

func TestSnapshotLineUOM_ServiceItem(t *testing.T) {
	db := snapshotDB(t)
	f := seedSnapFix(t, db)
	got := SnapshotLineUOM(db, f.CompanyID, &f.ServiceID, LineUOMSell, decimal.RequireFromString("1.5"), "", decimal.Zero)
	if got.LineUOM != "EA" {
		t.Errorf("service item should fall back to EA, got %s", got.LineUOM)
	}
	if !got.QtyInStockUOM.Equal(decimal.RequireFromString("1.5")) {
		t.Errorf("service QtyInStockUOM = %s, want 1.5", got.QtyInStockUOM)
	}
}

func TestSnapshotLineUOM_StockItem_SellSide(t *testing.T) {
	db := snapshotDB(t)
	f := seedSnapFix(t, db)
	got := SnapshotLineUOM(db, f.CompanyID, &f.StockItemID, LineUOMSell, decimal.NewFromInt(10), "", decimal.Zero)
	if got.LineUOM != "BOTTLE" {
		t.Errorf("Sell UOM should be BOTTLE, got %s", got.LineUOM)
	}
	if !got.LineUOMFactor.Equal(decimal.NewFromInt(1)) {
		t.Errorf("Sell factor should be 1, got %s", got.LineUOMFactor)
	}
	if !got.QtyInStockUOM.Equal(decimal.NewFromInt(10)) {
		t.Errorf("QtyInStockUOM = %s, want 10", got.QtyInStockUOM)
	}
}

func TestSnapshotLineUOM_StockItem_PurchaseSide(t *testing.T) {
	db := snapshotDB(t)
	f := seedSnapFix(t, db)
	// 10 CASE × factor 24 = 240 BOTTLE.
	got := SnapshotLineUOM(db, f.CompanyID, &f.StockItemID, LineUOMPurchase, decimal.NewFromInt(10), "", decimal.Zero)
	if got.LineUOM != "CASE" {
		t.Errorf("Purchase UOM should be CASE, got %s", got.LineUOM)
	}
	if !got.LineUOMFactor.Equal(decimal.NewFromInt(24)) {
		t.Errorf("Purchase factor should be 24, got %s", got.LineUOMFactor)
	}
	if !got.QtyInStockUOM.Equal(decimal.NewFromInt(240)) {
		t.Errorf("QtyInStockUOM = %s, want 240", got.QtyInStockUOM)
	}
}

func TestSnapshotLineUOM_PerLineOverride(t *testing.T) {
	db := snapshotDB(t)
	f := seedSnapFix(t, db)
	// Override: this PO line came as 1 PALLET = 240 BOTTLE.
	got := SnapshotLineUOM(db, f.CompanyID, &f.StockItemID, LineUOMPurchase, decimal.NewFromInt(1), "PALLET", decimal.NewFromInt(240))
	if got.LineUOM != "PALLET" {
		t.Errorf("override UOM ignored: %+v", got)
	}
	if !got.LineUOMFactor.Equal(decimal.NewFromInt(240)) {
		t.Errorf("override factor ignored: %s", got.LineUOMFactor)
	}
	if !got.QtyInStockUOM.Equal(decimal.NewFromInt(240)) {
		t.Errorf("QtyInStockUOM = %s, want 240", got.QtyInStockUOM)
	}
}

func TestSnapshotLineUOM_HalfOverrideFallsToDefault(t *testing.T) {
	db := snapshotDB(t)
	f := seedSnapFix(t, db)
	// UOM-only override (no factor) — falls to product default to avoid
	// a silent factor=1 surprise when operator only changed the unit.
	got := SnapshotLineUOM(db, f.CompanyID, &f.StockItemID, LineUOMPurchase, decimal.NewFromInt(2), "PALLET", decimal.Zero)
	if got.LineUOM != "CASE" {
		t.Errorf("half override should fall back to default UOM, got %s", got.LineUOM)
	}
	if !got.LineUOMFactor.Equal(decimal.NewFromInt(24)) {
		t.Errorf("factor should stay 24, got %s", got.LineUOMFactor)
	}
}
