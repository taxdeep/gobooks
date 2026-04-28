// 遵循project_guide.md
package services

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/services/pdf"
)

// purchase_order_pdf_adapter.go — PurchaseOrder → pdf.RenderInput.

func RenderPurchaseOrderPDFV2(ctx context.Context, db *gorm.DB, companyID, poID uint) ([]byte, string, error) {
	var po models.PurchaseOrder
	err := db.
		Preload("Vendor").
		Preload("Lines", func(d *gorm.DB) *gorm.DB { return d.Order("sort_order asc") }).
		Preload("Lines.ProductService").
		Preload("Lines.TaxCode").
		Where("id = ? AND company_id = ?", poID, companyID).
		First(&po).Error
	if err != nil {
		return nil, "", fmt.Errorf("load purchase order: %w", err)
	}
	tmpl, err := LoadPDFTemplate(db, companyID, models.PDFDocPurchaseOrder)
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

	currency := PDFEffectiveCurrency(po.CurrencyCode, company.BaseCurrencyCode)
	values := buildPurchaseOrderValues(po, company, currency)
	lines := buildPurchaseOrderLines(po, currency)

	html, err := pdf.RenderHTML(pdf.RenderInput{
		DocumentType: string(models.PDFDocPurchaseOrder),
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
	filename := "PO-" + sanitizePDFFilenameSegment(po.PONumber) + ".pdf"
	return pdfBytes, filename, nil
}

func buildPurchaseOrderValues(po models.PurchaseOrder, company models.Company, currency string) pdf.DocumentValues {
	v := pdf.DocumentValues{
		"company.name":    company.Name,
		"company.address": buildCompanyAddress(company),
		"company.logo":    LoadCompanyLogoDataURL(company),

		"purchase_order.number":   po.PONumber,
		"purchase_order.date":     po.PODate.Format("2006-01-02"),
		"purchase_order.notes":    po.Notes,
		"purchase_order.memo":     po.Memo,
		"purchase_order.currency": currency,

		"purchase_order.subtotal":  FormatPDFMoney(po.Subtotal, currency),
		"purchase_order.tax_total": FormatPDFMoney(po.TaxTotal, currency),
		"purchase_order.total":     FormatPDFMoney(po.Amount,   currency),

		"vendor.name":    po.Vendor.Name,
		"vendor.email":   po.Vendor.Email,
		"vendor.address": po.Vendor.Address,
	}
	if po.ExpectedDate != nil {
		v["purchase_order.delivery_date"] = po.ExpectedDate.Format("2006-01-02")
	}
	// PurchaseOrder.ship_to is the company's own delivery address — defaults
	// to the company address. Templates that show ship_to render the
	// company's letterhead address as the delivery target.
	v["purchase_order.ship_to"] = buildCompanyAddress(company)
	return v
}

func buildPurchaseOrderLines(po models.PurchaseOrder, currency string) []pdf.LineValues {
	out := make([]pdf.LineValues, 0, len(po.Lines))
	for _, l := range po.Lines {
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
