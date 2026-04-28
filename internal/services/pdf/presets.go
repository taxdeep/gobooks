// 遵循project_guide.md
package pdf

import (
	"encoding/json"

	"balanciz/internal/models"
)

// presets.go — 18 system-shipped templates: 3 styles × 6 document types.
//
// Layout is identical across the styles (Header → TwoCol bill-to/info →
// LinesTable → Totals → Footer text); the difference is theme + which
// columns appear by default. Doc-specific field bindings (invoice.number
// vs quote.number etc.) come from a small per-doc-type binding table that
// declares the right field keys for each slot.
//
// Editor "clone" pattern: a company-specific row is INSERT-ed via a deep
// JSON copy of the chosen system preset, so subsequent edits don't bleed
// back into the system row.

// PresetStyle is the visual variant identifier used as part of the
// system-template name and to drive theme defaults.
type PresetStyle string

const (
	PresetClassic PresetStyle = "classic"
	PresetModern  PresetStyle = "modern"
	PresetMinimal PresetStyle = "minimal"
)

// allPresetStyles is the iteration order used by the seeder + tests.
var allPresetStyles = []PresetStyle{PresetClassic, PresetModern, PresetMinimal}

// SystemPreset describes one of the seeded rows.
type SystemPreset struct {
	DocumentType models.PDFDocumentType
	Style        PresetStyle
	Name         string
	Description  string
	Schema       Schema
	IsDefault    bool // marks the default for company_id IS NULL when true
}

// AllSystemPresets returns the canonical 18-row preset list. The seeder
// uses this as the source of truth — re-running seed is idempotent
// (UPSERT by document_type + style + system flag).
func AllSystemPresets() []SystemPreset {
	out := make([]SystemPreset, 0, len(models.AllPDFDocumentTypes)*len(allPresetStyles))
	for _, doc := range models.AllPDFDocumentTypes {
		for _, style := range allPresetStyles {
			out = append(out, SystemPreset{
				DocumentType: doc,
				Style:        style,
				Name:         presetDisplayName(doc, style),
				Description:  presetDescription(style),
				Schema:       buildPresetSchema(doc, style),
				// Classic is the global fallback when a company hasn't
				// chosen its own default for this doc type.
				IsDefault: style == PresetClassic,
			})
		}
	}
	return out
}

func presetDisplayName(doc models.PDFDocumentType, style PresetStyle) string {
	docLabel := docTitleFor(doc)
	switch style {
	case PresetClassic:
		return docLabel + " — Classic"
	case PresetModern:
		return docLabel + " — Modern"
	case PresetMinimal:
		return docLabel + " — Minimal"
	}
	return docLabel
}

func presetDescription(style PresetStyle) string {
	switch style {
	case PresetClassic:
		return "Traditional letterhead layout with a blue accent rule. Safe default for most documents."
	case PresetModern:
		return "Tighter typography on a navy accent. Sans-serif, generous whitespace."
	case PresetMinimal:
		return "Monochrome, low-ink layout. Best for self-printed copies."
	}
	return ""
}

