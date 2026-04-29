// 遵循project_guide.md
package web

import (
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"balanciz/internal/models"
	"balanciz/internal/repository"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

const forgotPasswordGenericSuccess = "If an active account exists for that email, a reset code has been sent."

func (s *Server) handleForgotPasswordGet(c *fiber.Ctx) error {
	vm := pages.ForgotPasswordViewModel{
		Email:       strings.TrimSpace(c.Query("email")),
		ChallengeID: strings.TrimSpace(c.Query("cid")),
		Step:        strings.TrimSpace(c.Query("step")),
	}
	if c.Query("sent") == "1" {
		vm.FormSuccess = forgotPasswordGenericSuccess
	}
	if c.Query("err") != "" {
		vm.FormError = c.Query("err")
	}
	if vm.Step == "reset" && (vm.ChallengeID == "" || vm.Email == "") {
		vm.Step = ""
		vm.FormError = "Invalid password reset link. Request a new code."
	}
	return pages.ForgotPassword(vm).Render(c.Context(), c)
}

func (s *Server) handleForgotPasswordPost(c *fiber.Ctx) error {
	email := strings.TrimSpace(strings.ToLower(c.FormValue("email")))
	if email == "" || !strings.Contains(email, "@") {
		return pages.ForgotPassword(pages.ForgotPasswordViewModel{
			Email:     email,
			FormError: "Enter a valid email address.",
		}).Render(c.Context(), c)
	}

	smtpCfg, ready, err := services.EffectiveSMTPSystem(s.DB)
	if err != nil || !ready {
		return pages.ForgotPassword(pages.ForgotPasswordViewModel{
			Email:     email,
			FormError: "Password reset email is not configured. Contact your system administrator.",
		}).Render(c.Context(), c)
	}

	user, err := repository.NewUserRepository(s.DB).FindUserByEmail(email)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}

	if user != nil && user.IsActive && user.PermanentlyLockedAt == nil {
		rawCode, challengeID, err := services.CreatePasswordResetChallenge(s.DB, user.ID)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "could not create reset challenge")
		}
		resetURL := forgotPasswordResetURL(c, s.Cfg.PublicBaseURL, email, challengeID)
		subject := "Balanciz password reset"
		body := "Your Balanciz password reset code is: " + rawCode + "\n\nUse this link to reset your password:\n" + resetURL + "\n\nThis code expires in 15 minutes. If you did not request this reset, you can ignore this email."
		if err := services.SendEmail(smtpCfg, user.Email, subject, body); err != nil {
			return pages.ForgotPassword(pages.ForgotPasswordViewModel{
				Email:     email,
				FormError: "Could not send the reset email. Please try again later.",
			}).Render(c.Context(), c)
		}
		services.TryWriteAuditLogWithContextDetails(s.DB,
			"user.password_reset.requested", "user", 0,
			user.Email, map[string]any{"source": "forgot_password"},
			nil, &user.ID, nil, nil,
		)
	}

	return c.Redirect("/forgot-password?sent=1&email="+url.QueryEscape(email), fiber.StatusSeeOther)
}

func (s *Server) handleForgotPasswordResetGet(c *fiber.Ctx) error {
	email := strings.TrimSpace(strings.ToLower(c.Query("email")))
	cid := strings.TrimSpace(c.Query("cid"))
	if email == "" || cid == "" {
		return c.Redirect("/forgot-password?err=Invalid+password+reset+link.+Request+a+new+code.", fiber.StatusSeeOther)
	}
	return pages.ForgotPassword(pages.ForgotPasswordViewModel{
		Email:       email,
		ChallengeID: cid,
		Step:        "reset",
	}).Render(c.Context(), c)
}

func (s *Server) handleForgotPasswordResetPost(c *fiber.Ctx) error {
	email := strings.TrimSpace(strings.ToLower(c.FormValue("email")))
	cidStr := strings.TrimSpace(c.FormValue("challenge_id"))
	rawCode := strings.TrimSpace(c.FormValue("code"))
	newPassword := c.FormValue("new_password")
	confirmPassword := c.FormValue("confirm_password")

	vm := pages.ForgotPasswordViewModel{Email: email, ChallengeID: cidStr, Step: "reset"}
	if len(newPassword) < 8 {
		vm.FormError = "New password must be at least 8 characters."
		return pages.ForgotPassword(vm).Render(c.Context(), c)
	}
	if newPassword != confirmPassword {
		vm.FormError = "Passwords do not match."
		return pages.ForgotPassword(vm).Render(c.Context(), c)
	}
	cid, err := uuid.Parse(cidStr)
	if err != nil {
		vm.FormError = "Invalid password reset request. Request a new code."
		return pages.ForgotPassword(vm).Render(c.Context(), c)
	}

	user, err := repository.NewUserRepository(s.DB).FindUserByEmail(email)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}
	if user == nil || !user.IsActive {
		vm.FormError = "Invalid or expired reset request. Request a new code."
		return pages.ForgotPassword(vm).Render(c.Context(), c)
	}
	if user.PermanentlyLockedAt != nil {
		vm.FormError = "This account is blocked. Contact a system administrator to unlock it."
		return pages.ForgotPassword(vm).Render(c.Context(), c)
	}

	ch, err := services.VerifyChallenge(s.DB, cid, user.ID, rawCode)
	if err != nil {
		vm.FormError = err.Error()
		return pages.ForgotPassword(vm).Render(c.Context(), c)
	}
	if ch.Type != models.VerifyChallengeTypePasswordReset {
		vm.FormError = "Invalid password reset request. Request a new code."
		return pages.ForgotPassword(vm).Render(c.Context(), c)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "could not update password")
	}
	now := time.Now().UTC()
	if err := s.DB.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"password_hash":                string(hash),
		"failed_login_attempts":        0,
		"login_locked_until":           nil,
		"login_lock_window_started_at": nil,
		"login_lock_count":             0,
		"login_lock_reason":            "",
		"updated_at":                   now,
	}).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "database error")
	}
	s.DB.Model(&models.Session{}).
		Where("user_id = ? AND revoked_at IS NULL", user.ID).
		Update("revoked_at", now)

	services.TryWriteAuditLogWithContextDetails(s.DB,
		"user.password_reset.completed", "user", 0,
		user.Email, map[string]any{"source": "forgot_password", "sessions_revoked": true},
		nil, &user.ID, nil, nil,
	)

	return c.Redirect("/login?reset=1", fiber.StatusSeeOther)
}

func forgotPasswordResetURL(c *fiber.Ctx, publicBaseURL, email string, challengeID uuid.UUID) string {
	base := strings.TrimRight(publicBaseURL, "/")
	if base == "" {
		base = c.Protocol() + "://" + c.Hostname()
	}
	return base + "/forgot-password/reset?cid=" + url.QueryEscape(challengeID.String()) + "&email=" + url.QueryEscape(email)
}
