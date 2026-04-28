// 遵循project_guide.md
package web

import (
	"github.com/gofiber/fiber/v2"

	"balanciz/internal/numbering"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

func (s *Server) handleNumberingSettingsGet(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	rules, err := services.LoadMergedDisplayRules(s.DB, companyID)
	if err != nil {
		return pages.NumberingSettings(pages.NumberingSettingsVM{
			HasCompany: true,
			Breadcrumb: breadcrumbSettingsCompanyNumbering(),
			FormError:  "Could not load numbering settings.",
			Rules:      numbering.DefaultDisplayRules(),
		}).Render(c.Context(), c)
	}

	return pages.NumberingSettings(pages.NumberingSettingsVM{
		HasCompany: true,
		Breadcrumb: breadcrumbSettingsCompanyNumbering(),
		Rules:      rules,
		Saved:      c.Query("saved") == "1",
	}).Render(c.Context(), c)
}

func (s *Server) handleNumberingSettingsPost(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	beforeRules, err := services.LoadMergedDisplayRules(s.DB, companyID)
	if err != nil {
		beforeRules = numbering.DefaultDisplayRules()
	}

	rules, err := numbering.ParseRulesPost(c)
	if err != nil {
		return pages.NumberingSettings(pages.NumberingSettingsVM{
			HasCompany: true,
			Breadcrumb: breadcrumbSettingsCompanyNumbering(),
			FormError:  "Invalid form data.",
			Rules:      numbering.DefaultDisplayRules(),
		}).Render(c.Context(), c)
	}

	if err := services.SaveMergedDisplayRules(s.DB, companyID, rules); err != nil {
		return pages.NumberingSettings(pages.NumberingSettingsVM{
			HasCompany: true,
			Breadcrumb: breadcrumbSettingsCompanyNumbering(),
			FormError:  "Could not save numbering settings. Please try again.",
			Rules:      rules,
		}).Render(c.Context(), c)
	}

	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	services.TryWriteAuditLogWithContextDetails(s.DB, "settings.numbering.saved", "settings", companyID, actor, map[string]any{
		"modules":    len(rules),
		"company_id": companyID,
	}, &cid, &uid, beforeRules, rules)

	return c.Redirect("/settings/company/numbering?saved=1", fiber.StatusSeeOther)
}
