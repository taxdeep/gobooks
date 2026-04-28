// 遵循project_guide.md
package admin

import (
	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/admintmpl"
	"balanciz/internal/web/templates/pages"
)

// handleAdminSecurityGet renders the system security settings page.
func (s *Server) handleAdminSecurityGet(c *fiber.Ctx) error {
	row, err := services.LoadSystemSecuritySettings(s.DB)
	if err != nil {
		return admintmpl.AdminSecurity(pages.SystemSecuritySettingsVM{
			AdminEmail:      AdminUserFromCtx(c).Email,
			MaintenanceMode: IsMaintenanceMode(),
			FormError:       "Could not load security settings.",
		}).Render(c.Context(), c)
	}

	vm := sysSecurityVMFromRow(row, AdminUserFromCtx(c).Email)
	vm.Flash = c.Query("flash")
	return admintmpl.AdminSecurity(vm).Render(c.Context(), c)
}

// handleAdminSecurityPost saves the singleton system security settings.
func (s *Server) handleAdminSecurityPost(c *fiber.Ctx) error {
	in := services.SystemSecuritySettingsInput{
		UnusualIPLoginAlertDefaultEnabled:    c.FormValue("unusual_ip_login_alert_default_enabled") == "true",
		UnusualIPLoginCompanyOverrideAllowed: c.FormValue("unusual_ip_login_company_override_allowed") == "true",
		NewDeviceLoginAlertDefaultEnabled:    false, // coming soon — not yet submitted
		PasswordResetAlertDefaultEnabled:     false, // coming soon
		FailedLoginAlertDefaultEnabled:       false, // coming soon
		GlobalSecurityRulesJSON:              nil,
	}

	if err := services.UpsertSystemSecuritySettings(s.DB, in); err != nil {
		row, _ := services.LoadSystemSecuritySettings(s.DB)
		vm := sysSecurityVMFromRow(row, AdminUserFromCtx(c).Email)
		vm.FormError = "Could not save security settings. Please try again."
		vm.UnusualIPLoginAlertDefaultEnabled = in.UnusualIPLoginAlertDefaultEnabled
		vm.UnusualIPLoginCompanyOverrideAllowed = in.UnusualIPLoginCompanyOverrideAllowed
		return admintmpl.AdminSecurity(vm).Render(c.Context(), c)
	}

	services.TryWriteAuditLog(s.DB, "admin.settings.security.saved", "system", 0,
		AdminUserFromCtx(c).Email, map[string]any{"actor_type": "sysadmin"},
	)

	return c.Redirect("/admin/settings/security?flash=settings_saved", fiber.StatusSeeOther)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func sysSecurityVMFromRow(row models.SystemSecuritySettings, adminEmail string) pages.SystemSecuritySettingsVM {
	return pages.SystemSecuritySettingsVM{
		AdminEmail:                           adminEmail,
		MaintenanceMode:                      IsMaintenanceMode(),
		UnusualIPLoginAlertDefaultEnabled:    row.UnusualIPLoginAlertDefaultEnabled,
		UnusualIPLoginCompanyOverrideAllowed: row.UnusualIPLoginCompanyOverrideAllowed,
		NewDeviceLoginAlertDefaultEnabled:    row.NewDeviceLoginAlertDefaultEnabled,
		PasswordResetAlertDefaultEnabled:     row.PasswordResetAlertDefaultEnabled,
		FailedLoginAlertDefaultEnabled:       row.FailedLoginAlertDefaultEnabled,
	}
}
