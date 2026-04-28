// 遵循project_guide.md
package web

// Auth middleware (Fiber handlers) for session, user, company membership, and role checks.
//
// Typical per-route order:
//
//	LoadSession → RequireAuth → ResolveActiveCompany → RequireMembership → RequireRole(...)
//
// # Proposed route wiring (not applied globally yet — add per handler group in a later step)
//
// Read-only app pages (dashboard, reports, lists):
//
//	s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(), handler
//
// Mutations that must be restricted by role (example: owner or admin only):
//
//	s.LoadSession(), s.RequireAuth(), s.ResolveActiveCompany(), s.RequireMembership(),
//	  s.RequireRole(models.CompanyRoleOwner, models.CompanyRoleAdmin), handler
//
// Public / unauthenticated: bootstrap, login, static assets — no auth middleware.
// Login and logout: use LoadSession only where you need to read session (e.g. login GET).
//
// # Locals keys
//
// Use the Locals* constants and *FromCtx helpers below; do not scatter magic strings.

import (
	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
	"balanciz/internal/repository"
	"balanciz/internal/web/templates/ui"
)

// RequirePermission 允许请求继续，当且仅当当前成员角色能执行指定操作（依据 permissions.go 定义）。
// 必须在 RequireMembership 之后使用。
func (s *Server) RequirePermission(action string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		m := MembershipFromCtx(c)
		if m == nil {
			return c.Redirect("/select-company", fiber.StatusSeeOther)
		}
		if !CanPerformAction(string(m.Role), action) {
			return fiber.NewError(fiber.StatusForbidden, "Forbidden")
		}
		return c.Next()
	}
}

// Locals keys for Fiber c.Locals (auth pipeline).
const (
	LocalsSession           = "balanciz_auth_session"
	LocalsUser              = "balanciz_auth_user"
	LocalsActiveCompanyID   = "balanciz_auth_active_company_id"
	LocalsCompanyMembership = "balanciz_auth_company_membership"
)

// --- Context helpers (for use in handlers after middleware) ---

func SessionFromCtx(c *fiber.Ctx) *models.Session {
	v := c.Locals(LocalsSession)
	if v == nil {
		return nil
	}
	s, _ := v.(*models.Session)
	return s
}

func UserFromCtx(c *fiber.Ctx) *models.User {
	v := c.Locals(LocalsUser)
	if v == nil {
		return nil
	}
	u, _ := v.(*models.User)
	return u
}

// ActiveCompanyIDFromCtx returns the resolved active company id after ResolveActiveCompany.
func ActiveCompanyIDFromCtx(c *fiber.Ctx) (uint, bool) {
	v := c.Locals(LocalsActiveCompanyID)
	if v == nil {
		return 0, false
	}
	id, ok := v.(uint)
	if !ok {
		return 0, false
	}
	return id, true
}

func MembershipFromCtx(c *fiber.Ctx) *models.CompanyMembership {
	v := c.Locals(LocalsCompanyMembership)
	if v == nil {
		return nil
	}
	m, _ := v.(*models.CompanyMembership)
	return m
}

// --- Middleware ---

// LoadSession reads the auth cookie, loads a valid session row, and loads the current user.
// Invalid or expired sessions are treated as logged-out (Locals cleared).
func (s *Server) LoadSession() fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Locals(LocalsSession, nil)
		c.Locals(LocalsUser, nil)

		sess, err := s.sessionFromCookie(c)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "database error")
		}
		if sess == nil {
			return c.Next()
		}

		userRepo := repository.NewUserRepository(s.DB)
		user, err := userRepo.FindUserByID(sess.UserID)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "database error")
		}
		if user == nil || !user.IsActive {
			return c.Next()
		}

		c.Locals(LocalsSession, sess)
		c.Locals(LocalsUser, user)
		return c.Next()
	}
}

// RequireAuth redirects to /login when there is no authenticated user (after LoadSession).
func (s *Server) RequireAuth() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if UserFromCtx(c) == nil {
			return c.Redirect("/login", fiber.StatusSeeOther)
		}
		return c.Next()
	}
}

