// 遵循project_guide.md
package models

import (
	"time"

	"github.com/google/uuid"
)

// Number format constants for UserPreference.NumberFormat.
const (
	NumberFormatCommaDot   = "comma_dot"   // 1,000.00 — North American (default)
	NumberFormatDotComma   = "dot_comma"   // 1.000,00 — Continental European
	NumberFormatSpaceComma = "space_comma" // 1 000,00 — French / Nordic
	NumberFormatSpaceDot   = "space_dot"   // 1 000.00 — Swiss / ISO
)

// NumberFormatDefault is used when no preference has been saved yet.
const NumberFormatDefault = NumberFormatCommaDot

// NumberFormatOptions lists all valid format values in display order.
var NumberFormatOptions = []struct {
	Value   string
	Label   string
	Example string
}{
	{NumberFormatCommaDot, "North American", "1,000.00"},
	{NumberFormatDotComma, "Continental European", "1.000,00"},
	{NumberFormatSpaceComma, "French / Nordic", "1\u00A0000,00"},
	{NumberFormatSpaceDot, "Swiss / ISO", "1\u00A0000.00"},
}

// UserPreference stores per-user display and UX preferences.
// Uses user_id as the primary key (one row per user).
type UserPreference struct {
	UserID       uuid.UUID `gorm:"type:uuid;primaryKey"`
	NumberFormat string    `gorm:"not null;default:'comma_dot'"`
	UpdatedAt    time.Time
}
