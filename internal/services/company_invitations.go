// 遵循project_guide.md
package services

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"balanciz/internal/models"

	"gorm.io/gorm"
)

const companyInvitationTTL = 7 * 24 * time.Hour

// ErrInvitationDuplicate is returned when a pending invite already exists for the email.
var ErrInvitationDuplicate = errors.New("a pending invitation already exists for this email")

// ErrInvitationAlreadyMember is returned when the user is already a member of the company.
var ErrInvitationAlreadyMember = errors.New("this user is already a member of the company")

// ErrInvitationInvalidRole is returned when the role cannot be assigned via invitation (e.g. owner).
var ErrInvitationInvalidRole = errors.New("this role cannot be assigned via invitation")

// CreateCompanyInvitation stores a pending invitation with a hashed opaque token (acceptance URL deferred).
func CreateCompanyInvitation(db *gorm.DB, companyID uint, invitedByUserID uuid.UUID, email string, role models.CompanyRole) (*models.CompanyInvitation, string, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" || !strings.Contains(email, "@") {
		return nil, "", fmt.Errorf("valid email is required")
	}
	if role == models.CompanyRoleOwner {
		return nil, "", ErrInvitationInvalidRole
	}
	if !role.Valid() {
		return nil, "", fmt.Errorf("invalid role")
	}

	var existingUser models.User
	err := db.Where("lower(email) = ?", email).First(&existingUser).Error
	if err == nil {
		var n int64
		if err := db.Model(&models.CompanyMembership{}).
			Where("user_id = ? AND company_id = ? AND is_active = ?", existingUser.ID, companyID, true).
			Count(&n).Error; err != nil {
			return nil, "", err
		}
		if n > 0 {
			return nil, "", ErrInvitationAlreadyMember
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, "", err
	}

	var dup int64
	if err := db.Model(&models.CompanyInvitation{}).
		Where("company_id = ? AND lower(email) = ? AND status = ?", companyID, email, models.InvitationStatusPending).
		Count(&dup).Error; err != nil {
		return nil, "", err
	}
	if dup > 0 {
		return nil, "", ErrInvitationDuplicate
	}

	rawToken, tokenHash, err := newOpaqueTokenSHA256()
	if err != nil {
		return nil, "", err
	}

	inv := models.CompanyInvitation{
		CompanyID:       companyID,
		Email:           email,
		Role:            role,
		TokenHash:       tokenHash,
		InvitedByUserID: invitedByUserID,
		Status:          models.InvitationStatusPending,
		ExpiresAt:       time.Now().UTC().Add(companyInvitationTTL),
	}
	if err := db.Create(&inv).Error; err != nil {
		return nil, "", err
	}
	return &inv, rawToken, nil
}

// ListPendingInvitationsForCompany returns pending invitations (including expired pending rows).
func ListPendingInvitationsForCompany(db *gorm.DB, companyID uint) ([]models.CompanyInvitation, error) {
	var rows []models.CompanyInvitation
	err := db.Preload("InvitedBy").
		Where("company_id = ? AND status = ?", companyID, models.InvitationStatusPending).
		Order("created_at DESC").
		Find(&rows).Error
	return rows, err
}

func newOpaqueTokenSHA256() (rawHex string, tokenHash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(raw), hex.EncodeToString(sum[:]), nil
}
