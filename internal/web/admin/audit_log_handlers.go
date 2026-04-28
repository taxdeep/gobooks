// 遵循project_guide.md
package admin

import (
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
	"balanciz/internal/web/templates/admintmpl"
)

const adminAuditPageSize = 50

// handleAdminAuditLog 展示跨公司审计日志（SysAdmin 视角，无公司过滤）。
func (s *Server) handleAdminAuditLog(c *fiber.Ctx) error {
	q := strings.TrimSpace(c.Query("q"))
	filterAction := c.Query("action")
	filterFrom := c.Query("from")
	filterTo := c.Query("to")
	page, _ := strconv.Atoi(c.Query("page"))
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * adminAuditPageSize

	base := s.DB.Model(&models.AuditLog{})
	// 跨公司：不加 company_id 过滤
	if q != "" {
		like := "%" + q + "%"
		base = base.Where("action ILIKE ? OR entity_type ILIKE ? OR details_json ILIKE ?", like, like, like)
	}
	if filterAction != "" {
		base = base.Where("action = ?", filterAction)
	}
	if filterFrom != "" {
		if t, err := time.Parse("2006-01-02", filterFrom); err == nil {
			base = base.Where("created_at >= ?", t)
		}
	}
	if filterTo != "" {
		if t, err := time.Parse("2006-01-02", filterTo); err == nil {
			base = base.Where("created_at < ?", t.AddDate(0, 0, 1))
		}
	}

	var total int64
	base.Count(&total)

	var rows []models.AuditLog
	base.Order("created_at desc, id desc").Offset(offset).Limit(adminAuditPageSize).Find(&rows)

	// 可选过滤器候选值（所有 action 种类）
	var actions []string
	s.DB.Model(&models.AuditLog{}).Distinct("action").Order("action").Pluck("action", &actions)

	companyNames := buildCompanyNameMap(s, rows)

	totalPages := int(total) / adminAuditPageSize
	if int(total)%adminAuditPageSize > 0 {
		totalPages++
	}

	return admintmpl.AdminAuditLog(admintmpl.AdminAuditLogVM{
		AdminEmail:      AdminUserFromCtx(c).Email,
		MaintenanceMode: IsMaintenanceMode(),
		Items:           rows,
		CompanyNames:    companyNames,
		Actions:         actions,
		FilterQ:         q,
		FilterAction:    filterAction,
		FilterFrom:      filterFrom,
		FilterTo:        filterTo,
		Page:            page,
		TotalCount:      int(total),
		HasPrev:         page > 1,
		HasNext:         page < totalPages,
		PrevPage:        page - 1,
		NextPage:        page + 1,
	}).Render(c.Context(), c)
}
