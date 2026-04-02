// 遵循project_guide.md
package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"golang.org/x/crypto/bcrypt"

	"gobooks/internal/models"
	"gobooks/internal/repository"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/admintmpl"
)

// newAdminToken 生成 32 字节随机令牌并返回（cookieValue, tokenHash）。
func newAdminToken() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(raw), hex.EncodeToString(sum[:]), nil
}

// handleAdminLoginGet 显示管理员登录页。
// 特殊情况：若数据库中尚无系统管理员，则显示首次创建表单（类似业务侧 bootstrap）。
func (s *Server) handleAdminLoginGet(c *fiber.Ctx) error {
	if AdminUserFromCtx(c) != nil {
		return c.Redirect("/admin/dashboard", fiber.StatusSeeOther)
	}

	userRepo := repository.NewSysadminUserRepository(s.DB)
	count, err := userRepo.Count()
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}
	isBootstrap := count == 0 && strings.EqualFold(strings.TrimSpace(s.Cfg.Env), "dev")

	return admintmpl.AdminLogin(admintmpl.AdminLoginVM{
		IsBootstrap: isBootstrap,
	}).Render(c.Context(), c)
}

// handleAdminLoginPost 处理登录表单提交（支持首次 bootstrap 创建账号）。
func (s *Server) handleAdminLoginPost(c *fiber.Ctx) error {
	email := strings.TrimSpace(c.FormValue("email"))
	password := c.FormValue("password")
	confirmPassword := c.FormValue("confirm_password")

	userRepo := repository.NewSysadminUserRepository(s.DB)
	count, err := userRepo.Count()
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}
	isBootstrap := count == 0 && strings.EqualFold(strings.TrimSpace(s.Cfg.Env), "dev")

	vm := admintmpl.AdminLoginVM{
		IsBootstrap: isBootstrap,
		Email:       email,
	}

	// ── 输入验证 ─────────────────────────────────────────────────────────────
	if email == "" {
		vm.EmailError = "Email is required."
	}
	if password == "" {
		vm.PasswordError = "Password is required."
	}
	if isBootstrap && confirmPassword != password {
		vm.ConfirmError = "Passwords do not match."
	}
	if vm.EmailError != "" || vm.PasswordError != "" || vm.ConfirmError != "" {
		return admintmpl.AdminLogin(vm).Render(c.Context(), c)
	}

	// ── Bootstrap 路径：创建首个系统管理员 ──────────────────────────────────
	if isBootstrap {
		if len(password) < 8 {
			vm.PasswordError = "Password must be at least 8 characters."
			return admintmpl.AdminLogin(vm).Render(c.Context(), c)
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
			vm.FormError = "Could not create admin account. Please try again."
			return admintmpl.AdminLogin(vm).Render(c.Context(), c)
		}

		// 审计：首个系统管理员账号创建（bootstrap 路径，仅执行一次）
		services.TryWriteAuditLog(s.DB, "admin.sysadmin.created", "sysadmin_user", newUser.ID,
			email,
			map[string]any{
				"email":      email,
				"via":        "bootstrap",
				"actor_type": "sysadmin",
			},
		)

		return s.createAdminSessionAndRedirect(c, newUser)
	}

	// ── 普通登录路径 ──────────────────────────────────────────────────────
	blocked, err := services.CheckLoginThrottle(s.DB, nil, nil, c.IP())
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}
	if blocked.Blocked {
		services.RecordBlockedLogin(s.DB, nil, nil, c.IP(), c.Get("User-Agent"))
		vm.FormError = "Too many sign-in attempts. Try again in a few minutes."
		return admintmpl.AdminLogin(vm).Render(c.Context(), c)
	}

	user, err := userRepo.FindByEmail(email)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}
	userID := ""
	if user != nil {
		userID = strconv.FormatUint(uint64(user.ID), 10)
		blocked, err = services.CheckLoginThrottle(s.DB, nil, &userID, c.IP())
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "database error")
		}
		if blocked.Blocked {
			services.RecordBlockedLogin(s.DB, nil, &userID, c.IP(), c.Get("User-Agent"))
			vm.FormError = "Too many sign-in attempts. Try again in a few minutes."
			return admintmpl.AdminLogin(vm).Render(c.Context(), c)
		}
	}
	if user == nil || !user.IsActive {
		services.EvaluateLoginSecurity(s.DB, services.LoginSecurityContext{
			IPAddress: c.IP(),
			UserAgent: c.Get("User-Agent"),
			Success:   false,
		})
		vm.FormError = "Invalid email or password."
		return admintmpl.AdminLogin(vm).Render(c.Context(), c)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		services.EvaluateLoginSecurity(s.DB, services.LoginSecurityContext{
			UserID:    userID,
			UserEmail: user.Email,
			IPAddress: c.IP(),
			UserAgent: c.Get("User-Agent"),
			Success:   false,
		})
		vm.FormError = "Invalid email or password."
		return admintmpl.AdminLogin(vm).Render(c.Context(), c)
	}
	return s.createAdminSessionAndRedirect(c, user)
}

func (s *Server) createAdminSessionAndRedirect(c *fiber.Ctx, user *models.SysadminUser) error {
	cookieVal, tokenHash, err := newAdminToken()
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "could not create session")
	}
	sess := &models.SysadminSession{
		SysadminUserID: user.ID,
		TokenHash:      tokenHash,
		ExpiresAt:      time.Now().UTC().Add(8 * time.Hour),
		CreatedAt:      time.Now().UTC(),
	}
	sessRepo := repository.NewSysadminSessionRepository(s.DB)
	if err := sessRepo.Create(sess); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "could not create session")
	}
	services.EvaluateLoginSecurity(s.DB, services.LoginSecurityContext{
		UserID:    strconv.FormatUint(uint64(user.ID), 10),
		UserEmail: user.Email,
		IPAddress: c.IP(),
		UserAgent: c.Get("User-Agent"),
		Success:   true,
	})
	setAdminCookie(c, s.Cfg, cookieVal)
	return c.Redirect("/admin/dashboard", fiber.StatusSeeOther)
}

// handleAdminLogout 撤销当前管理员会话并清除 cookie。
func (s *Server) handleAdminLogout(c *fiber.Ctx) error {
	raw := c.Cookies(AdminCookieName)
	if raw != "" {
		if hash, ok := adminTokenHashFromCookie(raw); ok {
			_ = repository.NewSysadminSessionRepository(s.DB).DeleteByTokenHash(hash)
		}
	}
	clearAdminCookie(c, s.Cfg)
	return c.Redirect("/admin/login", fiber.StatusSeeOther)
}
