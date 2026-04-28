// 遵循project_guide.md
package web

// credit_note_prefill_test.go — locks the Invoice→CN pre-fill contract
// at the template layer. Without InitialLinesJSON carrying
// OriginalInvoiceLineID + ProductServiceID for each line, stock-line
// CN posts fail with ErrCreditNoteStockItemRequiresOriginalLine
// (IN.5). This test is intentionally a template render check — it
// does not hit the handler / DB — so it stays fast and isolates the
// attribute contract the x-data initializer depends on.

import (
	"context"
	"strings"
	"testing"

	"balanciz/internal/models"
	"balanciz/internal/web/templates/pages"
)

func TestCreditNoteForm_RendersInitialLinesDataAttribute(t *testing.T) {
	vm := pages.CreditNoteFormVM{
		HasCompany:    true,
		CompanyID:     1,
		CustomerID:    42,
		InvoiceID:     7,
		InvoiceNumber: "IN-2026-0007",
		Customers: []models.Customer{
			{Name: "Acme"},
		},
		Reasons: models.AllCreditNoteReasons(),
		InitialLinesJSON: `[{"description":"Widget","revenue_account_id":"5","qty":"2","unit_price":"10.0000","tax_code_id":"","product_service_id":"99","original_invoice_line_id":"123"}]`,
	}

	var sb strings.Builder
	if err := pages.CreditNoteForm(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render credit note form: %v", err)
	}
	html := sb.String()

	// Breadcrumb shows source invoice.
	if !strings.Contains(html, "from") || !strings.Contains(html, "IN-2026-0007") {
		t.Errorf("expected breadcrumb to reference source invoice, got:\n%s", html)
	}
	// Data attribute carries the JSON (HTML-escaped double quotes).
	if !strings.Contains(html, `data-initial-lines=`) {
		t.Errorf("expected data-initial-lines attribute, got:\n%s", html)
	}
	// Hidden trace-key inputs emitted inside the Alpine x-for row.
	for _, want := range []string{
		`name="product_service_id[]"`,
		`name="original_invoice_line_id[]"`,
		`initFromDataset()`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("expected CN form HTML to contain %q", want)
		}
	}
}

func TestCreditNoteForm_StandaloneOmitsBreadcrumb(t *testing.T) {
	vm := pages.CreditNoteFormVM{
		HasCompany: true,
		CompanyID:  1,
		Reasons:    models.AllCreditNoteReasons(),
	}
	var sb strings.Builder
	if err := pages.CreditNoteForm(vm).Render(context.Background(), &sb); err != nil {
		t.Fatalf("render credit note form: %v", err)
	}
	html := sb.String()
	if strings.Contains(html, "Pre-filled from Invoice") {
		t.Errorf("standalone CN should not show pre-fill notice")
	}
}
