// 遵循project_guide.md
package web

// pdf_template_edit_handlers.go — Phase 3 (G7) visual editor.
//
// Routes:
//   GET  /pdf-templates/:id/edit       — opens the editor page (system rows
//                                         get a "clone first" notice instead).
//   POST /pdf-templates/:id/save-schema — persists name + description +
//                                         schema_json. Validates parse first.
//   POST /pdf-templates/:id/save-as    — creates a new clone with the
//                                         posted name + the in-editor schema.
//   POST /pdf-templates/preview-html   — schema_json + doc_type in body,
//                                         returns rendered HTML for the
//                                         editor's live-preview iframe
//                                         (HTML, not PDF — instant refresh).

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/services/pdf"
	"balanciz/internal/web/templates/pages"
)

// GET /pdf-templates/:id/edit
func (s *Server) handlePDFTemplateEdit(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := strconv.ParseUint(c.Params("id"), 10, 32)
	if err != nil {
		return c.Redirect("/pdf-templates?error=invalid+id", fiber.StatusSeeOther)
	}
	var t models.PDFTemplate
	// Allow loading both system rows (read-only banner) and company-owned ones.
	if err := s.DB.Where("(company_id IS NULL OR company_id = ?) AND id = ?", companyID, id).
		First(&t).Error; err != nil {
		return c.Redirect("/pdf-templates?error=template+not+found", fiber.StatusSeeOther)
	}
	// Validate schema parses now so the editor doesn't crash mid-load on a
	// hand-mangled row. ParseSchema applies defaults for missing fields.
	parsed, parseErr := pdf.ParseSchema([]byte(t.SchemaJSON))
	pretty, _ := json.MarshalIndent(parsed, "", "  ")

	docType := models.PDFDocumentType(t.DocumentType)
	fields := pdf.SortFieldsByGroup(pdf.FieldsForDocType(docType))

	vm := pages.PDFTemplateEditVM{
		HasCompany:      true,
		Template:        t,
		DocType:         t.DocumentType,
		DocTypeLabel:    pages.PDFDocTypeDisplayLabel(docType),
		PrettySchemaJSON: string(pretty),
		Fields:          fields,
		BlockTypes:      pdf.AllBlockTypes,
		IsSystemReadOnly: t.IsSystem || t.CompanyID == nil,
		FlashErr:        strings.TrimSpace(c.Query("error")),
		FlashMsg:        strings.TrimSpace(c.Query("msg")),
	}
	if parseErr != nil {
		vm.FlashErr = "Schema parse error: " + parseErr.Error()
	}
	return pages.PDFTemplateEdit(vm).Render(c.Context(), c)
}

// POST /pdf-templates/:id/save-schema
//
// Form fields:
//   name        — required when present (existing name kept on empty input)
//   description — always overwrites (allows clearing)
//   schema_json — required, must parse via pdf.ParseSchema
func (s *Server) handlePDFTemplateSaveSchema(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := strconv.ParseUint(c.Params("id"), 10, 32)
	if err != nil {
		return c.Redirect("/pdf-templates?error=invalid+id", fiber.StatusSeeOther)
	}
	name := strings.TrimSpace(c.FormValue("name"))
	description := strings.TrimSpace(c.FormValue("description"))
	rawSchema := strings.TrimSpace(c.FormValue("schema_json"))
	if rawSchema == "" {
		return redirectErr(c, pdfTemplateEditURL(uint(id)), "schema_json is required")
	}
	if _, err := pdf.ParseSchema([]byte(rawSchema)); err != nil {
		return redirectErr(c, pdfTemplateEditURL(uint(id)), "schema parse failed: "+err.Error())
	}
	if err := services.UpdatePDFTemplateSchema(s.DB, companyID, uint(id), name, description, []byte(rawSchema)); err != nil {
		return redirectErr(c, pdfTemplateEditURL(uint(id)), err.Error())
	}
	return c.Redirect(pdfTemplateEditURL(uint(id))+"?msg=Saved", fiber.StatusSeeOther)
}

// POST /pdf-templates/:id/save-as
//
// Form fields: new_name (required) + schema_json (required, current
// editor state). Creates a fresh company-owned clone and redirects to
// its edit page.
func (s *Server) handlePDFTemplateSaveAs(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	id, err := strconv.ParseUint(c.Params("id"), 10, 32)
	if err != nil {
		return c.Redirect("/pdf-templates?error=invalid+id", fiber.StatusSeeOther)
	}
	newName := strings.TrimSpace(c.FormValue("new_name"))
	if newName == "" {
		return redirectErr(c, pdfTemplateEditURL(uint(id)), "new template name is required")
	}
	rawSchema := strings.TrimSpace(c.FormValue("schema_json"))
	if rawSchema == "" {
		return redirectErr(c, pdfTemplateEditURL(uint(id)), "schema_json is required")
	}
	if _, err := pdf.ParseSchema([]byte(rawSchema)); err != nil {
		return redirectErr(c, pdfTemplateEditURL(uint(id)), "schema parse failed: "+err.Error())
	}
	cloned, err := services.ClonePDFTemplate(s.DB, companyID, uint(id), newName)
	if err != nil {
		return redirectErr(c, pdfTemplateEditURL(uint(id)), err.Error())
	}
	// Persist the editor's current schema onto the clone (Clone defaults to
	// the source's schema; this overwrite captures unsaved edits made before
	// the operator clicked "Save as").
	if err := services.UpdatePDFTemplateSchema(s.DB, companyID, cloned.ID, newName, cloned.Description, []byte(rawSchema)); err != nil {
		return redirectErr(c, pdfTemplateEditURL(cloned.ID), err.Error())
	}
	return c.Redirect(pdfTemplateEditURL(cloned.ID)+"?msg=Saved+as+new+template", fiber.StatusSeeOther)
}

// POST /pdf-templates/preview-html
//
// JSON body: {"doc_type":"invoice","schema_json":"…"}
// Returns text/html — the rendered template applied to sample data. The
// editor iframes this URL with srcdoc=output for instant preview without
// touching chromedp (faster + lighter than a full PDF render).
func (s *Server) handlePDFTemplatePreviewHTML(c *fiber.Ctx) error {
	if _, ok := ActiveCompanyIDFromCtx(c); !ok {
		return c.Status(fiber.StatusBadRequest).SendString("company context required")
	}
	var body struct {
		DocType    string `json:"doc_type"`
		SchemaJSON string `json:"schema_json"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid JSON body: " + err.Error())
	}
	if body.DocType == "" || body.SchemaJSON == "" {
		return c.Status(fiber.StatusBadRequest).SendString("doc_type and schema_json are required")
	}
	schema, err := pdf.ParseSchema([]byte(body.SchemaJSON))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("schema parse failed: " + err.Error())
	}
	docType := models.PDFDocumentType(body.DocType)
	html, err := pdf.RenderHTML(pdf.RenderInput{
		DocumentType: body.DocType,
		Schema:       schema,
		Values:       pdf.SampleValues(docType),
		Lines:        pdf.SampleLines(docType),
	})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("render html: " + err.Error())
	}
	c.Set("Content-Type", "text/html; charset=utf-8")
	return c.SendString(html)
}

func pdfTemplateEditURL(id uint) string {
	return "/pdf-templates/" + strconv.FormatUint(uint64(id), 10) + "/edit"
}
