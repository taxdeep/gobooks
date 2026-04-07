// 遵循project_guide.md
package web

import (
	"github.com/gofiber/fiber/v2"
	"gobooks/internal/logging"
	"gobooks/internal/services"
)

// handleHostedInvoiceDownload generates and serves an invoice PDF for a hosted link.
// GET /i/:token/download
//
// Security: token validated before any content is served. All failures return 410
// Gone via sendHostedErrorPage — same policy as the main hosted invoice page.
//
// Render basis: RenderInvoiceToHTML (no toolbar). This is identical to the internal
// /invoices/:id/pdf route, ensuring visual consistency between the internal and
// customer-facing PDFs. The toolbar CSS is not included (it would be visible in
// the downloaded PDF and has no value to the customer).
//
// PDF engine: wkhtmltopdf via GenerateInvoicePDF. If wkhtmltopdf is not installed,
// the handler returns 503 Service Unavailable. The toolbar only shows the Download
// PDF link when wkhtmltopdf is detected (CanDownload=true in handleHostedInvoice),
// so customers will not normally reach this route without the engine being present.
//
// Filename: "Invoice-<sanitized_number>.pdf" — same convention as internal PDF route.
func (s *Server) handleHostedInvoiceDownload(c *fiber.Ctx) error {
	token := c.Params("token")

	link, err := services.ValidateHostedToken(s.DB, token)
	if err != nil {
		return sendHostedErrorPage(c)
	}

	invoice, err := loadInvoiceForRender(s.DB, link.CompanyID, link.InvoiceID)
	if err != nil {
		logging.L().Warn("hosted download: invoice load failed",
			"link_id", link.ID, "invoice_id", link.InvoiceID, "error", err.Error())
		return sendHostedErrorPage(c)
	}

	renderData, err := services.BuildInvoiceRenderData(s.DB, link.CompanyID, invoice)
	if err != nil {
		logging.L().Warn("hosted download: render data failed",
			"link_id", link.ID, "invoice_id", link.InvoiceID, "error", err.Error())
		return sendHostedErrorPage(c)
	}

	// RenderInvoiceToHTML — no toolbar. Consistent with internal /invoices/:id/pdf.
	html := services.RenderInvoiceToHTML(*renderData)

	pdfBytes, err := services.GenerateInvoicePDF(html)
	if err != nil {
		logging.L().Warn("hosted download: PDF generation failed",
			"link_id", link.ID, "invoice_id", link.InvoiceID, "error", err.Error())
		return c.Status(fiber.StatusServiceUnavailable).SendString("PDF generation is not available")
	}

	filename := services.InvoicePDFSafeFilename(invoice.InvoiceNumber)
	c.Set("Content-Type", "application/pdf")
	c.Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Set("Cache-Control", "no-store")
	return c.Send(pdfBytes)
}
