// 遵循project_guide.md
// Package admin implements the SysAdmin subsystem.
//
// 认证流程与业务用户完全隔离：
//   - 独立的 cookie（balanciz_admin_session）
//   - 独立的数据库表（sysadmin_users, sysadmin_sessions）
//   - 独立的中间件（LoadAdminSession / RequireAdminAuth）
//   - 独立的 Locals 键（不与业务用户 Locals 冲突）
//
// 所有 /admin/* 路由均通过 RegisterRoutes 挂载到主 Fiber 应用。
package admin

import (
	"balanciz/internal/config"
	"balanciz/internal/searchprojection"

	"gorm.io/gorm"
)

// Server 持有 SysAdmin 处理器所需的依赖。
type Server struct {
	DB  *gorm.DB
	Cfg config.Config

	// SearchProjector drives the "Rebuild search index" admin button.
	// Always non-nil at runtime — the parent web.Server falls back to
	// a NoopProjector when ent client init fails, so handlers never have
	// to nil-check.
	SearchProjector searchprojection.Projector

	// searchRebuildState tracks the most recent search-index rebuild run
	// so the admin page can show "last run at X, processed Y rows".
	// Pointer so the same status struct is observable by both the
	// goroutine writer and the handler reader (rebuildState owns its own
	// mutex).
	searchRebuildState *rebuildState
}

// NewServer 创建 SysAdmin 服务实例，并从数据库加载持久化状态（维护模式）。
func NewServer(cfg config.Config, db *gorm.DB, projector searchprojection.Projector) *Server {
	s := &Server{
		Cfg:                cfg,
		DB:                 db,
		SearchProjector:    projector,
		searchRebuildState: newRebuildState(),
	}
	// 从 system_settings 表加载维护模式状态，确保重启后状态一致
	initMaintenanceMode(db)
	return s
}
