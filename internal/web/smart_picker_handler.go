// 遵循project_guide.md
package web

import (
	"strconv"

	"github.com/gofiber/fiber/v2"
)

// handleSmartPickerSearch serves GET /api/smart-picker/search.
// Query params:
//
//	entity  — required; maps to SmartPickerProvider.EntityType() (e.g. "account")
//	context — optional; narrows purpose within the entity (e.g. "expense_form_category")
//	q       — optional search string
//	limit   — optional integer 1–50 (default 20)
//
// companyID is always sourced from the authenticated session — never from query params.
func (s *Server) handleSmartPickerSearch(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "no active company"})
	}

	entity := c.Query("entity")
	if entity == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "entity param required"})
	}

	provider, ok := defaultSmartPickerRegistry.get(entity)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "unknown entity: " + entity})
	}

	limit := 20
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 20 {
		limit = 20
	}

	ctx := SmartPickerContext{
		CompanyID: companyID,
		Context:   c.Query("context"),
		Limit:     limit,
	}

	result, err := provider.Search(s.DB, ctx, c.Query("q"))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "search failed"})
	}

	return c.JSON(result)
}
