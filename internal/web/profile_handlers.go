// 遵循project_guide.md
package web

import (
	"net/url"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

// handleProfileGet renders GET /profile.
func (s *Server) handleProfileGet(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}
	// Derive hasCompany from the raw session (ResolveActiveCompany is not wired
	// on profile routes — it would 403 or redirect users with no/multiple memberships).
	sess := SessionFromCtx(c)
	hasCompany := sess != nil && sess.ActiveCompanyID != nil

	vm := pages.UserProfileVM{
		HasCompany:   hasCompany,
		CurrentEmail: user.Email,
		DisplayName:  user.DisplayName,
	}

	// Flash messages from redirects.
	switch c.Query("saved") {
	case "email":
		vm.FormSuccess = "Email address updated successfully."
	case "password":
		vm.FormSuccess = "Password updated successfully."
	}

	// Restore verify step state from query params (set by request handlers on redirect).
	if c.Query("email_step") == "verify" {
		vm.EmailStep = "verify"
		vm.EmailNewInput = c.Query("new_email")
		vm.EmailChallengeID = c.Query("cid")
	}
	if c.Query("pw_step") == "verify" {
		vm.PasswordStep = "verify"
		vm.PasswordChallengeID = c.Query("cid")
	}
	if c.Query("err") != "" {
		vm.FormError = c.Query("err")
	}

	return pages.UserProfile(vm).Render(c.Context(), c)
}

// handleRequestEmailChange handles POST /profile/request-email-change.
// Validates the new email, checks SMTP readiness, creates a challenge, and
// sends the code to the new address.
func (s *Server) handleRequestEmailChange(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}

	newEmail := strings.TrimSpace(c.FormValue("new_email"))
	if newEmail == "" {
		return c.Redirect("/profile?err=New+email+address+is+required.", fiber.StatusSeeOther)
	}
	if strings.EqualFold(newEmail, user.Email) {
		return c.Redirect("/profile?err=New+email+is+the+same+as+your+current+email.", fiber.StatusSeeOther)
	}

	// Check SMTP readiness. Use system resolver because profile is not company-scoped.
	smtpCfg, ready, err := services.EffectiveSMTPSystem(s.DB)
	if err != nil || !ready {
		return c.Redirect("/profile?err=Email+delivery+is+not+configured.+Contact+your+system+administrator.", fiber.StatusSeeOther)
	}

	rawCode, challengeID, err := services.CreateEmailChangeChallenge(s.DB, user.ID, newEmail)
	if err != nil {
		return c.Redirect("/profile?err=Could+not+create+verification+challenge.+Please+try+again.", fiber.StatusSeeOther)
	}

	subject := "Balanciz – email change verification"
	body := "Your Balanciz email change verification code is: " + rawCode + "\n\nThis code expires in 15 minutes. If you did not request this change, you can safely ignore this email."
	if sendErr := services.SendEmail(smtpCfg, newEmail, subject, body); sendErr != nil {
		return c.Redirect("/profile?err=Failed+to+send+verification+email.+Check+your+SMTP+settings.", fiber.StatusSeeOther)
	}

	return c.Redirect("/profile?email_step=verify&cid="+challengeID.String()+"&new_email="+url.QueryEscape(newEmail), fiber.StatusSeeOther)
}

// handleVerifyEmailChange handles POST /profile/verify-email-change.
// Verifies the submitted code and updates the user's email address.
func (s *Server) handleVerifyEmailChange(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}

	rawCode := strings.TrimSpace(c.FormValue("code"))
	cidStr := strings.TrimSpace(c.FormValue("challenge_id"))
	newEmailPassthrough := url.QueryEscape(strings.TrimSpace(c.FormValue("new_email")))

	cid, err := uuid.Parse(cidStr)
	if err != nil {
		return c.Redirect("/profile?err=Invalid+verification+request.", fiber.StatusSeeOther)
	}

	// ownerUserID is checked inside VerifyChallenge before any attempt is recorded.
	ch, err := services.VerifyChallenge(s.DB, cid, user.ID, rawCode)
	if err != nil {
		return c.Redirect("/profile?email_step=verify&cid="+cidStr+"&new_email="+newEmailPassthrough+"&err="+url.QueryEscape(err.Error()), fiber.StatusSeeOther)
	}
	if ch.Type != "email_change" || ch.NewEmail == "" {
		return c.Redirect("/profile?err=Invalid+verification+request.", fiber.StatusSeeOther)
	}

	if err := s.DB.Model(user).Update("email", ch.NewEmail).Error; err != nil {
		return c.Redirect("/profile?err=Could+not+update+email+address.+Please+try+again.", fiber.StatusSeeOther)
	}

	services.TryWriteAuditLogWithContextDetails(s.DB,
		"user.profile.email_changed", "user", 0,
		user.Email, map[string]any{"new_email": ch.NewEmail},
		nil, &user.ID, nil, nil,
	)

	return c.Redirect("/profile?saved=email", fiber.StatusSeeOther)
}

