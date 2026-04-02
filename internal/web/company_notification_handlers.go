// 遵循project_guide.md
package web

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"gobooks/internal/models"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

// handleCompanyNotificationsGet renders the company notification settings page.
func (s *Server) handleCompanyNotificationsGet(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	sysSetting, _ := services.LoadSystemNotificationSettings(s.DB)

	row, err := services.LoadCompanyNotificationSettings(s.DB, companyID)
	if err != nil {
		return pages.CompanyNotificationSettings(pages.CompanyNotificationSettingsVM{
			HasCompany:           true,
			Breadcrumb:           breadcrumbSettingsCompanyNotifications(),
			FormError:            "Could not load notification settings.",
			SystemAllowsOverride: sysSetting.AllowCompanyOverride,
		}).Render(c.Context(), c)
	}

	vm := companyNotifVMFromRow(row, sysSetting.AllowCompanyOverride, !CanFromCtx(c, ActionSettingsUpdate))
	vm.Saved = c.Query("saved") == "1"

	switch c.Query("test_email") {
	case "ok":
		vm.TestEmailResult = "ok"
		vm.TestEmailMsg = "Test email sent. Check the configured inbox to confirm delivery."
	case "err":
		vm.TestEmailResult = "err"
		vm.TestEmailMsg = c.Query("test_email_msg")
		if vm.TestEmailMsg == "" {
			vm.TestEmailMsg = "Test email failed. Check your SMTP configuration."
		}
	}

	switch c.Query("test_sms") {
	case "ok":
		vm.TestSMSResult = "ok"
		vm.TestSMSMsg = "Test SMS submitted. (Stub — no actual SMS sent yet.)"
	case "err":
		vm.TestSMSResult = "err"
		vm.TestSMSMsg = c.Query("test_sms_msg")
		if vm.TestSMSMsg == "" {
			vm.TestSMSMsg = "Test SMS failed. Check your SMS configuration."
		}
	}

	return pages.CompanyNotificationSettings(vm).Render(c.Context(), c)
}

// handleCompanyNotificationsPost saves company notification settings.
func (s *Server) handleCompanyNotificationsPost(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	sysSetting, _ := services.LoadSystemNotificationSettings(s.DB)
	if !sysSetting.AllowCompanyOverride {
		return fiber.NewError(fiber.StatusForbidden, "System administrator has not enabled company overrides")
	}

	port, _ := strconv.Atoi(strings.TrimSpace(c.FormValue("smtp_port")))
	if port <= 0 {
		port = 587
	}

	in := services.CompanyNotificationSettingsInput{
		EmailEnabled:        c.FormValue("email_enabled") == "true",
		SMTPHost:            strings.TrimSpace(c.FormValue("smtp_host")),
		SMTPPort:            port,
		SMTPUsername:        strings.TrimSpace(c.FormValue("smtp_username")),
		SMTPPassword:        strings.TrimSpace(c.FormValue("smtp_password")),
		SMTPFromEmail:       strings.TrimSpace(c.FormValue("smtp_from_email")),
		SMTPFromName:        strings.TrimSpace(c.FormValue("smtp_from_name")),
		SMTPEncryption:      models.SMTPEncryption(strings.TrimSpace(c.FormValue("smtp_encryption"))),
		SMSEnabled:          c.FormValue("sms_enabled") == "true",
		SMSProvider:         strings.TrimSpace(c.FormValue("sms_provider")),
		SMSAPIKey:           strings.TrimSpace(c.FormValue("sms_api_key")),
		SMSAPISecret:        strings.TrimSpace(c.FormValue("sms_api_secret")),
		SMSSenderID:         strings.TrimSpace(c.FormValue("sms_sender_id")),
		AllowSystemFallback: c.FormValue("allow_system_fallback") == "true",
	}

	// Validate: require minimal fields when a channel is enabled.
	if in.EmailEnabled && (in.SMTPHost == "" || in.SMTPFromEmail == "") {
		row, _ := services.LoadCompanyNotificationSettings(s.DB, companyID)
		vm := companyNotifVMFromRow(row, sysSetting.AllowCompanyOverride, false)
		vm.FormError = "SMTP host and From email are required when email is enabled."
		applyFormOverrides(&vm, in)
		return pages.CompanyNotificationSettings(vm).Render(c.Context(), c)
	}
	if in.SMSEnabled && (in.SMSProvider == "" || in.SMSSenderID == "") {
		row, _ := services.LoadCompanyNotificationSettings(s.DB, companyID)
		vm := companyNotifVMFromRow(row, sysSetting.AllowCompanyOverride, false)
		vm.FormError = "SMS provider and Sender ID are required when SMS is enabled."
		applyFormOverrides(&vm, in)
		return pages.CompanyNotificationSettings(vm).Render(c.Context(), c)
	}

	if err := services.UpsertCompanyNotificationSettings(s.DB, companyID, in); err != nil {
		row, _ := services.LoadCompanyNotificationSettings(s.DB, companyID)
		vm := companyNotifVMFromRow(row, sysSetting.AllowCompanyOverride, false)
		vm.FormError = "Could not save notification settings. Please try again."
		return pages.CompanyNotificationSettings(vm).Render(c.Context(), c)
	}

	uid := user.ID
	cid := companyID
	services.TryWriteAuditLogWithContextDetails(s.DB,
		"settings.company.notifications.saved", "settings", companyID,
		user.Email, map[string]any{"company_id": companyID},
		&cid, &uid, nil, nil,
	)

	return c.Redirect("/settings/company/notifications?saved=1", fiber.StatusSeeOther)
}

