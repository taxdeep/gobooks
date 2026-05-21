package web

import (
	"strings"

	"github.com/gofiber/fiber/v2"
)

func (s *Server) redirectLegacySettingsCompany(c *fiber.Ctx) error {
	suffix := strings.TrimPrefix(c.Path(), "/settings/company")
	target := "/setting/company" + suffix
	if query := string(c.Request().URI().QueryString()); query != "" {
		target += "?" + query
	}
	return c.Redirect(target, fiber.StatusPermanentRedirect)
}
