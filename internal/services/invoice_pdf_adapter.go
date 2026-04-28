// 遵循project_guide.md
package services

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/services/pdf"
)

// invoice_pdf_adapter.go — bridges the Invoice GORM model into the Phase 3
// pdf package's RenderInput shape. Decouples the renderer (pdf package) from
// invoice-specific schemas (models package) so the renderer stays pure.
//
// Shared template lookup, money formatting, and logo loading live in
// document_pdf_helpers.go so all six per-doc adapters can reuse them.

// RenderInvoicePDFV2 loads the invoice, picks the appropriate PDFTemplate,
// builds the values + lines bundles, and returns the rendered PDF bytes.
// Returns the PDF bytes and a suggested filename. Caller is responsible for
// HTTP headers + delivery.
func RenderInvoicePDFV2(ctx context.Context, db *gorm.DB, companyID, invoiceID uint) ([]byte, string, error) {
	var inv models.Invoice
	err := db.
		Preload("Customer").
		Preload("Lines", func(d *gorm.DB) *gorm.DB { return d.Order("sort_order asc") }).
		Preload("Lines.ProductService").
		Preload("Lines.TaxCode").
		Preload("SalesOrder").
		Where("id = ? AND company_id = ?", invoiceID, companyID).
		First(&inv).Error
	if err != nil {
		return nil, "", fmt.Errorf("load invoice: %w", err)
	}

	tmpl, err := LoadPDFTemplate(db, companyID, models.PDFDocInvoice)
	if err != nil {
		return nil, "", err
	}
	schema, err := pdf.ParseSchema([]byte(tmpl.SchemaJSON))
	if err != nil {
		return nil, "", err
	}

	var company models.Company
	if err := db.First(&company, companyID).Error; err != nil {
		return nil, "", fmt.Errorf("load company: %w", err)
	}

	currency := PDFEffectiveCurrency(inv.CurrencyCode, company.BaseCurrencyCode)
	values := buildInvoiceValues(inv, company, currency)
	lines := buildInvoiceLines(inv, currency)

	html, err := pdf.RenderHTML(pdf.RenderInput{
		DocumentType: string(models.PDFDocInvoice),
		Schema:       schema,
		Values:       values,
		Lines:        lines,
	})
	if err != nil {
		return nil, "", fmt.Errorf("render html: %w", err)
	}
	pdfBytes, err := pdf.RenderPDF(ctx, html)
	if err != nil {
		return nil, "", fmt.Errorf("render pdf: %w", err)
	}
	return pdfBytes, InvoicePDFSafeFilename(inv.InvoiceNumber), nil
}

// buildInvoiceValues populates the doc-level field map. Empty strings for
// unset fields — the renderer hides FieldRefs marked HideWhenEmpty.
func buildInvoiceValues(inv models.Invoice, company models.Company, currency string) pdf.DocumentValues {
	v := pdf.DocumentValues{
		"company.name":    company.Name,
		"company.address": buildCompanyAddress(company),
		"company.logo":    LoadCompanyLogoDataURL(company),

		"invoice.number":   inv.InvoiceNumber,
		"invoice.date":     inv.InvoiceDate.Format("2006-01-02"),
		"invoice.terms":    inv.TermDescription,
		"invoice.memo":     inv.Memo,
		"invoice.currency": currency,

		"invoice.customer_po_number": inv.CustomerPONumber,

		"invoice.subtotal":    FormatPDFMoney(inv.Subtotal,   currency),
		"invoice.tax_total":   FormatPDFMoney(inv.TaxTotal,   currency),
		"invoice.amount":      FormatPDFMoney(inv.Amount,     currency),
		"invoice.balance_due": FormatPDFMoney(inv.BalanceDue, currency),

		"customer.name":          inv.CustomerNameSnapshot,
		"customer.email":         inv.CustomerEmailSnapshot,
		"customer.bill_to":       inv.CustomerAddressSnapshot,
		"customer.ship_to":       inv.ShipToSnapshot,
		"customer.ship_to_label": inv.ShipToLabel,
	}
	if inv.DueDate != nil {
		v["invoice.due_date"] = inv.DueDate.Format("2006-01-02")
	}
	if inv.SalesOrderID != nil && inv.SalesOrder != nil {
		v["invoice.sales_order_number"] = inv.SalesOrder.OrderNumber
	}
	return v
}

// buildInvoiceLines maps each InvoiceLine into a LineValues map keyed by
// the lines.* field-registry keys.
func buildInvoiceLines(inv models.Invoice, currency string) []pdf.LineValues {
	out := make([]pdf.LineValues, 0, len(inv.Lines))
	for _, l := range inv.Lines {
		row := pdf.LineValues{
			"lines.description": l.Description,
			"lines.qty":         PDFQtyWithUOM(l.Qty, l.ProductService, l.LineUOM),
			"lines.unit_price":  FormatPDFMoney(l.UnitPrice, currency),
			"lines.line_net":    FormatPDFMoney(l.LineNet,   currency),
			"lines.line_tax":    FormatPDFMoney(l.LineTax,   currency),
			"lines.line_total":  FormatPDFMoney(l.LineTotal, currency),
		}
		if l.ProductService != nil {
			row["lines.product_name"] = l.ProductService.Name
			row["lines.product_sku"] = l.ProductService.SKU
		}
		if l.TaxCode != nil {
			row["lines.tax_code"] = l.TaxCode.Code
		}
		out = append(out, row)
	}
	return out
}
