// 遵循project_guide.md
package web

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/services"
)

// handleAccountRecommendations POST /api/accounts/recommendations — unified account recommendations (rule-only by default; set "enhance": true for optional AI).
func (s *Server) handleAccountRecommendations(c *fiber.Ctx) error {
	if UserFromCtx(c) == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "no active company"})
	}

	var req services.AccountRecommendationRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON"})
	}

	if req.CompanyID != 0 && req.CompanyID != companyID {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "company_id does not match active company"})
	}

	codeLen := req.CodeLength
	if codeLen == 0 {
		var err error
		codeLen, err = companyAccountCodeLength(s.DB, companyID)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not load company settings"})
		}
	}
	req.CodeLength = codeLen

	out, err := services.RecommendAccount(s.DB, companyID, req)
	if err != nil {
		msg := strings.TrimSpace(err.Error())
		if msg == "" {
			msg = "invalid request"
		}
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": msg})
	}

	return c.JSON(out)
}
