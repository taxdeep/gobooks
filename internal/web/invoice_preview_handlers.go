// 遵循project_guide.md
package web

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"gobooks/internal/models"
	"gobooks/internal/services"
)

// handleInvoicePreview renders invoice HTML for browser preview.
// GET /invoices/:id/preview
func (s *Server) handleInvoicePreview(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company context required")
	}

	invoiceID, err := strconv.ParseUint(c.Params("id"), 10, 32)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid invoice ID")
	}

	invoice, err := loadInvoiceForRender(s.DB, companyID, uint(invoiceID))
	if err != nil {
		return c.Status(fiber.StatusNotFound).SendString("invoice not found")
	}

	// Optional template override via query param
	if tmplIDRaw := c.Query("template_id"); tmplIDRaw != "" {
		if tmplID, err := strconv.ParseUint(tmplIDRaw, 10, 32); err == nil {
			id := uint(tmplID)
			invoice.TemplateID = &id
		}
	}

	renderData, err := services.BuildInvoiceRenderData(s.DB, companyID, invoice)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("render failed: " + err.Error())
	}

	html := services.RenderInvoiceToHTML(*renderData)
	c.Set("Content-Type", "text/html; charset=utf-8")
	return c.SendString(html)
}

// handleInvoicePDF generates and downloads invoice as PDF.
// GET /invoices/:id/pdf
func (s *Server) handleInvoicePDF(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company context required")
	}

	invoiceID, err := strconv.ParseUint(c.Params("id"), 10, 32)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid invoice ID")
	}

	invoice, err := loadInvoiceForRender(s.DB, companyID, uint(invoiceID))
	if err != nil {
		return c.Status(fiber.StatusNotFound).SendString("invoice not found")
	}

	renderData, err := services.BuildInvoiceRenderData(s.DB, companyID, invoice)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("render failed: " + err.Error())
	}

	html := services.RenderInvoiceToHTML(*renderData)

	pdfBytes, err := services.GenerateInvoicePDF(html)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("PDF generation failed: " + err.Error())
	}

	// Sanitize filename
	safeNumber := strings.ReplaceAll(invoice.InvoiceNumber, "/", "-")
	safeNumber = strings.ReplaceAll(safeNumber, "\\", "-")
	filename := "Invoice-" + safeNumber + ".pdf"

	c.Set("Content-Type", "application/pdf")
	c.Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	return c.Send(pdfBytes)
}

// loadInvoiceForRender loads an invoice with all preloads needed for rendering.
func loadInvoiceForRender(db *gorm.DB, companyID, invoiceID uint) (*models.Invoice, error) {
	var inv models.Invoice
	err := db.
		Preload("Customer").
		Preload("Lines", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order asc") }).
		Preload("Lines.ProductService").
		Preload("Lines.TaxCode").
		Where("id = ? AND company_id = ?", invoiceID, companyID).
		First(&inv).Error
	if err != nil {
		return nil, err
	}
	return &inv, nil
}
