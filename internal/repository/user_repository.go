// 遵循project_guide.md
package repository

import (
	"errors"

	"github.com/google/uuid"
	"balanciz/internal/models"
	"gorm.io/gorm"
)

// UserRepository persists users.
type UserRepository struct {
	db *gorm.DB
}

func NewUserRepository(db *gorm.DB) *UserRepository {
	return &UserRepository{db: db}
}

// CreateUser inserts a new user. If u.ID is zero, a new UUID is assigned.
func (r *UserRepository) CreateUser(u *models.User) error {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	return r.db.Create(u).Error
}

// FindUserByEmail returns the user with matching email (case-insensitive).
func (r *UserRepository) FindUserByEmail(email string) (*models.User, error) {
	var out models.User
	err := r.db.Where("lower(email) = lower(?)", email).First(&out).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}

// FindUserByID returns the user by primary key, or nil if not found.
func (r *UserRepository) FindUserByID(id uuid.UUID) (*models.User, error) {
	var out models.User
	err := r.db.First(&out, "id = ?", id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}
