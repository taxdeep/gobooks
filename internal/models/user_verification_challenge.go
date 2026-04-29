// 遵循project_guide.md
package models

import (
	"time"

	"github.com/google/uuid"
)

// VerifyChallengeType identifies which user action a challenge guards.
type VerifyChallengeType string

const (
	VerifyChallengeTypeEmailChange    VerifyChallengeType = "email_change"
	VerifyChallengeTypePasswordChange VerifyChallengeType = "password_change"
	VerifyChallengeTypePasswordReset  VerifyChallengeType = "password_reset"
)

// UserVerificationChallenge is a single-use, time-limited challenge that
// guards sensitive user profile changes (email or password). A 6-character
// alphanumeric code is sent to the user via email; the hashed form is stored
// here. The raw code is never persisted.
type UserVerificationChallenge struct {
	ID       uuid.UUID           `gorm:"type:uuid;primaryKey"`
	UserID   uuid.UUID           `gorm:"type:uuid;not null;index"`
	Type     VerifyChallengeType `gorm:"type:text;not null"`
	CodeHash string              `gorm:"type:text;not null"` // bcrypt of uppercase 6-char code

	// NewEmail is only populated for email_change challenges; it is the address
	// the account will be changed to if the challenge is verified successfully.
	NewEmail string `gorm:"type:text;not null;default:''"`

	ExpiresAt    time.Time  `gorm:"not null;index"`
	AttemptCount int        `gorm:"not null;default:0"`
	MaxAttempts  int        `gorm:"not null;default:5"`
	UsedAt       *time.Time // nil = not yet consumed

	CreatedAt time.Time
}
