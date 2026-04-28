// 遵循project_guide.md
package services

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"balanciz/internal/models"

	"gorm.io/gorm"
)

// ── Config hash helpers ────────────────────────────────────────────────────────
//
// The hash captures all config fields whose change should invalidate a prior
// successful test. Encrypted secrets are NOT included directly (their ciphertext
// changes on every encrypt call due to the random GCM nonce). Instead the
// masked hint is used: it changes whenever the underlying secret is replaced,
// which is sufficient to detect secret rotation without exposing plaintext.

func emailConfigHash(
	enabled bool, host string, port int, username, maskedHint,
	fromEmail, fromName string, enc models.SMTPEncryption,
) string {
	data := fmt.Sprintf("%v|%s|%d|%s|%s|%s|%s|%s",
		enabled, host, port, username, maskedHint, fromEmail, fromName, string(enc))
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

func smsConfigHash(
	enabled bool, provider, apiKeyHint, apiSecretHint, senderID string,
) string {
	data := fmt.Sprintf("%v|%s|%s|%s|%s",
		enabled, provider, apiKeyHint, apiSecretHint, senderID)
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

// emailConfigComplete returns true when the minimum required SMTP fields are
// present for an actual send attempt. Mirrors ValidateEmailConfig.
func emailConfigComplete(host, fromEmail string, port int) bool {
	return strings.TrimSpace(host) != "" &&
		strings.TrimSpace(fromEmail) != "" &&
		port > 0
}

// smsConfigComplete returns true when the minimum required SMS fields are present.
func smsConfigComplete(provider, apiKeyHint, senderID string) bool {
	return strings.TrimSpace(provider) != "" &&
		strings.TrimSpace(apiKeyHint) != "" &&
		strings.TrimSpace(senderID) != ""
}

// ── Company: update config hashes on every save ───────────────────────────────

// applyCompanyEmailConfigHash recomputes the email config hash from the row and
// updates EmailConfigHash in place. If the hash has changed since the last
// successful test, EmailVerificationReady is set to false. This must be called
// after all form fields (including new masked hints) have been applied to the row.
func applyCompanyEmailConfigHash(row *models.CompanyNotificationSettings) {
	row.EmailConfigHash = emailConfigHash(
		row.EmailEnabled, row.SMTPHost, row.SMTPPort, row.SMTPUsername,
		row.SMTPPasswordMaskedHint, row.SMTPFromEmail, row.SMTPFromName,
		row.SMTPEncryption,
	)
	if row.EmailConfigHash != row.EmailTestedConfigHash {
		row.EmailVerificationReady = false
	}
}

func applyCompanySMSConfigHash(row *models.CompanyNotificationSettings) {
	row.SMSConfigHash = smsConfigHash(
		row.SMSEnabled, row.SMSProvider,
		row.SMSAPIKeyMaskedHint, row.SMSAPISecretMaskedHint,
		row.SMSSenderID,
	)
	if row.SMSConfigHash != row.SMSTestedConfigHash {
		row.SMSVerificationReady = false
	}
}

// ── System: same for the singleton row ────────────────────────────────────────

func applySystemEmailConfigHash(row *models.SystemNotificationSettings) {
	row.EmailConfigHash = emailConfigHash(
		row.EmailEnabled, row.SMTPHost, row.SMTPPort, row.SMTPUsername,
		row.SMTPPasswordMaskedHint, row.SMTPFromEmail, row.SMTPFromName,
		row.SMTPEncryption,
	)
	if row.EmailConfigHash != row.EmailTestedConfigHash {
		row.EmailVerificationReady = false
	}
}

func applySystemSMSConfigHash(row *models.SystemNotificationSettings) {
	row.SMSConfigHash = smsConfigHash(
		row.SMSEnabled, row.SMSProvider,
		row.SMSAPIKeyMaskedHint, row.SMSAPISecretMaskedHint,
		row.SMSSenderID,
	)
	if row.SMSConfigHash != row.SMSTestedConfigHash {
		row.SMSVerificationReady = false
	}
}

// ── Company: record test results ──────────────────────────────────────────────

// RecordCompanyEmailTestResult persists the outcome of a test email for a
// specific company. On success it stamps EmailTestedConfigHash with the current
// EmailConfigHash, marking the config as verified. On failure it clears
// EmailVerificationReady. Both paths record timestamps and actor.
func RecordCompanyEmailTestResult(db *gorm.DB, companyID uint, success bool, errMsg, testedBy string) error {
	row, err := loadCompanyNotificationSettingsRow(db, companyID)
	if err != nil {
		return err
	}
	if row.ID == 0 {
		return errors.New("notification settings not found — save settings before testing")
	}

	now := time.Now().UTC()
	row.EmailLastTestedAt = &now
	row.EmailLastTestedBy = testedBy

	if success {
		row.EmailTestStatus = models.NotifTestStatusSuccess
		row.EmailLastSuccessAt = &now
		row.EmailLastError = ""
		row.EmailTestedConfigHash = row.EmailConfigHash
		row.EmailVerificationReady = row.EmailEnabled &&
			emailConfigComplete(row.SMTPHost, row.SMTPFromEmail, row.SMTPPort) &&
			row.EmailConfigHash == row.EmailTestedConfigHash
	} else {
		row.EmailTestStatus = models.NotifTestStatusFailed
		row.EmailLastFailureAt = &now
		row.EmailLastError = errMsg
		row.EmailVerificationReady = false
	}

	return db.Save(&row).Error
}

// RecordCompanySMSTestResult persists the outcome of a test SMS for a company.
func RecordCompanySMSTestResult(db *gorm.DB, companyID uint, success bool, errMsg, testedBy string) error {
	row, err := loadCompanyNotificationSettingsRow(db, companyID)
	if err != nil {
		return err
	}
	if row.ID == 0 {
		return errors.New("notification settings not found — save settings before testing")
	}

	now := time.Now().UTC()
	row.SMSLastTestedAt = &now
	row.SMSLastTestedBy = testedBy

	if success {
		row.SMSTestStatus = models.NotifTestStatusSuccess
		row.SMSLastSuccessAt = &now
		row.SMSLastError = ""
		row.SMSTestedConfigHash = row.SMSConfigHash
		row.SMSVerificationReady = row.SMSEnabled &&
			smsConfigComplete(row.SMSProvider, row.SMSAPIKeyMaskedHint, row.SMSSenderID) &&
			row.SMSConfigHash == row.SMSTestedConfigHash
	} else {
		row.SMSTestStatus = models.NotifTestStatusFailed
		row.SMSLastFailureAt = &now
		row.SMSLastError = errMsg
		row.SMSVerificationReady = false
	}

	return db.Save(&row).Error
}

// ── System: record test results ───────────────────────────────────────────────

// RecordSystemEmailTestResult persists the test outcome for the system SMTP config.
func RecordSystemEmailTestResult(db *gorm.DB, success bool, errMsg, testedBy string) error {
	row, err := loadSystemNotificationSettingsRow(db)
	if err != nil {
		return err
	}
	if row.ID == 0 {
		return errors.New("system notification settings not found — save settings before testing")
	}

	now := time.Now().UTC()
	row.EmailLastTestedAt = &now
	row.EmailLastTestedBy = testedBy

	if success {
		row.EmailTestStatus = models.NotifTestStatusSuccess
		row.EmailLastSuccessAt = &now
		row.EmailLastError = ""
		row.EmailTestedConfigHash = row.EmailConfigHash
		row.EmailVerificationReady = row.EmailEnabled &&
			emailConfigComplete(row.SMTPHost, row.SMTPFromEmail, row.SMTPPort) &&
			row.EmailConfigHash == row.EmailTestedConfigHash
	} else {
		row.EmailTestStatus = models.NotifTestStatusFailed
		row.EmailLastFailureAt = &now
		row.EmailLastError = errMsg
		row.EmailVerificationReady = false
	}

	return db.Save(&row).Error
}

// RecordSystemSMSTestResult persists the test outcome for the system SMS config.
func RecordSystemSMSTestResult(db *gorm.DB, success bool, errMsg, testedBy string) error {
	row, err := loadSystemNotificationSettingsRow(db)
	if err != nil {
		return err
	}
	if row.ID == 0 {
		return errors.New("system notification settings not found — save settings before testing")
	}

	now := time.Now().UTC()
	row.SMSLastTestedAt = &now
	row.SMSLastTestedBy = testedBy

	if success {
		row.SMSTestStatus = models.NotifTestStatusSuccess
		row.SMSLastSuccessAt = &now
		row.SMSLastError = ""
		row.SMSTestedConfigHash = row.SMSConfigHash
		row.SMSVerificationReady = row.SMSEnabled &&
			smsConfigComplete(row.SMSProvider, row.SMSAPIKeyMaskedHint, row.SMSSenderID) &&
			row.SMSConfigHash == row.SMSTestedConfigHash
	} else {
		row.SMSTestStatus = models.NotifTestStatusFailed
		row.SMSLastFailureAt = &now
		row.SMSLastError = errMsg
		row.SMSVerificationReady = false
	}

	return db.Save(&row).Error
}

// ── SMTP gate ────────────────────────────────────────────────────────────────

// ErrSMTPNotReady is returned by CheckSMTPGate when no verified SMTP configuration
// exists for the company. This sentinel allows callers to distinguish "gate rejected"
// from "send failed" in error handling and test assertions.
var ErrSMTPNotReady = errors.New("SMTP not configured or not verified for this company — configure and test SMTP in Notification Settings first")

// CheckSMTPGate is the named gate for invoice email delivery.
// It returns nil if the company has a verified SMTP configuration ready for use,
// or ErrSMTPNotReady if not.
//
// This is a pure readiness check — it does NOT send anything, does NOT log anything,
// and does NOT create any DB records. A failed gate must NEVER cause a delivery log
// to be created; that would misrepresent history.
//
// Internally delegates to EffectiveSMTPForCompany, which checks:
//   - Company override config (EmailVerificationReady = true, hash match)
//   - System fallback config (if AllowSystemFallback, EmailVerificationReady = true)
func CheckSMTPGate(db *gorm.DB, companyID uint) error {
	_, ready, err := EffectiveSMTPForCompany(db, companyID)
	if err != nil {
		return fmt.Errorf("SMTP config lookup failed: %w", err)
	}
	if !ready {
		return ErrSMTPNotReady
	}
	return nil
}

// ── Backend-authoritative readiness checks ────────────────────────────────────
//
// These functions are the single source of truth for callers (e.g. future
// security flows, invoice delivery) that need to know whether a channel is
// ready. They re-read from the DB so they are never relying on frontend state.

// IsCompanyEmailVerificationReady returns true only if the company SMTP config
// is fully verified and unchanged since the last successful test.
func IsCompanyEmailVerificationReady(db *gorm.DB, companyID uint) (bool, error) {
	row, err := loadCompanyNotificationSettingsRow(db, companyID)
	if err != nil || row.ID == 0 {
		return false, err
	}
	return row.EmailVerificationReady, nil
}

// IsCompanySMSVerificationReady returns true only if the company SMS config
// is fully verified and unchanged since the last successful test.
func IsCompanySMSVerificationReady(db *gorm.DB, companyID uint) (bool, error) {
	row, err := loadCompanyNotificationSettingsRow(db, companyID)
	if err != nil || row.ID == 0 {
		return false, err
	}
	return row.SMSVerificationReady, nil
}

// IsSystemEmailVerificationReady returns true if the system SMTP config is ready.
func IsSystemEmailVerificationReady(db *gorm.DB) (bool, error) {
	row, err := loadSystemNotificationSettingsRow(db)
	if err != nil || row.ID == 0 {
		return false, err
	}
	return row.EmailVerificationReady, nil
}

// IsSystemSMSVerificationReady returns true if the system SMS config is ready.
func IsSystemSMSVerificationReady(db *gorm.DB) (bool, error) {
	row, err := loadSystemNotificationSettingsRow(db)
	if err != nil || row.ID == 0 {
		return false, err
	}
	return row.SMSVerificationReady, nil
}
