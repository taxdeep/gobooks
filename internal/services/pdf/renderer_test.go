// 遵循project_guide.md
package pdf

import (
	"strings"
	"testing"

	"balanciz/internal/models"
)

func TestRenderHTMLEmptySchemaProducesValidShell(t *testing.T) {
	in := RenderInput{
		DocumentType: string(models.PDFDocInvoice),
		Schema:       DefaultSchema(),
		Values:       DocumentValues{},
		Lines:        nil,
	}
	out, err := RenderHTML(in)
	if err != nil {
		t.Fatalf("RenderHTML error: %v", err)
	}
	wantSubstrings := []string{
		"<!doctype html>",
		"@page { size: Letter portrait;",
		"--gb-pdf-accent:#0066cc",
		"<body><main></main></body></html>",
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q\nfull:\n%s", s, out)
		}
	}
}

func TestRenderHTMLInvoicePresetClassicSmoke(t *testing.T) {
	preset := buildPresetSchema(models.PDFDocInvoice, PresetClassic)
	in := RenderInput{
		DocumentType: string(models.PDFDocInvoice),
		Schema:       preset,
		Values: DocumentValues{
			"company.name":               "Acme Corp",
			"company.address":            "1 Main St\nSpringfield, IL 62701",
			"invoice.number":             "IN-1042",
			"invoice.date":               "2026-04-22",
			"invoice.due_date":           "2026-05-22",
			"invoice.terms":              "Net 30",
			"invoice.customer_po_number": "PO-7788",
			"invoice.subtotal":           "$1,000.00",
			"invoice.tax_total":          "$50.00",
			"invoice.amount":             "$1,050.00",
			"invoice.balance_due":        "$1,050.00",
			"customer.name":              "Foo Customer",
			"customer.bill_to":           "200 Park Ave\nNew York, NY 10166",
		},
		Lines: []LineValues{
			{
				"lines.description": "Consulting hours",
				"lines.qty":         "10",
				"lines.unit_price":  "$100.00",
				"lines.line_total":  "$1,000.00",
			},
		},
	}
	out, err := RenderHTML(in)
	if err != nil {
		t.Fatalf("RenderHTML error: %v", err)
	}
	mustContain := []string{
		"INVOICE",                   // header right title
		"Acme Corp",                 // company name
		"IN-1042",                   // invoice number
		"PO-7788",                   // customer PO
		"Foo Customer",              // bill-to name
		"200 Park Ave",              // multi-line address line 1
		"<br>New York",              // address rendered with <br>
		"Consulting hours",          // line description
		"$1,000.00",                 // line total
		"Balance Due",               // totals row label
		"Thank you for your business.", // footer text
		"gb-pdf-totals-grand",       // emphasis class on the last totals row
	}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("rendered HTML missing %q", s)
		}
	}
}

func TestRenderHTMLHidesFieldsWithHideWhenEmpty(t *testing.T) {
	preset := buildPresetSchema(models.PDFDocInvoice, PresetClassic)
	// Only required fields populated — Customer PO# / SO# / Due date should
	// hide rather than render an empty "Customer PO#: " label row.
	in := RenderInput{
		DocumentType: string(models.PDFDocInvoice),
		Schema:       preset,
		Values: DocumentValues{
			"invoice.number":   "IN-1",
			"invoice.date":     "2026-04-22",
			"customer.name":    "X",
		},
	}
	out, err := RenderHTML(in)
	if err != nil {
		t.Fatalf("RenderHTML error: %v", err)
	}
	// Plain "Customer PO#:" with empty value would appear if HideWhenEmpty
	// were ignored. Make sure it doesn't show up.
	if strings.Contains(out, "Customer PO#:") {
		t.Error("expected Customer PO# row to hide when empty, but label rendered")
	}
}

func TestRenderHTMLPackingSlipPresetHidesPrices(t *testing.T) {
	preset := buildPresetSchema(models.PDFDocShipment, PresetClassic)
	in := RenderInput{
		DocumentType: string(models.PDFDocShipment),
		Schema:       preset,
		Values:       DocumentValues{"shipment.number": "SHIP-1"},
		Lines: []LineValues{
			{"lines.product_name": "Widget", "lines.qty": "5"},
		},
	}
	out, err := RenderHTML(in)
	if err != nil {
		t.Fatalf("RenderHTML error: %v", err)
	}
	// Packing slip should NOT include unit_price / line_total columns or any
	// totals block (showTotals=false in the binding).
	if strings.Contains(out, "Unit Price") || strings.Contains(out, "Total</th>") {
		t.Error("packing slip preset must not show price/total columns")
	}
	// `gb-pdf-totals` appears in the CSS rules; check for the rendered
	// section wrapper instead, which only exists when the block is visible.
	if strings.Contains(out, "gb-pdf-block-totals") {
		t.Error("packing slip preset must not render the totals block")
	}
}

func TestExpandTemplateVarsSubstitutesAndStripsUnknown(t *testing.T) {
	values := DocumentValues{
		"invoice.number": "IN-9",
	}
	got := expandTemplateVars("Order #{{invoice.number}} (ref {{invoice.unknown}})", models.PDFDocInvoice, values)
	want := "Order #IN-9 (ref )"
	if got != want {
		t.Errorf("expandTemplateVars: got %q want %q", got, want)
	}
}

func TestAllPresetsHaveBlocks(t *testing.T) {
	presets := AllSystemPresets()
	if len(presets) != 18 {
		t.Fatalf("expected 18 presets (3 styles × 6 doc types), got %d", len(presets))
	}
	seen := map[string]bool{}
	for _, p := range presets {
		key := string(p.DocumentType) + "/" + string(p.Style)
		if seen[key] {
			t.Errorf("duplicate preset %s", key)
		}
		seen[key] = true
		if len(p.Schema.Blocks) == 0 {
			t.Errorf("preset %s/%s has no blocks", p.DocumentType, p.Style)
		}
	}
}
