// 遵循project_guide.md
package web

// pdf_templates_handlers.go — Phase 3 (G6) management page for PDF templates.
//
// Routes:
//   GET  /pdf-templates                   — list page (system + company-owned)
//   POST /pdf-templates/:id/clone         — clone any template into the company
//   POST /pdf-templates/:id/set-default   — promote a company-owned template
//   POST /pdf-templates/:id/delete        — delete a company-owned template
//   GET  /pdf-templates/:id/preview       — render the template against sample
//                                            data via chromedp; opens in a new tab

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/services/pdf"
	"balanciz/internal/web/templates/pages"
)

// GET /pdf-templates
func (s *Server) handlePDFTemplatesList(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	rows, err := services.ListPDFTemplatesForCompany(s.DB, companyID)
	if err != nil {
		return redirectErr(c, "/pdf-templates", "could not load templates: "+err.Error())
	}
	vm := pages.PDFTemplatesVM{
		HasCompany: true,
		Rows:       rows,
		FlashMsg:   strings.TrimSpace(c.Query("msg")),
		FlashErr:   strings.TrimSpace(c.Query("error")),
	}
	return pages.PDFTemplates(vm).Render(c.Context(), c)
}

// POST /pdf-templates/:id/clone
func (s *Server) handlePDFTemplateClone(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := strconv.ParseUint(c.Params("id"), 10, 32)
	if err != nil {
		return c.Redirect("/pdf-templates?error=invalid+id", fiber.StatusSeeOther)
	}
	cloned, err := services.ClonePDFTemplate(s.DB, companyID, uint(id), "")
	if err != nil {
		return redirectErr(c, "/pdf-templates", err.Error())
	}
	return c.Redirect("/pdf-templates?msg=Cloned+as+"+cloned.Name, fiber.StatusSeeOther)
}

// POST /pdf-templates/:id/set-default
func (s *Server) handlePDFTemplateSetDefault(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := strconv.ParseUint(c.Params("id"), 10, 32)
	if err != nil {
		return c.Redirect("/pdf-templates?error=invalid+id", fiber.StatusSeeOther)
	}
	if err := services.SetDefaultPDFTemplate(s.DB, companyID, uint(id)); err != nil {
		return redirectErr(c, "/pdf-templates", err.Error())
	}
	return c.Redirect("/pdf-templates?msg=Default+template+updated", fiber.StatusSeeOther)
}

// POST /pdf-templates/:id/delete
func (s *Server) handlePDFTemplateDelete(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := strconv.ParseUint(c.Params("id"), 10, 32)
	if err != nil {
		return c.Redirect("/pdf-templates?error=invalid+id", fiber.StatusSeeOther)
	}
	if err := services.DeletePDFTemplate(s.DB, companyID, uint(id)); err != nil {
		return redirectErr(c, "/pdf-templates", err.Error())
	}
	return c.Redirect("/pdf-templates?msg=Template+deleted", fiber.StatusSeeOther)
}

// GET /pdf-templates/:id/preview
//
// Renders the template against pdf.SampleValues / pdf.SampleLines so users
// can eyeball layout changes without binding the template to a real
// document. Returns a PDF inline so browsers display it in the new tab.
func (s *Server) handlePDFTemplatePreview(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company context required")
	}
	id, err := strconv.ParseUint(c.Params("id"), 10, 32)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid template ID")
	}
	var t models.PDFTemplate
	// Allow access to system rows (NULL company_id) or rows owned by this company.
	if err := s.DB.Where("(company_id IS NULL OR company_id = ?) AND id = ?", companyID, id).
		First(&t).Error; err != nil {
		return c.Status(fiber.StatusNotFound).SendString("template not found")
	}
	schema, err := pdf.ParseSchema([]byte(t.SchemaJSON))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("schema parse failed: " + err.Error())
	}
	docType := models.PDFDocumentType(t.DocumentType)
	html, err := pdf.RenderHTML(pdf.RenderInput{
		DocumentType: t.DocumentType,
		Schema:       schema,
		Values:       pdf.SampleValues(docType),
		Lines:        pdf.SampleLines(docType),
	})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("render html: " + err.Error())
	}
	pdfBytes, err := pdf.RenderPDF(c.Context(), html)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("render pdf: " + err.Error())
	}
	c.Set("Content-Type", "application/pdf")
	// inline so the browser tab displays the PDF rather than downloading it.
	c.Set("Content-Disposition", `inline; filename="preview.pdf"`)
	return c.Send(pdfBytes)
}
