package web

import (
	"context"
	"strings"
	"testing"

	"balanciz/internal/web/templates/pages"
)

func TestBillEditor_UsesDarkSurfaceInputsAndSingleInit(t *testing.T) {
	vm := pages.BillEditorVM{
		HasCompany:           true,
		BillDate:             "2026-04-10",
		DueDate:              "2026-05-10",
		MultiCurrencyEnabled: true,
		BaseCurrencyCode:     "CAD",
		AccountsJSON:         "[]",
		TaxCodesJSON:         "[]",
		TasksJSON:            "[]",
		InitialLinesJSON:     "[]",
		PaymentTermsJSON:     "[]",
		VendorsTermsJSON:     "{}",
	}

	var sb strings.Builder
	if err := pages.BillEditor(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render bill editor: %v", err)
	}
	html := sb.String()

	for _, want := range []string{
		// Description input (still amount-only class shape; unchanged by IN.1).
		`placeholder="Description" autocomplete="off" @input="calcLine(idx)" class="block w-full rounded-md bg-surface px-2 py-1.5 text-body text-text placeholder:text-text-muted2 outline-none"`,
		// IN.1 adds numeric alignment (text-right) to Qty / Unit Price / Amount.
		// Keep the dark-surface sentinel substring but allow the text-right variant
		// that IN.1 introduced.
		`placeholder="0.00" class="block w-full rounded-md border border-border-input bg-surface px-2 py-1.5 text-body text-text text-right placeholder:text-text-muted2 outline-none focus:ring-2 focus:ring-primary-focus"`,
		`class="w-24 rounded-md border border-border-input bg-surface px-2 py-0.5 text-right text-body text-text tabular-nums outline-none focus:ring-2 focus:ring-primary-focus"`,
		`/static/bill_editor.js?v=9`,
		`data-entity="vendor"`,
		`data-context="bill.vendor_picker"`,
		`data-field-name="vendor_id"`,
		`@balanciz-picker-create.window="onPickerCreate($event)"`,
		`:name="'line_expense_account_id[' + idx + ']'" :value="line.expense_account_id || ''"`,
		`@input="onCategoryInput(idx)"`,
		`role="combobox"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected bill editor HTML to contain %q", want)
		}
	}
	if strings.Contains(html, `x-init="init()"`) {
		t.Fatal("bill editor should rely on Alpine auto-init and must not call init() twice")
	}
	for _, want := range []string{
		`name="bill_number"`,
		`name="bill_date"`,
		`name="memo"`,
		`name="exchange_rate"`,
		`style="color-scheme: dark;"`,
		// Header inputs now go through pages.fieldClass() (same dark-surface
		// shape as Invoice / Quote / SO after the unified-shell migration).
		`border border-border-input bg-surface px-3 py-2 text-body text-text placeholder:text-text-muted outline-none focus:ring-2 focus:ring-primary-focus`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected bill editor HTML to contain %q", want)
		}
	}
}
