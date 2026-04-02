package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"strings"

	"github.com/gofiber/fiber/v2"

	"gobooks/internal/config"
)

const (
	CSRFCookieName = "gobooks_csrf"
	CSRFFormField  = "_csrf"
	CSRFHeaderName = "X-CSRF-Token"
)

func CSRFMiddleware(cfg config.Config) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ensureCSRFCookie(c, cfg)

		if csrfSkipped(c) {
			return c.Next()
		}

		cookieToken := strings.TrimSpace(c.Cookies(CSRFCookieName))
		requestToken := strings.TrimSpace(c.Get(CSRFHeaderName))
		if requestToken == "" {
			requestToken = strings.TrimSpace(c.FormValue(CSRFFormField))
		}

		if cookieToken == "" || requestToken == "" {
			return fiber.NewError(fiber.StatusForbidden, "CSRF token missing")
		}
		if subtle.ConstantTimeCompare([]byte(cookieToken), []byte(requestToken)) != 1 {
			return fiber.NewError(fiber.StatusForbidden, "CSRF token invalid")
		}

		return c.Next()
	}
}

func csrfSkipped(c *fiber.Ctx) bool {
	switch c.Method() {
	case fiber.MethodGet, fiber.MethodHead, fiber.MethodOptions:
		return true
	}

	switch c.Path() {
	case "/setup", "/setup/bootstrap":
		return true
	default:
		return false
	}
}

func ensureCSRFCookie(c *fiber.Ctx, cfg config.Config) {
	if strings.TrimSpace(c.Cookies(CSRFCookieName)) != "" {
		return
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return
	}

	secure := strings.EqualFold(cfg.Env, "production") || strings.EqualFold(cfg.Env, "prod")
	c.Cookie(&fiber.Cookie{
		Name:     CSRFCookieName,
		Value:    hex.EncodeToString(raw),
		Path:     "/",
		HTTPOnly: false,
		SameSite: "Lax",
		Secure:   secure,
	})
}
