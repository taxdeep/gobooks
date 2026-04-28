// 遵循project_guide.md
package services

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/services/pdf"
)

// shipment_pdf_adapter.go — Shipment → pdf.RenderInput. Renders a packing
// slip (no prices, no totals — see the Shipment preset in pdf/presets.go).
//
// Customer PO# / SO Number are joined-through from the source SalesOrder
// per spec ("始终以SO为准") — Shipment doesn't store its own PO# column.

func RenderShipmentPDFV2(ctx context.Context, db *gorm.DB, companyID, shipmentID uint) ([]byte, string, error) {
	var s models.Shipment
	err := db.
		Preload("Customer").
		Preload("Warehouse").
		Preload("Lines", func(d *gorm.DB) *gorm.DB { return d.Order("sort_order asc") }).
		Preload("Lines.ProductService").
		Where("id = ? AND company_id = ?", shipmentID, companyID).
		First(&s).Error
	if err != nil {
		return nil, "", fmt.Errorf("load shipment: %w", err)
	}
	tmpl, err := LoadPDFTemplate(db, companyID, models.PDFDocShipment)
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

	// Pull SO-side fields (Customer PO# + SO Number) when this shipment is
	// linked to a SalesOrder. Best-effort: a missing SO leaves the fields blank.
	var soNumber, soPONumber, soShipTo string
	if s.SalesOrderID != nil && *s.SalesOrderID != 0 {
		var so models.SalesOrder
		if err := db.Select("id", "order_number", "customer_po_number").
			Where("id = ? AND company_id = ?", *s.SalesOrderID, companyID).
			First(&so).Error; err == nil {
			soNumber = so.OrderNumber
			soPONumber = so.CustomerPONumber
		}
	}

	values := buildShipmentValues(s, company, soNumber, soPONumber, soShipTo)
	lines := buildShipmentLines(s)

	html, err := pdf.RenderHTML(pdf.RenderInput{
		DocumentType: string(models.PDFDocShipment),
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
	filename := "Shipment-" + sanitizePDFFilenameSegment(s.ShipmentNumber) + ".pdf"
	return pdfBytes, filename, nil
}

func buildShipmentValues(sh models.Shipment, company models.Company, soNumber, soPONumber, soShipTo string) pdf.DocumentValues {
	v := pdf.DocumentValues{
		"company.name":    company.Name,
		"company.address": buildCompanyAddress(company),
		"company.logo":    LoadCompanyLogoDataURL(company),

		"shipment.number":              sh.ShipmentNumber,
		"shipment.date":                sh.ShipDate.Format("2006-01-02"),
		"shipment.notes":               sh.Memo,
		"shipment.tracking_number":     sh.Reference,
		"shipment.sales_order_number":  soNumber,
		"shipment.customer_po_number":  soPONumber,
	}
	// Customer info — Shipment customer is nullable; render the linked
	// customer's live record.
	if sh.Customer != nil {
		v["customer.name"]    = sh.Customer.Name
		v["customer.bill_to"] = sh.Customer.FormattedAddress()
	}
	// Ship-to: prefer the SO's customer ship-to snapshot if available,
	// otherwise fall back to the customer's billing address.
	if soShipTo != "" {
		v["customer.ship_to"] = soShipTo
	} else if sh.Customer != nil {
		v["customer.ship_to"] = sh.Customer.FormattedAddress()
	}
	return v
}

func buildShipmentLines(sh models.Shipment) []pdf.LineValues {
	out := make([]pdf.LineValues, 0, len(sh.Lines))
	for _, l := range sh.Lines {
		row := pdf.LineValues{
			"lines.description": l.Description,
			"lines.qty":         l.Qty.String(),
		}
		if l.ProductService != nil {
			row["lines.product_name"] = l.ProductService.Name
			row["lines.product_sku"] = l.ProductService.SKU
		}
		out = append(out, row)
	}
	return out
}
