// 遵循project_guide.md
package web

import (
	"sort"
	"strconv"

	"github.com/gofiber/fiber/v2"

	"gobooks/internal/models"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/ui"
)

// LoadSidebarData is a Fiber middleware that resolves the company-switcher data
// for the current authenticated user and stores it in the Go request context via
// ui.WithSidebarData. layout.templ reads it with ui.SidebarDataFromCtx(ctx).
//
// Must be placed after LoadSession() + RequireAuth() in the middleware chain so
// that UserFromCtx and SessionFromCtx are already populated.
//
// The middleware is intentionally fault-tolerant: any DB error results in an
// empty SidebarData (no switcher shown) rather than a 500, because it is purely
// cosmetic data that must never block the primary request.
func (s *Server) LoadSidebarData() fiber.Handler {
	return func(c *fiber.Ctx) error {
		user := UserFromCtx(c)
		if user == nil {
			return c.Next()
		}

		sess := SessionFromCtx(c)
		var activeID uint
		if sess != nil && sess.ActiveCompanyID != nil {
			activeID = *sess.ActiveCompanyID
		}

		sd := s.buildSidebarData(user, activeID)

		// Store in both Fiber's request context (used by templates rendered
		// with c.Context()) and UserContext (used by service-style code).
		ui.AttachSidebarData(c.Context(), sd)
		c.SetUserContext(ui.WithSidebarData(c.UserContext(), sd))

		return c.Next()
	}
}

// buildSidebarData loads company + plan info for the sidebar switcher.
// Returns zero-value SidebarData on any error (cosmetic only — must not block requests).
func (s *Server) buildSidebarData(user *models.User, activeCompanyID uint) ui.SidebarData {
	// 1. Load plan name.
	var plan models.UserPlan
	planName := ""
	if err := s.DB.First(&plan, user.PlanID).Error; err == nil {
		planName = plan.Name
	}

	// 2. Load active company name.
	companyName := ""
	if activeCompanyID != 0 {
		var co models.Company
		if err := s.DB.Select("id, name").First(&co, activeCompanyID).Error; err == nil {
			companyName = co.Name
		}
	}

	// 3. Load number format preference (one SELECT, shared across all return paths).
	numFmt := services.GetUserNumberFormat(s.DB, user.ID)

	// 4. Load all memberships for this user to build the switcher list.
	var memberships []models.CompanyMembership
	if err := s.DB.Where("user_id = ? AND is_active = true", user.ID).
		Find(&memberships).Error; err != nil || len(memberships) == 0 {
		return ui.SidebarData{
			CompanyName:  companyName,
			PlanName:     planName,
			NumberFormat: numFmt,
		}
	}

	ids := make([]uint, 0, len(memberships))
	for _, m := range memberships {
		ids = append(ids, m.CompanyID)
	}

	var companies []models.Company
	if err := s.DB.Select("id, name").Where("id IN ?", ids).Find(&companies).Error; err != nil {
		return ui.SidebarData{
			CompanyName:  companyName,
			PlanName:     planName,
			NumberFormat: numFmt,
		}
	}

	byID := make(map[uint]string, len(companies))
	for _, co := range companies {
		byID[co.ID] = co.Name
	}

	rows := make([]ui.SwitcherRow, 0, len(memberships))
	for _, m := range memberships {
		name, ok := byID[m.CompanyID]
		if !ok {
			continue
		}
		rows = append(rows, ui.SwitcherRow{
			CompanyIDStr: strconv.FormatUint(uint64(m.CompanyID), 10),
			Name:         name,
			IsActive:     m.CompanyID == activeCompanyID,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	return ui.SidebarData{
		CompanyName:  companyName,
		PlanName:     planName,
		SwitcherRows: rows,
		NumberFormat: numFmt,
	}
}
