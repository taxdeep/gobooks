package pages

import (
	"context"
	"strings"
	"testing"

	"balanciz/internal/models"
)

func TestPDFTemplateEditMountsReactGrapesJSIsland(t *testing.T) {
	var sb strings.Builder
	vm := PDFTemplateEditVM{
		HasCompany:       true,
		Template:         models.PDFTemplate{ID: 7, Name: "Invoice default", Description: "Test template"},
		DocType:          string(models.PDFDocInvoice),
		DocTypeLabel:     "Invoice",
		PrettySchemaJSON: `{"version":1,"page":{"size":"Letter","orientation":"portrait","margins":[40,40,40,40]},"theme":{"accent_color":"#0066cc","font_family":"Inter","font_size_pt":11,"line_height":"1.4","text_color":"#1a1a1a","muted_color":"#6b7280"},"blocks":[]}`,
	}

	if err := PDFTemplateEdit(vm).Render(context.Background(), &sb); err != nil {
		t.Fatal(err)
	}
	html := sb.String()
	for _, want := range []string{
		`id="pdf-template-editor-island"`,
		`data-gb-react="pdf-template-editor"`,
		`data-save-url="/pdf-templates/7/save-schema"`,
		`/static/react/pdf_template_editor.js?v=1`,
		`/static/react/pdf_template_editor.css?v=1`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected PDF template editor HTML to contain %q", want)
		}
	}
}
