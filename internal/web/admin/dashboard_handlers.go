// 遵循产品需求 v1.0
package admin

import (
	"github.com/gofiber/fiber/v2"

	"gobooks/internal/models"
	"gobooks/internal/web/templates/admintmpl"
)

func (s *Server) handleAdminDashboard(c *fiber.Ctx) error {
	user := AdminUserFromCtx(c)

	// 统计基础数据
	var companyCount, activeCompanyCount, userCount int64
	s.DB.Model(&models.Company{}).Count(&companyCount)
	s.DB.Model(&models.Company{}).Where("is_active = true").Count(&activeCompanyCount)
	s.DB.Model(&models.User{}).Count(&userCount)

	// 最近 10 条审计日志（跨公司）
	var recentLogs []models.AuditLog
	s.DB.Order("created_at desc").Limit(10).Find(&recentLogs)

	sys := collectAdminSystemStats(s)

	return admintmpl.AdminDashboard(admintmpl.AdminDashboardVM{
		AdminEmail:         user.Email,
		CompanyCount:       int(companyCount),
		ActiveCompanyCount: int(activeCompanyCount),
		UserCount:          int(userCount),
		RecentAuditLogs:    recentLogs,
		MaintenanceMode:    IsMaintenanceMode(),
		SysCPU:             sys.formatCPU(),
		SysMemoryMB:        sys.formatMemoryMB(),
		SysDatabaseSize:    sys.DatabaseSize,
		SysStorageUsed:     sys.StorageUsedReadable,
	}).Render(c.Context(), c)
}