// handleCompanyNotificationsTestEmail runs a test email using the company SMTP config.
// The outcome is persisted to the DB so readiness state survives page reloads.
func (s *Server) handleCompanyNotificationsTestEmail(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	row, err := services.LoadCompanyNotificationSettings(s.DB, companyID)
	if err != nil || row.ID == 0 {
		return c.Redirect("/settings/company/notifications?test_email=err&test_email_msg=Save+settings+first#email-test-card", fiber.StatusSeeOther)
	}

	cfg := services.EmailConfig{
		Host:       row.SMTPHost,
		Port:       row.SMTPPort,
		Username:   row.SMTPUsername,
		Password:   row.SMTPPasswordEncrypted, // decrypted by LoadCompanyNotificationSettings
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
	_ = services.RecordCompanyEmailTestResult(s.DB, companyID, success, errMsg, user.Email)

	if !success {
		return c.Redirect("/settings/company/notifications?test_email=err&test_email_msg="+url.QueryEscape(errMsg)+"#email-test-card", fiber.StatusSeeOther)
	}
	return c.Redirect("/settings/company/notifications?test_email=ok#email-test-card", fiber.StatusSeeOther)
}

// handleCompanyNotificationsTestSMS runs a test SMS using the company SMS config.
// The outcome is persisted to the DB so readiness state survives page reloads.
func (s *Server) handleCompanyNotificationsTestSMS(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	row, err := services.LoadCompanyNotificationSettings(s.DB, companyID)
	if err != nil || row.ID == 0 {
		return c.Redirect("/settings/company/notifications?test_sms=err&test_sms_msg=Save+settings+first#sms-test-card", fiber.StatusSeeOther)
	}

	cfg := services.SMSConfig{
		Provider:  row.SMSProvider,
		APIKey:    row.SMSAPIKeyEncrypted, // decrypted by LoadCompanyNotificationSettings
		APISecret: row.SMSAPISecretEncrypted,
		SenderID:  row.SMSSenderID,
	}
	_, testErr := services.SendTestSMS(cfg)

	success := testErr == nil
	errMsg := ""
	if testErr != nil {
		errMsg = testErr.Error()
	}
	_ = services.RecordCompanySMSTestResult(s.DB, companyID, success, errMsg, user.Email)

	if !success {
		return c.Redirect("/settings/company/notifications?test_sms=err&test_sms_msg="+url.QueryEscape(errMsg)+"#sms-test-card", fiber.StatusSeeOther)
	}
	return c.Redirect("/settings/company/notifications?test_sms=ok#sms-test-card", fiber.StatusSeeOther)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func companyNotifVMFromRow(row models.CompanyNotificationSettings, systemAllowsOverride bool, readOnly bool) pages.CompanyNotificationSettingsVM {
	port := strconv.Itoa(row.SMTPPort)
	if row.SMTPPort <= 0 {
		port = "587"
	}
	return pages.CompanyNotificationSettingsVM{
		HasCompany:             true,
		Breadcrumb:             breadcrumbSettingsCompanyNotifications(),
		ReadOnly:               readOnly,
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
		AllowSystemFallback:    row.AllowSystemFallback,
		SystemAllowsOverride:   systemAllowsOverride,
		EmailStatus:            notifEmailStatusVM(row.EmailTestStatus, row.EmailLastTestedAt, row.EmailLastSuccessAt, row.EmailLastFailureAt, row.EmailLastTestedBy, row.EmailLastError, row.EmailVerificationReady),
		SMSStatus:              notifSMSStatusVM(row.SMSTestStatus, row.SMSLastTestedAt, row.SMSLastSuccessAt, row.SMSLastFailureAt, row.SMSLastTestedBy, row.SMSLastError, row.SMSVerificationReady),
	}
}

// notifEmailStatusVM builds a NotifChannelStatusVM from raw model fields.
func notifEmailStatusVM(
	status models.NotifTestStatus,
	lastTestedAt, lastSuccessAt, lastFailureAt *time.Time,
	lastTestedBy, lastError string,
	ready bool,
) pages.NotifChannelStatusVM {
	return pages.NotifChannelStatusVM{
		TestStatus:        string(status),
		LastTestedAt:      fmtOptTime(lastTestedAt),
		LastTestedBy:      lastTestedBy,
		LastSuccessAt:     fmtOptTime(lastSuccessAt),
		LastFailureAt:     fmtOptTime(lastFailureAt),
		LastError:         lastError,
		VerificationReady: ready,
	}
}

// notifSMSStatusVM builds a NotifChannelStatusVM from raw SMS model fields.
func notifSMSStatusVM(
	status models.NotifTestStatus,
	lastTestedAt, lastSuccessAt, lastFailureAt *time.Time,
	lastTestedBy, lastError string,
	ready bool,
) pages.NotifChannelStatusVM {
	return pages.NotifChannelStatusVM{
		TestStatus:        string(status),
		LastTestedAt:      fmtOptTime(lastTestedAt),
		LastTestedBy:      lastTestedBy,
		LastSuccessAt:     fmtOptTime(lastSuccessAt),
		LastFailureAt:     fmtOptTime(lastFailureAt),
		LastError:         lastError,
		VerificationReady: ready,
	}
}

// fmtOptTime formats a nullable time pointer as "2006-01-02 15:04 UTC".
// Returns empty string if nil.
func fmtOptTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

// applyFormOverrides copies form-entered non-secret values into vm so the user
// does not lose their edits when validation fails.
func applyFormOverrides(vm *pages.CompanyNotificationSettingsVM, in services.CompanyNotificationSettingsInput) {
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
	vm.AllowSystemFallback = in.AllowSystemFallback
}
