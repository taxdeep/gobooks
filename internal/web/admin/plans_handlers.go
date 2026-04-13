// 遵循project_guide.md
package admin

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"gobooks/internal/models"
	"gobooks/internal/web/templates/admintmpl"
)

// handleAdminPlans lists all user plans with their user counts.
func (s *Server) handleAdminPlans(c *fiber.Ctx) error {
	var plans []models.UserPlan
	if err := s.DB.Order("sort_order asc, id asc").Find(&plans).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}

	rows := make([]admintmpl.AdminPlanRow, 0, len(plans))
	for _, p := range plans {
		var cnt int64
		s.DB.Model(&models.User{}).Where("plan_id = ?", p.ID).Count(&cnt)
		rows = append(rows, admintmpl.AdminPlanRow{
			Plan:      p,
			UserCount: int(cnt),
		})
	}

	return admintmpl.AdminPlans(admintmpl.AdminPlansVM{
		AdminEmail:      AdminUserFromCtx(c).Email,
		MaintenanceMode: IsMaintenanceMode(),
		Plans:           rows,
		Flash:           c.Query("flash"),
	}).Render(c.Context(), c)
}

// handleAdminPlanNewGet renders the "New Plan" form.
func (s *Server) handleAdminPlanNewGet(c *fiber.Ctx) error {
	return admintmpl.AdminPlanForm(admintmpl.AdminPlanFormVM{
		AdminEmail:      AdminUserFromCtx(c).Email,
		MaintenanceMode: IsMaintenanceMode(),
		Plan: models.UserPlan{
			MaxOwnedCompanies:    3,
			MaxMembersPerCompany: 5,
			IsActive:             true,
			SortOrder:            10,
		},
		IsEdit: false,
	}).Render(c.Context(), c)
}

// handleAdminPlanNewPost creates a new UserPlan.
func (s *Server) handleAdminPlanNewPost(c *fiber.Ctx) error {
	plan, formErr := parsePlanForm(c)
	if formErr != "" {
		return admintmpl.AdminPlanForm(admintmpl.AdminPlanFormVM{
			AdminEmail:      AdminUserFromCtx(c).Email,
			MaintenanceMode: IsMaintenanceMode(),
			Plan:            plan,
			IsEdit:          false,
			FormError:       formErr,
		}).Render(c.Context(), c)
	}

	if err := s.DB.Create(&plan).Error; err != nil {
		return admintmpl.AdminPlanForm(admintmpl.AdminPlanFormVM{
			AdminEmail:      AdminUserFromCtx(c).Email,
			MaintenanceMode: IsMaintenanceMode(),
			Plan:            plan,
			IsEdit:          false,
			FormError:       "Could not create plan. Name may already be taken.",
		}).Render(c.Context(), c)
	}

	return c.Redirect("/admin/plans?flash=created", fiber.StatusSeeOther)
}

// handleAdminPlanEditGet renders the edit form for an existing plan.
func (s *Server) handleAdminPlanEditGet(c *fiber.Ctx) error {
	plan, err := loadPlan(s, c)
	if err != nil {
		return c.Redirect("/admin/plans", fiber.StatusSeeOther)
	}

	return admintmpl.AdminPlanForm(admintmpl.AdminPlanFormVM{
		AdminEmail:      AdminUserFromCtx(c).Email,
		MaintenanceMode: IsMaintenanceMode(),
		Plan:            plan,
		IsEdit:          true,
	}).Render(c.Context(), c)
}

// handleAdminPlanEditPost saves edits to an existing plan.
func (s *Server) handleAdminPlanEditPost(c *fiber.Ctx) error {
	existing, err := loadPlan(s, c)
	if err != nil {
		return c.Redirect("/admin/plans", fiber.StatusSeeOther)
	}

	updated, formErr := parsePlanForm(c)
	if formErr != "" {
		updated.ID = existing.ID
		return admintmpl.AdminPlanForm(admintmpl.AdminPlanFormVM{
			AdminEmail:      AdminUserFromCtx(c).Email,
			MaintenanceMode: IsMaintenanceMode(),
			Plan:            updated,
			IsEdit:          true,
			FormError:       formErr,
		}).Render(c.Context(), c)
	}

	updated.ID = existing.ID
	if err := s.DB.Model(&updated).Select(
		"Name", "MaxOwnedCompanies", "MaxMembersPerCompany", "IsActive", "SortOrder",
	).Updates(&updated).Error; err != nil {
		return admintmpl.AdminPlanForm(admintmpl.AdminPlanFormVM{
			AdminEmail:      AdminUserFromCtx(c).Email,
			MaintenanceMode: IsMaintenanceMode(),
			Plan:            updated,
			IsEdit:          true,
			FormError:       "Could not save changes. Name may already be taken.",
		}).Render(c.Context(), c)
	}

	return c.Redirect("/admin/plans?flash=updated", fiber.StatusSeeOther)
}

// ── helpers ──────────────────────────────────────────────────────────────────

func loadPlan(s *Server, c *fiber.Ctx) (models.UserPlan, error) {
	idStr := c.Params("id")
	id64, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id64 <= 0 {
		return models.UserPlan{}, fiber.NewError(fiber.StatusNotFound, "plan not found")
	}
	var plan models.UserPlan
	if err := s.DB.First(&plan, int(id64)).Error; err != nil {
		return models.UserPlan{}, fiber.NewError(fiber.StatusNotFound, "plan not found")
	}
	return plan, nil
}

func parsePlanForm(c *fiber.Ctx) (models.UserPlan, string) {
	name := strings.TrimSpace(c.FormValue("name"))
	if name == "" {
		return models.UserPlan{}, "Plan name is required."
	}

	maxOwned, err := strconv.Atoi(strings.TrimSpace(c.FormValue("max_owned_companies")))
	if err != nil || maxOwned < -1 {
		return models.UserPlan{}, "Max owned companies must be a number ≥ -1 (use -1 for unlimited)."
	}

	maxMembers, err := strconv.Atoi(strings.TrimSpace(c.FormValue("max_members_per_company")))
	if err != nil || maxMembers < -1 {
		return models.UserPlan{}, "Max members per company must be a number ≥ -1 (use -1 for unlimited)."
	}

	sortOrder, err := strconv.Atoi(strings.TrimSpace(c.FormValue("sort_order")))
	if err != nil || sortOrder < 0 {
		return models.UserPlan{}, "Sort order must be a non-negative number."
	}

	isActive := c.FormValue("is_active") == "true"

	return models.UserPlan{
		Name:                 name,
		MaxOwnedCompanies:    maxOwned,
		MaxMembersPerCompany: maxMembers,
		SortOrder:            sortOrder,
		IsActive:             isActive,
	}, ""
}
