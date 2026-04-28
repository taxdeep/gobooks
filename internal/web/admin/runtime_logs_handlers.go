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

const adminRuntimeLogPageSize = 50

// handleAdminRuntimeLogs 展示运行时结构化日志（system_logs 表）。
// 支持按日志级别、关键词、时间范围过滤，分页展示。
func (s *Server) handleAdminRuntimeLogs(c *fiber.Ctx) error {
	q := strings.TrimSpace(c.Query("q"))
	filterLevel := c.Query("level")
	filterFrom := c.Query("from")
	filterTo := c.Query("to")
	page, _ := strconv.Atoi(c.Query("page"))
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * adminRuntimeLogPageSize

	base := s.DB.Model(&models.SystemLog{})
	if q != "" {
		like := "%" + q + "%"
		base = base.Where("message ILIKE ? OR path ILIKE ? OR request_id ILIKE ?", like, like, like)
	}
	if filterLevel != "" {
		base = base.Where("level = ?", filterLevel)
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

	var rows []models.SystemLog
	base.Order("created_at desc, id desc").Offset(offset).Limit(adminRuntimeLogPageSize).Find(&rows)

	totalPages := int(total) / adminRuntimeLogPageSize
	if int(total)%adminRuntimeLogPageSize > 0 {
		totalPages++
	}

	return admintmpl.AdminRuntimeLogs(admintmpl.AdminRuntimeLogsVM{
		AdminEmail:      AdminUserFromCtx(c).Email,
		MaintenanceMode: IsMaintenanceMode(),
		Items:           rows,
		FilterQ:         q,
		FilterLevel:     filterLevel,
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