// buildPresetSchema is the single source for all 18 system templates. The
// scaffold is the same per style; per-doc field bindings are pulled from
// docBindings. Saved templates clone this output.
func buildPresetSchema(doc models.PDFDocumentType, style PresetStyle) Schema {
	bindings := docBindings[doc]
	theme := themeForStyle(style)

	headerCfg := mustEncode(HeaderConfig{
		Left: []FieldRef{
			{Type: "image", Field: "company.logo", HideWhenEmpty: true},
			{Type: "field", Field: "company.name", EmphasisLevel: 1},
			{Type: "field", Field: "company.address", HideWhenEmpty: true},
		},
		Right: []FieldRef{
			{Type: "literal", Value: bindings.docTitle, EmphasisLevel: 2},
			{Type: "field", Field: bindings.docNumber, Label: bindings.docNumberLabel + ": "},
			{Type: "field", Field: bindings.docDate, Label: bindings.docDateLabel + ": "},
		},
	})

	twoColCfg := mustEncode(TwoColConfig{
		LeftTitle:  bindings.counterpartyTitle,
		Left:       bindings.counterpartyRefs,
		RightTitle: "Document",
		Right:      bindings.metaRefs,
	})

	linesCfg := mustEncode(LinesTableConfig{
		Columns: bindings.lineColumns,
	})

	totalsCfg := mustEncode(TotalsConfig{
		Rows:                   bindings.totalsRows,
		ShowGrandTotalEmphasis: true,
	})

	notesCfg := mustEncode(TextConfig{
		Title: "Notes",
		Body:  bindings.notesTemplate,
	})

	footerCfg := mustEncode(TextConfig{
		Title: "",
		Body:  bindings.footerText,
		Align: "center",
		Italic: true,
	})

	return Schema{
		Version: 1,
		Page: Page{
			Size:        "Letter",
			Orientation: "portrait",
			Margins:     [4]int{40, 40, 40, 40},
		},
		Theme: theme,
		Blocks: []Block{
			{ID: "blk_header",  Type: BlockTypeHeader,     Visible: true, Config: headerCfg},
			{ID: "blk_meta",    Type: BlockTypeTwoCol,     Visible: true, Config: twoColCfg},
			{ID: "blk_lines",   Type: BlockTypeLinesTable, Visible: true, Config: linesCfg},
			{ID: "blk_totals",  Type: BlockTypeTotals,     Visible: bindings.showTotals, Config: totalsCfg},
			{ID: "blk_notes",   Type: BlockTypeText,       Visible: bindings.notesTemplate != "", Config: notesCfg},
			{ID: "blk_footer",  Type: BlockTypeText,       Visible: true, Config: footerCfg},
		},
	}
}

func themeForStyle(style PresetStyle) Theme {
	switch style {
	case PresetModern:
		return Theme{
			AccentColor: "#1a1a2e",
			FontFamily:  "Inter",
			FontSizePt:  10,
			LineHeight:  "1.45",
			TextColor:   "#1a1a1a",
			MutedColor:  "#6b7280",
		}
	case PresetMinimal:
		return Theme{
			AccentColor: "#111827",
			FontFamily:  "Helvetica",
			FontSizePt:  10,
			LineHeight:  "1.4",
			TextColor:   "#111827",
			MutedColor:  "#9ca3af",
		}
	default: // Classic
		return Theme{
			AccentColor: "#0066cc",
			FontFamily:  "Times",
			FontSizePt:  11,
			LineHeight:  "1.4",
			TextColor:   "#1a1a1a",
			MutedColor:  "#5b6470",
		}
	}
}

// docTitleFor returns the human-readable title used in preset names + as
// the default header right-side title literal (e.g. "INVOICE").
func docTitleFor(doc models.PDFDocumentType) string {
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
		return "Shipment"
	}
	return string(doc)
}

// docBinding is the per-doc-type slot map for the shared preset scaffold.
type docBinding struct {
	docTitle           string // header right-side title literal (UPPERCASE convention)
	docNumber          string // field key for the document number
	docNumberLabel     string // header label for docNumber
	docDate            string // field key for the document date
	docDateLabel       string
	counterpartyTitle  string // "Bill To" / "Vendor" etc.
	counterpartyRefs   []FieldRef
	metaRefs           []FieldRef // right column of the two-col band
	lineColumns        []LinesTableColumn
	totalsRows         []TotalsRow
	showTotals         bool
	notesTemplate      string // free-form text shown in the Notes block (supports {{field}})
	footerText         string
}

