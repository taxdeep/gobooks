// 遵循project_guide.md
package pdf

import (
	"time"

	"balanciz/internal/models"
)

// preview.go — sample data for the template management page's "Preview"
// button. Lets users render a template against plausible-looking values
// before binding it to a real document. Independent of the GORM models so
// there's nothing to load — just hard-coded representative strings.

// SampleValues returns mock DocumentValues for the given doc type. Covers
// the full registry surface so any preset / customised template renders
// fully populated rows (HideWhenEmpty FieldRefs all become visible).
func SampleValues(docType models.PDFDocumentType) DocumentValues {
	common := DocumentValues{
		"company.name":    "Acme Corporation",
		"company.address": "1 Main Street\nSpringfield, IL 62701\nUnited States",
		"company.email":   "billing@acme.example.com",
		"company.phone":   "+1 (555) 010-9999",
		"company.tax_id":  "EIN 12-3456789",
		"company.logo":    "", // empty by design — preview shows the layout, not branding
		"doc.printed_at":  time.Now().Format("2006-01-02"),

		"customer.name":          "Foo Customer Ltd.",
		"customer.email":         "ap@foocustomer.example.com",
		"customer.bill_to":       "200 Park Avenue\nNew York, NY 10166\nUnited States",
		"customer.ship_to":       "Warehouse A\n55 Industrial Blvd\nNewark, NJ 07102",
		"customer.ship_to_label": "Warehouse A",

		"vendor.name":    "Bar Supplies Inc.",
		"vendor.email":   "ar@barsupplies.example.com",
		"vendor.address": "990 Vendor Road\nChicago, IL 60601",
	}
	switch docType {
	case models.PDFDocInvoice:
		common["invoice.number"]              = "IN-1042"
		common["invoice.date"]                = "2026-04-22"
		common["invoice.due_date"]            = "2026-05-22"
		common["invoice.terms"]               = "Net 30"
		common["invoice.memo"]                = "Sample preview — values are placeholders."
		common["invoice.customer_po_number"]  = "PO-7788"
		common["invoice.sales_order_number"]  = "SO-0042"
		common["invoice.subtotal"]            = "1,000.00 USD"
		common["invoice.tax_total"]           = "50.00 USD"
		common["invoice.amount"]              = "1,050.00 USD"
		common["invoice.balance_due"]         = "1,050.00 USD"
		common["invoice.currency"]            = "USD"
	case models.PDFDocQuote:
		common["quote.number"]      = "Q-0099"
		common["quote.date"]        = "2026-04-22"
		common["quote.valid_until"] = "2026-05-22"
		common["quote.notes"]       = "Customer-facing notes go here."
		common["quote.subtotal"]    = "5,500.00 USD"
		common["quote.tax_total"]   = "275.00 USD"
		common["quote.total"]       = "5,775.00 USD"
		common["quote.currency"]    = "USD"
	case models.PDFDocSalesOrder:
		common["sales_order.number"]              = "SO-0042"
		common["sales_order.date"]                = "2026-04-22"
		common["sales_order.required_by"]         = "2026-05-15"
		common["sales_order.customer_po_number"]  = "PO-7788"
		common["sales_order.quote_number"]        = "Q-0099"
		common["sales_order.notes"]               = "Confirm shipping window with carrier."
		common["sales_order.subtotal"]            = "5,500.00 USD"
		common["sales_order.tax_total"]           = "275.00 USD"
		common["sales_order.total"]               = "5,775.00 USD"
		common["sales_order.currency"]            = "USD"
	case models.PDFDocBill:
		common["bill.number"]                = "BILL-0007"
		common["bill.vendor_invoice_number"] = "VINV-9001"
		common["bill.date"]                  = "2026-04-22"
		common["bill.due_date"]              = "2026-05-22"
		common["bill.terms"]                 = "Net 30"
		common["bill.subtotal"]              = "2,400.00 USD"
		common["bill.tax_total"]             = "120.00 USD"
		common["bill.amount"]                = "2,520.00 USD"
		common["bill.balance_due"]           = "2,520.00 USD"
		common["bill.currency"]              = "USD"
	case models.PDFDocPurchaseOrder:
		common["purchase_order.number"]        = "PO-1234"
		common["purchase_order.date"]          = "2026-04-22"
		common["purchase_order.delivery_date"] = "2026-05-15"
		common["purchase_order.notes"]         = "Deliver to receiving dock between 9-11am."
		common["purchase_order.subtotal"]      = "12,000.00 USD"
		common["purchase_order.tax_total"]     = "600.00 USD"
		common["purchase_order.total"]         = "12,600.00 USD"
		common["purchase_order.currency"]      = "USD"
		common["purchase_order.ship_to"]       = "Receiving Dock 3\n1 Main Street\nSpringfield, IL 62701"
	case models.PDFDocShipment:
		common["shipment.number"]              = "SHIP-0210"
		common["shipment.date"]                = "2026-04-22"
		common["shipment.sales_order_number"]  = "SO-0042"
		common["shipment.customer_po_number"]  = "PO-7788"
		common["shipment.tracking_number"]     = "1Z999AA10123456784"
		common["shipment.carrier"]             = "UPS Ground"
		common["shipment.notes"]               = "Two cartons; signature required."
	}
	return common
}

// SampleLines returns mock LineValues rows for the given doc type. Three
// rows so the lines table renders multi-row layout meaningfully.
func SampleLines(docType models.PDFDocumentType) []LineValues {
	rows := []LineValues{
		{
			"lines.product_name": "Consulting Hours",
			"lines.product_sku":  "SVC-CONS",
			"lines.description":  "Senior consultant",
			"lines.qty":          "10",
			"lines.unit_price":   "150.00 USD",
			"lines.line_net":     "1,500.00 USD",
			"lines.line_tax":     "75.00 USD",
			"lines.line_total":   "1,575.00 USD",
			"lines.tax_code":     "GST",
		},
		{
			"lines.product_name": "Widget A",
			"lines.product_sku":  "WID-A-100",
			"lines.description":  "Widget A — large",
			"lines.qty":          "20",
			"lines.unit_price":   "100.00 USD",
			"lines.line_net":     "2,000.00 USD",
			"lines.line_tax":     "100.00 USD",
			"lines.line_total":   "2,100.00 USD",
			"lines.tax_code":     "GST",
		},
		{
			"lines.product_name": "Setup Fee",
			"lines.product_sku":  "SVC-SETUP",
			"lines.description":  "Initial setup + configuration",
			"lines.qty":          "1",
			"lines.unit_price":   "2,000.00 USD",
			"lines.line_net":     "2,000.00 USD",
			"lines.line_tax":     "100.00 USD",
			"lines.line_total":   "2,100.00 USD",
			"lines.tax_code":     "GST",
		},
	}
	// Shipment template hides prices — keep the same mock shape regardless;
	// the template-driven renderer will only pull the columns it asks for.
	if docType == models.PDFDocShipment {
		// no-op — the same rows render with only product_name + qty visible
		// per the Shipment preset's column list.
	}
	return rows
}
