// 遵循project_guide.md
package admin

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/admintmpl"
	"balanciz/internal/web/templates/pages"
)

// handleAdminNotificationsGet renders the system notification settings page.
func (s *Server) handleAdminNotificationsGet(c *fiber.Ctx) error {
	row, err := services.LoadSystemNotificationSettings(s.DB)
	if err != nil {
		return admintmpl.AdminNotifications(pages.SystemNotificationSettingsVM{
			AdminEmail:      AdminUserFromCtx(c).Email,
			MaintenanceMode: IsMaintenanceMode(),
			FormError:       "Could not load notification settings.",
		}).Render(c.Context(), c)
	}

	vm := sysNotifVMFromRow(row, AdminUserFromCtx(c).Email)
	vm.Flash = c.Query("flash")
	return admintmpl.AdminNotifications(vm).Render(c.Context(), c)
}

// handleAdminNotificationsPost saves the singleton system notification settings.
func (s *Server) handleAdminNotificationsPost(c *fiber.Ctx) error {
	port, _ := strconv.Atoi(strings.TrimSpace(c.FormValue("smtp_port")))
	if port <= 0 {
		port = 587
	}

	in := services.SystemNotificationSettingsInput{
		EmailEnabled:         c.FormValue("email_enabled") == "true",
		SMTPHost:             strings.TrimSpace(c.FormValue("smtp_host")),
		SMTPPort:             port,
		SMTPUsername:         strings.TrimSpace(c.FormValue("smtp_username")),
		SMTPPassword:         strings.TrimSpace(c.FormValue("smtp_password")),
		SMTPFromEmail:        strings.TrimSpace(c.FormValue("smtp_from_email")),
		SMTPFromName:         strings.TrimSpace(c.FormValue("smtp_from_name")),
		SMTPEncryption:       models.SMTPEncryption(strings.TrimSpace(c.FormValue("smtp_encryption"))),
		SMSEnabled:           c.FormValue("sms_enabled") == "true",
		SMSProvider:          strings.TrimSpace(c.FormValue("sms_provider")),
		SMSAPIKey:            strings.TrimSpace(c.FormValue("sms_api_key")),
		SMSAPISecret:         strings.TrimSpace(c.FormValue("sms_api_secret")),
		SMSSenderID:          strings.TrimSpace(c.FormValue("sms_sender_id")),
		AllowCompanyOverride: c.FormValue("allow_company_override") == "true",
	}

	// Validate minimal fields when a channel is enabled.
	if in.EmailEnabled && (in.SMTPHost == "" || in.SMTPFromEmail == "") {
		row, _ := services.LoadSystemNotificationSettings(s.DB)
		vm := sysNotifVMFromRow(row, AdminUserFromCtx(c).Email)
		vm.FormError = "SMTP host and From email are required when email is enabled."
		applySysNotifFormOverrides(&vm, in)
		return admintmpl.AdminNotifications(vm).Render(c.Context(), c)
	}
	if in.SMSEnabled && (in.SMSProvider == "" || in.SMSSenderID == "") {
		row, _ := services.LoadSystemNotificationSettings(s.DB)
		vm := sysNotifVMFromRow(row, AdminUserFromCtx(c).Email)
		vm.FormError = "SMS provider and Sender ID are required when SMS is enabled."
		applySysNotifFormOverrides(&vm, in)
		return admintmpl.AdminNotifications(vm).Render(c.Context(), c)
	}

	if err := services.UpsertSystemNotificationSettings(s.DB, in); err != nil {
		row, _ := services.LoadSystemNotificationSettings(s.DB)
		vm := sysNotifVMFromRow(row, AdminUserFromCtx(c).Email)
		vm.FormError = "Could not save notification settings. Please try again."
		return admintmpl.AdminNotifications(vm).Render(c.Context(), c)
	}

	services.TryWriteAuditLog(s.DB, "admin.settings.notifications.saved", "system", 0,
		AdminUserFromCtx(c).Email, map[string]any{"actor_type": "sysadmin"},
	)

	return c.Redirect("/admin/settings/notifications?flash=settings_saved", fiber.StatusSeeOther)
}

// handleAdminNotificationsTestEmail runs a test email using the system SMTP config.
// The outcome is persisted so readiness state survives page reloads.
func (s *Server) handleAdminNotificationsTestEmail(c *fiber.Ctx) error {
	row, err := services.LoadSystemNotificationSettings(s.DB)
	if err != nil || row.ID == 0 {
		return c.Redirect("/admin/settings/notifications?flash=test_email_err", fiber.StatusSeeOther)
	}

	cfg := services.EmailConfig{
		Host:       row.SMTPHost,
		Port:       row.SMTPPort,
		Username:   row.SMTPUsername,
		Password:   row.SMTPPasswordEncrypted, // decrypted by LoadSystemNotificationSettings
		FromEmail:  row.SMTPFromEmail,
		FromName:   row.SMTPFromName,
		Encryption: row.SMTPEncryption,
	}
	_, testErr := services.SendTestEmail(cfg)

	success := testErr == nil
	errMsg := ""
	if testErr != nil {
		errMsg = testErr.Error()
	}
	_ = services.RecordSystemEmailTestResult(s.DB, success, errMsg, AdminUserFromCtx(c).Email)

	if !success {
		return c.Redirect("/admin/settings/notifications?flash=test_email_err&msg="+url.QueryEscape(errMsg), fiber.StatusSeeOther)
	}
	return c.Redirect("/admin/settings/notifications?flash=test_email_ok", fiber.StatusSeeOther)
}

