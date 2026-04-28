// 遵循project_guide.md
package services

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/services/pdf"
)

// sales_order_pdf_adapter.go — SalesOrder → pdf.RenderInput.

func RenderSalesOrderPDFV2(ctx context.Context, db *gorm.DB, companyID, soID uint) ([]byte, string, error) {
	var so models.SalesOrder
	err := db.
		Preload("Customer").
		Preload("Quote").
		Preload("Lines", func(d *gorm.DB) *gorm.DB { return d.Order("sort_order asc") }).
		Preload("Lines.ProductService").
		Preload("Lines.TaxCode").
		Where("id = ? AND company_id = ?", soID, companyID).
		First(&so).Error
	if err != nil {
		return nil, "", fmt.Errorf("load sales order: %w", err)
	}
	tmpl, err := LoadPDFTemplate(db, companyID, models.PDFDocSalesOrder)
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

	currency := PDFEffectiveCurrency(so.CurrencyCode, company.BaseCurrencyCode)
	values := buildSalesOrderValues(so, company, currency)
	lines := buildSalesOrderLines(so, currency)

	html, err := pdf.RenderHTML(pdf.RenderInput{
		DocumentType: string(models.PDFDocSalesOrder),
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
	filename := "SalesOrder-" + sanitizePDFFilenameSegment(so.OrderNumber) + ".pdf"
	return pdfBytes, filename, nil
}

func buildSalesOrderValues(so models.SalesOrder, company models.Company, currency string) pdf.DocumentValues {
	v := pdf.DocumentValues{
		"company.name":    company.Name,
		"company.address": buildCompanyAddress(company),
		"company.logo":    LoadCompanyLogoDataURL(company),

		"sales_order.number":              so.OrderNumber,
		"sales_order.date":                so.OrderDate.Format("2006-01-02"),
		"sales_order.notes":               so.Notes,
		"sales_order.memo":                so.Memo,
		"sales_order.currency":            currency,
		"sales_order.customer_po_number":  so.CustomerPONumber,

		"sales_order.subtotal":  FormatPDFMoney(so.Subtotal, currency),
		"sales_order.tax_total": FormatPDFMoney(so.TaxTotal, currency),
		"sales_order.total":     FormatPDFMoney(so.Total,    currency),

		"customer.name":    so.Customer.Name,
		"customer.email":   so.Customer.Email,
		"customer.bill_to": so.Customer.FormattedAddress(),
	}
	if so.RequiredBy != nil {
		v["sales_order.required_by"] = so.RequiredBy.Format("2006-01-02")
	}
	if so.Quote != nil {
		v["sales_order.quote_number"] = so.Quote.QuoteNumber
	}
	return v
}

func buildSalesOrderLines(so models.SalesOrder, currency string) []pdf.LineValues {
	out := make([]pdf.LineValues, 0, len(so.Lines))
	for _, l := range so.Lines {
		row := pdf.LineValues{
			"lines.description": l.Description,
			"lines.qty":         PDFQtyWithUOM(l.Quantity, l.ProductService, l.LineUOM),
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
