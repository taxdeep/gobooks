// 遵循project_guide.md
package web

import (
	"log/slog"
	"time"

	"balanciz/internal/logging"

	"github.com/gofiber/fiber/v2"
)

// RequestLogger 是基于 slog 的请求日志中间件，替代 fiber 内置的 logger.New()。
// 每个请求在响应完成后输出一条结构化 JSON 日志。
// 状态码 >= 500 → ERROR，>= 400 → WARN，其余 → INFO。
func RequestLogger() fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		latency := time.Since(start)

		status := c.Response().StatusCode()

		level := slog.LevelInfo
		if status >= 500 {
			level = slog.LevelError
		} else if status >= 400 {
			level = slog.LevelWarn
		}

		logging.L().Log(c.Context(), level, "request",
			"method", c.Method(),
			"path", c.Path(),
			"status", status,
			"latency_ms", latency.Milliseconds(),
			"request_id", RequestIDFromCtx(c),
			"ip", c.IP(),
		)
		return err
	}
}
