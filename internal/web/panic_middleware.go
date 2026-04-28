// 遵循project_guide.md
package web

import (
	"fmt"
	"runtime/debug"

	"balanciz/internal/logging"
	"balanciz/internal/models"
	"balanciz/internal/services"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PanicRecovery 替代 fiber 内置的 recover.New()。
// panic 发生时：
//  1. 用 slog 以 ERROR 级别记录 panic 信息和 goroutine 堆栈；
//  2. 将日志持久化到 system_logs 表（含 request_id、path、method、user/company 上下文）；
//  3. 向客户端返回 500 纯文本响应，避免暴露内部信息。
func PanicRecovery(db *gorm.DB) fiber.Handler {
	return func(c *fiber.Ctx) (retErr error) {
		defer func() {
			r := recover()
			if r == nil {
				return
			}

			stack := string(debug.Stack())
			msg := fmt.Sprintf("panic: %v", r)

			logging.L().Error("panic recovered",
				"message", msg,
				"stack", stack,
				"request_id", RequestIDFromCtx(c),
				"method", c.Method(),
				"path", c.Path(),
			)

			// 尽力提取业务上下文（中间件可能尚未运行完，允许为 nil）
			var companyID *uint
			var userID *uuid.UUID
			if m := MembershipFromCtx(c); m != nil {
				cid := m.CompanyID
				companyID = &cid
			}
			if u := UserFromCtx(c); u != nil {
				uid := u.ID
				userID = &uid
			}

			services.WriteSystemLog(db, models.SystemLog{
				Level:     "ERROR",
				Message:   msg,
				RequestID: RequestIDFromCtx(c),
				Path:      c.Path(),
				Method:    c.Method(),
				CompanyID: companyID,
				UserID:    userID,
				Stack:     stack,
			})

			c.Status(fiber.StatusInternalServerError)
			retErr = c.SendString("An unexpected error occurred. Please try again later.")
		}()
		return c.Next()
	}
}
