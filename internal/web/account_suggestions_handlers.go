// 遵循project_guide.md
package web

import (
	"github.com/gofiber/fiber/v2"

	"balanciz/internal/services"
)

func (s *Server) handleAccountSuggestions(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "no active company"})
	}

	codeLen, err := companyAccountCodeLength(s.DB, companyID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not load company settings"})
	}

	var in services.AccountRecommendationInput
	if err := c.BodyParser(&in); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON"})
	}

	rec, err := services.RuleBasedAccountRecommendation(s.DB, companyID, codeLen, in)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(rec)
}
