// 遵循project_guide.md
package web

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

func (s *Server) handleUserPreferencesHub(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	vm := pages.UserPreferencesHubVM{
		HasCompany: true,
		Breadcrumb: []pages.SettingsBreadcrumbPart{
			{Label: "Settings"},
			{Label: "User Preferences"},
		},
	}
	return pages.UserPreferencesHub(vm).Render(c.Context(), c)
}

func (s *Server) handleUserPrefSystemSetupGet(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	numFmt := services.GetUserNumberFormat(s.DB, user.ID)
	vm := pages.UserPrefSystemSetupVM{
		HasCompany:   true,
		NumberFormat: numFmt,
		Saved:        c.Query("saved") == "1",
		Breadcrumb: []pages.SettingsBreadcrumbPart{
			{Label: "Settings"},
			{Label: "User Preferences", Href: "/settings/user-preferences"},
			{Label: "System Setup"},
		},
	}
	return pages.UserPrefSystemSetup(vm).Render(c.Context(), c)
}

func (s *Server) handleUserPrefSystemSetupPost(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}

	numFmt := strings.TrimSpace(c.FormValue("number_format"))

	// Validate
	valid := false
	for _, opt := range models.NumberFormatOptions {
		if opt.Value == numFmt {
			valid = true
			break
		}
	}

	vm := pages.UserPrefSystemSetupVM{
		HasCompany:   true,
		NumberFormat: numFmt,
		Breadcrumb: []pages.SettingsBreadcrumbPart{
			{Label: "Settings"},
			{Label: "User Preferences", Href: "/settings/user-preferences"},
			{Label: "System Setup"},
		},
	}

	if !valid {
		vm.FormError = "Please select a valid number format."
		return pages.UserPrefSystemSetup(vm).Render(c.Context(), c)
	}

	if err := services.SaveUserNumberFormat(s.DB, user.ID, numFmt); err != nil {
		vm.FormError = "Could not save preference: " + err.Error()
		return pages.UserPrefSystemSetup(vm).Render(c.Context(), c)
	}

	return c.Redirect("/settings/user-preferences/system-setup?saved=1", fiber.StatusSeeOther)
}
