// 遵循project_guide.md
package services

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/services/pdf"
)

// bill_pdf_adapter.go — Bill → pdf.RenderInput.

func RenderBillPDFV2(ctx context.Context, db *gorm.DB, companyID, billID uint) ([]byte, string, error) {
	var b models.Bill
	err := db.
		Preload("Vendor").
		Preload("Lines", func(d *gorm.DB) *gorm.DB { return d.Order("sort_order asc") }).
		Preload("Lines.ProductService").
		Preload("Lines.TaxCode").
		Where("id = ? AND company_id = ?", billID, companyID).
		First(&b).Error
	if err != nil {
		return nil, "", fmt.Errorf("load bill: %w", err)
	}
	tmpl, err := LoadPDFTemplate(db, companyID, models.PDFDocBill)
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

	currency := PDFEffectiveCurrency(b.CurrencyCode, company.BaseCurrencyCode)
	values := buildBillValues(b, company, currency)
	lines := buildBillLines(b, currency)

	html, err := pdf.RenderHTML(pdf.RenderInput{
		DocumentType: string(models.PDFDocBill),
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
	filename := "Bill-" + sanitizePDFFilenameSegment(b.BillNumber) + ".pdf"
	return pdfBytes, filename, nil
}

func buildBillValues(b models.Bill, company models.Company, currency string) pdf.DocumentValues {
	v := pdf.DocumentValues{
		"company.name":    company.Name,
		"company.address": buildCompanyAddress(company),
		"company.logo":    LoadCompanyLogoDataURL(company),

		"bill.number":   b.BillNumber,
		"bill.date":     b.BillDate.Format("2006-01-02"),
		"bill.terms":    b.TermDescription,
		"bill.memo":     b.Memo,
		"bill.currency": currency,

		"bill.subtotal":    FormatPDFMoney(b.Subtotal,   currency),
		"bill.tax_total":   FormatPDFMoney(b.TaxTotal,   currency),
		"bill.amount":      FormatPDFMoney(b.Amount,     currency),
		"bill.balance_due": FormatPDFMoney(b.BalanceDue, currency),

		"vendor.name":    b.Vendor.Name,
		"vendor.email":   b.Vendor.Email,
		"vendor.address": b.Vendor.Address,
	}
	if b.DueDate != nil {
		v["bill.due_date"] = b.DueDate.Format("2006-01-02")
	}
	return v
}

func buildBillLines(b models.Bill, currency string) []pdf.LineValues {
	out := make([]pdf.LineValues, 0, len(b.Lines))
	for _, l := range b.Lines {
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
