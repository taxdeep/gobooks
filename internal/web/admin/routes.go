// 遵循project_guide.md
package admin

import "github.com/gofiber/fiber/v2"

// RegisterRoutes 将所有 /admin/* 路由挂载到主 Fiber 应用。
//
// 路由组使用独立的 LoadAdminSession / RequireAdminAuth 中间件，
// 与业务用户的认证链（LoadSession / RequireAuth / RequireMembership）完全隔离。
func (s *Server) RegisterRoutes(app *fiber.App) {
	// ── 公开路由（不需要管理员认证）─────────────────────────────────────────
	app.Get("/admin/login", s.LoadAdminSession(), s.handleAdminLoginGet)
	app.Post("/admin/login", s.LoadAdminSession(), s.handleAdminLoginPost)
	app.Post("/admin/logout", s.LoadAdminSession(), s.handleAdminLogout)

	// ── 需要管理员认证的路由 ─────────────────────────────────────────────────
	auth := func(handlers ...fiber.Handler) []fiber.Handler {
		chain := []fiber.Handler{s.LoadAdminSession(), s.RequireAdminAuth()}
		return append(chain, handlers...)
	}

	// 仪表板
	app.Get("/admin/dashboard", auth(s.handleAdminDashboard)...)
	app.Get("/admin/system/stats", auth(s.handleAdminSystemStats)...)
	// /admin 根路径重定向到仪表板
	app.Get("/admin", auth(func(c *fiber.Ctx) error {
		return c.Redirect("/admin/dashboard", fiber.StatusSeeOther)
	})...)

	// 公司管理
	app.Get("/admin/companies", auth(s.handleAdminCompanies)...)
	app.Post("/admin/companies/:id/deactivate", auth(s.handleAdminCompanyDeactivate)...)
	app.Post("/admin/companies/:id/reactivate", auth(s.handleAdminCompanyReactivate)...)

	// 用户管理
	app.Get("/admin/users", auth(s.handleAdminUsers)...)
	app.Post("/admin/users/:id/deactivate", auth(s.handleAdminUserDeactivate)...)
	app.Post("/admin/users/:id/reactivate", auth(s.handleAdminUserReactivate)...)
	app.Post("/admin/users/:id/reset-password", auth(s.handleAdminUserResetPassword)...)
	app.Post("/admin/users/:id/change-plan", auth(s.handleAdminUserChangePlan)...)
	app.Post("/admin/users/:id/unlock-login", auth(s.handleAdminUserUnlockLogin)...)

	// Plan 管理（subscription tier CRUD）
	app.Get("/admin/plans", auth(s.handleAdminPlans)...)
	app.Get("/admin/plans/new", auth(s.handleAdminPlanNewGet)...)
	app.Post("/admin/plans/new", auth(s.handleAdminPlanNewPost)...)
	app.Get("/admin/plans/:id/edit", auth(s.handleAdminPlanEditGet)...)
	app.Post("/admin/plans/:id/edit", auth(s.handleAdminPlanEditPost)...)

	// 系统管理员账户管理
	app.Get("/admin/sysadmins", auth(s.handleAdminSysadmins)...)
	app.Post("/admin/sysadmins/new", auth(s.handleAdminSysadminCreate)...)
	app.Post("/admin/sysadmins/:id/deactivate", auth(s.handleAdminSysadminDeactivate)...)
	app.Post("/admin/sysadmins/:id/reactivate", auth(s.handleAdminSysadminReactivate)...)
	app.Post("/admin/sysadmins/:id/reset-password", auth(s.handleAdminSysadminResetPassword)...)

	// 自助账户管理（修改当前 SysAdmin 密码）
	app.Get("/admin/account", auth(s.handleAdminAccountGet)...)
	app.Post("/admin/account/change-password", auth(s.handleAdminAccountChangePassword)...)

	// 审计日志（跨公司视图）
	app.Get("/admin/audit-logs", auth(s.handleAdminAuditLog)...)

	// 运行时日志（system_logs 表，错误/警告持久化视图）
	app.Get("/admin/runtime-logs", auth(s.handleAdminRuntimeLogs)...)

	// 系统控制
	app.Get("/admin/system", auth(s.handleAdminSystem)...)
	app.Post("/admin/system/maintenance/enable", auth(s.handleAdminMaintenanceEnable)...)
	app.Post("/admin/system/maintenance/disable", auth(s.handleAdminMaintenanceDisable)...)
	app.Post("/admin/system/restart", auth(s.handleAdminRestartStub)...)
	app.Get("/admin/system/backups/:name", auth(s.handleAdminDatabaseBackupDownload)...)
	app.Post("/admin/system/database/backup", auth(s.handleAdminDatabaseBackup)...)
	app.Post("/admin/system/database/optimize", auth(s.handleAdminDatabaseOptimize)...)
	// Phase 5: rebuild the search_documents projection from canonical
	// business tables. Idempotent + safe under live traffic; runs async.
	app.Post("/admin/system/search-rebuild", auth(s.handleAdminSearchRebuild)...)

	// 系统设置：通知与安全
	app.Get("/admin/settings/notifications", auth(s.handleAdminNotificationsGet)...)
	app.Post("/admin/settings/notifications", auth(s.handleAdminNotificationsPost)...)
	app.Post("/admin/settings/notifications/test-email", auth(s.handleAdminNotificationsTestEmail)...)
	app.Post("/admin/settings/notifications/test-sms", auth(s.handleAdminNotificationsTestSMS)...)
	app.Get("/admin/settings/security", auth(s.handleAdminSecurityGet)...)
	app.Post("/admin/settings/security", auth(s.handleAdminSecurityPost)...)
}
