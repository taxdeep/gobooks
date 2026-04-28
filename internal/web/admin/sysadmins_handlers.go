// 遵循project_guide.md
package admin

import (
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"golang.org/x/crypto/bcrypt"

	"balanciz/internal/models"
	"balanciz/internal/repository"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/admintmpl"
)

// ── SysAdmin 账户管理（/admin/sysadmins）────────────────────────────────────────

// handleAdminSysadmins 展示系统管理员列表及创建表单。
func (s *Server) handleAdminSysadmins(c *fiber.Ctx) error {
	items, err := s.buildSysadminRows(c)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}
	return admintmpl.AdminSysadmins(admintmpl.AdminSysadminsVM{
		AdminEmail:      AdminUserFromCtx(c).Email,
		MaintenanceMode: IsMaintenanceMode(),
		Items:           items,
		Flash:           c.Query("flash"),
	}).Render(c.Context(), c)
}

// handleAdminSysadminCreate 创建新的系统管理员账户。
func (s *Server) handleAdminSysadminCreate(c *fiber.Ctx) error {
	email := strings.ToLower(strings.TrimSpace(c.FormValue("email")))
	password := c.FormValue("password")
	confirm := c.FormValue("confirm_password")

	vm := admintmpl.AdminSysadminsVM{
		AdminEmail:      AdminUserFromCtx(c).Email,
		MaintenanceMode: IsMaintenanceMode(),
		FormEmail:       email,
		Flash:           "",
	}

	// 输入验证
	if email == "" || !strings.Contains(email, "@") {
		vm.EmailError = "A valid email address is required."
	}
	if len(password) < 8 {
		vm.PasswordError = "Password must be at least 8 characters."
	}
	if password != confirm {
		vm.ConfirmError = "Passwords do not match."
	}

	userRepo := repository.NewSysadminUserRepository(s.DB)

	// 邮箱唯一性检查
	if vm.EmailError == "" {
		existing, _ := userRepo.FindByEmail(email)
		if existing != nil {
			vm.EmailError = "An account with this email already exists."
		}
	}

	if vm.EmailError != "" || vm.PasswordError != "" || vm.ConfirmError != "" {
		items, _ := s.buildSysadminRows(c)
		vm.Items = items
		return admintmpl.AdminSysadmins(vm).Render(c.Context(), c)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "could not hash password")
	}
	newUser := &models.SysadminUser{
		Email:        email,
		PasswordHash: string(hash),
		IsActive:     true,
	}
	if err := userRepo.Create(newUser); err != nil {
		items, _ := s.buildSysadminRows(c)
		vm.Items = items
		vm.FormError = "Could not create account. Please try again."
		return admintmpl.AdminSysadmins(vm).Render(c.Context(), c)
	}

	// 审计：新系统管理员账号由现有 SysAdmin 创建（非 bootstrap 路径）
	services.TryWriteAuditLog(s.DB, "admin.sysadmin.created", "sysadmin_user", newUser.ID,
		AdminUserFromCtx(c).Email,
		map[string]any{
			"email":      email,
			"via":        "admin_panel",
			"actor_type": "sysadmin",
		},
	)

	return c.Redirect("/admin/sysadmins?flash=created", fiber.StatusSeeOther)
}