// handleAdminNotificationsTestSMS runs a test SMS using the system SMS config.
// The outcome is persisted so readiness state survives page reloads.
func (s *Server) handleAdminNotificationsTestSMS(c *fiber.Ctx) error {
	row, err := services.LoadSystemNotificationSettings(s.DB)
	if err != nil || row.ID == 0 {
		return c.Redirect("/admin/settings/notifications?flash=test_sms_err", fiber.StatusSeeOther)
	}

	cfg := services.SMSConfig{
		Provider:  row.SMSProvider,
		APIKey:    row.SMSAPIKeyEncrypted, // decrypted by LoadSystemNotificationSettings
		APISecret: row.SMSAPISecretEncrypted,
		SenderID:  row.SMSSenderID,
	}
	_, testErr := services.SendTestSMS(cfg)

	success := testErr == nil
	errMsg := ""
	if testErr != nil {
		errMsg = testErr.Error()
	}
	_ = services.RecordSystemSMSTestResult(s.DB, success, errMsg, AdminUserFromCtx(c).Email)

	if !success {
		return c.Redirect("/admin/settings/notifications?flash=test_sms_err&msg="+url.QueryEscape(errMsg), fiber.StatusSeeOther)
	}
	return c.Redirect("/admin/settings/notifications?flash=test_sms_ok", fiber.StatusSeeOther)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func sysNotifVMFromRow(row models.SystemNotificationSettings, adminEmail string) pages.SystemNotificationSettingsVM {
	port := strconv.Itoa(row.SMTPPort)
	if row.SMTPPort <= 0 {
		port = "587"
	}
	return pages.SystemNotificationSettingsVM{
		AdminEmail:             adminEmail,
		MaintenanceMode:        IsMaintenanceMode(),
		EmailEnabled:           row.EmailEnabled,
		SMTPHost:               row.SMTPHost,
		SMTPPort:               port,
		SMTPUsername:           row.SMTPUsername,
		SMTPPasswordMaskedHint: row.SMTPPasswordMaskedHint,
		SMTPFromEmail:          row.SMTPFromEmail,
		SMTPFromName:           row.SMTPFromName,
		SMTPEncryption:         string(row.SMTPEncryption),
		SMSEnabled:             row.SMSEnabled,
		SMSProvider:            row.SMSProvider,
		SMSAPIKeyMaskedHint:    row.SMSAPIKeyMaskedHint,
		SMSAPISecretMaskedHint: row.SMSAPISecretMaskedHint,
		SMSSenderID:            row.SMSSenderID,
		AllowCompanyOverride:   row.AllowCompanyOverride,
		EmailStatus:            sysNotifEmailStatusVM(row),
		SMSStatus:              sysNotifSMSStatusVM(row),
	}
}

func sysNotifEmailStatusVM(row models.SystemNotificationSettings) pages.NotifChannelStatusVM {
	return pages.NotifChannelStatusVM{
		TestStatus:        string(row.EmailTestStatus),
		LastTestedAt:      fmtOptTime(row.EmailLastTestedAt),
		LastTestedBy:      row.EmailLastTestedBy,
		LastSuccessAt:     fmtOptTime(row.EmailLastSuccessAt),
		LastFailureAt:     fmtOptTime(row.EmailLastFailureAt),
		LastError:         row.EmailLastError,
		VerificationReady: row.EmailVerificationReady,
	}
}

func sysNotifSMSStatusVM(row models.SystemNotificationSettings) pages.NotifChannelStatusVM {
	return pages.NotifChannelStatusVM{
		TestStatus:        string(row.SMSTestStatus),
		LastTestedAt:      fmtOptTime(row.SMSLastTestedAt),
		LastTestedBy:      row.SMSLastTestedBy,
		LastSuccessAt:     fmtOptTime(row.SMSLastSuccessAt),
		LastFailureAt:     fmtOptTime(row.SMSLastFailureAt),
		LastError:         row.SMSLastError,
		VerificationReady: row.SMSVerificationReady,
	}
}

// fmtOptTime formats a nullable time pointer as "2006-01-02 15:04 UTC".
func fmtOptTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

func applySysNotifFormOverrides(vm *pages.SystemNotificationSettingsVM, in services.SystemNotificationSettingsInput) {
	vm.EmailEnabled = in.EmailEnabled
	vm.SMTPHost = in.SMTPHost
	vm.SMTPPort = strconv.Itoa(in.SMTPPort)
	vm.SMTPUsername = in.SMTPUsername
	vm.SMTPFromEmail = in.SMTPFromEmail
	vm.SMTPFromName = in.SMTPFromName
	vm.SMTPEncryption = string(in.SMTPEncryption)
	vm.SMSEnabled = in.SMSEnabled
	vm.SMSProvider = in.SMSProvider
	vm.SMSSenderID = in.SMSSenderID
	vm.AllowCompanyOverride = in.AllowCompanyOverride
}
