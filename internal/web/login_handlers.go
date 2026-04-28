// 遵循project_guide.md
package web

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"balanciz/internal/models"
	"balanciz/internal/repository"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

// sessionFromCookie returns the current valid session, or nil if none.
func (s *Server) sessionFromCookie(c *fiber.Ctx) (*models.Session, error) {
	raw := c.Cookies(SessionCookieName)
	if raw == "" {
		return nil, nil
	}
	hash, err := TokenHashFromCookieValue(raw)
	if err != nil {
		return nil, nil
	}
	repo := repository.NewSessionRepository(s.DB)
	return repo.FindValidSessionByTokenHash(hash)
}

func (s *Server) handleLoginForm(c *fiber.Ctx) error {
	sess, err := s.sessionFromCookie(c)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}
	if sess != nil {
		return c.Redirect("/", fiber.StatusSeeOther)
	}

	return pages.Login(pages.LoginViewModel{}).Render(c.Context(), c)
}

func (s *Server) handleLoginPost(c *fiber.Ctx) error {
	email := strings.TrimSpace(c.FormValue("email"))
	password := c.FormValue("password")

	vm := pages.LoginViewModel{Email: email}

	if email == "" {
		vm.EmailError = "Email is required."
	}
	if password == "" {
		vm.PasswordError = "Password is required."
	}
	if vm.EmailError != "" || vm.PasswordError != "" {
		return pages.Login(vm).Render(c.Context(), c)
	}

	blocked, err := services.CheckLoginThrottle(s.DB, nil, nil, c.IP())
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}
	if blocked.Blocked {
		services.RecordBlockedLogin(s.DB, nil, nil, c.IP(), c.Get("User-Agent"))
		vm.FormError = "Too many sign-in attempts. Try again in a few minutes."
		return pages.Login(vm).Render(c.Context(), c)
	}

	userRepo := repository.NewUserRepository(s.DB)
	user, err := userRepo.FindUserByEmail(email)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}

	var activeCompanyID *uint
	var userID string
	if user != nil {
		activeCompanyID = firstMembershipCompanyID(s.DB, user.ID)
		userID = user.ID.String()
		blocked, err = services.CheckLoginThrottle(s.DB, activeCompanyID, &userID, c.IP())
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "database error")
		}
		if blocked.Blocked {
			services.RecordBlockedLogin(s.DB, activeCompanyID, &userID, c.IP(), c.Get("User-Agent"))
			vm.FormError = "Too many sign-in attempts. Try again in a few minutes."
			return pages.Login(vm).Render(c.Context(), c)
		}
	}

	if user == nil || !user.IsActive {
		services.EvaluateLoginSecurity(s.DB, services.LoginSecurityContext{
			IPAddress: c.IP(),
			UserAgent: c.Get("User-Agent"),
			Success:   false,
		})
		vm.FormError = "Invalid email or password."
		return pages.Login(vm).Render(c.Context(), c)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		services.EvaluateLoginSecurity(s.DB, services.LoginSecurityContext{
			CompanyID: activeCompanyID,
			UserID:    userID,
			UserEmail: user.Email,
			IPAddress: c.IP(),
			UserAgent: c.Get("User-Agent"),
			Success:   false,
		})
		vm.FormError = "Invalid email or password."
		return pages.Login(vm).Render(c.Context(), c)
	}

	cookieVal, tokenHash, err := NewOpaqueSessionToken()
	if err != nil {
		vm.FormError = "Could not create session. Please try again."
		return pages.Login(vm).Render(c.Context(), c)
	}

	expiresAt := time.Now().UTC().Add(30 * 24 * time.Hour)

	sess := &models.Session{
		TokenHash:       tokenHash,
		UserID:          user.ID,
		ActiveCompanyID: activeCompanyID,
		ExpiresAt:       expiresAt,
	}
	sessRepo := repository.NewSessionRepository(s.DB)
	if err := sessRepo.CreateSession(sess); err != nil {
		vm.FormError = "Could not create session. Please try again."
		return pages.Login(vm).Render(c.Context(), c)
	}

	userID = user.ID.String()
	services.EvaluateLoginSecurity(s.DB, services.LoginSecurityContext{
		CompanyID: activeCompanyID,
		UserID:    userID,
		UserEmail: user.Email,
		IPAddress: c.IP(),
		UserAgent: c.Get("User-Agent"),
		Success:   true,
	})

	setSessionCookie(c, s.Cfg, cookieVal, SessionCookieMaxAgeSec)

	if c.Get("HX-Request") == "true" {
		c.Set("HX-Redirect", "/")
		return c.SendStatus(fiber.StatusNoContent)
	}
	return c.Redirect("/", fiber.StatusSeeOther)
}

func (s *Server) handleLogoutPost(c *fiber.Ctx) error {
	sess, err := s.sessionFromCookie(c)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}
	if sess != nil {
		if err := repository.NewSessionRepository(s.DB).RevokeSession(sess.ID); err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "database error")
		}
	}
	clearSessionCookie(c, s.Cfg)

	if c.Get("HX-Request") == "true" {
		c.Set("HX-Redirect", "/login")
		return c.SendStatus(fiber.StatusNoContent)
	}
	return c.Redirect("/login", fiber.StatusSeeOther)
}

func firstMembershipCompanyID(db *gorm.DB, userID uuid.UUID) *uint {
	memRepo := repository.NewMembershipRepository(db)
	memberships, err := memRepo.ListMembershipsByUser(userID)
	if err != nil || len(memberships) == 0 {
		return nil
	}
	for _, m := range memberships {
		if m.IsActive {
			cid := m.CompanyID
			return &cid
		}
	}
	cid := memberships[0].CompanyID
	return &cid
}
