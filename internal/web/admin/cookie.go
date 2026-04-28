// 遵循project_guide.md
package admin

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/config"
)

// AdminCookieName 是系统管理员会话 cookie 的名称。
// 使用不同于业务用户（balanciz_session）的名称，防止令牌命名空间混用。
const AdminCookieName = "balanciz_admin_session"

// AdminCookieMaxAgeSec 是管理员 cookie 的浏览器存活时间（8 小时，短于业务用户 30 天）。
const AdminCookieMaxAgeSec = 8 * 3600

func setAdminCookie(c *fiber.Ctx, cfg config.Config, rawToken string) {
	sec := strings.EqualFold(cfg.Env, "production") || strings.EqualFold(cfg.Env, "prod")
	c.Cookie(&fiber.Cookie{
		Name:     AdminCookieName,
		Value:    rawToken,
		Path:     "/admin",
		HTTPOnly: true,
		SameSite: "Lax",
		Secure:   sec,
		MaxAge:   AdminCookieMaxAgeSec,
	})
}

func clearAdminCookie(c *fiber.Ctx, cfg config.Config) {
	sec := strings.EqualFold(cfg.Env, "production") || strings.EqualFold(cfg.Env, "prod")
	c.Cookie(&fiber.Cookie{
		Name:     AdminCookieName,
		Value:    "",
		Path:     "/admin",
		HTTPOnly: true,
		SameSite: "Lax",
		Secure:   sec,
		MaxAge:   -1,
	})
}
