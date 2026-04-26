// 遵循project_guide.md
package services

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// line_uom_e2e_test.go — end-to-end U2 verification:
//   * SO line write path snapshots SellUOM + factor + qty_in_stock_uom.
//   * PO line write path snapshots PurchaseUOM + factor + qty_in_stock_uom.
//   * AdjustSalesOrderLineQty (S2) keeps qty_in_stock_uom in sync.

func e2eDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Company{},
		&models.Customer{},
		&models.Vendor{},
		&models.Account{},
		&models.TaxCode{},
		&models.ProductService{},
		&models.SalesOrder{},
		&models.SalesOrderLine{},
		&models.PurchaseOrder{},
		&models.PurchaseOrderLine{},
		&models.AuditLog{},
		&models.NumberingSetting{},
		&models.Warehouse{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

type e2eFix struct {
	CompanyID   uint
	CustomerID  uint
	VendorID    uint
	StockItemID uint
}

func seedE2EFix(t *testing.T, db *gorm.DB) e2eFix {
	t.Helper()
	co := models.Company{Name: "E2E Co", BaseCurrencyCode: "CAD", IsActive: true, AccountCodeLength: 4}
	db.Create(&co)
	cust := models.Customer{CompanyID: co.ID, Name: "Cust"}
	db.Create(&cust)
	vend := models.Vendor{CompanyID: co.ID, Name: "Vend"}
	db.Create(&vend)
	rev := models.Account{
		CompanyID: co.ID, Code: "4000", Name: "Rev",
		RootAccountType: models.RootRevenue, DetailAccountType: "sales_revenue", IsActive: true,
	}
	db.Create(&rev)
	stock := models.ProductService{
		CompanyID: co.ID, Name: "Bottle of water",
		Type: models.ProductServiceTypeInventory, RevenueAccountID: rev.ID, IsActive: true,
	}
	stock.ApplyTypeDefaults()
	db.Create(&stock)
	// Now set non-default UOMs so we can verify the snapshot path.
	db.Model(&stock).Updates(map[string]any{
		"stock_uom":           "BOTTLE",
		"sell_uom":            "BOTTLE",
		"sell_uom_factor":     decimal.NewFromInt(1),
		"purchase_uom":        "CASE",
		"purchase_uom_factor": decimal.NewFromInt(24),
	})
	return e2eFix{CompanyID: co.ID, CustomerID: cust.ID, VendorID: vend.ID, StockItemID: stock.ID}
}

// TestCreateSalesOrder_SnapshotsSellUOM — SO Create writes the line
// with SellUOM + factor 1 + qty_in_stock_uom = qty.
func TestCreateSalesOrder_SnapshotsSellUOM(t *testing.T) {
	db := e2eDB(t)
	f := seedE2EFix(t, db)

	so, err := CreateSalesOrder(db, f.CompanyID, SalesOrderInput{
		CustomerID: f.CustomerID,
		OrderDate:  time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
		Lines: []SalesOrderLineInput{{
			ProductServiceID: &f.StockItemID,
			Description:      "Bottle",
			Quantity:         decimal.NewFromInt(50),
			UnitPrice:        decimal.NewFromInt(2),
		}},
	})
	if err != nil {
		t.Fatalf("CreateSalesOrder: %v", err)
	}
	var line models.SalesOrderLine
	db.Where("sales_order_id = ?", so.ID).First(&line)
	if line.LineUOM != "BOTTLE" {
		t.Errorf("LineUOM = %q, want BOTTLE", line.LineUOM)
	}
	if !line.LineUOMFactor.Equal(decimal.NewFromInt(1)) {
		t.Errorf("LineUOMFactor = %s, want 1", line.LineUOMFactor)
	}
	if !line.QtyInStockUOM.Equal(decimal.NewFromInt(50)) {
		t.Errorf("QtyInStockUOM = %s, want 50", line.QtyInStockUOM)
	}
}

// TestCreatePurchaseOrder_SnapshotsPurchaseUOM — PO Create writes the
// line with PurchaseUOM (CASE) + factor 24 + qty_in_stock_uom = qty × 24.
func TestCreatePurchaseOrder_SnapshotsPurchaseUOM(t *testing.T) {
	db := e2eDB(t)
	f := seedE2EFix(t, db)

	po, err := CreatePurchaseOrder(db, f.CompanyID, POInput{
		VendorID:     f.VendorID,
		PODate:       time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
		CurrencyCode: "CAD",
		Lines: []POLineInput{{
			ProductServiceID: &f.StockItemID,
			Description:      "Case of water",
			Qty:              decimal.NewFromInt(10),
			UnitPrice:        decimal.NewFromInt(24),
		}},
	})
	if err != nil {
		t.Fatalf("CreatePurchaseOrder: %v", err)
	}
	var line models.PurchaseOrderLine
	db.Where("purchase_order_id = ?", po.ID).First(&line)
	if line.LineUOM != "CASE" {
		t.Errorf("LineUOM = %q, want CASE", line.LineUOM)
	}
	if !line.LineUOMFactor.Equal(decimal.NewFromInt(24)) {
		t.Errorf("LineUOMFactor = %s, want 24", line.LineUOMFactor)
	}
	want := decimal.NewFromInt(240)
	if !line.QtyInStockUOM.Equal(want) {
		t.Errorf("QtyInStockUOM = %s, want %s (10 CASE × 24 BOTTLE)", line.QtyInStockUOM, want)
	}
}

// TestAdjustSalesOrderLineQty_RecomputesQtyInStockUOM — S2 adjust path
// must update qty_in_stock_uom alongside Qty so inventory + reports
// stay consistent after a partially-invoiced edit.
func TestAdjustSalesOrderLineQty_RecomputesQtyInStockUOM(t *testing.T) {
	db := e2eDB(t)
	f := seedE2EFix(t, db)

	// Build a partially-invoiced SO with a line at SellUOM=BOTTLE
	// (factor 1) so qty_in_stock_uom == qty.  We then change qty
	// from 50 → 80 and verify both move together.
	so := models.SalesOrder{
		CompanyID: f.CompanyID, CustomerID: f.CustomerID,
		OrderNumber: "SO-0001", Status: models.SalesOrderStatusPartiallyInvoiced,
		OrderDate: time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC), CurrencyCode: "CAD",
	}
	db.Create(&so)
	line := models.SalesOrderLine{
		SalesOrderID:     so.ID,
		ProductServiceID: &f.StockItemID,
		Description:      "Bottle",
		Quantity:         decimal.NewFromInt(50),
		OriginalQuantity: decimal.NewFromInt(50),
		UnitPrice:        decimal.NewFromInt(2),
		LineUOM:          "BOTTLE",
		LineUOMFactor:    decimal.NewFromInt(1),
		QtyInStockUOM:    decimal.NewFromInt(50),
		LineNet:          decimal.NewFromInt(100),
		LineTotal:        decimal.NewFromInt(100),
		InvoicedQty:      decimal.NewFromInt(20),
	}
	db.Create(&line)

	// Bump buffer so 80 > 50 is allowed.
	db.Model(&models.Company{}).Where("id = ?", f.CompanyID).Updates(map[string]any{
		"over_shipment_enabled": true,
		"over_shipment_mode":    string(models.OverShipmentModeQty),
		"over_shipment_value":   decimal.NewFromInt(50),
	})

	if _, err := AdjustSalesOrderLineQty(db, f.CompanyID, so.ID, line.ID, decimal.NewFromInt(80), "tester", nil); err != nil {
		t.Fatalf("AdjustSalesOrderLineQty: %v", err)
	}

	var reloaded models.SalesOrderLine
	db.First(&reloaded, line.ID)
	if !reloaded.Quantity.Equal(decimal.NewFromInt(80)) {
		t.Errorf("Quantity = %s, want 80", reloaded.Quantity)
	}
	if !reloaded.QtyInStockUOM.Equal(decimal.NewFromInt(80)) {
		t.Errorf("QtyInStockUOM = %s, want 80 (factor 1)", reloaded.QtyInStockUOM)
	}
}
