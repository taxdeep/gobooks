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
		`href="/warehouses/new"`,
		"+ New Warehouse",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected warehouses page HTML to contain %q", want)
		}
	}
}
