// 遵循project_guide.md
package services

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"gobooks/internal/models"
)

// GetUserNumberFormat loads the user's saved number format preference.
// Returns NumberFormatDefault when no preference exists (no DB write).
func GetUserNumberFormat(db *gorm.DB, userID uuid.UUID) string {
	var pref models.UserPreference
	err := db.Where("user_id = ?", userID).First(&pref).Error
	if err != nil {
		return models.NumberFormatDefault
	}
	return pref.NumberFormat
}

// SaveUserNumberFormat upserts the user's number format preference.
// Creates the row on first save; updates on subsequent saves.
func SaveUserNumberFormat(db *gorm.DB, userID uuid.UUID, numberFormat string) error {
	if !isValidNumberFormat(numberFormat) {
		return fmt.Errorf("invalid number format: %q", numberFormat)
	}

	var pref models.UserPreference
	err := db.Where("user_id = ?", userID).First(&pref).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		pref = models.UserPreference{
			UserID:       userID,
			NumberFormat: numberFormat,
		}
		return db.Create(&pref).Error
	}
	if err != nil {
		return fmt.Errorf("load user preference: %w", err)
	}
	return db.Model(&pref).Update("number_format", numberFormat).Error
}

func isValidNumberFormat(f string) bool {
	for _, opt := range models.NumberFormatOptions {
		if opt.Value == f {
			return true
		}
	}
	return false
}
