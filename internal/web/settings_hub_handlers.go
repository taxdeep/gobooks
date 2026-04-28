// 遵循project_guide.md
package web

import (
	"github.com/gofiber/fiber/v2"

	"balanciz/internal/web/templates/pages"
)

// handleSettingsHub renders the top-level Settings landing page.
// Every /settings/* sub-page points its "Settings" breadcrumb back to this hub.
func (s *Server) handleSettingsHub(c *fiber.Ctx) error {
	if _, ok := ActiveCompanyIDFromCtx(c); !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	return pages.SettingsHub(pages.SettingsHubVM{
		HasCompany: true,
		Breadcrumb: []pages.SettingsBreadcrumbPart{
			{Label: "Settings", Href: ""},
		},
	}).Render(c.Context(), c)
}
