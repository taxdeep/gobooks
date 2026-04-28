// 遵循project_guide.md
package web

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/services"
)

// AIConnectEditableFromCtx 返回当前成员是否有权修改 AI Connect 设置。
// 依赖 ActionSettingsUpdate → PermManageSettings（owner / admin）。
func AIConnectEditableFromCtx(c *fiber.Ctx) bool {
	return CanFromCtx(c, ActionSettingsUpdate)
}

// aiConnectSaveErrorMessage maps save failures to a short UI hint; log the full error at the call site.
func aiConnectSaveErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, services.ErrAISecretKeyNotConfigured) {
		return "Could not save the API key: the server has no AI_SECRET_KEY. " +
			"Add a base64-encoded 32-byte key to your environment, restart Balanciz, then save again."
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "foreign key"):
		return "Could not save AI connection settings: the database rejected the company reference."
	case strings.Contains(msg, "duplicate key") || strings.Contains(msg, "unique constraint"):
		return "Could not save AI connection settings: a conflicting row already exists for this company."
	default:
		return "Could not save AI connection settings. Please try again. (Details were written to the server log.)"
	}
}

// OwnerOrAdminFromCtx 返回当前成员是否有成员管理权限（邀请 / 角色调整）。
// 依赖 ActionMemberManage → PermManageMembers（owner / admin）。
// 仅用于 UI 可见性判断；路由层的强制检查由 RequirePermission(ActionMemberManage) 负责。
func OwnerOrAdminFromCtx(c *fiber.Ctx) bool {
	return CanFromCtx(c, ActionMemberManage)
}
