// 遵循project_guide.md
package pages

// inventory_stock_test.go — pure-function coverage for the Stock
// Report's grouping + KPI helpers. The template itself is visual
// polish; these helpers are the load-bearing logic behind the
// per-item subtotal row and the three summary cards.

import (
	"testing"

	"github.com/shopspring/decimal"

	"balanciz/internal/models"
	"balanciz/internal/services"
)

func TestGroupStockRowsByItem_MergesConsecutiveSameItemRows(t *testing.T) {
	rows := []services.StockRow{
		{ItemID: 1, ItemName: "Widget", QuantityOnHand: decimal.NewFromInt(10), TotalValue: decimal.NewFromInt(100)},
		{ItemID: 1, ItemName: "Widget", QuantityOnHand: decimal.NewFromInt(5), TotalValue: decimal.NewFromInt(50)},
		{ItemID: 2, ItemName: "Gadget", QuantityOnHand: decimal.NewFromInt(3), TotalValue: decimal.NewFromInt(30)},
	}
	groups := groupStockRowsByItem(rows)
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(groups))
	}
	if groups[0].ItemID != 1 || len(groups[0].Rows) != 2 {
		t.Errorf("group 0: id=%d rows=%d, want id=1 rows=2", groups[0].ItemID, len(groups[0].Rows))
	}
	if !groups[0].TotalQty.Equal(decimal.NewFromInt(15)) {
		t.Errorf("group 0 TotalQty: got %s, want 15", groups[0].TotalQty)
	}
	if !groups[0].TotalValue.Equal(decimal.NewFromInt(150)) {
		t.Errorf("group 0 TotalValue: got %s, want 150", groups[0].TotalValue)
	}
	if groups[1].ItemID != 2 || len(groups[1].Rows) != 1 {
		t.Errorf("group 1: id=%d rows=%d, want id=2 rows=1", groups[1].ItemID, len(groups[1].Rows))
	}
}

func TestGroupStockRowsByItem_EmptyInput(t *testing.T) {
	if got := groupStockRowsByItem(nil); got != nil {
		t.Errorf("nil input: got %v, want nil", got)
	}
}

func TestStockDistinctItemLabel(t *testing.T) {
	report := &services.StockReport{
		Rows: []services.StockRow{
			{ItemID: 1}, {ItemID: 1}, {ItemID: 2}, {ItemID: 3},
		},
	}
	if got := stockDistinctItemLabel(report); got != "3" {
		t.Errorf("distinct items: got %q, want %q", got, "3")
	}
}

func TestStockTotalUnits_SumsAcrossLocations(t *testing.T) {
	report := &services.StockReport{
		Rows: []services.StockRow{
			{QuantityOnHand: decimal.NewFromFloat(10.5)},
			{QuantityOnHand: decimal.NewFromFloat(4.5)},
			{QuantityOnHand: decimal.NewFromInt(1)},
		},
	}
	if got := stockTotalUnits(report); got != "16" {
		t.Errorf("total units: got %q, want %q", got, "16")
	}
}

func TestStockWarehouseDisplayName_PrefersNameOverCode(t *testing.T) {
	wid := uint(7)
	row := services.StockRow{WarehouseID: &wid, WarehouseName: "Main", WarehouseCode: "MAIN"}
	if got := stockWarehouseDisplayName(row); got != "Main" {
		t.Errorf("prefers name: got %q, want %q", got, "Main")
	}
	row.WarehouseName = ""
	if got := stockWarehouseDisplayName(row); got != "MAIN" {
		t.Errorf("falls back to code: got %q, want %q", got, "MAIN")
	}
}

func TestAmazonFBALabel_IncludesRefWhenPresent(t *testing.T) {
	row := services.StockRow{LocationType: models.LocationTypeAmazonFBA, LocationRef: "USW2"}
	if got := amazonFBALabel(row); got != "Amazon FBA — USW2" {
		t.Errorf("got %q", got)
	}
	row.LocationRef = ""
	if got := amazonFBALabel(row); got != "Amazon FBA" {
		t.Errorf("got %q", got)
	}
}