// handleRequestPasswordChange handles POST /profile/request-password-change.
// Verifies the current password, checks SMTP readiness, creates a challenge,
// and sends the code to the user's current email.
func (s *Server) handleRequestPasswordChange(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}

	currentPassword := c.FormValue("current_password")
	if currentPassword == "" {
		return c.Redirect("/profile?err=Current+password+is+required.", fiber.StatusSeeOther)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(currentPassword)); err != nil {
		return c.Redirect("/profile?err=Current+password+is+incorrect.", fiber.StatusSeeOther)
	}

	// Check SMTP readiness.
	smtpCfg, ready, err := services.EffectiveSMTPSystem(s.DB)
	if err != nil || !ready {
		return c.Redirect("/profile?err=Email+delivery+is+not+configured.+Contact+your+system+administrator.", fiber.StatusSeeOther)
	}

	rawCode, challengeID, err := services.CreatePasswordChangeChallenge(s.DB, user.ID)
	if err != nil {
		return c.Redirect("/profile?err=Could+not+create+verification+challenge.+Please+try+again.", fiber.StatusSeeOther)
	}

	subject := "Balanciz – password change verification"
	body := "Your Balanciz password change verification code is: " + rawCode + "\n\nThis code expires in 15 minutes. If you did not request this change, please secure your account immediately."
	if sendErr := services.SendEmail(smtpCfg, user.Email, subject, body); sendErr != nil {
		return c.Redirect("/profile?err=Failed+to+send+verification+email.+Check+your+SMTP+settings.", fiber.StatusSeeOther)
	}

	return c.Redirect("/profile?pw_step=verify&cid="+challengeID.String(), fiber.StatusSeeOther)
}

// handleVerifyPasswordChange handles POST /profile/verify-password-change.
// Verifies the submitted code and updates the user's password.
func (s *Server) handleVerifyPasswordChange(c *fiber.Ctx) error {
	user := UserFromCtx(c)
	if user == nil {
		return c.Redirect("/login", fiber.StatusSeeOther)
	}

	rawCode := strings.TrimSpace(c.FormValue("code"))
	cidStr := strings.TrimSpace(c.FormValue("challenge_id"))
	newPassword := c.FormValue("new_password")
	confirmPassword := c.FormValue("confirm_password")

	cid, err := uuid.Parse(cidStr)
	if err != nil {
		return c.Redirect("/profile?err=Invalid+verification+request.", fiber.StatusSeeOther)
	}

	if len(newPassword) < 8 {
		return c.Redirect("/profile?pw_step=verify&cid="+cidStr+"&err=New+password+must+be+at+least+8+characters.", fiber.StatusSeeOther)
	}
	if newPassword != confirmPassword {
		return c.Redirect("/profile?pw_step=verify&cid="+cidStr+"&err=Passwords+do+not+match.", fiber.StatusSeeOther)
	}

	// ownerUserID is checked inside VerifyChallenge before any attempt is recorded.
	ch, err := services.VerifyChallenge(s.DB, cid, user.ID, rawCode)
	if err != nil {
		return c.Redirect("/profile?pw_step=verify&cid="+cidStr+"&err="+url.QueryEscape(err.Error()), fiber.StatusSeeOther)
	}
	if ch.Type != "password_change" {
		return c.Redirect("/profile?err=Invalid+verification+request.", fiber.StatusSeeOther)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return c.Redirect("/profile?err=Could+not+update+password.+Please+try+again.", fiber.StatusSeeOther)
	}
	if err := s.DB.Model(user).Update("password_hash", string(hash)).Error; err != nil {
		return c.Redirect("/profile?err=Could+not+update+password.+Please+try+again.", fiber.StatusSeeOther)
	}

	services.TryWriteAuditLogWithContextDetails(s.DB,
		"user.profile.password_changed", "user", 0,
		user.Email, map[string]any{},
		nil, &user.ID, nil, nil,
	)

	return c.Redirect("/profile?saved=password", fiber.StatusSeeOther)
}
