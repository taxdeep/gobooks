// 遵循project_guide.md
package web

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

func (s *Server) handleMembersGet(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	var memberships []models.CompanyMembership
	if err := s.DB.Preload("User").Where("company_id = ? AND is_active = ?", companyID, true).Find(&memberships).Error; err != nil {
		return pages.Members(pages.MembersVM{
			HasCompany: true,
			Active:     "Members Settings",
			FormError:  "Could not load members.",
		}).Render(c.Context(), c)
	}

	sort.Slice(memberships, func(i, j int) bool {
		return strings.ToLower(memberships[i].User.Email) < strings.ToLower(memberships[j].User.Email)
	})

	memberRows := make([]pages.MemberRow, 0, len(memberships))
	for _, m := range memberships {
		memberRows = append(memberRows, pages.MemberRow{
			Email: m.User.Email,
			Role:  string(m.Role),
			Since: m.CreatedAt.Format("2006-01-02"),
		})
	}

	invs, err := services.ListPendingInvitationsForCompany(s.DB, companyID)
	if err != nil {
		return pages.Members(pages.MembersVM{
			HasCompany: true,
			Active:     "Members Settings",
			FormError:  "Could not load invitations.",
		}).Render(c.Context(), c)
	}

	now := time.Now()
	invRows := make([]pages.InvitationRow, 0, len(invs))
	for _, inv := range invs {
		by := inv.InvitedBy.Email
		if strings.TrimSpace(by) == "" {
			by = "—"
		}
		invRows = append(invRows, pages.InvitationRow{
			Email:     inv.Email,
			Role:      string(inv.Role),
			Expires:   inv.ExpiresAt.Format("2006-01-02 15:04"),
			InvitedBy: by,
			Created:   inv.CreatedAt.Format("2006-01-02"),
			IsExpired: now.After(inv.ExpiresAt),
		})
	}

	return pages.Members(pages.MembersVM{
		HasCompany:  true,
		Active:      "Members Settings",
		ReadOnly:    !OwnerOrAdminFromCtx(c),
		Members:     memberRows,
		Invitations: invRows,
		Created:     c.Query("created") == "1",
	}).Render(c.Context(), c)
}

func (s *Server) handleMembersInvitePost(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	if !OwnerOrAdminFromCtx(c) {
		return fiber.NewError(fiber.StatusForbidden, "Forbidden")
	}

	email := strings.TrimSpace(c.FormValue("email"))
	roleRaw := strings.TrimSpace(c.FormValue("role"))

	vm := pages.MembersVM{
		HasCompany: true,
		Active:     "Members Settings",
		ReadOnly:   false,
		Email:      email,
		Role:       roleRaw,
	}

	if email == "" {
		vm.EmailError = "Email is required."
	} else if !strings.Contains(email, "@") {
		vm.EmailError = "Enter a valid email address."
	}

	var role models.CompanyRole
	if roleRaw == "" {
		vm.RoleError = "Role is required."
	} else {
		var err error
		role, err = models.ParseCompanyRole(roleRaw)
		if err != nil {
			vm.RoleError = "Invalid role."
		}
	}

	if vm.EmailError != "" || vm.RoleError != "" {
		return s.renderMembersPageWithErrors(c, companyID, vm)
	}

	inv, _, err := services.CreateCompanyInvitation(s.DB, companyID, user.ID, email, role)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrInvitationDuplicate):
			vm.FormError = "A pending invitation already exists for this email."
		case errors.Is(err, services.ErrInvitationAlreadyMember):
			vm.FormError = "This user is already a member of the company."
		case errors.Is(err, services.ErrInvitationInvalidRole):
			vm.FormError = "Invitations cannot assign the owner role."
		default:
			vm.FormError = "Could not create invitation. Please try again."
		}
		return s.renderMembersPageWithErrors(c, companyID, vm)
	}

	cid := companyID
	uid := user.ID
	actor := user.Email
	if actor == "" {
		actor = "user"
	}
	services.TryWriteAuditLogWithContext(s.DB, "invitation.created", "company_invitation", 0, actor, map[string]any{
		"invitation_id": inv.ID.String(),
		"email":         inv.Email,
		"role":          string(inv.Role),
		"expires_at":    inv.ExpiresAt.Format(time.RFC3339),
		"company_id":    companyID,
	}, &cid, &uid)

	return c.Redirect("/settings/members?created=1", fiber.StatusSeeOther)
}

func (s *Server) renderMembersPageWithErrors(c *fiber.Ctx, companyID uint, vm pages.MembersVM) error {
	var memberships []models.CompanyMembership
	_ = s.DB.Preload("User").Where("company_id = ? AND is_active = ?", companyID, true).Find(&memberships).Error
	sort.Slice(memberships, func(i, j int) bool {
		if len(memberships) == 0 {
			return false
		}
		return strings.ToLower(memberships[i].User.Email) < strings.ToLower(memberships[j].User.Email)
	})

	memberRows := make([]pages.MemberRow, 0, len(memberships))
	for _, m := range memberships {
		memberRows = append(memberRows, pages.MemberRow{
			Email: m.User.Email,
			Role:  string(m.Role),
			Since: m.CreatedAt.Format("2006-01-02"),
		})
	}

	invs, _ := services.ListPendingInvitationsForCompany(s.DB, companyID)
	now := time.Now()
	invRows := make([]pages.InvitationRow, 0, len(invs))
	for _, inv := range invs {
		by := inv.InvitedBy.Email
		if strings.TrimSpace(by) == "" {
			by = "—"
		}
		invRows = append(invRows, pages.InvitationRow{
			Email:     inv.Email,
			Role:      string(inv.Role),
			Expires:   inv.ExpiresAt.Format("2006-01-02 15:04"),
			InvitedBy: by,
			Created:   inv.CreatedAt.Format("2006-01-02"),
			IsExpired: now.After(inv.ExpiresAt),
		})
	}

	vm.HasCompany = true
	vm.Active = "Members Settings"
	vm.Members = memberRows
	vm.Invitations = invRows
	vm.ReadOnly = !OwnerOrAdminFromCtx(c)

	return pages.Members(vm).Render(c.Context(), c)
}
