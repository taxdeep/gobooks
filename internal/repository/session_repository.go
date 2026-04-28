// 遵循project_guide.md
package repository

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"balanciz/internal/models"
	"gorm.io/gorm"
)

// SessionRepository persists sessions.
type SessionRepository struct {
	db *gorm.DB
}

func NewSessionRepository(db *gorm.DB) *SessionRepository {
	return &SessionRepository{db: db}
}

// CreateSession inserts a session. If s.ID is zero, a new UUID is assigned.
func (r *SessionRepository) CreateSession(s *models.Session) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	return r.db.Create(s).Error
}

// FindValidSessionByTokenHash returns a non-revoked session with matching hash and future expiry.
func (r *SessionRepository) FindValidSessionByTokenHash(tokenHash string) (*models.Session, error) {
	var out models.Session
	now := time.Now().UTC()
	err := r.db.Where(
		"token_hash = ? AND expires_at > ? AND revoked_at IS NULL",
		tokenHash,
		now,
	).First(&out).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}

// RevokeSession sets revoked_at for the session id. Rows already revoked are left unchanged.
func (r *SessionRepository) RevokeSession(sessionID uuid.UUID) error {
	res := r.db.Model(&models.Session{}).
		Where("id = ? AND revoked_at IS NULL", sessionID).
		Update("revoked_at", time.Now().UTC())
	if res.Error != nil {
		return res.Error
	}
	return nil
}
