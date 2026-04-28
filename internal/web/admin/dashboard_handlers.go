// 遵循project_guide.md
package admin

import (
	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
	"balanciz/internal/web/templates/admintmpl"
)

// buildCompanyNameMap builds a companyID→name lookup from a slice of AuditLog rows.
// It issues at most one query (IN clause) for all distinct non-nil company IDs.
func buildCompanyNameMap(s *Server, logs []models.AuditLog) map[uint]string {
	seen := make(map[uint]struct{})
	for _, l := range logs {
		if l.CompanyID != nil {
			seen[*l.CompanyID] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	var companies []models.Company
	s.DB.Select("id, name").Where("id IN ?", ids).Find(&companies)
	m := make(map[uint]string, len(companies))
	for _, co := range companies {
		m[co.ID] = co.Name
	}
	return m
}

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

	// 为最近日志构建 companyID → name 映射
	companyNames := buildCompanyNameMap(s, recentLogs)

	sys := collectAdminSystemStats(s)

	return admintmpl.AdminDashboard(admintmpl.AdminDashboardVM{
		AdminEmail:         user.Email,
		CompanyCount:       int(companyCount),
		ActiveCompanyCount: int(activeCompanyCount),
		UserCount:          int(userCount),
		RecentAuditLogs:    recentLogs,
		CompanyNames:       companyNames,
		MaintenanceMode:    IsMaintenanceMode(),
		SysCPU:             sys.formatCPU(),
		SysMemoryMB:        sys.formatMemoryMB(),
		SysDatabaseSize:    sys.DatabaseSize,
		SysStorageUsed:     sys.StorageUsedReadable,
	}).Render(c.Context(), c)
}
