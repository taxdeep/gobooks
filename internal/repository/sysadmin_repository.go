// 遵循project_guide.md
package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ─────────────────────────────────────────────────────────────────────────────
// SysadminUserRepository
// ─────────────────────────────────────────────────────────────────────────────

type SysadminUserRepository struct {
	db *gorm.DB
}

func NewSysadminUserRepository(db *gorm.DB) *SysadminUserRepository {
	return &SysadminUserRepository{db: db}
}

// Count 返回系统管理员总数（用于首次 bootstrap 判断）。
func (r *SysadminUserRepository) Count() (int64, error) {
	var n int64
	return n, r.db.Model(&models.SysadminUser{}).Count(&n).Error
}

// Create 创建新的系统管理员账户。
func (r *SysadminUserRepository) Create(u *models.SysadminUser) error {
	return r.db.Create(u).Error
}

// FindByEmail 按邮箱（不区分大小写）查找系统管理员。
func (r *SysadminUserRepository) FindByEmail(email string) (*models.SysadminUser, error) {
	var out models.SysadminUser
	err := r.db.Where("lower(email) = lower(?)", email).First(&out).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}

// FindByID 按主键查找系统管理员。
func (r *SysadminUserRepository) FindByID(id uint) (*models.SysadminUser, error) {
	var out models.SysadminUser
	err := r.db.First(&out, id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}

// ListAll 返回全部系统管理员，按 id 排序。
func (r *SysadminUserRepository) ListAll() ([]models.SysadminUser, error) {
	var out []models.SysadminUser
	return out, r.db.Order("id asc").Find(&out).Error
}

// Save 更新系统管理员记录（用于修改密码、停用等）。
func (r *SysadminUserRepository) Save(u *models.SysadminUser) error {
	return r.db.Save(u).Error
}

// ─────────────────────────────────────────────────────────────────────────────
// SysadminSessionRepository
// ─────────────────────────────────────────────────────────────────────────────

type SysadminSessionRepository struct {
	db *gorm.DB
}

func NewSysadminSessionRepository(db *gorm.DB) *SysadminSessionRepository {
	return &SysadminSessionRepository{db: db}
}

// Create 插入一条新的系统管理员会话。
func (r *SysadminSessionRepository) Create(s *models.SysadminSession) error {
	return r.db.Create(s).Error
}

// FindValid 查找有效会话（令牌匹配且未过期）。
func (r *SysadminSessionRepository) FindValid(tokenHash string) (*models.SysadminSession, error) {
	var out models.SysadminSession
	err := r.db.
		Where("token_hash = ? AND expires_at > ?", tokenHash, time.Now().UTC()).
		First(&out).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}

// Delete 删除会话（登出时调用）。SysadminSession 不需要 revoked_at 字段，直接删除即可。
func (r *SysadminSessionRepository) Delete(id uint) error {
	return r.db.Delete(&models.SysadminSession{}, id).Error
}

// DeleteByTokenHash 按令牌哈希删除会话（登出时从 cookie 直接删）。
func (r *SysadminSessionRepository) DeleteByTokenHash(tokenHash string) error {
	return r.db.Where("token_hash = ?", tokenHash).Delete(&models.SysadminSession{}).Error
}

// DeleteAllByUserID 撤销指定系统管理员的全部会话（停用账户时调用）。
func (r *SysadminSessionRepository) DeleteAllByUserID(userID uint) error {
	return r.db.Where("sysadmin_user_id = ?", userID).Delete(&models.SysadminSession{}).Error
}
