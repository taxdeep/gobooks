// 遵循project_guide.md
package repository

import (
	"errors"

	"github.com/google/uuid"
	"balanciz/internal/models"
	"gorm.io/gorm"
)

// MembershipRepository persists company memberships.
type MembershipRepository struct {
	db *gorm.DB
}

func NewMembershipRepository(db *gorm.DB) *MembershipRepository {
	return &MembershipRepository{db: db}
}

// ListMembershipsByUser returns all memberships for the user, ordered by company id.
func (r *MembershipRepository) ListMembershipsByUser(userID uuid.UUID) ([]models.CompanyMembership, error) {
	var rows []models.CompanyMembership
	err := r.db.Where("user_id = ?", userID).Order("company_id ASC").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// FindMembershipByUserAndCompany returns the membership for the pair, or nil if none.
func (r *MembershipRepository) FindMembershipByUserAndCompany(userID uuid.UUID, companyID uint) (*models.CompanyMembership, error) {
	var out models.CompanyMembership
	err := r.db.Where("user_id = ? AND company_id = ?", userID, companyID).First(&out).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}
