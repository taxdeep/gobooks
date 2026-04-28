// 遵循project_guide.md
package web

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// LocalsRequestID 是存储请求 ID 的 Fiber Locals 键。
const LocalsRequestID = "balanciz_request_id"

// RequestID 中间件：为每个请求生成唯一 ID 并存入 Locals。
// 若请求头已携带 X-Request-ID，则复用（兼容反向代理下发的追踪 ID）。
// 响应头 X-Request-ID 始终写回，便于客户端关联日志。
func RequestID() fiber.Handler {
	return func(c *fiber.Ctx) error {
		id := c.Get("X-Request-ID")
		if id == "" {
			id = uuid.New().String()
		}
		c.Locals(LocalsRequestID, id)
		c.Set("X-Request-ID", id)
		return c.Next()
	}
}

// RequestIDFromCtx 从 Locals 中读取请求 ID（由 RequestID 中间件写入）。
func RequestIDFromCtx(c *fiber.Ctx) string {
	id, _ := c.Locals(LocalsRequestID).(string)
	return id
}
