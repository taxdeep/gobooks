package web

import (
	"context"
	"strings"
	"testing"

	"gobooks/internal/models"
	"gobooks/internal/web/templates/pages"
)

func TestInvoiceEditor_UsesCustomerSmartPickerContextAndMemoStateGuards(t *testing.T) {
	vm := pages.InvoiceEditorVM{
		HasCompany:    true,
		IsEdit:        true,
		EditingID:     42,
		CustomerID:    "7",
		InvoiceNumber: "INV-UI-001",
		InvoiceDate:   "2026-04-10",
		Memo:          "Draft memo",
		Customers: []models.Customer{
			{ID: 7, Name: "UI Customer"},
		},
	}

	var sb strings.Builder
	if err := pages.InvoiceEditor(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render invoice editor: %v", err)
	}
	html := sb.String()

	for _, want := range []string{
		`data-context="invoice_editor_customer"`,
		`:disabled="invoiceId === 0 || memoAssist.loading || memoAssist.visible"`,
		`Save the draft first to use AI assist.`,
		`No suggestion available right now.`,
		`/static/invoice_editor.js?v=15`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected invoice editor HTML to contain %q", want)
		}
	}
	if strings.Contains(html, `x-init="init()"`) {
		t.Fatal("invoice editor should rely on Alpine auto-init and must not call init() twice")
	}
}
