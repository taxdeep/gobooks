// 遵循project_guide.md
package models

import (
	"time"

	"github.com/google/uuid"
)

// User is an authenticated account (email + password hash).
type User struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey"`
	Email        string    `gorm:"not null;uniqueIndex"`
	PasswordHash string    `gorm:"not null"`
	DisplayName  string    `gorm:"not null;default:''"`
	IsActive     bool      `gorm:"not null;default:true"`

	// Login lockout state for repeated incorrect password attempts.
	FailedLoginAttempts      int `gorm:"not null;default:0"`
	LoginLockedUntil         *time.Time
	LoginLockWindowStartedAt *time.Time
	LoginLockCount           int `gorm:"not null;default:0"`
	PermanentlyLockedAt      *time.Time
	LoginLockReason          string `gorm:"not null;default:''"`

	// PlanID references the UserPlan that governs this user's quotas.
	// Default 1 = Starter plan (seeded in migration).
	// SysAdmin users (SysadminUser) are never assigned a UserPlan.
	PlanID int      `gorm:"not null;default:1"`
	Plan   UserPlan `gorm:"foreignKey:PlanID"`

	CreatedAt time.Time
	UpdatedAt time.Time
}
