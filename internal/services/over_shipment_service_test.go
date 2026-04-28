// 遵循project_guide.md
package services

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// over_shipment_service_test.go — locks the company-default + warehouse-
// override precedence rules (S3 — 2026-04-25) and the max-allowed-qty math
// helper on models.OverShipmentPolicy.

func overShipmentDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Company{}, &models.Warehouse{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedCompanyForOverShipment(t *testing.T, db *gorm.DB, enabled bool, mode models.OverShipmentMode, value string) uint {
	t.Helper()
	c := models.Company{
		Name:                "Buffer Co",
		BaseCurrencyCode:    "CAD",
		IsActive:            true,
		AccountCodeLength:   4,
		OverShipmentEnabled: enabled,
		OverShipmentMode:    mode,
		OverShipmentValue:   decimal.RequireFromString(value),
	}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func seedWarehouseForOverShipment(t *testing.T, db *gorm.DB, companyID uint, enabled bool, mode models.OverShipmentMode, value string) uint {
	t.Helper()
	w := models.Warehouse{
		CompanyID:           companyID,
		Code:                "MAIN",
		Name:                "Main",
		IsDefault:           true,
		IsActive:            true,
		OverShipmentEnabled: enabled,
		OverShipmentMode:    mode,
		OverShipmentValue:   decimal.RequireFromString(value),
	}
	if err := db.Create(&w).Error; err != nil {
		t.Fatal(err)
	}
	return w.ID
}

// ── Resolution precedence ────────────────────────────────────────────────────

// TestResolveOverShipmentPolicy_BothDisabled — neither layer set; policy is
// disabled and the source is empty. MaxAllowedQty short-circuits to original.
func TestResolveOverShipmentPolicy_BothDisabled(t *testing.T) {
	db := overShipmentDB(t)
	cid := seedCompanyForOverShipment(t, db, false, models.OverShipmentModePercent, "0")
	wid := seedWarehouseForOverShipment(t, db, cid, false, models.OverShipmentModePercent, "0")

	p, err := ResolveOverShipmentPolicy(db, cid, wid)
	if err != nil {
		t.Fatal(err)
	}
	if p.Enabled {
		t.Errorf("expected disabled, got %+v", p)
	}
	if got := p.MaxAllowedQty(decimal.NewFromInt(8)); !got.Equal(decimal.NewFromInt(8)) {
		t.Errorf("MaxAllowedQty = %s, want 8", got)
	}
}

// TestResolveOverShipmentPolicy_CompanyOnly — warehouse off; company default
// applies and the source is "company".
func TestResolveOverShipmentPolicy_CompanyOnly(t *testing.T) {
	db := overShipmentDB(t)
	cid := seedCompanyForOverShipment(t, db, true, models.OverShipmentModePercent, "5")
	wid := seedWarehouseForOverShipment(t, db, cid, false, models.OverShipmentModePercent, "0")

	p, err := ResolveOverShipmentPolicy(db, cid, wid)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Enabled || p.Source != "company" {
		t.Errorf("want enabled+company, got %+v", p)
	}
	if !p.Value.Equal(decimal.NewFromInt(5)) {
		t.Errorf("value = %s, want 5", p.Value)
	}
}

// TestResolveOverShipmentPolicy_WarehouseOverride — warehouse on; its values
// win regardless of company state. Source = "warehouse".
func TestResolveOverShipmentPolicy_WarehouseOverride(t *testing.T) {
	db := overShipmentDB(t)
	cid := seedCompanyForOverShipment(t, db, true, models.OverShipmentModePercent, "5")
	wid := seedWarehouseForOverShipment(t, db, cid, true, models.OverShipmentModeQty, "2")

	p, err := ResolveOverShipmentPolicy(db, cid, wid)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Enabled || p.Source != "warehouse" {
		t.Errorf("want enabled+warehouse, got %+v", p)
	}
	if p.Mode != models.OverShipmentModeQty || !p.Value.Equal(decimal.NewFromInt(2)) {
		t.Errorf("warehouse override not honoured: %+v", p)
	}
}

// TestResolveOverShipmentPolicy_NoWarehouseID — passing warehouseID=0 skips
// the override layer entirely (typical for SO-line writes that don't carry
// a per-line warehouse). Company default applies.
func TestResolveOverShipmentPolicy_NoWarehouseID(t *testing.T) {
	db := overShipmentDB(t)
	cid := seedCompanyForOverShipment(t, db, true, models.OverShipmentModeQty, "3")

	p, err := ResolveOverShipmentPolicy(db, cid, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Enabled || p.Source != "company" {
		t.Errorf("want enabled+company, got %+v", p)
	}
}

// ── MaxAllowedQty math ───────────────────────────────────────────────────────

func TestOverShipmentPolicy_MaxAllowedQty(t *testing.T) {
	cases := []struct {
		name        string
		enabled     bool
		mode        models.OverShipmentMode
		value       string
		originalQty string
		want        string
	}{
		{"disabled returns original", false, models.OverShipmentModePercent, "5", "8", "8"},
		{"enabled but value=0 returns original", true, models.OverShipmentModePercent, "0", "8", "8"},
		{"percent 5% on 100", true, models.OverShipmentModePercent, "5", "100", "105"},
		{"percent 5% on 8 (fractional)", true, models.OverShipmentModePercent, "5", "8", "8.4"},
		{"qty 2 on 8", true, models.OverShipmentModeQty, "2", "8", "10"},
		{"qty 0 (= disabled effectively)", true, models.OverShipmentModeQty, "0", "8", "8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := models.OverShipmentPolicy{
				Enabled: tc.enabled,
				Mode:    tc.mode,
				Value:   decimal.RequireFromString(tc.value),
			}
			got := p.MaxAllowedQty(decimal.RequireFromString(tc.originalQty))
			want := decimal.RequireFromString(tc.want)
			if !got.Equal(want) {
				t.Errorf("MaxAllowedQty = %s, want %s", got, want)
			}
		})
	}
}

// TestNormalizeOverShipmentMode locks the unknown-mode → "percent" rule.
func TestNormalizeOverShipmentMode(t *testing.T) {
	cases := []struct {
		in   models.OverShipmentMode
		want models.OverShipmentMode
	}{
		{models.OverShipmentModePercent, models.OverShipmentModePercent},
		{models.OverShipmentModeQty, models.OverShipmentModeQty},
		{"", models.OverShipmentModePercent},
		{"weird", models.OverShipmentModePercent},
	}
	for _, tc := range cases {
		got := NormalizeOverShipmentMode(tc.in)
		if got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
