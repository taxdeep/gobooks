// 遵循project_guide.md
package pdf

// fields.go — per-document-type field registry.
//
// The registry is the contract between the template editor (which lists
// available fields) and the renderer (which resolves keys to values at
// render time). Templates store registry KEYS (not labels); the editor
// shows labels via lookup so renaming a label never breaks any saved
// template.
//
// Field key naming convention:
//   • doc-level scalar:           "<doc>.<attr>"            e.g. "invoice.number"
//   • doc-level money:            "<doc>.<attr>"            e.g. "invoice.balance_due"
//   • counterparty (cust/vendor): "customer.<attr>" / "vendor.<attr>"
//   • company:                    "company.<attr>"
//   • per-row line column:        "lines.<attr>"            (only valid inside lines_table block)
//
// FieldType drives the renderer's default formatter. Templates can override
// per-FieldRef via the Format field.

import (
	"sort"

	"balanciz/internal/models"
)

// FieldType is the data kind, used by the renderer to pick a formatter.
type FieldType string

const (
	FieldTypeString  FieldType = "string"  // plain text
	FieldTypeMoney   FieldType = "money"   // 2-dp with currency symbol
	FieldTypeDate    FieldType = "date"    // YYYY-MM-DD by default
	FieldTypeNumber  FieldType = "number"  // integer / decimal, no currency
	FieldTypeAddress FieldType = "address" // multi-line; renderer preserves \n
	FieldTypeImage   FieldType = "image"   // base64 data URL or http URL
	FieldTypeRichText FieldType = "rich"   // pre-sanitised HTML; renderer outputs as-is
)

// FieldScope distinguishes top-level fields from line-row fields. Line-row
// fields are only valid inside a lines_table block's column list; the editor
// hides them elsewhere.
type FieldScope string

const (
	FieldScopeDocument FieldScope = "document"
	FieldScopeLine     FieldScope = "line"
)

// Field is one entry in the registry — describes a single available data
// point for a document type.
type Field struct {
	Key   string     `json:"key"`
	Label string     `json:"label"`
	Type  FieldType  `json:"type"`
	Scope FieldScope `json:"scope"`
	// Group is the editor's section heading ("Document", "Customer", "Lines").
	Group string `json:"group"`
}

