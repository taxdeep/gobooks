// 遵循project_guide.md
package web

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

// handleCompanySecurityGet renders the company security settings page.
func (s *Server) handleCompanySecurityGet(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	sysSettings, _ := services.LoadSystemSecuritySettings(s.DB)

	row, err := services.LoadCompanySecuritySettings(s.DB, companyID)
	if err != nil {
		return pages.CompanySecuritySettings(pages.CompanySecuritySettingsVM{
			HasCompany:             true,
			Breadcrumb:             breadcrumbSettingsCompanySecurity(),
			FormError:              "Could not load security settings.",
			CompanyOverrideAllowed: sysSettings.UnusualIPLoginCompanyOverrideAllowed,
		}).Render(c.Context(), c)
	}

	vm := companySecurityVMFromRow(row, sysSettings.UnusualIPLoginCompanyOverrideAllowed, !CanFromCtx(c, ActionSettingsUpdate))
	vm.Saved = c.Query("saved") == "1"
	return pages.CompanySecuritySettings(vm).Render(c.Context(), c)
}

// handleCompanySecurityPost saves company security settings.
func (s *Server) handleCompanySecurityPost(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	sysSettings, _ := services.LoadSystemSecuritySettings(s.DB)
	if !sysSettings.UnusualIPLoginCompanyOverrideAllowed {
		return fiber.NewError(fiber.StatusForbidden, "System administrator has not enabled company security overrides")
	}

	channel := strings.TrimSpace(c.FormValue("unusual_ip_login_alert_channel"))
	if channel == "" {
		channel = "email"
	}

	in := services.CompanySecuritySettingsInput{
		UnusualIPLoginAlertEnabled: c.FormValue("unusual_ip_login_alert_enabled") == "true",
		UnusualIPLoginAlertChannel: models.AlertChannel(channel),
		NewDeviceLoginAlertEnabled: false, // coming soon — not yet submitted
		PasswordResetAlertEnabled:  false, // coming soon
		FailedLoginAlertEnabled:    false, // coming soon
		FutureRulesJSON:            nil,
	}

	if err := services.UpsertCompanySecuritySettings(s.DB, companyID, in); err != nil {
		row, _ := services.LoadCompanySecuritySettings(s.DB, companyID)
		vm := companySecurityVMFromRow(row, sysSettings.UnusualIPLoginCompanyOverrideAllowed, false)
		vm.FormError = "Could not save security settings. Please try again."
		vm.UnusualIPLoginAlertEnabled = in.UnusualIPLoginAlertEnabled
		vm.UnusualIPLoginAlertChannel = string(in.UnusualIPLoginAlertChannel)
		return pages.CompanySecuritySettings(vm).Render(c.Context(), c)
	}

	uid := user.ID
	cid := companyID
	services.TryWriteAuditLogWithContextDetails(s.DB,
		"settings.company.security.saved", "settings", companyID,
		user.Email, map[string]any{"company_id": companyID},
		&cid, &uid, nil, nil,
	)

	return c.Redirect("/settings/company/security?saved=1", fiber.StatusSeeOther)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func companySecurityVMFromRow(row models.CompanySecuritySettings, overrideAllowed bool, readOnly bool) pages.CompanySecuritySettingsVM {
	channel := string(row.UnusualIPLoginAlertChannel)
	if channel == "" {
		channel = "email"
	}
	return pages.CompanySecuritySettingsVM{
		HasCompany:                 true,
		Breadcrumb:                 breadcrumbSettingsCompanySecurity(),
		ReadOnly:                   readOnly,
		UnusualIPLoginAlertEnabled: row.UnusualIPLoginAlertEnabled,
		UnusualIPLoginAlertChannel: channel,
		NewDeviceLoginAlertEnabled: row.NewDeviceLoginAlertEnabled,
		PasswordResetAlertEnabled:  row.PasswordResetAlertEnabled,
		FailedLoginAlertEnabled:    row.FailedLoginAlertEnabled,
		CompanyOverrideAllowed:     overrideAllowed,
	}
}
