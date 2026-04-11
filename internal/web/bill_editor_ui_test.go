package web

import (
	"context"
	"strings"
	"testing"

	"gobooks/internal/web/templates/pages"
)

func TestBillEditor_UsesDarkSurfaceInputsAndSingleInit(t *testing.T) {
	vm := pages.BillEditorVM{
		HasCompany:            true,
		BillDate:              "2026-04-10",
		DueDate:               "2026-05-10",
		MultiCurrencyEnabled:  true,
		BaseCurrencyCode:      "CAD",
		AccountsJSON:          "[]",
		TaxCodesJSON:          "[]",
		TasksJSON:             "[]",
		InitialLinesJSON:      "[]",
		PaymentTermsJSON:      "[]",
		VendorsTermsJSON:      "{}",
	}

	var sb strings.Builder
	if err := pages.BillEditor(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render bill editor: %v", err)
	}
	html := sb.String()

	for _, want := range []string{
		`placeholder="Description" autocomplete="off" @input="calcLine(idx)" class="block w-full rounded-md bg-surface px-2 py-1.5 text-body text-text placeholder:text-text-muted2 outline-none"`,
		`placeholder="0.00" class="block w-full rounded-md border border-border-input bg-surface px-2 py-1.5 text-body text-text placeholder:text-text-muted2 outline-none focus:ring-2 focus:ring-primary-focus"`,
		`class="w-24 rounded-md border border-border-input bg-surface px-2 py-0.5 text-right text-body text-text tabular-nums outline-none focus:ring-2 focus:ring-primary-focus"`,
		`/static/bill_editor.js?v=7`,
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
		`border border-border-input bg-surface px-3 py-2 text-body text-text placeholder:text-text-muted2 outline-none focus:ring-2 focus:ring-primary-focus`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected bill editor HTML to contain %q", want)
		}
	}
}
