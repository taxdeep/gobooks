// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"balanciz/internal/models"

	"gorm.io/gorm"
)

// MaskAPIKey returns a display-safe hint (never the full secret).
func MaskAPIKey(secret string) string {
	if secret == "" {
		return ""
	}
	n := len(secret)
	if n <= 4 {
		return "••••••••"
	}
	return "••••••••" + secret[n-4:]
}

// ParseAIProvider normalizes form input to a known provider id.
func ParseAIProvider(s string) (string, error) {
	switch strings.TrimSpace(s) {
	case "", models.AIProviderOpenAICompatible:
		return models.AIProviderOpenAICompatible, nil
	case models.AIProviderCustomEndpoint:
		return models.AIProviderCustomEndpoint, nil
	default:
		return "", fmt.Errorf("invalid provider")
	}
}

// LoadAIConnectionSettings returns the row for the company or a zero row with defaults (not persisted).
func LoadAIConnectionSettings(db *gorm.DB, companyID uint) (models.AIConnectionSettings, error) {
	row, err := loadAIConnectionSettingsRow(db, companyID)
	if err != nil {
		return models.AIConnectionSettings{}, err
	}
	if row.ID == 0 {
		return row, nil
	}
	row.APIKey, err = decryptAISecret(row.APIKey)
	if err != nil {
		return models.AIConnectionSettings{}, err
	}
	return row, nil
}

func loadAIConnectionSettingsRow(db *gorm.DB, companyID uint) (models.AIConnectionSettings, error) {
	var row models.AIConnectionSettings
	err := db.Where("company_id = ?", companyID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.AIConnectionSettings{
			CompanyID: companyID,
			Provider:  models.AIProviderOpenAICompatible,
		}, nil
	}
	if err != nil {
		return models.AIConnectionSettings{}, err
	}
	return row, nil
}

// UpsertAIConnectionSettings saves non-secret fields and updates API key only when newKey is non-empty.
func UpsertAIConnectionSettings(db *gorm.DB, companyID uint, provider, apiBaseURL, newKey, modelName string, enabled, vision bool) error {
	row, err := loadAIConnectionSettingsRow(db, companyID)
	if err != nil {
		return err
	}
	row.CompanyID = companyID
	row.Provider = provider
	row.APIBaseURL = strings.TrimSpace(apiBaseURL)
	row.ModelName = strings.TrimSpace(modelName)
	row.Enabled = enabled
	row.VisionEnabled = vision
	if strings.TrimSpace(newKey) != "" {
		encrypted, err := encryptAISecret(newKey)
		if err != nil {
			return err
		}
		row.APIKey = encrypted
	}
	if row.ID == 0 {
		return db.Create(&row).Error
	}
	return db.Save(&row).Error
}

// RunAIConnectionTest validates saved settings and performs a lightweight HTTP reachability check.
// It does not send your API key to the browser. Full provider auth flows can be added later.
// If skipped is true, nothing was persisted (e.g. settings row does not exist yet).
func RunAIConnectionTest(db *gorm.DB, companyID uint) (ok bool, message string, skipped bool, err error) {
	row, err := LoadAIConnectionSettings(db, companyID)
	if err != nil {
		return false, "", false, err
	}
	if row.ID == 0 {
		return false, "Save your settings at least once before testing the connection.", true, nil
	}

	now := time.Now()
	msg := ""
	success := false

	switch {
	case strings.TrimSpace(row.APIBaseURL) == "":
		msg = "Set an API Base URL and save before testing."
	case row.APIKey == "":
		msg = "Save an API key before testing the connection."
	default:
		u, perr := url.Parse(strings.TrimSpace(row.APIBaseURL))
		if perr != nil || u.Scheme == "" || u.Host == "" {
			msg = "API Base URL must be a valid http(s) URL."
		} else {
			client := &http.Client{Timeout: 8 * time.Second}
			req, rerr := http.NewRequest(http.MethodGet, u.String(), nil)
			if rerr != nil {
				msg = "Could not build a request to the API Base URL."
			} else {
				resp, derr := client.Do(req)
				if derr != nil {
					msg = "Could not reach the API Base URL: " + derr.Error()
				} else {
					_ = resp.Body.Close()
					success = true
					msg = fmt.Sprintf("Reachability check OK (HTTP %d). Provider authentication is not executed yet; your key stays on the server.", resp.StatusCode)
				}
			}
		}
	}

	if err := db.Model(&models.AIConnectionSettings{}).
		Where("id = ?", row.ID).
		Updates(map[string]any{
			"last_test_at":      &now,
			"last_test_ok":      success,
			"last_test_message": msg,
		}).Error; err != nil {
		return false, "", false, err
	}
	return success, msg, false, nil
}
