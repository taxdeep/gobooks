// 遵循project_guide.md
package web

import (
	"strconv"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/services"
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

// handleInvoicePDFV2 renders an invoice via the Phase 3 block-template +
// chromedp pipeline (G2 + G3). Side-by-side with handleInvoicePDF (legacy
// wkhtmltopdf path) so the new system can be validated against real data
// before the legacy path is retired.
//
// GET /invoices/:id/pdf-v2
func (s *Server) handleInvoicePDFV2(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company context required")
	}
	invoiceID, err := strconv.ParseUint(c.Params("id"), 10, 32)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid invoice ID")
	}
	pdfBytes, filename, err := services.RenderInvoicePDFV2(c.Context(), s.DB, companyID, uint(invoiceID))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("PDF (v2) generation failed: " + err.Error())
	}
	c.Set("Content-Type", "application/pdf")
	c.Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	return c.Send(pdfBytes)
}

// handleInvoicePDF is the legacy /invoices/:id/pdf endpoint. After Phase 3
// G4-cleanup it redirects to /pdf-v2 (the chromedp pipeline) so old
// bookmarks / external links stay functional. Removed entirely after a
// release cycle when no production traffic still hits this path.
func (s *Server) handleInvoicePDF(c *fiber.Ctx) error {
	return c.Redirect("/invoices/"+c.Params("id")+"/pdf-v2", fiber.StatusMovedPermanently)
}

// handleInvoicePrint renders a print-friendly invoice page that auto-triggers window.print().
// GET /invoices/:id/print
func (s *Server) handleInvoicePrint(c *fiber.Ctx) error {
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

	html := services.RenderInvoiceForPrint(*renderData)
	c.Set("Content-Type", "text/html; charset=utf-8")
	return c.SendString(html)
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