// handleAdminSysadminDeactivate 停用指定系统管理员账户。
// 安全守卫：不能停用自己；不能停用最后一个活跃管理员。
func (s *Server) handleAdminSysadminDeactivate(c *fiber.Ctx) error {
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil || id == 0 {
		return c.Redirect("/admin/sysadmins?flash=invalid_id", fiber.StatusSeeOther)
	}

	self := AdminUserFromCtx(c)
	if uint(id) == self.ID {
		return c.Redirect("/admin/sysadmins?flash=cannot_self_deactivate", fiber.StatusSeeOther)
	}

	var activeCount int64
	s.DB.Model(&models.SysadminUser{}).Where("is_active = true").Count(&activeCount)
	if activeCount <= 1 {
		return c.Redirect("/admin/sysadmins?flash=last_admin", fiber.StatusSeeOther)
	}

	userRepo := repository.NewSysadminUserRepository(s.DB)
	target, err := userRepo.FindByID(uint(id))
	if err != nil || target == nil {
		return c.Redirect("/admin/sysadmins?flash=invalid_id", fiber.StatusSeeOther)
	}

	target.IsActive = false
	if err := userRepo.Save(target); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}
	// 撤销该账户所有活跃会话
	_ = repository.NewSysadminSessionRepository(s.DB).DeleteAllByUserID(target.ID)

	services.TryWriteAuditLog(s.DB, "admin.sysadmin.deactivated", "sysadmin_user", target.ID,
		self.Email,
		map[string]any{
			"target_email": target.Email,
			"actor_type":   "sysadmin",
		},
	)

	return c.Redirect("/admin/sysadmins?flash=sysadmin_deactivated", fiber.StatusSeeOther)
}

// handleAdminSysadminReactivate 重新激活指定系统管理员账户。
func (s *Server) handleAdminSysadminReactivate(c *fiber.Ctx) error {
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil || id == 0 {
		return c.Redirect("/admin/sysadmins?flash=invalid_id", fiber.StatusSeeOther)
	}

	userRepo := repository.NewSysadminUserRepository(s.DB)
	target, err := userRepo.FindByID(uint(id))
	if err != nil || target == nil {
		return c.Redirect("/admin/sysadmins?flash=invalid_id", fiber.StatusSeeOther)
	}

	target.IsActive = true
	if err := userRepo.Save(target); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}

	services.TryWriteAuditLog(s.DB, "admin.sysadmin.reactivated", "sysadmin_user", target.ID,
		AdminUserFromCtx(c).Email,
		map[string]any{
			"target_email": target.Email,
			"actor_type":   "sysadmin",
		},
	)

	return c.Redirect("/admin/sysadmins?flash=sysadmin_reactivated", fiber.StatusSeeOther)
}

// handleAdminSysadminResetPassword 重置另一位系统管理员的密码（仅其他管理员可操作）。
func (s *Server) handleAdminSysadminResetPassword(c *fiber.Ctx) error {
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil || id == 0 {
		return c.Redirect("/admin/sysadmins?flash=invalid_id", fiber.StatusSeeOther)
	}

	self := AdminUserFromCtx(c)
	if uint(id) == self.ID {
		// 不允许通过此接口重置自己的密码，请使用 /admin/account/change-password
		return c.Redirect("/admin/sysadmins?flash=use_account_page", fiber.StatusSeeOther)
	}

	newPassword := strings.TrimSpace(c.FormValue("new_password"))
	if len(newPassword) < 8 {
		return c.Redirect("/admin/sysadmins?flash=password_too_short", fiber.StatusSeeOther)
	}

	userRepo := repository.NewSysadminUserRepository(s.DB)
	target, err := userRepo.FindByID(uint(id))
	if err != nil || target == nil {
		return c.Redirect("/admin/sysadmins?flash=invalid_id", fiber.StatusSeeOther)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "could not hash password")
	}
	target.PasswordHash = string(hash)
	if err := userRepo.Save(target); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}
	// 强制登出：撤销目标账户所有活跃会话
	_ = repository.NewSysadminSessionRepository(s.DB).DeleteAllByUserID(target.ID)

	services.TryWriteAuditLog(s.DB, "admin.sysadmin.password_reset", "sysadmin_user", target.ID,
		self.Email,
		map[string]any{
			"target_email":     target.Email,
			"sessions_revoked": true,
			"actor_type":       "sysadmin",
		},
	)

	return c.Redirect("/admin/sysadmins?flash=password_reset", fiber.StatusSeeOther)
}

// ── 自助账户管理（/admin/account）───────────────────────────────────────────────

