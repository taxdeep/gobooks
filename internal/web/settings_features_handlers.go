// 遵循project_guide.md
package web

// settings_features_handlers.go — HTTP surface for the Features page
// under Settings → Company → Features.
//
// Three handlers:
//   GET  /settings/company/features              — render page
//   POST /settings/company/features/enable       — owner-only enable
//   POST /settings/company/features/disable      — owner-only disable
//
// The GET handler is open to any company member (matches the rest of
// Settings). The two POST handlers enforce owner role server-side
// even though the UI hides the buttons for non-owners — the prompt
// explicitly requires backend guard, not just UI hiding.
//
// Flash messages (success / error) ride through `?flash=...` query
// params on the redirect after POST. No cookies/session mutation.

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

// handleCompanyFeaturesGet renders the Features page. Any company
// member can read; only owners see the enable/disable affordances.
func (s *Server) handleCompanyFeaturesGet(c *fiber.Ctx) error {
	cid, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	membership := MembershipFromCtx(c)

	views, err := services.GetCompanyFeatures(s.DB, cid)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "load features: "+err.Error())
	}

	reasonOpts := reasonCodeOptions()
	cards := make([]pages.FeatureCardVM, 0, len(views))
	for _, v := range views {
		cards = append(cards, pages.FeatureCardVM{
			Key:               v.Key,
			Label:             v.Label,
			Maturity:          v.Maturity,
			Description:       v.Description,
			FitDescription:    v.FitDescription,
			SelfServeEnable:   v.SelfServeEnable,
			TypedConfirmText:  v.TypedConfirmText,
			AckVersion:        v.AckVersion,
			RequiredAcks:      v.RequiredAcks,
			Status:            v.Status,
			EnabledAt:         v.EnabledAt,
			EnabledByUserID:   v.EnabledByUserID,
			ReasonCode:        v.ReasonCode,
			ReasonNote:        v.ReasonNote,
			ReasonCodeOptions: reasonOpts,
		})
	}

	vm := pages.CompanyFeaturesVM{
		HasCompany: true,
		CanManage:  membership != nil && membership.Role == models.CompanyRoleOwner,
		Breadcrumb: breadcrumbSettingsCompanyFeatures(),
		Features:   cards,
		Flash: pages.CompanyFeaturesFlash{
			Success: c.Query("ok"),
			Error:   c.Query("err"),
		},
	}
	return pages.CompanyFeatures(vm).Render(c.Context(), c)
}

// handleCompanyFeatureEnable posts the multi-step enable form.
// Validation happens twice: once here for shape / owner gate /
// acknowledgement count (fast-fail with redirect + flash), and
// again deeper in services.EnableCompanyFeature for the authoritative
// typed-confirmation and ack-version check.
func (s *Server) handleCompanyFeatureEnable(c *fiber.Ctx) error {
	cid, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	user := UserFromCtx(c)
	membership := MembershipFromCtx(c)
	if user == nil || membership == nil {
		return fiber.NewError(fiber.StatusUnauthorized, "not authenticated")
	}
	if membership.Role != models.CompanyRoleOwner {
		return fiber.NewError(fiber.StatusForbidden, "only the company owner can enable features")
	}

	featureKey := models.FeatureKey(strings.TrimSpace(c.FormValue("feature_key")))
	reasonCode := models.ReasonCode(strings.TrimSpace(c.FormValue("reason_code")))
	reasonNote := strings.TrimSpace(c.FormValue("reason_note"))
	ackVersion := strings.TrimSpace(c.FormValue("ack_version"))
	typedConfirmation := c.FormValue("typed_confirmation")

	// Form sends multiple "ack" checkboxes, one per required ack.
	ackValues := c.Context().PostArgs().PeekMulti("ack")
	acks := make([]bool, 0, len(ackValues))
	for _, v := range ackValues {
		acks = append(acks, string(v) == "true")
	}
	// Pad to the required length so the service sees a length match
	// with all false when the user un-checked some boxes.
	def := models.LookupCompanyFeatureDefinition(featureKey)
	if def != nil {
		required := 0
		switch featureKey {
		case models.FeatureKeyInventory:
			required = len(models.InventoryAlphaRequiredAcknowledgements())
		}
		for len(acks) < required {
			acks = append(acks, false)
		}
	}

	userID := user.ID
	in := services.EnableCompanyFeatureInput{
		CompanyID:                cid,
		FeatureKey:               featureKey,
		Actor:                    user.Email,
		ActorUserID:              &userID,
		ActorRole:                membership.Role,
		ReasonCode:               reasonCode,
		ReasonNote:               reasonNote,
		AckVersion:               ackVersion,
		TypedConfirmation:        typedConfirmation,
		ConfirmAcknowledgements:  acks,
	}
	if err := services.EnableCompanyFeature(s.DB, in); err != nil {
		return redirectFeaturesWithFlash(c, "", featureEnableErrorMessage(err))
	}
	return redirectFeaturesWithFlash(c, string(featureKey)+" enabled.", "")
}

