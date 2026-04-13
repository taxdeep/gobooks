// 遵循project_guide.md
package web

import (
	"strconv"

	"github.com/gofiber/fiber/v2"

	"gobooks/internal/models"
	"gobooks/internal/web/templates/pages"
)

// handleCompaniesGet renders GET /companies — the user's company list.
// Does not require an active company in the session (same pattern as /profile).
func (s *Server) handleCompaniesGet(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}

	// Load the user with their plan preloaded.
	var fullUser models.User
	if err := s.DB.Preload("Plan").First(&fullUser, "id = ?", user.ID).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}

	// Active company from session (may be nil for users without a company yet).
	sess := SessionFromCtx(c)
	var activeID uint
	if sess != nil && sess.ActiveCompanyID != nil {
		activeID = *sess.ActiveCompanyID
	}

	// Reuse the existing helper that loads memberships + company names.
	selectRows, err := s.buildSelectCompanyRows(user.ID)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}

	rows := make([]pages.CompanyRowVM, 0, len(selectRows))
	for _, r := range selectRows {
		rows = append(rows, pages.CompanyRowVM{
			CompanyID:    r.CompanyID,
			CompanyIDStr: strconv.FormatUint(uint64(r.CompanyID), 10),
			Name:         r.Name,
			RoleLabel:    r.RoleLabel,
			IsActive:     r.CompanyID == activeID,
		})
	}

	return pages.Companies(pages.CompaniesVM{
		Rows:            rows,
		ActiveCompanyID: activeID,
		PlanName:        fullUser.Plan.Name,
	}).Render(c.Context(), c)
}
