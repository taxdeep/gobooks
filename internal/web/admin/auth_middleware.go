// 遵循project_guide.md
package admin

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
	"balanciz/internal/repository"
)

// Locals 键（仅在 /admin/* 中有效，不与业务用户 Locals 冲突）
const (
	LocalsAdminSession = "balanciz_admin_session_obj"
	LocalsAdminUser    = "balanciz_admin_user_obj"
)

// AdminSessionFromCtx 从请求上下文取出当前 SysAdmin 会话。
func AdminSessionFromCtx(c *fiber.Ctx) *models.SysadminSession {
	v := c.Locals(LocalsAdminSession)
	if v == nil {
		return nil
	}
	s, _ := v.(*models.SysadminSession)
	return s
}

// AdminUserFromCtx 从请求上下文取出当前 SysAdmin 用户。
func AdminUserFromCtx(c *fiber.Ctx) *models.SysadminUser {
	v := c.Locals(LocalsAdminUser)
	if v == nil {
		return nil
	}
	u, _ := v.(*models.SysadminUser)
	return u
}

// adminTokenHashFromCookie 将 cookie 原始值转换为存储在 sysadmin_sessions 的 SHA-256 哈希。
func adminTokenHashFromCookie(cookieValue string) (string, bool) {
	raw, err := hex.DecodeString(cookieValue)
	if err != nil || len(raw) != 32 {
		return "", false
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), true
}

// LoadAdminSession 读取管理员 cookie，加载有效会话及用户，写入 Locals。
// 无效或过期的会话视为未登录（Locals 保持 nil）。
func (s *Server) LoadAdminSession() fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Locals(LocalsAdminSession, nil)
		c.Locals(LocalsAdminUser, nil)

		raw := c.Cookies(AdminCookieName)
		if raw == "" {
			return c.Next()
		}
		hash, ok := adminTokenHashFromCookie(raw)
		if !ok {
			return c.Next()
		}

		sessRepo := repository.NewSysadminSessionRepository(s.DB)
		sess, err := sessRepo.FindValid(hash)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "database error")
		}
		if sess == nil {
			return c.Next()
		}

		userRepo := repository.NewSysadminUserRepository(s.DB)
		user, err := userRepo.FindByID(sess.SysadminUserID)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "database error")
		}
		if user == nil || !user.IsActive {
			return c.Next()
		}

		c.Locals(LocalsAdminSession, sess)
		c.Locals(LocalsAdminUser, user)
		return c.Next()
	}
}

// RequireAdminAuth 在没有已认证管理员时重定向到 /admin/login。
func (s *Server) RequireAdminAuth() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if AdminUserFromCtx(c) == nil {
			return c.Redirect("/admin/login", fiber.StatusSeeOther)
		}
		return c.Next()
	}
}
