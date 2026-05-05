package pages

import (
	"context"
	"strings"
	"testing"

	"balanciz/internal/models"
)

func TestWarehousesPageShowsTransferEntryAction(t *testing.T) {
	vm := WarehousesVM{
		HasCompany: true,
		Warehouses: []models.Warehouse{
			{ID: 1, Code: "MAIN", Name: "Main Warehouse", IsActive: true, IsDefault: true},
		},
	}

	var sb strings.Builder
	if err := Warehouses(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render warehouses page: %v", err)
	}
	html := sb.String()
	for _, want := range []string{
		`href="/inventory/transfers"`,
		"Warehouse Transfer",
		`border border-border-input`,
		`href="/warehouses/new"`,
		"New Warehouse",
		`bg-primary px-4 py-2`,
		"All Warehouses",
		`bg-surface-tableHeader`,
		`hover:bg-surface-rowHover`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected warehouses page HTML to contain %q", want)
		}
	}
	for _, notWant := range []string{
		`btn btn-primary`,
		`table table-zebra`,
		`badge badge-`,
	} {
		if strings.Contains(html, notWant) {
			t.Fatalf("expected warehouses page HTML not to contain legacy class %q", notWant)
		}
	}
}
