// 遵循project_guide.md
package models

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// product_service_uom_test.go — locks the UOM validator + normaliser
// behaviour added in Phase U1 (2026-04-25). Service-layer tests for
// ChangeStockUOM / SaveProductUOMs live in
// internal/services/product_service_uom_test.go.

func TestNormalizeUOM(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"EA", "EA"},
		{"ea", "EA"},
		{"  Bottle  ", "BOTTLE"},
		{"", "EA"},
		{" \t ", "EA"},
		{"case_24", "CASE_24"},
	}
	for _, tc := range cases {
		if got := NormalizeUOM(tc.in); got != tc.want {
			t.Errorf("NormalizeUOM(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestApplyUOMDefaults_Idempotent — re-running on a fully-populated
// ProductService is a no-op; running on a zero-value row fills defaults.
func TestApplyUOMDefaults_Idempotent(t *testing.T) {
	t.Run("zero value", func(t *testing.T) {
		ps := ProductService{}
		ps.ApplyUOMDefaults()
		if ps.StockUOM != "EA" || ps.SellUOM != "EA" || ps.PurchaseUOM != "EA" {
			t.Errorf("expected EA defaults, got %+v", ps)
		}
		if !ps.SellUOMFactor.Equal(decimal.NewFromInt(1)) || !ps.PurchaseUOMFactor.Equal(decimal.NewFromInt(1)) {
			t.Errorf("expected factor=1, got sell=%s purchase=%s", ps.SellUOMFactor, ps.PurchaseUOMFactor)
		}
	})
	t.Run("populated row stays put", func(t *testing.T) {
		ps := ProductService{
			StockUOM: "BOTTLE", SellUOM: "BOTTLE", SellUOMFactor: decimal.NewFromInt(1),
			PurchaseUOM: "CASE", PurchaseUOMFactor: decimal.NewFromInt(24),
		}
		ps.ApplyUOMDefaults()
		if ps.PurchaseUOM != "CASE" || !ps.PurchaseUOMFactor.Equal(decimal.NewFromInt(24)) {
			t.Errorf("populated row was disturbed: %+v", ps)
		}
	})
}

func TestValidateUOMs(t *testing.T) {
	cases := []struct {
		name    string
		ps      ProductService
		wantErr string
	}{
		{
			name: "stock item EA defaults — ok",
			ps: ProductService{
				Name: "Watermelon", IsStockItem: true,
				StockUOM: "EA", SellUOM: "EA", SellUOMFactor: decimal.NewFromInt(1),
				PurchaseUOM: "EA", PurchaseUOMFactor: decimal.NewFromInt(1),
			},
		},
		{
			name: "stock item case→bottle — ok",
			ps: ProductService{
				Name: "Bottle of water", IsStockItem: true,
				StockUOM: "BOTTLE", SellUOM: "BOTTLE", SellUOMFactor: decimal.NewFromInt(1),
				PurchaseUOM: "CASE", PurchaseUOMFactor: decimal.NewFromInt(24),
			},
		},
		{
			name: "sell == stock with factor != 1 — reject",
			ps: ProductService{
				Name: "Bad", IsStockItem: true,
				StockUOM: "EA", SellUOM: "EA", SellUOMFactor: decimal.NewFromInt(5),
				PurchaseUOM: "EA", PurchaseUOMFactor: decimal.NewFromInt(1),
			},
			wantErr: "sell UOM equals stock UOM",
		},
		{
			name: "purchase == stock with factor != 1 — reject",
			ps: ProductService{
				Name: "Bad", IsStockItem: true,
				StockUOM: "EA", SellUOM: "EA", SellUOMFactor: decimal.NewFromInt(1),
				PurchaseUOM: "EA", PurchaseUOMFactor: decimal.NewFromInt(2),
			},
			wantErr: "purchase UOM equals stock UOM",
		},
		{
			name: "zero sell factor — reject",
			ps: ProductService{
				Name: "Bad", IsStockItem: true,
				StockUOM: "BOTTLE", SellUOM: "PACK_6", SellUOMFactor: decimal.Zero,
				PurchaseUOM: "BOTTLE", PurchaseUOMFactor: decimal.NewFromInt(1),
			},
			wantErr: "sell UOM factor must be > 0",
		},
		{
			name: "negative purchase factor — reject",
			ps: ProductService{
				Name: "Bad", IsStockItem: true,
				StockUOM: "BOTTLE", SellUOM: "BOTTLE", SellUOMFactor: decimal.NewFromInt(1),
				PurchaseUOM: "CASE", PurchaseUOMFactor: decimal.NewFromInt(-1),
			},
			wantErr: "purchase UOM factor must be > 0",
		},
		{
			name: "non-stock service with non-default SellUOM — reject",
			ps: ProductService{
				Name: "Consulting", IsStockItem: false,
				ItemStructureType: ItemStructureSingle,
				StockUOM:          "EA", SellUOM: "HOUR", SellUOMFactor: decimal.NewFromInt(1),
				PurchaseUOM: "HOUR", PurchaseUOMFactor: decimal.NewFromInt(1),
			},
		},
		{
			name: "non-stock service with all defaults — ok",
			ps: ProductService{
				Name: "Consulting", IsStockItem: false,
				ItemStructureType: ItemStructureSingle,
				StockUOM:          "EA", SellUOM: "EA", SellUOMFactor: decimal.NewFromInt(1),
				PurchaseUOM: "EA", PurchaseUOMFactor: decimal.NewFromInt(1),
			},
		},
		{
			name: "bundle parent (non-stock) with custom SellUOM — ok",
			ps: ProductService{
				Name: "Gift Box", IsStockItem: false,
				ItemStructureType: ItemStructureBundle,
				StockUOM:          "EA", SellUOM: "PACK", SellUOMFactor: decimal.NewFromInt(1),
				PurchaseUOM: "EA", PurchaseUOMFactor: decimal.NewFromInt(1),
			},
		},
		{
			name: "bundle parent with custom StockUOM — reject (no stock unit)",
			ps: ProductService{
				Name: "Gift Box", IsStockItem: false,
				ItemStructureType: ItemStructureBundle,
				StockUOM:          "PACK", SellUOM: "EA", SellUOMFactor: decimal.NewFromInt(1),
				PurchaseUOM: "EA", PurchaseUOMFactor: decimal.NewFromInt(1),
			},
			wantErr: "non-stock items must keep StockUOM=EA",
		},
		{
			name: "bundle parent with custom SellUOMFactor — reject (no stock to convert from)",
			ps: ProductService{
				Name: "Gift Box", IsStockItem: false,
				ItemStructureType: ItemStructureBundle,
				StockUOM:          "EA", SellUOM: "PACK", SellUOMFactor: decimal.NewFromInt(3),
				PurchaseUOM: "EA", PurchaseUOMFactor: decimal.NewFromInt(1),
			},
			wantErr: "non-stock items must keep StockUOM=EA",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.ps.ValidateUOMs()
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("expected ok, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}
