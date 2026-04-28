// 遵循project_guide.md
package web

// product_service_default_warehouse_test.go — PS.1 tests.
//
// PS.1 auto-provisions a MAIN default warehouse when the user saves
// the company's first stock item (Type = inventory). Rationale: a
// stock item without a warehouse is a silent Rule #4 trap — the
// ProductService save succeeds but the first Bill/Invoice/Expense
// using it fails downstream with "no warehouse configured". Auto-
// provisioning closes that gap.

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"balanciz/internal/models"
)

// TestProductServiceCreate_PS1_AutoCreatesDefaultWarehouse_OnStockItem
// locks the primary PS.1 behavior: saving a stock-item ProductService
// creates a MAIN default warehouse for the company.
func TestProductServiceCreate_PS1_AutoCreatesDefaultWarehouse_OnStockItem(t *testing.T) {
	db := testProductServiceValidationDB(t)
	server := &Server{DB: db}
	user := seedPSUser(t, db)

	companyID := seedValidationCompany(t, db, "Acme")
	revenueID := seedValidationAccount(t, db, companyID, "4000", models.RootRevenue, models.DetailServiceRevenue)
	cogsID := seedValidationAccount(t, db, companyID, "5000", models.RootCostOfSales, models.DetailCostOfGoodsSold)
	invID := seedValidationAccount(t, db, companyID, "1300", models.RootAsset, models.DetailInventory)

	// Pre-condition: no warehouse exists.
	var preCount int64
	db.Model(&models.Warehouse{}).Where("company_id = ?", companyID).Count(&preCount)
	if preCount != 0 {
		t.Fatalf("pre-condition: expected 0 warehouses, got %d", preCount)
	}

	app := productServiceValidationApp(server, user, companyID)
	resp := performFormRequest(t, app, http.MethodPost, "/products-services", url.Values{
		"name":                 {"Widget"},
		"type":                 {string(models.ProductServiceTypeInventory)},
		"structure_type":       {string(models.ItemStructureSingle)},
		"default_price":        {"50.00"},
		"revenue_account_id":   {fmt.Sprintf("%d", revenueID)},
		"cogs_account_id":      {fmt.Sprintf("%d", cogsID)},
		"inventory_account_id": {fmt.Sprintf("%d", invID)},
	}, "")

	if resp.StatusCode != http.StatusSeeOther {
		body := readResponseBody(t, resp)
		t.Fatalf("expected 303 redirect on success, got %d\nbody: %s", resp.StatusCode, body)
	}

	// The item itself must be saved.
	var item models.ProductService
	if err := db.Where("company_id = ? AND name = ?", companyID, "Widget").First(&item).Error; err != nil {
		t.Fatalf("expected item to exist: %v", err)
	}
	if !item.IsStockItem {
		t.Fatalf("expected IsStockItem=true on inventory-type item")
	}

	// A single default warehouse (MAIN) must have been auto-created.
	var whs []models.Warehouse
	if err := db.Where("company_id = ?", companyID).Find(&whs).Error; err != nil {
		t.Fatalf("load warehouses: %v", err)
	}
	if len(whs) != 1 {
		t.Fatalf("expected 1 warehouse auto-created, got %d", len(whs))
	}
	w := whs[0]
	if w.Code != "MAIN" {
		t.Errorf("expected default warehouse code MAIN, got %q", w.Code)
	}
	if !w.IsDefault {
		t.Errorf("expected IsDefault=true on auto-created warehouse")
	}
	if !w.IsActive {
		t.Errorf("expected IsActive=true on auto-created warehouse")
	}
}

// TestProductServiceCreate_PS1_NoWarehouseForServiceItem verifies that
// saving a non-stock item (service type) does NOT auto-create a
// warehouse. Only stock items need the warehouse.
func TestProductServiceCreate_PS1_NoWarehouseForServiceItem(t *testing.T) {
	db := testProductServiceValidationDB(t)
	server := &Server{DB: db}
	user := seedPSUser(t, db)

	companyID := seedValidationCompany(t, db, "Acme")
	revenueID := seedValidationAccount(t, db, companyID, "4000", models.RootRevenue, models.DetailServiceRevenue)

	app := productServiceValidationApp(server, user, companyID)
	resp := performFormRequest(t, app, http.MethodPost, "/products-services", url.Values{
		"name":               {"Consulting"},
		"type":               {string(models.ProductServiceTypeService)},
		"structure_type":     {string(models.ItemStructureSingle)},
		"default_price":      {"120.00"},
		"revenue_account_id": {fmt.Sprintf("%d", revenueID)},
	}, "")

	if resp.StatusCode != http.StatusSeeOther {
		body := readResponseBody(t, resp)
		t.Fatalf("expected 303 redirect on success, got %d\nbody: %s", resp.StatusCode, body)
	}

	var count int64
	db.Model(&models.Warehouse{}).Where("company_id = ?", companyID).Count(&count)
	if count != 0 {
		t.Fatalf("expected 0 warehouses for service-only item, got %d", count)
	}
}

// TestProductServiceCreate_PS1_Idempotent verifies that creating a
// second stock item does NOT create a second warehouse. The first
// call provisions MAIN; subsequent calls are no-ops.
func TestProductServiceCreate_PS1_Idempotent(t *testing.T) {
	db := testProductServiceValidationDB(t)
	server := &Server{DB: db}
	user := seedPSUser(t, db)

	companyID := seedValidationCompany(t, db, "Acme")
	revenueID := seedValidationAccount(t, db, companyID, "4000", models.RootRevenue, models.DetailServiceRevenue)
	cogsID := seedValidationAccount(t, db, companyID, "5000", models.RootCostOfSales, models.DetailCostOfGoodsSold)
	invID := seedValidationAccount(t, db, companyID, "1300", models.RootAsset, models.DetailInventory)

	app := productServiceValidationApp(server, user, companyID)

	post := func(name string) *http.Response {
		return performFormRequest(t, app, http.MethodPost, "/products-services", url.Values{
			"name":                 {name},
			"type":                 {string(models.ProductServiceTypeInventory)},
			"structure_type":       {string(models.ItemStructureSingle)},
			"default_price":        {"50.00"},
			"revenue_account_id":   {fmt.Sprintf("%d", revenueID)},
			"cogs_account_id":      {fmt.Sprintf("%d", cogsID)},
			"inventory_account_id": {fmt.Sprintf("%d", invID)},
		}, "")
	}

	if r := post("Widget A"); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("first item: expected 303, got %d", r.StatusCode)
	}
	if r := post("Widget B"); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("second item: expected 303, got %d", r.StatusCode)
	}
	if r := post("Widget C"); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("third item: expected 303, got %d", r.StatusCode)
	}

	var count int64
	db.Model(&models.Warehouse{}).Where("company_id = ?", companyID).Count(&count)
	if count != 1 {
		t.Fatalf("expected idempotency: 1 warehouse after 3 stock items, got %d", count)
	}
}