// ResolveActiveCompany sets the active company and membership in Locals.
//
// Rules:
//   - If session.active_company_id is set and the user has an active membership for it, use it.
//   - Otherwise, if the user has exactly one active membership, persist it on the session and use it.
//   - If the user has more than one active membership and none is resolved, redirect to /select-company.
//   - If the user has no active memberships, respond 403.
func (s *Server) ResolveActiveCompany() fiber.Handler {
	return func(c *fiber.Ctx) error {
		sess := SessionFromCtx(c)
		user := UserFromCtx(c)
		if sess == nil || user == nil {
			return c.Redirect("/login", fiber.StatusSeeOther)
		}

		memRepo := repository.NewMembershipRepository(s.DB)
		memberships, err := memRepo.ListMembershipsByUser(user.ID)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "database error")
		}
		active := filterActiveMemberships(memberships)

		var chosen *models.CompanyMembership

		if sess.ActiveCompanyID != nil {
			for i := range active {
				if active[i].CompanyID == *sess.ActiveCompanyID {
					chosen = &active[i]
					break
				}
			}
		}

		if chosen == nil && len(active) == 1 {
			chosen = &active[0]
			cid := chosen.CompanyID
			if err := s.DB.Model(&models.Session{}).Where("id = ?", sess.ID).Update("active_company_id", cid).Error; err != nil {
				return fiber.NewError(fiber.StatusInternalServerError, "database error")
			}
			sess.ActiveCompanyID = &cid
		}

		if chosen == nil {
			if len(active) == 0 {
				return c.Status(fiber.StatusForbidden).SendString("No company access.")
			}
			return c.Redirect("/select-company", fiber.StatusSeeOther)
		}

		// 检查公司是否仍处于活跃状态（SysAdmin 可将公司设为非活跃）
		var company models.Company
		if err := s.DB.Select("id, is_active").First(&company, chosen.CompanyID).Error; err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "database error")
		}
		if !company.IsActive {
			return c.Status(fiber.StatusForbidden).SendString("This company has been suspended. Please contact support.")
		}

		c.Locals(LocalsActiveCompanyID, chosen.CompanyID)
		c.Locals(LocalsCompanyMembership, chosen)

		// Inject sidebar switcher data into Go context so layout.templ can render
		// the company switcher without any page template needing to change.
		// Fault-tolerant: errors produce empty SidebarData (switcher just won't show).
		sd := s.buildSidebarData(user, chosen.CompanyID)
		ui.AttachSidebarData(c.Context(), sd)
		c.SetUserContext(ui.WithSidebarData(c.UserContext(), sd))

		return c.Next()
	}
}

// RequireMembership ensures ResolveActiveCompany ran successfully (membership in context).
// 额外规则：viewer 角色为只读，任何非 GET 请求一律返回 403。
// 这是一道安全兜底：即使某个变更路由遗漏了 RequirePermission，viewer 也无法写入。
func (s *Server) RequireMembership() fiber.Handler {
	return func(c *fiber.Ctx) error {
		m := MembershipFromCtx(c)
		if m == nil {
			return c.Redirect("/select-company", fiber.StatusSeeOther)
		}
		if m.Role == models.CompanyRoleViewer && c.Method() != fiber.MethodGet {
			return fiber.NewError(fiber.StatusForbidden, "Forbidden")
		}
		return c.Next()
	}
}

// RequireRole allows the request only if the current membership role is one of the given roles.
// If roles is empty, any resolved membership is allowed (same as RequireMembership).
func (s *Server) RequireRole(roles ...models.CompanyRole) fiber.Handler {
	return func(c *fiber.Ctx) error {
		m := MembershipFromCtx(c)
		if m == nil {
			return c.Redirect("/select-company", fiber.StatusSeeOther)
		}
		if len(roles) == 0 {
			return c.Next()
		}
		for _, r := range roles {
			if m.Role == r {
				return c.Next()
			}
		}
		return fiber.NewError(fiber.StatusForbidden, "Forbidden")
	}
}

func filterActiveMemberships(memberships []models.CompanyMembership) []models.CompanyMembership {
	out := make([]models.CompanyMembership, 0, len(memberships))
	for i := range memberships {
		if memberships[i].IsActive {
			out = append(out, memberships[i])
		}
	}
	return out
}
