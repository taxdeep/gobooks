package pages

import (
	"context"
	"strings"
	"testing"

	"balanciz/internal/models"
)

func TestWarehouseDetailShowsInventoryActionEntries(t *testing.T) {
	vm := WarehouseDetailVM{
		HasCompany: true,
		Warehouse: models.Warehouse{
			ID:       9,
			Code:     "MAIN",
			Name:     "Main Warehouse",
			IsActive: true,
		},
	}

	var sb strings.Builder
	if err := WarehouseDetail(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render warehouse detail: %v", err)
	}
	html := sb.String()
	for _, want := range []string{
		"Warehouse Transfer",
		`href="/inventory/transfers/new?from_warehouse_id=9"`,
		"Received",
		`href="/ar-return-receipts/new?warehouse_id=9"`,
		"Shipment",
		`href="/vendor-return-shipments/new?warehouse_id=9"`,
		`border border-border-input`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected warehouse detail HTML to contain %q", want)
		}
	}
}

func TestWarehouseDetailHidesInventoryActionsForNewWarehouse(t *testing.T) {
	vm := WarehouseDetailVM{HasCompany: true}

	var sb strings.Builder
	if err := WarehouseDetail(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render new warehouse detail: %v", err)
	}
	html := sb.String()
	for _, notWant := range []string{
		`/inventory/transfers/new?from_warehouse_id=`,
		`/ar-return-receipts/new?warehouse_id=`,
		`/vendor-return-shipments/new?warehouse_id=`,
	} {
		if strings.Contains(html, notWant) {
			t.Fatalf("expected new warehouse detail HTML not to contain %q", notWant)
		}
	}
}
