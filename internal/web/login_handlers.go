// 遵循project_guide.md
package web

import (
	"strconv"
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

	vm := pages.LoginViewModel{}
	if c.Query("reset") == "1" {
		vm.FormSuccess = "Password reset successfully. Sign in with your new password."
	}
	return pages.Login(vm).Render(c.Context(), c)
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

	lockout, err := services.CheckUserLoginLockout(s.DB, user, time.Now().UTC())
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}
	if lockout.Locked {
		services.RecordBlockedLogin(s.DB, activeCompanyID, &userID, c.IP(), c.Get("User-Agent"))
		vm.FormError = userLoginLockoutMessage(lockout)
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
		lockout, lockErr := services.RecordUserPasswordFailure(s.DB, user, time.Now().UTC())
		if lockErr != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "database error")
		}
		vm.FormError = userPasswordFailureMessage(lockout)
		return pages.Login(vm).Render(c.Context(), c)
	}

	if err := services.RecordUserPasswordSuccess(s.DB, user); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
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

func userPasswordFailureMessage(state services.UserLoginLockoutState) string {
	if state.Locked {
		return userLoginLockoutMessage(state)
	}
	if state.RemainingAttempts > 0 {
		return "Invalid email or password. " + strconv.Itoa(state.RemainingAttempts) + " attempt(s) remaining before this account is temporarily locked."
	}
	return "Invalid email or password."
}

func userLoginLockoutMessage(state services.UserLoginLockoutState) string {
	if state.Permanent {
		return "This account is permanently blocked because it reached the daily login lock limit. Contact a system administrator to unlock it."
	}
	if state.LockedUntil != nil {
		return "Too many incorrect password attempts. This account is locked until " + state.LockedUntil.Local().Format("2006-01-02 15:04") + "."
	}
	if state.RetryAfter > 0 {
		minutes := int(state.RetryAfter.Round(time.Minute) / time.Minute)
		if minutes < 1 {
			minutes = 1
		}
		return "Too many incorrect password attempts. Try again in " + strconv.Itoa(minutes) + " minute(s)."
	}
	return "Too many incorrect password attempts. Try again later."
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
