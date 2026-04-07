// 遵循project_guide.md
package web

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/gofiber/fiber/v2"
	"gobooks/internal/models"
	"gobooks/internal/services"
)

// handleInvoiceTemplatesList retrieves and displays all templates for a company.
// GET /settings/invoice-templates
// Requires: ActionSettingsView (read-only for all members)
func (s *Server) handleInvoiceTemplatesList(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	// Load all templates for company
	templates, err := services.ListInvoiceTemplates(s.DB, companyID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(map[string]string{
			"error": fmt.Sprintf("failed to load templates: %v", err),
		})
	}

	// Return JSON (for API) or could render a page if needed
	return c.JSON(map[string]interface{}{
		"templates": templates,
	})
}

// handleInvoiceTemplateGet retrieves a single template by ID.
// GET /settings/invoice-templates/:id
// Requires: ActionSettingsView
func (s *Server) handleInvoiceTemplateGet(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{
			"error": "company context required",
		})
	}

	templateIDStr := c.Params("id")
	templateID, err := strconv.ParseUint(templateIDStr, 10, 32)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{
			"error": "invalid template ID",
		})
	}

	template, err := services.GetInvoiceTemplate(s.DB, companyID, uint(templateID))
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(map[string]string{
			"error": fmt.Sprintf("template not found: %v", err),
		})
	}

	// Parse config for response
	config, _ := template.UnmarshalConfig()

	return c.JSON(map[string]interface{}{
		"template": map[string]interface{}{
			"id":          template.ID,
			"name":        template.Name,
			"description": template.Description,
			"config":      config,
			"is_default":  template.IsDefault,
		},
	})
}

// handleInvoiceTemplateCreate creates a new invoice template.
// POST /settings/invoice-templates
// Requires: ActionSettingsUpdate (owner/admin only)
func (s *Server) handleInvoiceTemplateCreate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{
			"error": "company context required",
		})
	}

	// Parse request
	type CreateRequest struct {
		Name        string                  `json:"name"`
		Description string                  `json:"description"`
		Config      *models.TemplateConfig `json:"config"`
		IsDefault   bool                    `json:"is_default"`
	}

	var req CreateRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{
			"error": fmt.Sprintf("invalid request: %v", err),
		})
	}

	// Validate
	if req.Name == "" {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{
			"error": "template name is required",
		})
	}

	// Convert config to JSON
	var configJSON []byte
	if req.Config != nil {
		var err error
		configJSON, err = json.Marshal(req.Config)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(map[string]string{
				"error": fmt.Sprintf("invalid config JSON: %v", err),
			})
		}
	} else {
		configJSON = []byte("{}")
	}

	// Create template
	template, err := services.CreateInvoiceTemplate(
		s.DB, companyID, req.Name, req.Description, configJSON, req.IsDefault,
	)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(map[string]string{
			"error": fmt.Sprintf("failed to create template: %v", err),
		})
	}

	return c.Status(fiber.StatusCreated).JSON(map[string]interface{}{
		"success":  true,
		"template": template,
	})
}

// handleInvoiceTemplateUpdate updates an existing template.
// POST /settings/invoice-templates/:id
// Requires: ActionSettingsUpdate
func (s *Server) handleInvoiceTemplateUpdate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{
			"error": "company context required",
		})
	}

	templateIDStr := c.Params("id")
	templateID, err := strconv.ParseUint(templateIDStr, 10, 32)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{
			"error": "invalid template ID",
		})
	}

	// Parse request
	type UpdateRequest struct {
		Name        string                  `json:"name"`
		Description string                  `json:"description"`
		Config      *models.TemplateConfig `json:"config"`
		IsDefault   bool                    `json:"is_default"`
	}

	var req UpdateRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{
			"error": fmt.Sprintf("invalid request: %v", err),
		})
	}

	// Validate
	if req.Name == "" {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{
			"error": "template name is required",
		})
	}

	// Convert config to JSON
	var configJSON []byte
	if req.Config != nil {
		var err error
		configJSON, err = json.Marshal(req.Config)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(map[string]string{
				"error": fmt.Sprintf("invalid config JSON: %v", err),
			})
		}
	} else {
		configJSON = []byte("{}")
	}

	// Update template
	template, err := services.UpdateInvoiceTemplate(
		s.DB, companyID, uint(templateID), req.Name, req.Description, configJSON, req.IsDefault,
	)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(map[string]string{
			"error": fmt.Sprintf("failed to update template: %v", err),
		})
	}

	return c.JSON(map[string]interface{}{
		"success":  true,
		"template": template,
	})
}

// handleInvoiceTemplateDelete deletes a template.
// POST /settings/invoice-templates/:id/delete
// Requires: ActionSettingsUpdate
func (s *Server) handleInvoiceTemplateDelete(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{
			"error": "company context required",
		})
	}

	templateIDStr := c.Params("id")
	templateID, err := strconv.ParseUint(templateIDStr, 10, 32)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{
			"error": "invalid template ID",
		})
	}

	// Delete template
	err = services.DeleteInvoiceTemplate(s.DB, companyID, uint(templateID))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(map[string]string{
			"error": fmt.Sprintf("failed to delete template: %v", err),
		})
	}

	return c.JSON(map[string]interface{}{
		"success": true,
		"message": "template deleted",
	})
}

// handleInvoiceTemplatesSettings displays the template settings page.
// GET /settings/invoice-templates/manage
// Requires: ActionSettingsView
func (s *Server) handleInvoiceTemplatesSettings(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	// Load all templates
	templates, err := services.ListInvoiceTemplates(s.DB, companyID)
	if err != nil {
		templates = []models.InvoiceTemplate{} // Empty list on error
	}

	// Future: render a templ page for template management
	// For now, return JSON
	return c.JSON(map[string]interface{}{
		"templates": templates,
	})
}

// handleSetDefaultInvoiceTemplate atomically sets a template as the company default.
// POST /settings/invoice-templates/:id/set-default
// Requires: ActionSettingsUpdate
func (s *Server) handleSetDefaultInvoiceTemplate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{
			"error": "company context required",
		})
	}

	templateID, err := strconv.ParseUint(c.Params("id"), 10, 32)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{
			"error": "invalid template ID",
		})
	}

	tmpl, err := services.SetDefaultInvoiceTemplate(s.DB, companyID, uint(templateID))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(map[string]string{
			"error": fmt.Sprintf("failed to set default: %v", err),
		})
	}

	// Support both JSON API calls and form-based redirects.
	if c.Get("Accept") == "application/json" || c.Get("HX-Request") != "" {
		return c.JSON(map[string]any{
			"success":     true,
			"template_id": tmpl.ID,
		})
	}
	return c.Redirect("/settings/company/templates?saved=1", fiber.StatusSeeOther)
}

// handleGetDefaultInvoiceTemplate retrieves the default template for a company.
// GET /api/invoice-templates/default
// Used by invoice creation form to pre-fill template
func (s *Server) handleGetDefaultInvoiceTemplate(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(map[string]string{
			"error": "company context required",
		})
	}

	template, err := services.GetDefaultInvoiceTemplate(s.DB, companyID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(map[string]string{
			"error": fmt.Sprintf("failed to load default template: %v", err),
		})
	}

	if template == nil {
		return c.Status(fiber.StatusNotFound).JSON(map[string]string{
			"message": "no default template set",
		})
	}

	return c.JSON(map[string]interface{}{
		"template": template,
	})
}
