// 遵循project_guide.md
package services

import (
	"errors"
	"strings"

	"balanciz/internal/models"

	"gorm.io/gorm"
)

// ── Input types ───────────────────────────────────────────────────────────────

// CompanyNotificationSettingsInput carries all writable fields for a save operation.
// Secret fields (SMTPPassword, SMSAPIKey, SMSAPISecret) are plaintext; leave empty
// to keep the existing encrypted value unchanged.
type CompanyNotificationSettingsInput struct {
	EmailEnabled        bool
	SMTPHost            string
	SMTPPort            int
	SMTPUsername        string
	SMTPPassword        string // plaintext; empty = keep existing
	SMTPFromEmail       string
	SMTPFromName        string
	SMTPEncryption      models.SMTPEncryption
	SMSEnabled          bool
	SMSProvider         string
	SMSAPIKey           string // plaintext; empty = keep existing
	SMSAPISecret        string // plaintext; empty = keep existing
	SMSSenderID         string
	AllowSystemFallback bool
}

// SystemNotificationSettingsInput carries all writable fields for a save operation.
// Secret fields follow the same empty-means-keep convention.
type SystemNotificationSettingsInput struct {
	EmailEnabled         bool
	SMTPHost             string
	SMTPPort             int
	SMTPUsername         string
	SMTPPassword         string // plaintext; empty = keep existing
	SMTPFromEmail        string
	SMTPFromName         string
	SMTPEncryption       models.SMTPEncryption
	SMSEnabled           bool
	SMSProvider          string
	SMSAPIKey            string // plaintext; empty = keep existing
	SMSAPISecret         string // plaintext; empty = keep existing
	SMSSenderID          string
	AllowCompanyOverride bool
}

// ── Company notification settings ─────────────────────────────────────────────

// LoadCompanyNotificationSettings returns the settings row for the company with all
// secret fields decrypted. Callers that render UI should use the *MaskedHint fields
// on the model and never forward the decrypted values to the browser.
// Returns a zero-value row (ID == 0) with defaults if no row exists yet.
func LoadCompanyNotificationSettings(db *gorm.DB, companyID uint) (models.CompanyNotificationSettings, error) {
	row, err := loadCompanyNotificationSettingsRow(db, companyID)
	if err != nil || row.ID == 0 {
		return row, err
	}
	if row.SMTPPasswordEncrypted, err = decryptAISecret(row.SMTPPasswordEncrypted); err != nil {
		return models.CompanyNotificationSettings{}, err
	}
	if row.SMSAPIKeyEncrypted, err = decryptAISecret(row.SMSAPIKeyEncrypted); err != nil {
		return models.CompanyNotificationSettings{}, err
	}
	if row.SMSAPISecretEncrypted, err = decryptAISecret(row.SMSAPISecretEncrypted); err != nil {
		return models.CompanyNotificationSettings{}, err
	}
	return row, nil
}

func loadCompanyNotificationSettingsRow(db *gorm.DB, companyID uint) (models.CompanyNotificationSettings, error) {
	var row models.CompanyNotificationSettings
	err := db.Where("company_id = ?", companyID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.CompanyNotificationSettings{
			CompanyID:           companyID,
			SMTPPort:            587,
			SMTPEncryption:      models.SMTPEncryptionSTARTTLS,
			AllowSystemFallback: true,
		}, nil
	}
	return row, err
}

// UpsertCompanyNotificationSettings saves company notification configuration.
// Only non-empty secret values are re-encrypted; empty values leave the existing
// ciphertext and masked hint unchanged.
func UpsertCompanyNotificationSettings(db *gorm.DB, companyID uint, in CompanyNotificationSettingsInput) error {
	row, err := loadCompanyNotificationSettingsRow(db, companyID)
	if err != nil {
		return err
	}

	row.CompanyID = companyID
	row.EmailEnabled = in.EmailEnabled
	row.SMTPHost = strings.TrimSpace(in.SMTPHost)
	row.SMTPPort = in.SMTPPort
	row.SMTPUsername = strings.TrimSpace(in.SMTPUsername)
	row.SMTPFromEmail = strings.TrimSpace(in.SMTPFromEmail)
	row.SMTPFromName = strings.TrimSpace(in.SMTPFromName)
	row.SMTPEncryption = in.SMTPEncryption
	row.SMSEnabled = in.SMSEnabled
	row.SMSProvider = strings.TrimSpace(in.SMSProvider)
	row.SMSSenderID = strings.TrimSpace(in.SMSSenderID)
	row.AllowSystemFallback = in.AllowSystemFallback

	if p := strings.TrimSpace(in.SMTPPassword); p != "" {
		enc, err := encryptAISecret(p)
		if err != nil {
			return err
		}
		row.SMTPPasswordEncrypted = enc
		row.SMTPPasswordMaskedHint = MaskAPIKey(p)
	}
	if k := strings.TrimSpace(in.SMSAPIKey); k != "" {
		enc, err := encryptAISecret(k)
		if err != nil {
			return err
		}
		row.SMSAPIKeyEncrypted = enc
		row.SMSAPIKeyMaskedHint = MaskAPIKey(k)
	}
	if s := strings.TrimSpace(in.SMSAPISecret); s != "" {
		enc, err := encryptAISecret(s)
		if err != nil {
			return err
		}
		row.SMSAPISecretEncrypted = enc
		row.SMSAPISecretMaskedHint = MaskAPIKey(s)
	}

	// Recompute config hashes so that any field change immediately invalidates
	// the prior test success on the backend — no frontend action required.
	applyCompanyEmailConfigHash(&row)
	applyCompanySMSConfigHash(&row)

	if row.ID == 0 {
		return db.Create(&row).Error
	}
	return db.Save(&row).Error
}

