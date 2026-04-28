// 遵循project_guide.md
package services

import (
	"errors"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ListUserReportFavourites returns the set of report keys this
// (user, company) has starred. Used by the Reports hub renderer to
// decide which star icons to show as filled and what to surface in
// the Favourites section at the top.
//
// Returns an empty map (not nil) on the no-favourites case so the
// caller can use plain `m[key]` lookups without nil-checking.
func ListUserReportFavourites(db *gorm.DB, userID uuid.UUID, companyID uint) (map[string]bool, error) {
	if userID == uuid.Nil || companyID == 0 {
		return map[string]bool{}, nil
	}
	var rows []models.ReportFavourite
	if err := db.
		Select("report_key").
		Where("user_id = ? AND company_id = ?", userID, companyID).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		out[r.ReportKey] = true
	}
	return out, nil
}

// ToggleReportFavourite adds or removes the (user, company, report) row
// based on its current state. Validates reportKey against the registry
// so a typo can't pollute the table — caller gets ErrUnknownReportKey
// to surface a clean error message.
//
// Returns the new starred state (true = now starred, false = now
// un-starred) so the handler can echo back to the UI without a second
// round-trip.
func ToggleReportFavourite(db *gorm.DB, userID uuid.UUID, companyID uint, reportKey string) (bool, error) {
	if userID == uuid.Nil || companyID == 0 {
		return false, errors.New("report favourites: user + company required")
	}
	if ReportByKey(reportKey) == nil {
		return false, ErrUnknownReportKey
	}

	var existing models.ReportFavourite
	err := db.
		Where("user_id = ? AND company_id = ? AND report_key = ?", userID, companyID, reportKey).
		First(&existing).Error

	switch {
	case err == nil:
		// Already starred → un-star.
		if delErr := db.Delete(&existing).Error; delErr != nil {
			return true, delErr
		}
		return false, nil

	case errors.Is(err, gorm.ErrRecordNotFound):
		// Not starred → star.
		row := models.ReportFavourite{
			UserID:    userID,
			CompanyID: companyID,
			ReportKey: reportKey,
		}
		if createErr := db.Create(&row).Error; createErr != nil {
			return false, createErr
		}
		return true, nil

	default:
		return false, err
	}
}

// ErrUnknownReportKey is returned by ToggleReportFavourite when the
// supplied key isn't in services.AllReports(). Sentinel so handlers
// can render a 400 instead of leaking the raw GORM error.
var ErrUnknownReportKey = errors.New("report favourites: unknown report_key")
