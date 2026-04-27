// 遵循project_guide.md
package web

import (
	"sort"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"gobooks/internal/models"
	"gobooks/internal/repository"
	"gobooks/internal/services"
	"gobooks/internal/web/templates/pages"
)

func (s *Server) handleSelectCompanyGet(c *fiber.Ctx) error {
	sess := SessionFromCtx(c)
	user := UserFromCtx(c)
	if sess == nil || user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}

	rows, err := s.buildSelectCompanyRows(user.ID)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}
	if len(rows) == 0 {
		return c.Status(fiber.StatusForbidden).SendString("No company access.")
	}
	if len(rows) == 1 {
		cid := rows[0].CompanyID
		if err := s.DB.Model(&models.Session{}).Where("id = ?", sess.ID).Update("active_company_id", cid).Error; err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "database error")
		}
		return c.Redirect("/", fiber.StatusSeeOther)
	}

	return pages.SelectCompany(pages.SelectCompanyVM{Rows: rows}).Render(c.Context(), c)
}

func (s *Server) handleSelectCompanyPost(c *fiber.Ctx) error {
	sess := SessionFromCtx(c)
	user := UserFromCtx(c)
	if sess == nil || user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}

	raw := strings.TrimSpace(c.FormValue("company_id"))
	id64, err := services.ParseUint(raw)
	if err != nil || id64 == 0 {
		return s.renderSelectCompanyWithError(c, user.ID, "Choose a company to continue.")
	}
	companyID := uint(id64)

	if !s.userHasActiveMembership(user.ID, companyID) {
		return s.renderSelectCompanyWithError(c, user.ID, "That company is not available for your account.")
	}

	if err := s.DB.Model(&models.Session{}).Where("id = ?", sess.ID).Update("active_company_id", companyID).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}
	s.recordCompanySwitcherSelection(companyID, user, sess, c)

	if c.Get("HX-Request") == "true" {
		c.Set("HX-Redirect", "/")
		return c.SendStatus(fiber.StatusNoContent)
	}
	return c.Redirect("/", fiber.StatusSeeOther)
}

func (s *Server) renderSelectCompanyWithError(c *fiber.Ctx, userID uuid.UUID, msg string) error {
	rows, err := s.buildSelectCompanyRows(userID)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}
	if len(rows) == 0 {
		return c.Status(fiber.StatusForbidden).SendString("No company access.")
	}
	if len(rows) == 1 {
		sess := SessionFromCtx(c)
		if sess == nil {
			return c.Redirect("/login", fiber.StatusSeeOther)
		}
		cid := rows[0].CompanyID
		if err := s.DB.Model(&models.Session{}).Where("id = ?", sess.ID).Update("active_company_id", cid).Error; err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "database error")
		}
		return c.Redirect("/", fiber.StatusSeeOther)
	}
	return pages.SelectCompany(pages.SelectCompanyVM{
		Rows:      rows,
		FormError: msg,
	}).Render(c.Context(), c)
}

func (s *Server) buildSelectCompanyRows(userID uuid.UUID) ([]pages.SelectCompanyRowVM, error) {
	return s.buildSelectCompanyRowsFiltered(userID, "")
}

func (s *Server) buildSelectCompanyRowsFiltered(userID uuid.UUID, companyNameQuery string) ([]pages.SelectCompanyRowVM, error) {
	memRepo := repository.NewMembershipRepository(s.DB)
	memberships, err := memRepo.ListMembershipsByUser(userID)
	if err != nil {
		return nil, err
	}
	active := filterActiveMemberships(memberships)
	if len(active) == 0 {
		return nil, nil
	}

	ids := make([]uint, 0, len(active))
	for _, m := range active {
		ids = append(ids, m.CompanyID)
	}
	var companies []models.Company
	q := s.DB.Where("id IN ? AND is_active = true", ids)
	if strings.TrimSpace(companyNameQuery) != "" {
		q = applySmartPickerTextSearch(q, s.DB.Dialector.Name(), companyNameQuery, "name")
	}
	if err := q.Find(&companies).Error; err != nil {
		return nil, err
	}
	byID := make(map[uint]models.Company, len(companies))
	for _, co := range companies {
		byID[co.ID] = co
	}

	rows := make([]pages.SelectCompanyRowVM, 0, len(active))
	for _, m := range active {
		co, ok := byID[m.CompanyID]
		if !ok {
			continue
		}
		rows = append(rows, pages.SelectCompanyRowVM{
			CompanyID:    m.CompanyID,
			CompanyIDStr: strconv.FormatUint(uint64(m.CompanyID), 10),
			Name:         co.Name,
			RoleLabel:    string(m.Role),
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Name < rows[j].Name
	})
	return rows, nil
}

func (s *Server) recordCompanySwitcherSelection(companyID uint, user *models.User, sess *models.Session, c *fiber.Ctx) {
	if user == nil || sess == nil {
		return
	}
	userID := user.ID
	rankPosition := 1
	input := smartPickerUsageEventInput{
		EntityType:       "company",
		Context:          "company.switcher",
		EventType:        models.SmartPickerEventSelect,
		SelectedEntityID: strconv.FormatUint(uint64(companyID), 10),
		RankPosition:     &rankPosition,
		SourceRoute:      c.Get("Referer"),
	}
	if err := recordSmartPickerUsageEvent(s.DB, companyID, &userID, sess.ID.String(), input); err != nil {
		// Company switching is already validated above; learning telemetry must
		// never block the user's ability to change business context.
		return
	}
}

func (s *Server) userHasActiveMembership(userID uuid.UUID, companyID uint) bool {
	var count int64
	if err := s.DB.Model(&models.CompanyMembership{}).
		Joins("JOIN companies ON companies.id = company_memberships.company_id").
		Where("company_memberships.user_id = ? AND company_memberships.company_id = ? AND company_memberships.is_active = true AND companies.is_active = true", userID, companyID).
		Count(&count).Error; err != nil {
		return false
	}
	return count > 0
}