// handleAdminAccountGet 显示当前登录 SysAdmin 的账户页（自助修改密码）。
func (s *Server) handleAdminAccountGet(c *fiber.Ctx) error {
	return admintmpl.AdminAccount(admintmpl.AdminAccountVM{
		AdminEmail:      AdminUserFromCtx(c).Email,
		MaintenanceMode: IsMaintenanceMode(),
		Flash:           c.Query("flash"),
	}).Render(c.Context(), c)
}

// handleAdminAccountChangePassword 处理当前登录 SysAdmin 的密码修改请求。
// 要求提供当前密码（防止 CSRF 后的账户接管）；成功后不撤销当前会话（不强制重新登录）。
func (s *Server) handleAdminAccountChangePassword(c *fiber.Ctx) error {
	self := AdminUserFromCtx(c)
	currentPassword := c.FormValue("current_password")
	newPassword := strings.TrimSpace(c.FormValue("new_password"))
	confirm := strings.TrimSpace(c.FormValue("confirm_password"))

	vm := admintmpl.AdminAccountVM{
		AdminEmail:      self.Email,
		MaintenanceMode: IsMaintenanceMode(),
	}

	if err := bcrypt.CompareHashAndPassword([]byte(self.PasswordHash), []byte(currentPassword)); err != nil {
		vm.CurrentPasswordError = "Current password is incorrect."
	}
	if len(newPassword) < 8 {
		vm.NewPasswordError = "New password must be at least 8 characters."
	}
	if newPassword != confirm {
		vm.ConfirmError = "Passwords do not match."
	}

	if vm.CurrentPasswordError != "" || vm.NewPasswordError != "" || vm.ConfirmError != "" {
		return admintmpl.AdminAccount(vm).Render(c.Context(), c)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "could not hash password")
	}

	userRepo := repository.NewSysadminUserRepository(s.DB)
	self.PasswordHash = string(hash)
	if err := userRepo.Save(self); err != nil {
		vm.FormError = "Could not update password. Please try again."
		return admintmpl.AdminAccount(vm).Render(c.Context(), c)
	}
	if err := repository.NewSysadminSessionRepository(s.DB).DeleteAllByUserID(self.ID); err != nil {
		vm.FormError = "Could not revoke existing sessions. Please try again."
		return admintmpl.AdminAccount(vm).Render(c.Context(), c)
	}

	cookieVal, tokenHash, err := newAdminToken()
	if err != nil {
		vm.FormError = "Could not create a fresh session. Please try again."
		return admintmpl.AdminAccount(vm).Render(c.Context(), c)
	}
	sess := &models.SysadminSession{
		SysadminUserID: self.ID,
		TokenHash:      tokenHash,
		ExpiresAt:      time.Now().UTC().Add(8 * time.Hour),
		CreatedAt:      time.Now().UTC(),
	}
	if err := repository.NewSysadminSessionRepository(s.DB).Create(sess); err != nil {
		vm.FormError = "Could not create a fresh session. Please try again."
		return admintmpl.AdminAccount(vm).Render(c.Context(), c)
	}
	setAdminCookie(c, s.Cfg, cookieVal)

	services.TryWriteAuditLog(s.DB, "admin.sysadmin.password_changed", "sysadmin_user", self.ID,
		self.Email,
		map[string]any{
			"actor_type":       "sysadmin",
			"self":             true,
			"sessions_revoked": true,
		},
	)

	return c.Redirect("/admin/account?flash=password_changed", fiber.StatusSeeOther)
}

// ── 内部辅助 ─────────────────────────────────────────────────────────────────────

func (s *Server) buildSysadminRows(c *fiber.Ctx) ([]admintmpl.AdminSysadminRow, error) {
	userRepo := repository.NewSysadminUserRepository(s.DB)
	users, err := userRepo.ListAll()
	if err != nil {
		return nil, err
	}
	selfID := AdminUserFromCtx(c).ID
	rows := make([]admintmpl.AdminSysadminRow, 0, len(users))
	for _, u := range users {
		rows = append(rows, admintmpl.AdminSysadminRow{
			User:   u,
			IsSelf: u.ID == selfID,
		})
	}
	return rows, nil
}
