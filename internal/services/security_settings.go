// 遵循project_guide.md
package services

import (
	"errors"
	"time"

	"balanciz/internal/models"

	"gorm.io/gorm"
)

// ── Input types ───────────────────────────────────────────────────────────────

// CompanySecuritySettingsInput carries all writable fields for a save operation.
type CompanySecuritySettingsInput struct {
	UnusualIPLoginAlertEnabled bool
	UnusualIPLoginAlertChannel models.AlertChannel
	NewDeviceLoginAlertEnabled bool
	PasswordResetAlertEnabled  bool
	FailedLoginAlertEnabled    bool
	FutureRulesJSON            *string // raw JSONB; nil clears the field
}

// SystemSecuritySettingsInput carries all writable fields for a save operation.
type SystemSecuritySettingsInput struct {
	UnusualIPLoginAlertDefaultEnabled    bool
	UnusualIPLoginCompanyOverrideAllowed bool
	NewDeviceLoginAlertDefaultEnabled    bool
	PasswordResetAlertDefaultEnabled     bool
	FailedLoginAlertDefaultEnabled       bool
	GlobalSecurityRulesJSON              *string // raw JSONB; nil clears the field
}

// ── Company security settings ─────────────────────────────────────────────────

// LoadCompanySecuritySettings returns the settings row for the company.
// Returns a zero-value row (ID == 0) with safe defaults if no row exists yet.
func LoadCompanySecuritySettings(db *gorm.DB, companyID uint) (models.CompanySecuritySettings, error) {
	var row models.CompanySecuritySettings
	err := db.Where("company_id = ?", companyID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.CompanySecuritySettings{
			CompanyID:                  companyID,
			UnusualIPLoginAlertEnabled: true,
			UnusualIPLoginAlertChannel: models.AlertChannelEmail,
			NewDeviceLoginAlertEnabled: true,
			PasswordResetAlertEnabled:  true,
			FailedLoginAlertEnabled:    true,
		}, nil
	}
	return row, err
}

// UpsertCompanySecuritySettings saves company security alert preferences.
func UpsertCompanySecuritySettings(db *gorm.DB, companyID uint, in CompanySecuritySettingsInput) error {
	row, err := LoadCompanySecuritySettings(db, companyID)
	if err != nil {
		return err
	}

	row.CompanyID = companyID
	row.UnusualIPLoginAlertEnabled = in.UnusualIPLoginAlertEnabled
	row.UnusualIPLoginAlertChannel = in.UnusualIPLoginAlertChannel
	row.NewDeviceLoginAlertEnabled = in.NewDeviceLoginAlertEnabled
	row.PasswordResetAlertEnabled = in.PasswordResetAlertEnabled
	row.FailedLoginAlertEnabled = in.FailedLoginAlertEnabled
	row.FutureRulesJSON = in.FutureRulesJSON

	if row.ID == 0 {
		return db.Create(&row).Error
	}
	return db.Save(&row).Error
}

// ── System security settings ──────────────────────────────────────────────────

// LoadSystemSecuritySettings returns the singleton system security settings row.
// Returns a zero-value row (ID == 0) with safe defaults if not yet saved.
func LoadSystemSecuritySettings(db *gorm.DB) (models.SystemSecuritySettings, error) {
	var row models.SystemSecuritySettings
	err := db.First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.SystemSecuritySettings{
			UnusualIPLoginAlertDefaultEnabled:    true,
			UnusualIPLoginCompanyOverrideAllowed: true,
			NewDeviceLoginAlertDefaultEnabled:    true,
			PasswordResetAlertDefaultEnabled:     true,
			FailedLoginAlertDefaultEnabled:       true,
		}, nil
	}
	return row, err
}

// UpsertSystemSecuritySettings saves the singleton system security settings.
func UpsertSystemSecuritySettings(db *gorm.DB, in SystemSecuritySettingsInput) error {
	row, err := LoadSystemSecuritySettings(db)
	if err != nil {
		return err
	}

	row.UnusualIPLoginAlertDefaultEnabled = in.UnusualIPLoginAlertDefaultEnabled
	row.UnusualIPLoginCompanyOverrideAllowed = in.UnusualIPLoginCompanyOverrideAllowed
	row.NewDeviceLoginAlertDefaultEnabled = in.NewDeviceLoginAlertDefaultEnabled
	row.PasswordResetAlertDefaultEnabled = in.PasswordResetAlertDefaultEnabled
	row.FailedLoginAlertDefaultEnabled = in.FailedLoginAlertDefaultEnabled
	row.GlobalSecurityRulesJSON = in.GlobalSecurityRulesJSON

	if row.ID == 0 {
		return db.Create(&row).Error
	}
	return db.Save(&row).Error
}

// ── Security events ───────────────────────────────────────────────────────────

// LogSecurityEvent appends an immutable security event record.
// companyID and userID are optional (pass nil for system-level events).
func LogSecurityEvent(db *gorm.DB, companyID *uint, userID *string, eventType, ipAddress, userAgent string, metadataJSON *string) error {
	ev := models.SecurityEvent{
		CompanyID:    companyID,
		UserID:       userID,
		EventType:    eventType,
		IPAddress:    ipAddress,
		UserAgent:    userAgent,
		MetadataJSON: metadataJSON,
		CreatedAt:    time.Now().UTC(),
	}
	return db.Create(&ev).Error
}
