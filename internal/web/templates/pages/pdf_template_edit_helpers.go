// 遵循project_guide.md
package pages

import (
	"encoding/json"

	"balanciz/internal/models"
	"balanciz/internal/services/pdf"
)

// PDFTemplateEditVM is the view-model for the Phase 3 (G7) visual editor.
type PDFTemplateEditVM struct {
	HasCompany       bool
	Template         models.PDFTemplate
	DocType          string
	DocTypeLabel     string
	PrettySchemaJSON string     // pretty-printed initial schema for the editor
	Fields           []pdf.Field // available fields for this doc type, grouped+sorted
	BlockTypes       []string    // canonical block-type list for the "Add Block" picker
	IsSystemReadOnly bool        // true when editing a system row — UI shows clone-first banner
	FlashMsg         string
	FlashErr         string
}

// PDFDocTypeDisplayLabel returns a human-readable doc-type name shown in
// the editor's title bar and in the management list. Re-exported from
// pdf_templates_helpers.go's pdfDocTypeLabel via this small public wrapper
// so the handler package doesn't need to import templ-internal helpers.
func PDFDocTypeDisplayLabel(doc models.PDFDocumentType) string {
	switch doc {
	case models.PDFDocInvoice:
		return "Invoice"
	case models.PDFDocQuote:
		return "Quote"
	case models.PDFDocSalesOrder:
		return "Sales Order"
	case models.PDFDocBill:
		return "Bill"
	case models.PDFDocPurchaseOrder:
		return "Purchase Order"
	case models.PDFDocShipment:
		return "Packing Slip / Shipment"
	}
	return string(doc)
}

func pdfEditorBoolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// pdfEditorBlockTypeLabel returns the human-readable label shown in the
// "Add block" dropdown in the visual editor.
func pdfEditorBlockTypeLabel(t string) string {
	switch t {
	case "header":
		return "Header (logo + doc title)"
	case "two_col":
		return "Two-column band (bill-to / meta)"
	case "lines_table":
		return "Line items table"
	case "totals":
		return "Totals block"
	case "text":
		return "Text block (notes / footer)"
	case "spacer":
		return "Spacer (vertical gap)"
	}
	return t
}

// PDFTemplateEditFieldsJSON serialises the available-field list for Alpine.
// Each entry exposes Key + Label + Type + Group so the editor can group the
// field-picker dropdown and validate types client-side.
func PDFTemplateEditFieldsJSON(fields []pdf.Field) string {
	type row struct {
		Key   string `json:"key"`
		Label string `json:"label"`
		Type  string `json:"type"`
		Group string `json:"group"`
		Scope string `json:"scope"`
	}
	out := make([]row, 0, len(fields))
	for _, f := range fields {
		out = append(out, row{
			Key:   f.Key,
			Label: f.Label,
			Type:  string(f.Type),
			Group: f.Group,
			Scope: string(f.Scope),
		})
	}
	b, _ := json.Marshal(out)
	return string(b)
}