// handleCompanyFeatureDisable posts the lightweight disable form.
func (s *Server) handleCompanyFeatureDisable(c *fiber.Ctx) error {
	cid, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}
	user := UserFromCtx(c)
	membership := MembershipFromCtx(c)
	if user == nil || membership == nil {
		return fiber.NewError(fiber.StatusUnauthorized, "not authenticated")
	}
	if membership.Role != models.CompanyRoleOwner {
		return fiber.NewError(fiber.StatusForbidden, "only the company owner can disable features")
	}

	featureKey := models.FeatureKey(strings.TrimSpace(c.FormValue("feature_key")))
	userID := user.ID
	in := services.DisableCompanyFeatureInput{
		CompanyID:   cid,
		FeatureKey:  featureKey,
		Actor:       user.Email,
		ActorUserID: &userID,
		ActorRole:   membership.Role,
	}
	if err := services.DisableCompanyFeature(s.DB, in); err != nil {
		return redirectFeaturesWithFlash(c, "", featureDisableErrorMessage(err))
	}
	return redirectFeaturesWithFlash(c, string(featureKey)+" disabled. Historical records remain unchanged.", "")
}

// ── helpers ──────────────────────────────────────────────────────────────────

func redirectFeaturesWithFlash(c *fiber.Ctx, success, errMsg string) error {
	u := "/settings/company/features"
	if success != "" {
		u += "?ok=" + escapeQueryValue(success)
	} else if errMsg != "" {
		u += "?err=" + escapeQueryValue(errMsg)
	}
	return c.Redirect(u, fiber.StatusSeeOther)
}

// escapeQueryValue replaces the handful of bytes Fiber's query
// decoder flags as structural. Keep this minimal: runbook flashes
// are short phrases, never user-authored free text that needs full
// RFC 3986 escaping.
func escapeQueryValue(s string) string {
	s = strings.ReplaceAll(s, "%", "%25")
	s = strings.ReplaceAll(s, " ", "%20")
	s = strings.ReplaceAll(s, "&", "%26")
	s = strings.ReplaceAll(s, "=", "%3D")
	s = strings.ReplaceAll(s, "#", "%23")
	return s
}

func reasonCodeOptions() []pages.ReasonCodeOption {
	codes := models.AllReasonCodes()
	out := make([]pages.ReasonCodeOption, 0, len(codes))
	for _, c := range codes {
		out = append(out, pages.ReasonCodeOption{
			Value: string(c),
			Label: models.ReasonCodeLabel(c),
		})
	}
	return out
}

// featureEnableErrorMessage maps service sentinels to the user-
// facing flash message shown after redirect.
func featureEnableErrorMessage(err error) string {
	switch {
	case errors.Is(err, services.ErrFeatureOwnerRequired):
		return "Only the company owner can enable this feature."
	case errors.Is(err, services.ErrFeatureUnknown):
		return "Unknown feature."
	case errors.Is(err, services.ErrFeatureNotSelfServe):
		return "This feature is not available for self-serve enablement yet."
	case errors.Is(err, services.ErrFeatureTypedConfirmationMismatch):
		return "Typed confirmation did not match. Please type it exactly as shown."
	case errors.Is(err, services.ErrFeatureAcknowledgementsIncomplete):
		return "All acknowledgements must be confirmed before enablement."
	case errors.Is(err, services.ErrFeatureReasonCodeInvalid):
		return "Please pick a reason from the list."
	}
	return "Unable to enable this feature. " + err.Error()
}

func featureDisableErrorMessage(err error) string {
	switch {
	case errors.Is(err, services.ErrFeatureOwnerRequired):
		return "Only the company owner can disable this feature."
	case errors.Is(err, services.ErrFeatureUnknown):
		return "Unknown feature."
	}
	return "Unable to disable this feature. " + err.Error()
}