// commonCompanyFields are appended to every document type — letterhead /
// company contact info that any printed doc needs.
var commonCompanyFields = []Field{
	{Key: "company.name",       Label: "Company Name",       Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Company"},
	{Key: "company.address",    Label: "Company Address",    Type: FieldTypeAddress, Scope: FieldScopeDocument, Group: "Company"},
	{Key: "company.email",      Label: "Company Email",      Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Company"},
	{Key: "company.phone",      Label: "Company Phone",      Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Company"},
	{Key: "company.tax_id",     Label: "Company Tax ID",     Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Company"},
	{Key: "company.logo",       Label: "Company Logo",       Type: FieldTypeImage,   Scope: FieldScopeDocument, Group: "Company"},
	{Key: "doc.printed_at",     Label: "Print Date",         Type: FieldTypeDate,    Scope: FieldScopeDocument, Group: "Document"},
}

// commonLineFields cover the columns shared by every doc that has line items
// (Invoice / Quote / SO / Bill / PO / Shipment).
var commonLineFields = []Field{
	{Key: "lines.row_number",   Label: "#",                  Type: FieldTypeNumber,  Scope: FieldScopeLine, Group: "Line"},
	{Key: "lines.product_name", Label: "Item",               Type: FieldTypeString,  Scope: FieldScopeLine, Group: "Line"},
	{Key: "lines.product_sku",  Label: "SKU",                Type: FieldTypeString,  Scope: FieldScopeLine, Group: "Line"},
	{Key: "lines.description",  Label: "Description",        Type: FieldTypeString,  Scope: FieldScopeLine, Group: "Line"},
	{Key: "lines.qty",          Label: "Quantity",           Type: FieldTypeNumber,  Scope: FieldScopeLine, Group: "Line"},
	{Key: "lines.unit_price",   Label: "Unit Price",         Type: FieldTypeMoney,   Scope: FieldScopeLine, Group: "Line"},
	{Key: "lines.line_net",     Label: "Net",                Type: FieldTypeMoney,   Scope: FieldScopeLine, Group: "Line"},
	{Key: "lines.line_tax",     Label: "Tax",                Type: FieldTypeMoney,   Scope: FieldScopeLine, Group: "Line"},
	{Key: "lines.line_total",   Label: "Total",              Type: FieldTypeMoney,   Scope: FieldScopeLine, Group: "Line"},
	{Key: "lines.tax_code",     Label: "Tax Code",           Type: FieldTypeString,  Scope: FieldScopeLine, Group: "Line"},
}

// invoiceFields are the doc-level fields available on an Invoice template.
var invoiceFields = []Field{
	{Key: "invoice.number",              Label: "Invoice Number",        Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Document"},
	{Key: "invoice.date",                Label: "Invoice Date",          Type: FieldTypeDate,    Scope: FieldScopeDocument, Group: "Document"},
	{Key: "invoice.due_date",            Label: "Due Date",              Type: FieldTypeDate,    Scope: FieldScopeDocument, Group: "Document"},
	{Key: "invoice.terms",               Label: "Payment Terms",         Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Document"},
	{Key: "invoice.memo",                Label: "Memo / Note",           Type: FieldTypeRichText,Scope: FieldScopeDocument, Group: "Document"},
	{Key: "invoice.customer_po_number",  Label: "Customer PO#",          Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Document"},
	{Key: "invoice.sales_order_number",  Label: "Sales Order #",         Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Document"},
	{Key: "invoice.subtotal",            Label: "Subtotal",              Type: FieldTypeMoney,   Scope: FieldScopeDocument, Group: "Totals"},
	{Key: "invoice.tax_total",           Label: "Tax Total",             Type: FieldTypeMoney,   Scope: FieldScopeDocument, Group: "Totals"},
	{Key: "invoice.amount",              Label: "Amount",                Type: FieldTypeMoney,   Scope: FieldScopeDocument, Group: "Totals"},
	{Key: "invoice.balance_due",         Label: "Balance Due",           Type: FieldTypeMoney,   Scope: FieldScopeDocument, Group: "Totals"},
	{Key: "invoice.currency",            Label: "Currency Code",         Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Document"},
	{Key: "customer.name",               Label: "Customer Name",         Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Customer"},
	{Key: "customer.email",              Label: "Customer Email",        Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Customer"},
	{Key: "customer.bill_to",            Label: "Bill To Address",       Type: FieldTypeAddress, Scope: FieldScopeDocument, Group: "Customer"},
	{Key: "customer.ship_to",            Label: "Ship To Address",       Type: FieldTypeAddress, Scope: FieldScopeDocument, Group: "Customer"},
	{Key: "customer.ship_to_label",      Label: "Ship To Label",         Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Customer"},
}

// quoteFields are the doc-level fields available on a Quote template.
var quoteFields = []Field{
	{Key: "quote.number",                Label: "Quote Number",          Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Document"},
	{Key: "quote.date",                  Label: "Quote Date",            Type: FieldTypeDate,    Scope: FieldScopeDocument, Group: "Document"},
	{Key: "quote.valid_until",           Label: "Valid Until",           Type: FieldTypeDate,    Scope: FieldScopeDocument, Group: "Document"},
	{Key: "quote.notes",                 Label: "Customer Notes",        Type: FieldTypeRichText,Scope: FieldScopeDocument, Group: "Document"},
	{Key: "quote.memo",                  Label: "Memo / Internal Note",  Type: FieldTypeRichText,Scope: FieldScopeDocument, Group: "Document"},
	{Key: "quote.subtotal",              Label: "Subtotal",              Type: FieldTypeMoney,   Scope: FieldScopeDocument, Group: "Totals"},
	{Key: "quote.tax_total",             Label: "Tax Total",             Type: FieldTypeMoney,   Scope: FieldScopeDocument, Group: "Totals"},
	{Key: "quote.total",                 Label: "Total",                 Type: FieldTypeMoney,   Scope: FieldScopeDocument, Group: "Totals"},
	{Key: "quote.currency",              Label: "Currency Code",         Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Document"},
	{Key: "customer.name",               Label: "Customer Name",         Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Customer"},
	{Key: "customer.email",              Label: "Customer Email",        Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Customer"},
	{Key: "customer.bill_to",            Label: "Bill To Address",       Type: FieldTypeAddress, Scope: FieldScopeDocument, Group: "Customer"},
}

// salesOrderFields are the doc-level fields available on a SalesOrder template.
var salesOrderFields = []Field{
	{Key: "sales_order.number",          Label: "Sales Order Number",    Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Document"},
	{Key: "sales_order.date",            Label: "Order Date",            Type: FieldTypeDate,    Scope: FieldScopeDocument, Group: "Document"},
	{Key: "sales_order.required_by",     Label: "Required By",           Type: FieldTypeDate,    Scope: FieldScopeDocument, Group: "Document"},
	{Key: "sales_order.customer_po_number", Label: "Customer PO#",       Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Document"},
	{Key: "sales_order.quote_number",    Label: "Quote #",               Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Document"},
	{Key: "sales_order.notes",           Label: "Customer Notes",        Type: FieldTypeRichText,Scope: FieldScopeDocument, Group: "Document"},
	{Key: "sales_order.memo",            Label: "Memo / Internal Note",  Type: FieldTypeRichText,Scope: FieldScopeDocument, Group: "Document"},
	{Key: "sales_order.subtotal",        Label: "Subtotal",              Type: FieldTypeMoney,   Scope: FieldScopeDocument, Group: "Totals"},
	{Key: "sales_order.tax_total",       Label: "Tax Total",             Type: FieldTypeMoney,   Scope: FieldScopeDocument, Group: "Totals"},
	{Key: "sales_order.total",           Label: "Total",                 Type: FieldTypeMoney,   Scope: FieldScopeDocument, Group: "Totals"},
	{Key: "sales_order.currency",        Label: "Currency Code",         Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Document"},
	{Key: "customer.name",               Label: "Customer Name",         Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Customer"},
	{Key: "customer.email",              Label: "Customer Email",        Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Customer"},
	{Key: "customer.bill_to",            Label: "Bill To Address",       Type: FieldTypeAddress, Scope: FieldScopeDocument, Group: "Customer"},
	{Key: "customer.ship_to",            Label: "Ship To Address",       Type: FieldTypeAddress, Scope: FieldScopeDocument, Group: "Customer"},
}

// billFields cover AP-side bill templates.
var billFields = []Field{
	{Key: "bill.number",                 Label: "Bill Number",           Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Document"},
	{Key: "bill.vendor_invoice_number",  Label: "Vendor Invoice #",      Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Document"},
	{Key: "bill.date",                   Label: "Bill Date",             Type: FieldTypeDate,    Scope: FieldScopeDocument, Group: "Document"},
	{Key: "bill.due_date",               Label: "Due Date",              Type: FieldTypeDate,    Scope: FieldScopeDocument, Group: "Document"},
	{Key: "bill.terms",                  Label: "Payment Terms",         Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Document"},
	{Key: "bill.memo",                   Label: "Memo",                  Type: FieldTypeRichText,Scope: FieldScopeDocument, Group: "Document"},
	{Key: "bill.subtotal",               Label: "Subtotal",              Type: FieldTypeMoney,   Scope: FieldScopeDocument, Group: "Totals"},
	{Key: "bill.tax_total",              Label: "Tax Total",             Type: FieldTypeMoney,   Scope: FieldScopeDocument, Group: "Totals"},
	{Key: "bill.amount",                 Label: "Amount",                Type: FieldTypeMoney,   Scope: FieldScopeDocument, Group: "Totals"},
	{Key: "bill.balance_due",            Label: "Balance Due",           Type: FieldTypeMoney,   Scope: FieldScopeDocument, Group: "Totals"},
	{Key: "bill.currency",               Label: "Currency Code",         Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Document"},
	{Key: "vendor.name",                 Label: "Vendor Name",           Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Vendor"},
	{Key: "vendor.email",                Label: "Vendor Email",          Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Vendor"},
	{Key: "vendor.address",              Label: "Vendor Address",        Type: FieldTypeAddress, Scope: FieldScopeDocument, Group: "Vendor"},
}

// purchaseOrderFields cover the AP-side PO template.
var purchaseOrderFields = []Field{
	{Key: "purchase_order.number",       Label: "PO Number",             Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Document"},
	{Key: "purchase_order.date",         Label: "Order Date",            Type: FieldTypeDate,    Scope: FieldScopeDocument, Group: "Document"},
	{Key: "purchase_order.delivery_date",Label: "Delivery Date",         Type: FieldTypeDate,    Scope: FieldScopeDocument, Group: "Document"},
	{Key: "purchase_order.notes",        Label: "Vendor Notes",          Type: FieldTypeRichText,Scope: FieldScopeDocument, Group: "Document"},
	{Key: "purchase_order.memo",         Label: "Memo / Internal",       Type: FieldTypeRichText,Scope: FieldScopeDocument, Group: "Document"},
	{Key: "purchase_order.subtotal",     Label: "Subtotal",              Type: FieldTypeMoney,   Scope: FieldScopeDocument, Group: "Totals"},
	{Key: "purchase_order.tax_total",    Label: "Tax Total",             Type: FieldTypeMoney,   Scope: FieldScopeDocument, Group: "Totals"},
	{Key: "purchase_order.total",        Label: "Total",                 Type: FieldTypeMoney,   Scope: FieldScopeDocument, Group: "Totals"},
	{Key: "purchase_order.currency",     Label: "Currency Code",         Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Document"},
	{Key: "vendor.name",                 Label: "Vendor Name",           Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Vendor"},
	{Key: "vendor.email",                Label: "Vendor Email",          Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Vendor"},
	{Key: "vendor.address",              Label: "Vendor Address",        Type: FieldTypeAddress, Scope: FieldScopeDocument, Group: "Vendor"},
	{Key: "purchase_order.ship_to",      Label: "Ship To Address",       Type: FieldTypeAddress, Scope: FieldScopeDocument, Group: "Shipping"},
}

// shipmentFields cover the outbound Shipment template (acts as packing slip).
// Customer PO# / SO# are joined-through from the source SalesOrder (per spec
// "始终以SO为准" — Shipment doesn't store its own PO#).
var shipmentFields = []Field{
	{Key: "shipment.number",             Label: "Shipment Number",       Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Document"},
	{Key: "shipment.date",               Label: "Ship Date",             Type: FieldTypeDate,    Scope: FieldScopeDocument, Group: "Document"},
	{Key: "shipment.sales_order_number", Label: "Sales Order #",         Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Document"},
	{Key: "shipment.customer_po_number", Label: "Customer PO#",          Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Document"},
	{Key: "shipment.tracking_number",    Label: "Tracking #",            Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Document"},
	{Key: "shipment.carrier",            Label: "Carrier",               Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Document"},
	{Key: "shipment.notes",              Label: "Shipment Notes",        Type: FieldTypeRichText,Scope: FieldScopeDocument, Group: "Document"},
	{Key: "customer.name",               Label: "Customer Name",         Type: FieldTypeString,  Scope: FieldScopeDocument, Group: "Customer"},
	{Key: "customer.bill_to",            Label: "Bill To Address",       Type: FieldTypeAddress, Scope: FieldScopeDocument, Group: "Customer"},
	{Key: "customer.ship_to",            Label: "Ship To Address",       Type: FieldTypeAddress, Scope: FieldScopeDocument, Group: "Customer"},
}

// docFieldsByType is the dispatch table built once at init time. Map values
// are flat slices in registry order (Document → Customer/Vendor → Totals →
// Line → Company); the editor groups by .Group for display.
var docFieldsByType = map[models.PDFDocumentType][]Field{}

func init() {
	concat := func(parts ...[]Field) []Field {
		out := make([]Field, 0)
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	docFieldsByType[models.PDFDocInvoice]       = concat(invoiceFields,       commonLineFields, commonCompanyFields)
	docFieldsByType[models.PDFDocQuote]         = concat(quoteFields,         commonLineFields, commonCompanyFields)
	docFieldsByType[models.PDFDocSalesOrder]    = concat(salesOrderFields,    commonLineFields, commonCompanyFields)
	docFieldsByType[models.PDFDocBill]          = concat(billFields,          commonLineFields, commonCompanyFields)
	docFieldsByType[models.PDFDocPurchaseOrder] = concat(purchaseOrderFields, commonLineFields, commonCompanyFields)
	docFieldsByType[models.PDFDocShipment]      = concat(shipmentFields,      commonLineFields, commonCompanyFields)
}

// FieldsForDocType returns the registry slice for a document type. The
// returned slice is a freshly-allocated copy so callers can sort / filter
// without affecting the registry.
func FieldsForDocType(docType models.PDFDocumentType) []Field {
	src, ok := docFieldsByType[docType]
	if !ok {
		return nil
	}
	out := make([]Field, len(src))
	copy(out, src)
	return out
}

// FieldByKey looks up a single field. Returns (zero, false) when the doc
// type or key is unknown — renderer treats unknown keys as empty strings
// so a stale template referencing a removed field degrades gracefully.
func FieldByKey(docType models.PDFDocumentType, key string) (Field, bool) {
	for _, f := range docFieldsByType[docType] {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

// SortFieldsByGroup returns the fields ordered by group then label —
// convenient for the editor's grouped picker. Document group is always first.
func SortFieldsByGroup(fields []Field) []Field {
	out := make([]Field, len(fields))
	copy(out, fields)
	groupRank := map[string]int{
		"Document": 0, "Customer": 1, "Vendor": 1, "Shipping": 2,
		"Totals": 3, "Line": 4, "Company": 5,
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := groupRank[out[i].Group], groupRank[out[j].Group]
		if ri != rj {
			return ri < rj
		}
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Label < out[j].Label
	})
	return out
}
