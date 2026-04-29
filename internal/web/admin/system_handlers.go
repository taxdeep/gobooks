// 遵循project_guide.md
package admin

import (
	"github.com/gofiber/fiber/v2"

	"balanciz/internal/services"
	"balanciz/internal/web/templates/admintmpl"
)

// handleAdminSystem 显示系统控制页面（维护模式开关、重启 stub、
// Rebuild search index）。Snapshot of the rebuild status is read once
// per render — no polling, the operator refreshes the page if they want
// updated counts.
func (s *Server) handleAdminSystem(c *fiber.Ctx) error {
	running, lastTriggered, lastResult := s.searchRebuildState.Snapshot()
	vm := admintmpl.AdminSystemVM{
		AdminEmail:      AdminUserFromCtx(c).Email,
		MaintenanceMode: IsMaintenanceMode(),
		Flash:           c.Query("flash"),
		Database:        s.adminDatabaseMaintenanceVM(),
		SearchRebuild: admintmpl.AdminSearchRebuildVM{
			Available:     s.SearchProjector != nil,
			Running:       running,
			LastTriggered: lastTriggered,
		},
	}
	if lastResult != nil {
		vm.SearchRebuild.HasLastRun = true
		vm.SearchRebuild.LastStarted = lastResult.Started
		vm.SearchRebuild.LastCompleted = lastResult.Completed
		vm.SearchRebuild.LastTotalRows = lastResult.TotalRows()
		if err := lastResult.FirstErr(); err != nil {
			vm.SearchRebuild.LastErr = err.Error()
		}
		vm.SearchRebuild.LastFamilies = make([]admintmpl.AdminSearchRebuildFamilyVM, 0, len(lastResult.Families))
		for _, fr := range lastResult.Families {
			row := admintmpl.AdminSearchRebuildFamilyVM{
				Family:     string(fr.Family),
				Rows:       fr.Rows,
				DurationMs: fr.Duration.Milliseconds(),
			}
			if fr.Err != nil {
				row.Err = fr.Err.Error()
			}
			vm.SearchRebuild.LastFamilies = append(vm.SearchRebuild.LastFamilies, row)
		}
	}
	return admintmpl.AdminSystem(vm).Render(c.Context(), c)
}

// handleAdminMaintenanceEnable 开启维护模式（持久化到 DB 并更新内存缓存）。
func (s *Server) handleAdminMaintenanceEnable(c *fiber.Ctx) error {
	if err := s.setMaintenanceMode(true); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "could not persist maintenance mode")
	}

	services.TryWriteAuditLog(s.DB, "admin.system.maintenance_enabled", "system", 0,
		AdminUserFromCtx(c).Email,
		map[string]any{
			"maintenance_mode": true,
			"actor_type":       "sysadmin",
		},
	)

	return c.Redirect("/admin/system?flash=maintenance_on", fiber.StatusSeeOther)
}

// handleAdminMaintenanceDisable 关闭维护模式（持久化到 DB 并更新内存缓存）。
func (s *Server) handleAdminMaintenanceDisable(c *fiber.Ctx) error {
	if err := s.setMaintenanceMode(false); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "could not persist maintenance mode")
	}

	services.TryWriteAuditLog(s.DB, "admin.system.maintenance_disabled", "system", 0,
		AdminUserFromCtx(c).Email,
		map[string]any{
			"maintenance_mode": false,
			"actor_type":       "sysadmin",
		},
	)

	return c.Redirect("/admin/system?flash=maintenance_off", fiber.StatusSeeOther)
}

// handleAdminRestartStub 重启占位符（安全 stub，不执行任何危险操作）。
// 真实重启逻辑需在生产环境通过进程管理器（systemd / Docker）实现。
func (s *Server) handleAdminRestartStub(c *fiber.Ctx) error {
	services.TryWriteAuditLog(s.DB, "admin.system.restart_requested", "system", 0,
		AdminUserFromCtx(c).Email,
		map[string]any{
			"actor_type": "sysadmin",
			"note":       "stub only; actual restart handled by process manager",
		},
	)

	return c.Redirect("/admin/system?flash=restart_requested", fiber.StatusSeeOther)
}
