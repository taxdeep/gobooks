// 遵循产品需求 v1.0
package admin

import (
	"strconv"

	"github.com/gofiber/fiber/v2"

	"gobooks/internal/models"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/admintmpl"
)

// handleAdminCompanies 列出所有公司（含停用公司）。
func (s *Server) handleAdminCompanies(c *fiber.Ctx) error {
	var companies []models.Company
	if err := s.DB.Order("id asc").Find(&companies).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}

	// 为每家公司统计成员数
	type companyRow struct {
		Company     models.Company
		MemberCount int
	}
	rows := make([]admintmpl.AdminCompanyRow, 0, len(companies))
	for _, co := range companies {
		var cnt int64
		s.DB.Model(&models.CompanyMembership{}).
			Where("company_id = ? AND is_active = true", co.ID).
			Count(&cnt)
		rows = append(rows, admintmpl.AdminCompanyRow{
			Company:     co,
			MemberCount: int(cnt),
		})
	}

	return admintmpl.AdminCompanies(admintmpl.AdminCompaniesVM{
		AdminEmail:      AdminUserFromCtx(c).Email,
		MaintenanceMode: IsMaintenanceMode(),
		Companies:       rows,
		Flash:           c.Query("flash"),
	}).Render(c.Context(), c)
}

// handleAdminCompanyDeactivate 软删除（停用）指定公司。
// 停用后该公司成员无法登录（ResolveActiveCompany 检查 is_active）。
func (s *Server) handleAdminCompanyDeactivate(c *fiber.Ctx) error {
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil || id == 0 {
		return c.Redirect("/admin/companies?flash=invalid_id", fiber.StatusSeeOther)
	}

	// 先加载公司名称，用于审计详情
	var company models.Company
	_ = s.DB.Select("id, name").First(&company, id).Error

	if err := s.DB.Model(&models.Company{}).Where("id = ?", id).
		Update("is_active", false).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}

	// 审计：SysAdmin 停用公司（含公司 ID 和名称，便于跨公司视图追溯）
	cid := uint(id)
	services.TryWriteAuditLogWithContext(s.DB, "admin.company.deactivated", "company", cid,
		AdminUserFromCtx(c).Email,
		map[string]any{
			"company_id":   id,
			"company_name": company.Name,
			"actor_type":   "sysadmin",
		},
		&cid, nil,
	)

	return c.Redirect("/admin/companies?flash=deactivated", fiber.StatusSeeOther)
}

// handleAdminCompanyReactivate 重新激活指定公司。
func (s *Server) handleAdminCompanyReactivate(c *fiber.Ctx) error {
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil || id == 0 {
		return c.Redirect("/admin/companies?flash=invalid_id", fiber.StatusSeeOther)
	}

	// 先加载公司名称，用于审计详情
	var company models.Company
	_ = s.DB.Select("id, name").First(&company, id).Error

	if err := s.DB.Model(&models.Company{}).Where("id = ?", id).
		Update("is_active", true).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}

	// 审计：SysAdmin 重新激活公司
	cid := uint(id)
	services.TryWriteAuditLogWithContext(s.DB, "admin.company.reactivated", "company", cid,
		AdminUserFromCtx(c).Email,
		map[string]any{
			"company_id":   id,
			"company_name": company.Name,
			"actor_type":   "sysadmin",
		},
		&cid, nil,
	)

	return c.Redirect("/admin/companies?flash=reactivated", fiber.StatusSeeOther)
}
