// 遵循project_guide.md
package admin

import (
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"golang.org/x/crypto/bcrypt"

	"github.com/google/uuid"
	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/admintmpl"
)

// handleAdminUsers 列出所有业务用户。
func (s *Server) handleAdminUsers(c *fiber.Ctx) error {
	var users []models.User
	if err := s.DB.Preload("Plan").Order("created_at asc").Find(&users).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}

	// Load active plans for the Change Plan dropdown.
	var activePlans []models.UserPlan
	s.DB.Where("is_active = true").Order("sort_order asc, id asc").Find(&activePlans)

	rows := make([]admintmpl.AdminUserRow, 0, len(users))
	for _, u := range users {
		var companyCnt int64
		s.DB.Model(&models.CompanyMembership{}).
			Where("user_id = ? AND is_active = true", u.ID).
			Count(&companyCnt)
		planName := u.Plan.Name
		if planName == "" {
			planName = "—"
		}
		rows = append(rows, admintmpl.AdminUserRow{
			User:         u,
			CompanyCount: int(companyCnt),
			PlanName:     planName,
		})
	}

	return admintmpl.AdminUsers(admintmpl.AdminUsersVM{
		AdminEmail:      AdminUserFromCtx(c).Email,
		MaintenanceMode: IsMaintenanceMode(),
		Users:           rows,
		ActivePlans:     activePlans,
		Flash:           c.Query("flash"),
	}).Render(c.Context(), c)
}

// handleAdminUserChangePlan updates the plan assigned to a business user.
func (s *Server) handleAdminUserChangePlan(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Redirect("/admin/users?flash=invalid_id", fiber.StatusSeeOther)
	}

	planIDStr := strings.TrimSpace(c.FormValue("plan_id"))
	planID64, err := strconv.ParseInt(planIDStr, 10, 64)
	if err != nil || planID64 <= 0 {
		return c.Redirect("/admin/users?flash=invalid_plan", fiber.StatusSeeOther)
	}
	planID := int(planID64)

	// Verify the plan exists and is active.
	var plan models.UserPlan
	if err := s.DB.Where("id = ? AND is_active = true", planID).First(&plan).Error; err != nil {
		return c.Redirect("/admin/users?flash=invalid_plan", fiber.StatusSeeOther)
	}

	if err := s.DB.Model(&models.User{}).Where("id = ?", id).Update("plan_id", planID).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}

	return c.Redirect("/admin/users?flash=plan_changed", fiber.StatusSeeOther)
}

// handleAdminUserDeactivate 停用指定业务用户（软删除）。
func (s *Server) handleAdminUserDeactivate(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Redirect("/admin/users?flash=invalid_id", fiber.StatusSeeOther)
	}

	// 加载用户邮箱，用于审计详情
	var user models.User
	_ = s.DB.Select("id, email").First(&user, id).Error

	if err := s.DB.Model(&models.User{}).Where("id = ?", id).
		Update("is_active", false).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}
	// 撤销该用户所有活跃会话
	s.DB.Model(&models.Session{}).
		Where("user_id = ? AND revoked_at IS NULL", id).
		Update("revoked_at", time.Now().UTC())

	// 审计：SysAdmin 停用业务用户（system 级别，不绑定特定公司）
	services.TryWriteAuditLog(s.DB, "admin.user.deactivated", "user", 0,
		AdminUserFromCtx(c).Email,
		map[string]any{
			"user_id":    id.String(),
			"user_email": user.Email,
			"actor_type": "sysadmin",
		},
	)

	return c.Redirect("/admin/users?flash=user_deactivated", fiber.StatusSeeOther)
}

// handleAdminUserReactivate 重新激活指定业务用户。
func (s *Server) handleAdminUserReactivate(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Redirect("/admin/users?flash=invalid_id", fiber.StatusSeeOther)
	}

	// 加载用户邮箱，用于审计详情
	var user models.User
	_ = s.DB.Select("id, email").First(&user, id).Error

	if err := s.DB.Model(&models.User{}).Where("id = ?", id).
		Update("is_active", true).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}

	// 审计：SysAdmin 重新激活业务用户
	services.TryWriteAuditLog(s.DB, "admin.user.reactivated", "user", 0,
		AdminUserFromCtx(c).Email,
		map[string]any{
			"user_id":    id.String(),
			"user_email": user.Email,
			"actor_type": "sysadmin",
		},
	)

	return c.Redirect("/admin/users?flash=user_reactivated", fiber.StatusSeeOther)
}

// handleAdminUserResetPassword 重置指定业务用户密码。
func (s *Server) handleAdminUserResetPassword(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Redirect("/admin/users?flash=invalid_id", fiber.StatusSeeOther)
	}
	newPassword := strings.TrimSpace(c.FormValue("new_password"))
	if len(newPassword) < 8 {
		return c.Redirect("/admin/users?flash=password_too_short", fiber.StatusSeeOther)
	}

	// 加载用户邮箱，用于审计详情
	var user models.User
	_ = s.DB.Select("id, email").First(&user, id).Error

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "could not hash password")
	}
	if err := s.DB.Model(&models.User{}).Where("id = ?", id).
		Update("password_hash", string(hash)).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}
	// 强制登出：撤销所有会话
	s.DB.Model(&models.Session{}).
		Where("user_id = ? AND revoked_at IS NULL", id).
		Update("revoked_at", time.Now().UTC())

	// 审计：SysAdmin 重置业务用户密码（安全敏感操作，单独记录）
	services.TryWriteAuditLog(s.DB, "admin.user.password_reset", "user", 0,
		AdminUserFromCtx(c).Email,
		map[string]any{
			"user_id":          id.String(),
			"user_email":       user.Email,
			"actor_type":       "sysadmin",
			"sessions_revoked": true,
		},
	)

	return c.Redirect("/admin/users?flash=password_reset", fiber.StatusSeeOther)
}