var docBindings = map[models.PDFDocumentType]docBinding{
	models.PDFDocInvoice: {
		docTitle:       "INVOICE",
		docNumber:      "invoice.number",
		docNumberLabel: "Invoice #",
		docDate:        "invoice.date",
		docDateLabel:   "Date",
		counterpartyTitle: "Bill To",
		counterpartyRefs: []FieldRef{
			{Type: "field", Field: "customer.name", EmphasisLevel: 1},
			{Type: "field", Field: "customer.bill_to", HideWhenEmpty: true},
			{Type: "field", Field: "customer.email", HideWhenEmpty: true},
		},
		metaRefs: []FieldRef{
			{Type: "field", Field: "invoice.due_date", Label: "Due Date: ", HideWhenEmpty: true},
			{Type: "field", Field: "invoice.terms", Label: "Terms: ", HideWhenEmpty: true},
			{Type: "field", Field: "invoice.customer_po_number", Label: "Customer PO#: ", HideWhenEmpty: true},
			{Type: "field", Field: "invoice.sales_order_number", Label: "SO #: ", HideWhenEmpty: true},
		},
		lineColumns: []LinesTableColumn{
			{Field: "lines.row_number",   WidthPct: 5},
			{Field: "lines.description",  WidthPct: 50},
			{Field: "lines.qty",          WidthPct: 10},
			{Field: "lines.unit_price",   WidthPct: 15},
			{Field: "lines.line_total",   WidthPct: 20},
		},
		totalsRows: []TotalsRow{
			{Field: "invoice.subtotal"},
			{Field: "invoice.tax_total"},
			{Field: "invoice.amount", LabelOverride: "Total"},
			{Field: "invoice.balance_due"},
		},
		showTotals:    true,
		notesTemplate: "{{invoice.memo}}",
		footerText:    "Thank you for your business.",
	},
	models.PDFDocQuote: {
		docTitle:       "QUOTE",
		docNumber:      "quote.number",
		docNumberLabel: "Quote #",
		docDate:        "quote.date",
		docDateLabel:   "Date",
		counterpartyTitle: "Prepared For",
		counterpartyRefs: []FieldRef{
			{Type: "field", Field: "customer.name", EmphasisLevel: 1},
			{Type: "field", Field: "customer.bill_to", HideWhenEmpty: true},
			{Type: "field", Field: "customer.email", HideWhenEmpty: true},
		},
		metaRefs: []FieldRef{
			{Type: "field", Field: "quote.valid_until", Label: "Valid Until: ", HideWhenEmpty: true},
			{Type: "field", Field: "quote.currency",    Label: "Currency: ",    HideWhenEmpty: true},
		},
		lineColumns: []LinesTableColumn{
			{Field: "lines.row_number",   WidthPct: 5},
			{Field: "lines.description",  WidthPct: 55},
			{Field: "lines.qty",          WidthPct: 10},
			{Field: "lines.unit_price",   WidthPct: 15},
			{Field: "lines.line_total",   WidthPct: 15},
		},
		totalsRows: []TotalsRow{
			{Field: "quote.subtotal"},
			{Field: "quote.tax_total"},
			{Field: "quote.total"},
		},
		showTotals:    true,
		notesTemplate: "{{quote.notes}}",
		footerText:    "Quote subject to change. Acceptance constitutes a binding order.",
	},
	models.PDFDocSalesOrder: {
		docTitle:       "SALES ORDER",
		docNumber:      "sales_order.number",
		docNumberLabel: "SO #",
		docDate:        "sales_order.date",
		docDateLabel:   "Order Date",
		counterpartyTitle: "Customer",
		counterpartyRefs: []FieldRef{
			{Type: "field", Field: "customer.name", EmphasisLevel: 1},
			{Type: "field", Field: "customer.bill_to", HideWhenEmpty: true},
			{Type: "field", Field: "customer.ship_to", Label: "Ship to: ", HideWhenEmpty: true},
		},
		metaRefs: []FieldRef{
			{Type: "field", Field: "sales_order.required_by",        Label: "Required By: ", HideWhenEmpty: true},
			{Type: "field", Field: "sales_order.customer_po_number", Label: "Customer PO#: ", HideWhenEmpty: true},
			{Type: "field", Field: "sales_order.quote_number",       Label: "Quote #: ",     HideWhenEmpty: true},
		},
		lineColumns: []LinesTableColumn{
			{Field: "lines.row_number",   WidthPct: 5},
			{Field: "lines.description",  WidthPct: 55},
			{Field: "lines.qty",          WidthPct: 10},
			{Field: "lines.unit_price",   WidthPct: 15},
			{Field: "lines.line_total",   WidthPct: 15},
		},
		totalsRows: []TotalsRow{
			{Field: "sales_order.subtotal"},
			{Field: "sales_order.tax_total"},
			{Field: "sales_order.total"},
		},
		showTotals:    true,
		notesTemplate: "{{sales_order.notes}}",
		footerText:    "",
	},
	models.PDFDocBill: {
		docTitle:       "BILL",
		docNumber:      "bill.number",
		docNumberLabel: "Bill #",
		docDate:        "bill.date",
		docDateLabel:   "Date",
		counterpartyTitle: "Vendor",
		counterpartyRefs: []FieldRef{
			{Type: "field", Field: "vendor.name", EmphasisLevel: 1},
			{Type: "field", Field: "vendor.address", HideWhenEmpty: true},
			{Type: "field", Field: "vendor.email",   HideWhenEmpty: true},
		},
		metaRefs: []FieldRef{
			{Type: "field", Field: "bill.due_date",              Label: "Due Date: ",      HideWhenEmpty: true},
			{Type: "field", Field: "bill.terms",                 Label: "Terms: ",         HideWhenEmpty: true},
			{Type: "field", Field: "bill.vendor_invoice_number", Label: "Vendor Inv #: ",  HideWhenEmpty: true},
		},
		lineColumns: []LinesTableColumn{
			{Field: "lines.row_number",   WidthPct: 5},
			{Field: "lines.description",  WidthPct: 55},
			{Field: "lines.qty",          WidthPct: 10},
			{Field: "lines.unit_price",   WidthPct: 15},
			{Field: "lines.line_total",   WidthPct: 15},
		},
		totalsRows: []TotalsRow{
			{Field: "bill.subtotal"},
			{Field: "bill.tax_total"},
			{Field: "bill.amount", LabelOverride: "Total"},
			{Field: "bill.balance_due"},
		},
		showTotals:    true,
		notesTemplate: "{{bill.memo}}",
		footerText:    "",
	},
	models.PDFDocPurchaseOrder: {
		docTitle:       "PURCHASE ORDER",
		docNumber:      "purchase_order.number",
		docNumberLabel: "PO #",
		docDate:        "purchase_order.date",
		docDateLabel:   "Order Date",
		counterpartyTitle: "Vendor",
		counterpartyRefs: []FieldRef{
			{Type: "field", Field: "vendor.name", EmphasisLevel: 1},
			{Type: "field", Field: "vendor.address", HideWhenEmpty: true},
			{Type: "field", Field: "vendor.email",   HideWhenEmpty: true},
		},
		metaRefs: []FieldRef{
			{Type: "field", Field: "purchase_order.delivery_date", Label: "Delivery: ", HideWhenEmpty: true},
			{Type: "field", Field: "purchase_order.ship_to",       Label: "Ship to: ",  HideWhenEmpty: true},
		},
		lineColumns: []LinesTableColumn{
			{Field: "lines.row_number",   WidthPct: 5},
			{Field: "lines.description",  WidthPct: 55},
			{Field: "lines.qty",          WidthPct: 10},
			{Field: "lines.unit_price",   WidthPct: 15},
			{Field: "lines.line_total",   WidthPct: 15},
		},
		totalsRows: []TotalsRow{
			{Field: "purchase_order.subtotal"},
			{Field: "purchase_order.tax_total"},
			{Field: "purchase_order.total"},
		},
		showTotals:    true,
		notesTemplate: "{{purchase_order.notes}}",
		footerText:    "Please reference the PO number on all correspondence and shipments.",
	},
	models.PDFDocShipment: {
		docTitle:       "PACKING SLIP",
		docNumber:      "shipment.number",
		docNumberLabel: "Shipment #",
		docDate:        "shipment.date",
		docDateLabel:   "Ship Date",
		counterpartyTitle: "Ship To",
		counterpartyRefs: []FieldRef{
			{Type: "field", Field: "customer.name", EmphasisLevel: 1},
			{Type: "field", Field: "customer.ship_to", HideWhenEmpty: true},
		},
		metaRefs: []FieldRef{
			{Type: "field", Field: "shipment.sales_order_number", Label: "SO #: ",          HideWhenEmpty: true},
			{Type: "field", Field: "shipment.customer_po_number", Label: "Customer PO#: ",  HideWhenEmpty: true},
			{Type: "field", Field: "shipment.carrier",            Label: "Carrier: ",       HideWhenEmpty: true},
			{Type: "field", Field: "shipment.tracking_number",    Label: "Tracking #: ",    HideWhenEmpty: true},
		},
		lineColumns: []LinesTableColumn{
			{Field: "lines.row_number",   WidthPct: 5},
			{Field: "lines.product_name", WidthPct: 35},
			{Field: "lines.description",  WidthPct: 45},
			{Field: "lines.qty",          WidthPct: 15},
		},
		totalsRows:    nil,
		showTotals:    false, // packing slips don't show prices/totals
		notesTemplate: "{{shipment.notes}}",
		footerText:    "Inspect contents on receipt and report any discrepancies within 7 days.",
	},
}

// mustEncode is a tiny convenience for the literal preset table —
// the structs above are all pure data so encoding never fails.
func mustEncode(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
