package pages

import (
	"context"
	"strings"
	"testing"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"github.com/shopspring/decimal"
)

func TestWarehouseStockShowsReadOnlyItemBalances(t *testing.T) {
	vm := WarehouseStockVM{
		HasCompany: true,
		Report: &services.WarehouseStockReport{
			Warehouse: models.Warehouse{ID: 9, Code: "MAIN", Name: "Main Warehouse"},
			Rows: []services.WarehouseStockRow{
				{
					ItemID:            1,
					ItemName:          "Widget",
					SKU:               "W-100",
					IsActive:          true,
					QuantityOnHand:    decimal.NewFromInt(10),
					QuantityReserved:  decimal.NewFromInt(2),
					QuantityAvailable: decimal.NewFromInt(8),
					AverageCost:       decimal.NewFromInt(5),
					TotalValue:        decimal.NewFromInt(50),
				},
			},
			TotalOnHand:    decimal.NewFromInt(10),
			TotalReserved:  decimal.NewFromInt(2),
			TotalAvailable: decimal.NewFromInt(8),
			TotalValue:     decimal.NewFromInt(50),
			ItemsWithStock: 1,
		},
	}

	var sb strings.Builder
	if err := WarehouseStock(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render warehouse stock: %v", err)
	}
	html := sb.String()
	for _, want := range []string{
		"Main Warehouse Stock",
		"Items in Warehouse",
		"Widget",
		"W-100",
		"Available",
		`href="/warehouses/9"`,
		"Edit Warehouse",
		`href="/inventory/transfers/new?from_warehouse_id=9"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected warehouse stock HTML to contain %q", want)
		}
	}
	for _, notWant := range []string{
		"Save Warehouse",
		`name="code"`,
		`name="name"`,
	} {
		if strings.Contains(html, notWant) {
			t.Fatalf("expected warehouse stock HTML not to contain edit form marker %q", notWant)
		}
	}
}
