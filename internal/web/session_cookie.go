// 遵循project_guide.md
package web

import (
	"github.com/gofiber/fiber/v2"

	"gobooks/internal/config"
)

// SessionCookieName is the HTTP-only cookie holding the opaque session token.
const SessionCookieName = "gobooks_session"

// SessionCookieMaxAgeSec is the default browser cookie lifetime (30 days).
const SessionCookieMaxAgeSec = 30 * 24 * 3600

// isHTTPS returns true when the request arrived over HTTPS, detected via the
// X-Forwarded-Proto header set by Nginx or by the raw connection protocol.
// This allows HTTP-only installs to work while automatically enforcing Secure
// cookies once HTTPS (certbot) is in place.
func isHTTPS(c *fiber.Ctx) bool {
	proto := c.Get("X-Forwarded-Proto")
	if proto == "" {
		proto = c.Protocol()
	}
	return proto == "https"
}

// setSessionCookie stores the opaque session token in a browser cookie.
func setSessionCookie(c *fiber.Ctx, _ config.Config, rawToken string, maxAgeSeconds int) {
	c.Cookie(&fiber.Cookie{
		Name:     SessionCookieName,
		Value:    rawToken,
		Path:     "/",
		HTTPOnly: true,
		SameSite: "Lax",
		Secure:   isHTTPS(c),
		MaxAge:   maxAgeSeconds,
	})
}

// clearSessionCookie removes the session cookie from the browser.
func clearSessionCookie(c *fiber.Ctx, _ config.Config) {
	c.Cookie(&fiber.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HTTPOnly: true,
		SameSite: "Lax",
		Secure:   isHTTPS(c),
		MaxAge:   -1,
	})
}
