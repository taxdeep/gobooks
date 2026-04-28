// 遵循project_guide.md
package services

import (
	"crypto/rand"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── errors ────────────────────────────────────────────────────────────────────

var (
	ErrChallengeNotFound  = errors.New("verification challenge not found")
	ErrChallengeExpired   = errors.New("verification code has expired")
	ErrChallengeUsed      = errors.New("verification code has already been used")
	ErrChallengeMaxTries  = errors.New("too many incorrect attempts; request a new code")
	ErrChallengeWrongCode = errors.New("incorrect verification code")
)

// ── code generation ───────────────────────────────────────────────────────────

const verifyCodeChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
const verifyCodeLen = 6

// randVerifyCode returns a cryptographically random 6-character uppercase
// alphanumeric code.
func randVerifyCode() (string, error) {
	b := make([]byte, verifyCodeLen)
	max := big.NewInt(int64(len(verifyCodeChars)))
	for i := range b {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = verifyCodeChars[n.Int64()]
	}
	return string(b), nil
}

// ── create challenges ─────────────────────────────────────────────────────────

// CreateEmailChangeChallenge creates a verification challenge for an email
// address change. The returned raw code must be sent to newEmail by the caller.
// Any previously unexpired challenge of the same type for this user is NOT
// cancelled — the service relies on attempt limits instead.
func CreateEmailChangeChallenge(db *gorm.DB, userID uuid.UUID, newEmail string) (rawCode string, challengeID uuid.UUID, err error) {
	rawCode, err = randVerifyCode()
	if err != nil {
		return "", uuid.Nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(rawCode), bcrypt.DefaultCost)
	if err != nil {
		return "", uuid.Nil, err
	}
	ch := models.UserVerificationChallenge{
		ID:          uuid.New(),
		UserID:      userID,
		Type:        models.VerifyChallengeTypeEmailChange,
		CodeHash:    string(hash),
		NewEmail:    strings.TrimSpace(newEmail),
		ExpiresAt:   time.Now().UTC().Add(15 * time.Minute),
		MaxAttempts: 5,
	}
	if err := db.Create(&ch).Error; err != nil {
		return "", uuid.Nil, err
	}
	return rawCode, ch.ID, nil
}

// CreatePasswordChangeChallenge creates a verification challenge for a
// password change. The returned raw code must be sent to the user's current
// email address by the caller.
func CreatePasswordChangeChallenge(db *gorm.DB, userID uuid.UUID) (rawCode string, challengeID uuid.UUID, err error) {
	rawCode, err = randVerifyCode()
	if err != nil {
		return "", uuid.Nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(rawCode), bcrypt.DefaultCost)
	if err != nil {
		return "", uuid.Nil, err
	}
	ch := models.UserVerificationChallenge{
		ID:          uuid.New(),
		UserID:      userID,
		Type:        models.VerifyChallengeTypePasswordChange,
		CodeHash:    string(hash),
		ExpiresAt:   time.Now().UTC().Add(15 * time.Minute),
		MaxAttempts: 5,
	}
	if err := db.Create(&ch).Error; err != nil {
		return "", uuid.Nil, err
	}
	return rawCode, ch.ID, nil
}

// ── verify challenge ──────────────────────────────────────────────────────────

// VerifyChallenge checks the submitted raw code against the stored challenge.
// ownerUserID must match the challenge's UserID; if it does not, the function
// returns ErrChallengeNotFound without incrementing the attempt counter (no
// information leakage, no denial-of-service against the real owner).
//
// On success the challenge is marked used and the updated record is returned.
//
// Errors: ErrChallengeNotFound, ErrChallengeExpired, ErrChallengeUsed,
// ErrChallengeMaxTries, ErrChallengeWrongCode.
func VerifyChallenge(db *gorm.DB, challengeID uuid.UUID, ownerUserID uuid.UUID, rawCode string) (*models.UserVerificationChallenge, error) {
	var ch models.UserVerificationChallenge
	if err := db.First(&ch, "id = ?", challengeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrChallengeNotFound
		}
		return nil, err
	}

	// Ownership check first — before any attempt is recorded.
	if ch.UserID != ownerUserID {
		return nil, ErrChallengeNotFound
	}

	if ch.UsedAt != nil {
		return nil, ErrChallengeUsed
	}
	if time.Now().UTC().After(ch.ExpiresAt) {
		return nil, ErrChallengeExpired
	}
	if ch.AttemptCount >= ch.MaxAttempts {
		return nil, ErrChallengeMaxTries
	}

	// Normalise to uppercase before compare (case-insensitive matching).
	normalised := strings.ToUpper(strings.TrimSpace(rawCode))
	err := bcrypt.CompareHashAndPassword([]byte(ch.CodeHash), []byte(normalised))

	// Increment attempt counter regardless of outcome.
	db.Model(&ch).UpdateColumn("attempt_count", gorm.Expr("attempt_count + 1"))
	ch.AttemptCount++

	if err != nil {
		if ch.AttemptCount >= ch.MaxAttempts {
			return nil, ErrChallengeMaxTries
		}
		return nil, ErrChallengeWrongCode
	}

	// Mark as used.
	now := time.Now().UTC()
	db.Model(&ch).UpdateColumn("used_at", now)
	ch.UsedAt = &now
	return &ch, nil
}
