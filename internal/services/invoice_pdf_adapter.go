// 遵循project_guide.md
package services

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"gobooks/internal/models"
	"gobooks/internal/services/pdf"
)

// invoice_pdf_adapter.go — bridges the Invoice GORM model into the Phase 3
// pdf package's RenderInput shape. Decouples the renderer (pdf package) from
// invoice-specific schemas (models package) so the renderer stays pure.
//
// The adapter is responsible for:
//   • Choosing the right pdf_template (company default → system fallback).
//   • Resolving every registry field key invoice templates may reference
//     into a string. Money/date formatting decisions live here so locale
//     and currency are applied once.
//   • Loading + base64-encoding the company logo (if any).
//
// This file is the new entry point — old invoice_render_service.go +
// invoice_pdf_service.go remain in place for now (G4-cleanup retires them).

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

	tmpl, err := loadPDFTemplate(db, companyID, models.PDFDocInvoice)
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

	currency := effectiveCurrency(inv, company)
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

// loadPDFTemplate picks the company's chosen default for the given doc type,
// falling back to the system Classic preset when the company has none set
// or none active. Falls back further to the first system row of any kind.
func loadPDFTemplate(db *gorm.DB, companyID uint, docType models.PDFDocumentType) (*models.PDFTemplate, error) {
	var t models.PDFTemplate
	// Company-owned default first.
	err := db.Where("company_id = ? AND document_type = ? AND is_default = ? AND is_active = ?",
		companyID, string(docType), true, true).First(&t).Error
	if err == nil {
		return &t, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("lookup company template: %w", err)
	}
	// System default (NULL company_id, is_default=true).
	err = db.Where("company_id IS NULL AND document_type = ? AND is_default = ? AND is_active = ?",
		string(docType), true, true).First(&t).Error
	if err == nil {
		return &t, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("lookup system default template: %w", err)
	}
	// Last resort: any system row for this doc type.
	if err := db.Where("company_id IS NULL AND document_type = ? AND is_active = ?",
		string(docType), true).First(&t).Error; err != nil {
		return nil, fmt.Errorf("no PDF template available for %s: %w", docType, err)
	}
	return &t, nil
}

// buildInvoiceValues populates the doc-level field map. Empty strings for
// unset fields — the renderer hides FieldRefs marked HideWhenEmpty.
func buildInvoiceValues(inv models.Invoice, company models.Company, currency string) pdf.DocumentValues {
	v := pdf.DocumentValues{
		"company.name":    company.Name,
		"company.address": buildCompanyAddress(company),

		"invoice.number":   inv.InvoiceNumber,
		"invoice.date":     inv.InvoiceDate.Format("2006-01-02"),
		"invoice.terms":    inv.TermDescription,
		"invoice.memo":     inv.Memo,
		"invoice.currency": currency,

		"invoice.customer_po_number": inv.CustomerPONumber,

		"invoice.subtotal":    formatMoney(inv.Subtotal,   currency),
		"invoice.tax_total":   formatMoney(inv.TaxTotal,   currency),
		"invoice.amount":      formatMoney(inv.Amount,     currency),
		"invoice.balance_due": formatMoney(inv.BalanceDue, currency),

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
	// Logo: only emit when the company has one stored. Adapter loads + base64
	// encodes via the existing helper to match the legacy renderer.
	if logo := loadCompanyLogoDataURL(company); logo != "" {
		v["company.logo"] = logo
	}
	return v
}

// buildInvoiceLines maps each InvoiceLine into a LineValues map keyed by the
// lines.* field-registry keys.
func buildInvoiceLines(inv models.Invoice, currency string) []pdf.LineValues {
	out := make([]pdf.LineValues, 0, len(inv.Lines))
	for _, l := range inv.Lines {
		row := pdf.LineValues{
			"lines.description": l.Description,
			"lines.qty":         l.Qty.String(),
			"lines.unit_price":  formatMoney(l.UnitPrice, currency),
			"lines.line_net":    formatMoney(l.LineNet,   currency),
			"lines.line_tax":    formatMoney(l.LineTax,   currency),
			"lines.line_total":  formatMoney(l.LineTotal, currency),
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

// effectiveCurrency returns the doc currency code or company base when blank.
func effectiveCurrency(inv models.Invoice, company models.Company) string {
	if inv.CurrencyCode != "" {
		return inv.CurrencyCode
	}
	return company.BaseCurrencyCode
}

// loadCompanyLogoDataURL returns a data:image/...;base64,... URL for the
// company logo, or empty when no logo / load error. Mirrors the existing
// invoice_render_service helper but kept local so the new adapter doesn't
// reach into the legacy package's internals.
func loadCompanyLogoDataURL(_ models.Company) string {
	// TODO(G5): copy the existing logo-loading + base64 encode from
	// invoice_render_service.go. Empty string for now — system templates
	// hide the company.logo field via HideWhenEmpty so renders still work.
	return ""
}

// formatMoneyD converts a Decimal to "1,234.56 CCY" with thousands separators.
// Currency code is appended only when non-empty; the renderer's
// formatValue() escapes the result.
func formatMoney(d decimal.Decimal, currency string) string {
	s := d.StringFixed(2)
	formatted := insertThousandsSeparators(s)
	if currency == "" {
		return formatted
	}
	return formatted + " " + currency
}

// insertThousandsSeparators inserts commas into the integer part of a
// "[-]NNNN.NN" string. Hand-rolled (no fmt.Sprintf) to stay locale-neutral.
func insertThousandsSeparators(s string) string {
	neg := false
	if len(s) > 0 && s[0] == '-' {
		neg = true
		s = s[1:]
	}
	dot := -1
	for i, c := range s {
		if c == '.' {
			dot = i
			break
		}
	}
	intPart := s
	frac := ""
	if dot >= 0 {
		intPart = s[:dot]
		frac = s[dot:]
	}
	// Walk integer part right-to-left, inserting comma every 3 digits.
	out := make([]byte, 0, len(intPart)+len(intPart)/3+1)
	for i, c := range []byte(intPart) {
		// Position from the right: when (len-i) is a multiple of 3 and i>0.
		if i > 0 && (len(intPart)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	result := string(out) + frac
	if neg {
		result = "-" + result
	}
	return result
}