// ── System notification settings ──────────────────────────────────────────────

// LoadSystemNotificationSettings returns the singleton system settings row with
// all secret fields decrypted. Returns a zero-value row (ID == 0) if not yet saved.
func LoadSystemNotificationSettings(db *gorm.DB) (models.SystemNotificationSettings, error) {
	row, err := loadSystemNotificationSettingsRow(db)
	if err != nil || row.ID == 0 {
		return row, err
	}
	if row.SMTPPasswordEncrypted, err = decryptAISecret(row.SMTPPasswordEncrypted); err != nil {
		return models.SystemNotificationSettings{}, err
	}
	if row.SMSAPIKeyEncrypted, err = decryptAISecret(row.SMSAPIKeyEncrypted); err != nil {
		return models.SystemNotificationSettings{}, err
	}
	if row.SMSAPISecretEncrypted, err = decryptAISecret(row.SMSAPISecretEncrypted); err != nil {
		return models.SystemNotificationSettings{}, err
	}
	return row, nil
}

func loadSystemNotificationSettingsRow(db *gorm.DB) (models.SystemNotificationSettings, error) {
	var row models.SystemNotificationSettings
	err := db.First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.SystemNotificationSettings{
			SMTPPort:             587,
			SMTPEncryption:       models.SMTPEncryptionSTARTTLS,
			AllowCompanyOverride: true,
		}, nil
	}
	return row, err
}

// UpsertSystemNotificationSettings saves the singleton system notification settings.
// Only non-empty secret values are re-encrypted.
func UpsertSystemNotificationSettings(db *gorm.DB, in SystemNotificationSettingsInput) error {
	row, err := loadSystemNotificationSettingsRow(db)
	if err != nil {
		return err
	}

	row.EmailEnabled = in.EmailEnabled
	row.SMTPHost = strings.TrimSpace(in.SMTPHost)
	row.SMTPPort = in.SMTPPort
	row.SMTPUsername = strings.TrimSpace(in.SMTPUsername)
	row.SMTPFromEmail = strings.TrimSpace(in.SMTPFromEmail)
	row.SMTPFromName = strings.TrimSpace(in.SMTPFromName)
	row.SMTPEncryption = in.SMTPEncryption
	row.SMSEnabled = in.SMSEnabled
	row.SMSProvider = strings.TrimSpace(in.SMSProvider)
	row.SMSSenderID = strings.TrimSpace(in.SMSSenderID)
	row.AllowCompanyOverride = in.AllowCompanyOverride

	if p := strings.TrimSpace(in.SMTPPassword); p != "" {
		enc, err := encryptAISecret(p)
		if err != nil {
			return err
		}
		row.SMTPPasswordEncrypted = enc
		row.SMTPPasswordMaskedHint = MaskAPIKey(p)
	}
	if k := strings.TrimSpace(in.SMSAPIKey); k != "" {
		enc, err := encryptAISecret(k)
		if err != nil {
			return err
		}
		row.SMSAPIKeyEncrypted = enc
		row.SMSAPIKeyMaskedHint = MaskAPIKey(k)
	}
	if s := strings.TrimSpace(in.SMSAPISecret); s != "" {
		enc, err := encryptAISecret(s)
		if err != nil {
			return err
		}
		row.SMSAPISecretEncrypted = enc
		row.SMSAPISecretMaskedHint = MaskAPIKey(s)
	}

	applySystemEmailConfigHash(&row)
	applySystemSMSConfigHash(&row)

	if row.ID == 0 {
		return db.Create(&row).Error
	}
	return db.Save(&row).Error
}
