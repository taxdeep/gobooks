// 遵循project_guide.md
package pages

import "balanciz/internal/models"

// pdfTemplatesForDoc filters the full template list down to one doc type.
// Used by the per-doc-type group renderer on the management page.
func pdfTemplatesForDoc(rows []models.PDFTemplate, doc models.PDFDocumentType) []models.PDFTemplate {
	out := make([]models.PDFTemplate, 0, len(rows))
	for _, r := range rows {
		if r.DocumentType == string(doc) {
			out = append(out, r)
		}
	}
	return out
}

// pdfDocTypeLabel returns the human-readable heading for a document type
// shown on the management page's group cards.
func pdfDocTypeLabel(doc models.PDFDocumentType) string {
	switch doc {
	case models.PDFDocInvoice:
		return "Invoice Templates"
	case models.PDFDocQuote:
		return "Quote Templates"
	case models.PDFDocSalesOrder:
		return "Sales Order Templates"
	case models.PDFDocBill:
		return "Bill Templates"
	case models.PDFDocPurchaseOrder:
		return "Purchase Order Templates"
	case models.PDFDocShipment:
		return "Packing Slip / Shipment Templates"
	}
	return string(doc)
}
