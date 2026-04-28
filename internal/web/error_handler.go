// 遵循project_guide.md
package web

import (
	"errors"
	"log/slog"

	"balanciz/internal/logging"
	"balanciz/internal/models"
	"balanciz/internal/services"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// NewErrorHandler 返回 Fiber 的自定义错误处理器，替代默认实现。
//
// 行为：
//   - 5xx 错误：slog ERROR 记录 + 持久化到 system_logs 表
//   - 4xx 错误：slog WARN 记录（无 DB 写入，避免噪声）
//   - 5xx 向客户端返回通用消息，避免暴露内部错误文本
func NewErrorHandler(db *gorm.DB) fiber.ErrorHandler {
	return func(c *fiber.Ctx, err error) error {
		code := fiber.StatusInternalServerError
		var fe *fiber.Error
		if errors.As(err, &fe) {
			code = fe.Code
		}

		msg := err.Error()
		clientMsg := msg

		if code >= 500 {
			clientMsg = "Internal Server Error"
			logging.L().Error("server error",
				"status", code,
				"message", msg,
				"request_id", RequestIDFromCtx(c),
				"method", c.Method(),
				"path", c.Path(),
			)

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

			if db != nil {
				services.WriteSystemLog(db, models.SystemLog{
					Level:     "ERROR",
					Message:   msg,
					RequestID: RequestIDFromCtx(c),
					Path:      c.Path(),
					Method:    c.Method(),
					CompanyID: companyID,
					UserID:    userID,
				})
			}
		} else {
			logging.L().Log(c.Context(), slog.LevelWarn, "client error",
				"status", code,
				"message", msg,
				"request_id", RequestIDFromCtx(c),
				"method", c.Method(),
				"path", c.Path(),
			)
		}

		c.Status(code)
		return c.SendString(clientMsg)
	}
}
