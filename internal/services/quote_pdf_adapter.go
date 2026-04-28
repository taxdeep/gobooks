// 遵循project_guide.md
package services

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/services/pdf"
)

// quote_pdf_adapter.go — Quote → pdf.RenderInput. Shape mirrors
// invoice_pdf_adapter; differences are field bindings (quote.* keys vs
// invoice.* keys) and the absence of bill-to/ship-to snapshots (Quote
// renders the live customer record).

func RenderQuotePDFV2(ctx context.Context, db *gorm.DB, companyID, quoteID uint) ([]byte, string, error) {
	var q models.Quote
	err := db.
		Preload("Customer").
		Preload("Lines", func(d *gorm.DB) *gorm.DB { return d.Order("sort_order asc") }).
		Preload("Lines.ProductService").
		Preload("Lines.TaxCode").
		Where("id = ? AND company_id = ?", quoteID, companyID).
		First(&q).Error
	if err != nil {
		return nil, "", fmt.Errorf("load quote: %w", err)
	}
	tmpl, err := LoadPDFTemplate(db, companyID, models.PDFDocQuote)
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

	currency := PDFEffectiveCurrency(q.CurrencyCode, company.BaseCurrencyCode)
	values := buildQuoteValues(q, company, currency)
	lines := buildQuoteLines(q, currency)

	html, err := pdf.RenderHTML(pdf.RenderInput{
		DocumentType: string(models.PDFDocQuote),
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
	filename := "Quote-" + sanitizePDFFilenameSegment(q.QuoteNumber) + ".pdf"
	return pdfBytes, filename, nil
}

func buildQuoteValues(q models.Quote, company models.Company, currency string) pdf.DocumentValues {
	v := pdf.DocumentValues{
		"company.name":    company.Name,
		"company.address": buildCompanyAddress(company),
		"company.logo":    LoadCompanyLogoDataURL(company),

		"quote.number":   q.QuoteNumber,
		"quote.date":     q.QuoteDate.Format("2006-01-02"),
		"quote.notes":    q.Notes,
		"quote.memo":     q.Memo,
		"quote.currency": currency,

		"quote.subtotal":  FormatPDFMoney(q.Subtotal, currency),
		"quote.tax_total": FormatPDFMoney(q.TaxTotal, currency),
		"quote.total":     FormatPDFMoney(q.Total,    currency),

		"customer.name":    q.Customer.Name,
		"customer.email":   q.Customer.Email,
		"customer.bill_to": q.Customer.FormattedAddress(),
	}
	if q.ExpiryDate != nil {
		v["quote.valid_until"] = q.ExpiryDate.Format("2006-01-02")
	}
	return v
}

func buildQuoteLines(q models.Quote, currency string) []pdf.LineValues {
	out := make([]pdf.LineValues, 0, len(q.Lines))
	for _, l := range q.Lines {
		// QuoteLine doesn't carry a snapshot UOM (deferred to a later
		// slice). Derive from the product's live SellUOM so the
		// printed quote still shows "10 CASE" instead of "10".
		quoteUOM := ""
		if l.ProductService != nil {
			quoteUOM = l.ProductService.SellUOM
		}
		row := pdf.LineValues{
			"lines.description": l.Description,
			"lines.qty":         PDFQtyWithUOM(l.Quantity, l.ProductService, quoteUOM),
			"lines.unit_price":  FormatPDFMoney(l.UnitPrice, currency),
			"lines.line_net":    FormatPDFMoney(l.LineNet,   currency),
			"lines.line_tax":    FormatPDFMoney(l.TaxAmount, currency),
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
