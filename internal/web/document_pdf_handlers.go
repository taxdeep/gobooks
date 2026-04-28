// 遵循project_guide.md
package web

// document_pdf_handlers.go — Phase 3 (G5) HTTP handlers for the new
// chromedp-based PDF pipeline. One handler per non-Invoice document type
// (Invoice's own /pdf-v2 lives in invoice_preview_handlers.go alongside
// the legacy /pdf endpoint).
//
// Route convention: /<doc>/:id/pdf-v2 — same shape across all documents
// so the JS / templ helpers can build URLs uniformly.

import (
	"strconv"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/services"
)

// GET /quotes/:id/pdf-v2
func (s *Server) handleQuotePDFV2(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company context required")
	}
	id, err := strconv.ParseUint(c.Params("id"), 10, 32)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid quote ID")
	}
	pdfBytes, filename, err := services.RenderQuotePDFV2(c.Context(), s.DB, companyID, uint(id))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("PDF generation failed: " + err.Error())
	}
	return sendPDFResponse(c, pdfBytes, filename)
}

// GET /sales-orders/:id/pdf-v2
func (s *Server) handleSalesOrderPDFV2(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company context required")
	}
	id, err := strconv.ParseUint(c.Params("id"), 10, 32)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid sales order ID")
	}
	pdfBytes, filename, err := services.RenderSalesOrderPDFV2(c.Context(), s.DB, companyID, uint(id))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("PDF generation failed: " + err.Error())
	}
	return sendPDFResponse(c, pdfBytes, filename)
}

// GET /bills/:id/pdf-v2
func (s *Server) handleBillPDFV2(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company context required")
	}
	id, err := strconv.ParseUint(c.Params("id"), 10, 32)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid bill ID")
	}
	pdfBytes, filename, err := services.RenderBillPDFV2(c.Context(), s.DB, companyID, uint(id))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("PDF generation failed: " + err.Error())
	}
	return sendPDFResponse(c, pdfBytes, filename)
}

// GET /purchase-orders/:id/pdf-v2
func (s *Server) handlePurchaseOrderPDFV2(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company context required")
	}
	id, err := strconv.ParseUint(c.Params("id"), 10, 32)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid purchase order ID")
	}
	pdfBytes, filename, err := services.RenderPurchaseOrderPDFV2(c.Context(), s.DB, companyID, uint(id))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("PDF generation failed: " + err.Error())
	}
	return sendPDFResponse(c, pdfBytes, filename)
}

// GET /shipments/:id/pdf-v2
func (s *Server) handleShipmentPDFV2(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company context required")
	}
	id, err := strconv.ParseUint(c.Params("id"), 10, 32)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid shipment ID")
	}
	pdfBytes, filename, err := services.RenderShipmentPDFV2(c.Context(), s.DB, companyID, uint(id))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("PDF generation failed: " + err.Error())
	}
	return sendPDFResponse(c, pdfBytes, filename)
}

func sendPDFResponse(c *fiber.Ctx, pdfBytes []byte, filename string) error {
	c.Set("Content-Type", "application/pdf")
	c.Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	return c.Send(pdfBytes)
}
