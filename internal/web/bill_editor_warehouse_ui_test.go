// 遵循project_guide.md
package web

// bill_editor_warehouse_ui_test.go — locks the single-vs-multi
// warehouse rendering contract on the Bill editor. A single active
// warehouse auto-binds via hidden input (no picker, no required).
// Multiple warehouses surface the type-ahead combobox with the
// warehouse_id input marked required so stock lines cannot post
// without a deliberate pick (avoids silent routing to NULL).

import (
	"context"
	"strings"
	"testing"

	"gobooks/internal/models"
	"gobooks/internal/web/templates/pages"
)

func renderBillEditor(t *testing.T, vm pages.BillEditorVM) string {
	t.Helper()
	var sb strings.Builder
	if err := pages.BillEditor(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render bill editor: %v", err)
	}
	return sb.String()
}

func TestBillEditor_SingleWarehouse_AutoBoundHiddenInput(t *testing.T) {
	vm := pages.BillEditorVM{
		HasCompany:       true,
		BillDate:         "2026-04-21",
		DueDate:          "2026-05-21",
		AccountsJSON:     "[]",
		TaxCodesJSON:     "[]",
		TasksJSON:        "[]",
		ProductsJSON:     "[]",
		InitialLinesJSON: "[]",
		PaymentTermsJSON: "[]",
		VendorsTermsJSON: "{}",
		WarehousesJSON:   `[{"id":5,"code":"MAIN","name":"Main"}]`,
		Warehouses: []models.Warehouse{
			{ID: 5, Code: "MAIN", Name: "Main Warehouse", IsActive: true, IsDefault: true},
		},
	}
	html := renderBillEditor(t, vm)

	// Hidden input carries the sole warehouse ID; no required attr
	// (nothing for the operator to pick).
	if !strings.Contains(html, `name="warehouse_id" value="5"`) {
		t.Errorf("expected hidden warehouse_id=5 for single warehouse, got:\n%s", html)
	}
	if !strings.Contains(html, `type="hidden"`) {
		t.Errorf("expected a type=hidden input in single-warehouse bill editor")
	}
	// Combobox must NOT render in single-warehouse case.
	if strings.Contains(html, `data-warehouses=`) {
		t.Errorf("combobox data-warehouses attribute should not appear for single warehouse")
	}
	// Name + code displayed read-only.
	for _, want := range []string{"Main Warehouse", "MAIN"} {
		if !strings.Contains(html, want) {
			t.Errorf("expected display to include %q", want)
		}
	}
}

func TestBillEditor_MultiWarehouse_RendersCombobox(t *testing.T) {
	vm := pages.BillEditorVM{
		HasCompany:       true,
		BillDate:         "2026-04-21",
		DueDate:          "2026-05-21",
		AccountsJSON:     "[]",
		TaxCodesJSON:     "[]",
		TasksJSON:        "[]",
		ProductsJSON:     "[]",
		InitialLinesJSON: "[]",
		PaymentTermsJSON: "[]",
		VendorsTermsJSON: "{}",
		WarehousesJSON:   `[{"id":1,"code":"MAIN","name":"Main","label":"Main (MAIN)","search":"main"},{"id":2,"code":"BBY","name":"Burnaby","label":"Burnaby (BBY)","search":"burnaby bby"}]`,
		Warehouses: []models.Warehouse{
			{ID: 1, Code: "MAIN", Name: "Main", IsActive: true},
			{ID: 2, Code: "BBY", Name: "Burnaby", IsActive: true},
		},
	}
	html := renderBillEditor(t, vm)

	// Combobox must render and expose the warehouses JSON.
	if !strings.Contains(html, `data-warehouses=`) {
		t.Errorf("expected combobox data-warehouses attribute for multi-warehouse, got:\n%s", html)
	}
	// Hidden input still carries warehouse_id, but via :value binding.
	if !strings.Contains(html, `name="warehouse_id"`) {
		t.Errorf("expected warehouse_id hidden input in combobox form")
	}
	// Required — operators cannot submit a stock-line bill without
	// a deliberate warehouse pick when there are 2+ to choose from.
	if !strings.Contains(html, `required`) {
		t.Errorf("expected required on warehouse combobox input")
	}
}

func TestBillEditor_NoWarehouses_NoPickerNoHidden(t *testing.T) {
	vm := pages.BillEditorVM{
		HasCompany:       true,
		BillDate:         "2026-04-21",
		DueDate:          "2026-05-21",
		AccountsJSON:     "[]",
		TaxCodesJSON:     "[]",
		TasksJSON:        "[]",
		ProductsJSON:     "[]",
		InitialLinesJSON: "[]",
		PaymentTermsJSON: "[]",
		VendorsTermsJSON: "{}",
		WarehousesJSON:   `[]`,
		Warehouses:       nil,
	}
	html := renderBillEditor(t, vm)
	if strings.Contains(html, `name="warehouse_id"`) {
		t.Errorf("expected no warehouse_id field when there are zero warehouses")
	}
}
