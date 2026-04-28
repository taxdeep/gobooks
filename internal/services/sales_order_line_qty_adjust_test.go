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

// sales_order_line_qty_adjust_test.go — locks the partially-invoiced
// per-line Qty editing contract (S2 — 2026-04-25):
//
//   * Status guard: only partially_invoiced SOs may use this path.
//   * Floor: newQty < InvoicedQty is rejected.
//   * Ceiling: newQty > original + buffer is rejected; the buffer comes
//     from the company default unless a warehouse override exists (S3).
//   * Stock-item integer rule (S1) re-applies to adjusted qty.
//   * SO Subtotal/TaxTotal/Total recompute after a successful adjust.
//   * Audit log carries before/after qty + the buffer source.

func qtyAdjustDB(t *testing.T) *gorm.DB {
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
		&models.Warehouse{},
		&models.AuditLog{},
		&models.NumberingSetting{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

type qtyAdjustFixture struct {
	CompanyID    uint
	CustomerID   uint
	StockItemID  uint
	ServiceItemID uint
	SO           models.SalesOrder
	StockLine    models.SalesOrderLine
	ServiceLine  models.SalesOrderLine
}

// seedQtyAdjustFixture builds a partially-invoiced SO with two lines:
//   - a stock-item line: original 8, currently 8, invoiced 6
//   - a service line:    original 2, currently 2, invoiced 1
// Company default over-ship buffer is whatever the test configures via
// extra Updates after this returns; default is disabled (no buffer).
func seedQtyAdjustFixture(t *testing.T, db *gorm.DB) qtyAdjustFixture {
	t.Helper()
	co := models.Company{
		Name: "Adjust Co", BaseCurrencyCode: "CAD", IsActive: true, AccountCodeLength: 4,
	}
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
		CompanyID: co.ID, Name: "Watermelon",
		Type: models.ProductServiceTypeInventory, RevenueAccountID: rev.ID, IsActive: true,
	}
	stock.ApplyTypeDefaults()
	if err := db.Create(&stock).Error; err != nil {
		t.Fatal(err)
	}
	svc := models.ProductService{
		CompanyID: co.ID, Name: "Consulting",
		Type: models.ProductServiceTypeService, RevenueAccountID: rev.ID, IsActive: true,
	}
	svc.ApplyTypeDefaults()
	if err := db.Create(&svc).Error; err != nil {
		t.Fatal(err)
	}

	so := models.SalesOrder{
		CompanyID:    co.ID,
		CustomerID:   cust.ID,
		OrderNumber:  "SO-0001",
		Status:       models.SalesOrderStatusPartiallyInvoiced,
		OrderDate:    time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
		CurrencyCode: "CAD",
		Subtotal:     decimal.RequireFromString("100"),
		TaxTotal:     decimal.Zero,
		Total:        decimal.RequireFromString("100"),
	}
	if err := db.Create(&so).Error; err != nil {
		t.Fatal(err)
	}
	stockLine := models.SalesOrderLine{
		SalesOrderID:     so.ID,
		ProductServiceID: &stock.ID,
		Description:      "Watermelon",
		Quantity:         decimal.NewFromInt(8),
		OriginalQuantity: decimal.NewFromInt(8),
		UnitPrice:        decimal.NewFromInt(10),
		LineNet:          decimal.NewFromInt(80),
		LineTotal:        decimal.NewFromInt(80),
		InvoicedQty:      decimal.NewFromInt(6),
		SortOrder:        0,
	}
	if err := db.Create(&stockLine).Error; err != nil {
		t.Fatal(err)
	}
	svcLine := models.SalesOrderLine{
		SalesOrderID:     so.ID,
		ProductServiceID: &svc.ID,
		Description:      "Consulting",
		Quantity:         decimal.NewFromInt(2),
		OriginalQuantity: decimal.NewFromInt(2),
		UnitPrice:        decimal.NewFromInt(10),
		LineNet:          decimal.NewFromInt(20),
		LineTotal:        decimal.NewFromInt(20),
		InvoicedQty:      decimal.NewFromInt(1),
		SortOrder:        1,
	}
	if err := db.Create(&svcLine).Error; err != nil {
		t.Fatal(err)
	}

	return qtyAdjustFixture{
		CompanyID:     co.ID,
		CustomerID:    cust.ID,
		StockItemID:   stock.ID,
		ServiceItemID: svc.ID,
		SO:            so,
		StockLine:     stockLine,
		ServiceLine:   svcLine,
	}
}

// ── Status guard ─────────────────────────────────────────────────────────────

func TestAdjustSalesOrderLineQty_RejectsNonPartiallyInvoicedStatus(t *testing.T) {
	db := qtyAdjustDB(t)
	f := seedQtyAdjustFixture(t, db)
	// Force draft status to violate the precondition.
	if err := db.Model(&f.SO).Update("status", models.SalesOrderStatusDraft).Error; err != nil {
		t.Fatal(err)
	}

	_, err := AdjustSalesOrderLineQty(db, f.CompanyID, f.SO.ID, f.StockLine.ID, decimal.NewFromInt(7), "tester", nil)
	if err == nil {
		t.Fatal("expected error for non-partially-invoiced SO, got nil")
	}
	if !strings.Contains(err.Error(), "partially-invoiced") {
		t.Errorf("error = %v, want partially-invoiced guidance", err)
	}
}

// ── Floor: newQty < InvoicedQty rejected ─────────────────────────────────────

func TestAdjustSalesOrderLineQty_RejectsBelowInvoicedFloor(t *testing.T) {
	db := qtyAdjustDB(t)
	f := seedQtyAdjustFixture(t, db)

	// Stock line invoiced = 6; try to set qty = 5.
	_, err := AdjustSalesOrderLineQty(db, f.CompanyID, f.SO.ID, f.StockLine.ID, decimal.NewFromInt(5), "tester", nil)
	if err == nil {
		t.Fatal("expected floor violation, got nil")
	}
	if !strings.Contains(err.Error(), "less than already-invoiced") {
		t.Errorf("error = %v, want floor-violation guidance", err)
	}
}

// ── Ceiling: no buffer → can't exceed original ───────────────────────────────

func TestAdjustSalesOrderLineQty_RejectsAboveCapWithNoBuffer(t *testing.T) {
	db := qtyAdjustDB(t)
	f := seedQtyAdjustFixture(t, db)

	// Stock line original = 8, no buffer → max = 8. Try 9 → reject.
	_, err := AdjustSalesOrderLineQty(db, f.CompanyID, f.SO.ID, f.StockLine.ID, decimal.NewFromInt(9), "tester", nil)
	if err == nil {
		t.Fatal("expected ceiling violation, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds the over-shipment cap") {
		t.Errorf("error = %v, want ceiling-violation guidance", err)
	}
}

// ── Ceiling: company-level buffer raises cap ─────────────────────────────────

func TestAdjustSalesOrderLineQty_HonoursCompanyBufferQty(t *testing.T) {
	db := qtyAdjustDB(t)
	f := seedQtyAdjustFixture(t, db)
	// Enable company over-ship: fixed +1 unit.
	if err := db.Model(&models.Company{}).Where("id = ?", f.CompanyID).
		Updates(map[string]any{
			"over_shipment_enabled": true,
			"over_shipment_mode":    string(models.OverShipmentModeQty),
			"over_shipment_value":   decimal.NewFromInt(1),
		}).Error; err != nil {
		t.Fatal(err)
	}

	// Original 8 + buffer 1 = max 9. 9 should succeed; 10 should fail.
	if _, err := AdjustSalesOrderLineQty(db, f.CompanyID, f.SO.ID, f.StockLine.ID, decimal.NewFromInt(9), "tester", nil); err != nil {
		t.Fatalf("qty=9 should succeed under +1 buffer: %v", err)
	}
	if _, err := AdjustSalesOrderLineQty(db, f.CompanyID, f.SO.ID, f.StockLine.ID, decimal.NewFromInt(10), "tester", nil); err == nil {
		t.Fatal("qty=10 should violate the +1 buffer cap")
	}
}

// ── Stock-item integer rule (S1) re-applies on adjust ────────────────────────

func TestAdjustSalesOrderLineQty_RejectsFractionalStockQty(t *testing.T) {
	db := qtyAdjustDB(t)
	f := seedQtyAdjustFixture(t, db)
	// Enable buffer so the only failing rule is integer-vs-fractional.
	if err := db.Model(&models.Company{}).Where("id = ?", f.CompanyID).
		Updates(map[string]any{
			"over_shipment_enabled": true,
			"over_shipment_mode":    string(models.OverShipmentModeQty),
			"over_shipment_value":   decimal.NewFromInt(2),
		}).Error; err != nil {
		t.Fatal(err)
	}

	_, err := AdjustSalesOrderLineQty(db, f.CompanyID, f.SO.ID, f.StockLine.ID, decimal.RequireFromString("8.5"), "tester", nil)
	if err == nil {
		t.Fatal("expected stock-item integer rejection, got nil")
	}
	if !strings.Contains(err.Error(), "whole-unit") {
		t.Errorf("error = %v, want whole-unit guidance", err)
	}
}

// ── Service line accepts fractional under buffer ─────────────────────────────

func TestAdjustSalesOrderLineQty_AcceptsServiceFractionalUnderBuffer(t *testing.T) {
	db := qtyAdjustDB(t)
	f := seedQtyAdjustFixture(t, db)
	// Enable percent buffer so service line can grow a little.
	if err := db.Model(&models.Company{}).Where("id = ?", f.CompanyID).
		Updates(map[string]any{
			"over_shipment_enabled": true,
			"over_shipment_mode":    string(models.OverShipmentModePercent),
			"over_shipment_value":   decimal.NewFromInt(50), // +50% on a 2-qty line → max 3
		}).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := AdjustSalesOrderLineQty(db, f.CompanyID, f.SO.ID, f.ServiceLine.ID, decimal.RequireFromString("2.5"), "tester", nil); err != nil {
		t.Fatalf("service line should accept fractional under buffer: %v", err)
	}
}

// ── Successful adjust recomputes SO totals ───────────────────────────────────

func TestAdjustSalesOrderLineQty_RecomputesSOTotals(t *testing.T) {
	db := qtyAdjustDB(t)
	f := seedQtyAdjustFixture(t, db)

	// Service line: original 2 invoiced 1 → can drop to 1 with no buffer.
	// Original 100 total = 80 (stock) + 20 (service).
	// After: 80 + 10 = 90.
	if _, err := AdjustSalesOrderLineQty(db, f.CompanyID, f.SO.ID, f.ServiceLine.ID, decimal.NewFromInt(1), "tester", nil); err != nil {
		t.Fatalf("adjust should succeed: %v", err)
	}

	var reloaded models.SalesOrder
	if err := db.First(&reloaded, f.SO.ID).Error; err != nil {
		t.Fatal(err)
	}
	want := decimal.NewFromInt(90)
	if !reloaded.Total.Equal(want) {
		t.Errorf("SO Total = %s, want %s (recompute after adjust)", reloaded.Total, want)
	}
	if !reloaded.Subtotal.Equal(want) {
		t.Errorf("SO Subtotal = %s, want %s", reloaded.Subtotal, want)
	}
}

// ── Audit row written with before/after + buffer source ──────────────────────

func TestAdjustSalesOrderLineQty_WritesAuditRow(t *testing.T) {
	db := qtyAdjustDB(t)
	f := seedQtyAdjustFixture(t, db)
	// Buffer +1 so original=8 → 9 is legal.
	if err := db.Model(&models.Company{}).Where("id = ?", f.CompanyID).
		Updates(map[string]any{
			"over_shipment_enabled": true,
			"over_shipment_mode":    string(models.OverShipmentModeQty),
			"over_shipment_value":   decimal.NewFromInt(1),
		}).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := AdjustSalesOrderLineQty(db, f.CompanyID, f.SO.ID, f.StockLine.ID, decimal.NewFromInt(9), "ops@example.com", nil); err != nil {
		t.Fatal(err)
	}

	var rows []models.AuditLog
	if err := db.Where("action = ?", "sales_order.line.qty_adjusted").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 audit row, got %d", len(rows))
	}
	r := rows[0]
	if r.Actor != "ops@example.com" {
		t.Errorf("actor = %q, want ops@example.com", r.Actor)
	}
	if r.EntityID != f.StockLine.ID {
		t.Errorf("entity_id = %d, want %d", r.EntityID, f.StockLine.ID)
	}
	// JSON snapshots should mention the qty + buffer source.
	asJSON := r.DetailsJSON
	for _, want := range []string{"\"qty\":\"8\"", "\"qty\":\"9\"", "\"buffer_source\":\"company\""} {
		if !strings.Contains(asJSON, want) {
			t.Errorf("audit details missing %q; got %s", want, asJSON)
		}
	}
}
