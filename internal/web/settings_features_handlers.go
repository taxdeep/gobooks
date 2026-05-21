// 遵循project_guide.md
package web

// settings_features_handlers.go — HTTP surface for the Features page
// under Settings → Company → Features.
//
// Three handlers:
//   GET  /setting/company/features              — render page
//   POST /setting/company/features/enable       — owner-only enable
//   POST /setting/company/features/disable      — owner-only disable
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
			Key:                 v.Key,
			Label:               v.Label,
			Maturity:            v.Maturity,
			Description:         v.Description,
			FitDescription:      v.FitDescription,
			SelfServeEnable:     v.SelfServeEnable,
			TypedConfirmText:    v.TypedConfirmText,
			AckVersion:          v.AckVersion,
			RequiredAcks:        v.RequiredAcks,
			BeforeEnableText:    featureBeforeEnableText(v.Key),
			BeforeEnableBullets: featureBeforeEnableBullets(v.Key),
			PermissionHint:      featurePermissionHint(v.Key),
			Status:              v.Status,
			EnabledAt:           v.EnabledAt,
			EnabledByUserID:     v.EnabledByUserID,
			ReasonCode:          v.ReasonCode,
			ReasonNote:          v.ReasonNote,
			ReasonCodeOptions:   reasonOpts,
		})
	}

	vm := pages.CompanyFeaturesVM{
		HasCompany: true,
		CanManage:  membership != nil && membership.Role == models.CompanyRoleOwner,
		Breadcrumb: breadcrumbSettingsCompanyFeatures(),
		Features:   cards,
		Flash:      companyFeaturesFlashFromQuery(c),
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
		required := featureRequiredAckCount(featureKey)
		for len(acks) < required {
			acks = append(acks, false)
		}
	}

	userID := user.ID
	in := services.EnableCompanyFeatureInput{
		CompanyID:               cid,
		FeatureKey:              featureKey,
		Actor:                   user.Email,
		ActorUserID:             &userID,
		ActorRole:               membership.Role,
		ReasonCode:              reasonCode,
		ReasonNote:              reasonNote,
		AckVersion:              ackVersion,
		TypedConfirmation:       typedConfirmation,
		ConfirmAcknowledgements: acks,
	}
	if err := services.EnableCompanyFeature(s.DB, in); err != nil {
		return redirectFeaturesWithFlash(c, "", featureEnableErrorMessage(err))
	}
	if featureHasMemberPermissions(featureKey) {
		return redirectFeaturesWithMemberCTA(c, string(featureKey)+" enabled.")
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
	u := "/setting/company/features"
	if success != "" {
		u += "?ok=" + escapeQueryValue(success)
	} else if errMsg != "" {
		u += "?err=" + escapeQueryValue(errMsg)
	}
	return c.Redirect(u, fiber.StatusSeeOther)
}

func redirectFeaturesWithMemberCTA(c *fiber.Ctx, success string) error {
	u := "/setting/company/features?ok=" + escapeQueryValue(success) + "&next=members"
	return c.Redirect(u, fiber.StatusSeeOther)
}

func companyFeaturesFlashFromQuery(c *fiber.Ctx) pages.CompanyFeaturesFlash {
	flash := pages.CompanyFeaturesFlash{
		Success: c.Query("ok"),
		Error:   c.Query("err"),
	}
	if flash.Success != "" && c.Query("next") == "members" {
		flash.ActionText = "Review who can access this module before the team starts using it."
		flash.ActionHref = "/settings/members"
		flash.ActionLabel = "Manage member permissions"
	}
	return flash
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

func featureRequiredAckCount(key models.FeatureKey) int {
	switch key {
	case models.FeatureKeyInventory:
		return len(models.InventoryAlphaRequiredAcknowledgements())
	}
	return 0
}

func featureBeforeEnableText(key models.FeatureKey) string {
	switch key {
	case models.FeatureKeyInventory:
		return "Before you enable Inventory:"
	case models.FeatureKeyTask:
		return "Before you enable Task:"
	case models.FeatureKeyEmployee:
		return "Before you enable Employee:"
	case models.FeatureKeyPayroll:
		return "Before you enable Payroll:"
	case models.FeatureKeyCheque:
		return "Before you enable Cheque:"
	default:
		return "Before you enable:"
	}
}

func featureBeforeEnableBullets(key models.FeatureKey) []string {
	switch key {
	case models.FeatureKeyInventory:
		return []string{
			"This feature is in Alpha and is still being validated.",
			"It is intended for businesses with physical inventory workflows, such as retail, wholesale, and light manufacturing.",
			"Enabling it changes how future inventory-related activity is processed.",
			"Disabling it later will not rewrite historical records.",
			"Some situations may still require manual reconciliation or operational support.",
		}
	case models.FeatureKeyTask:
		return []string{
			"Task adds work tracking, billable labor, linked expenses, and task-generated invoice drafts.",
			"Access is controlled separately from invoice permissions through Task view, create, update, and bill permissions.",
			"Task-generated invoice drafts remain linked to their source task records for traceability.",
			"Disabling it later hides new task workflows but does not rewrite historical tasks or invoices.",
		}
	case models.FeatureKeyEmployee:
		return []string{
			"Employee adds company-scoped employee records for payroll, cheque, and reporting workflows.",
			"Sensitive employee profile fields require a separate permission beyond basic employee view access.",
			"Search results respect Employee permissions so unauthorized members cannot discover employee records.",
			"Disabling it later hides employee workflows but does not delete existing employee records.",
		}
	case models.FeatureKeyPayroll:
		return []string{
			"Payroll adds payroll runs, payroll entries, remittances, exports, and posting workflows.",
			"Payroll detail access is separate from basic payroll run visibility to protect employee pay information.",
			"Global and advanced search only expose payroll detail entities to members with payroll detail permission.",
			"Disabling it later hides payroll workflows but does not rewrite finalized payroll history or journal entries.",
		}
	case models.FeatureKeyCheque:
		return []string{
			"Cheque adds controlled cheque creation, printing, bank-account setup, and void workflows.",
			"Cheque bank-account management and cheque printing are separate permissions.",
			"Cheque search visibility requires both the Cheque feature and cheque view permission.",
			"Disabling it later hides new cheque workflows but does not rewrite printed or voided cheque records.",
		}
	default:
		return []string{
			"Enabling this feature changes which workflows are available for this company.",
			"Disabling it later does not rewrite historical records.",
		}
	}
}

func featureHasMemberPermissions(key models.FeatureKey) bool {
	return featurePermissionHint(key) != ""
}

func featurePermissionHint(key models.FeatureKey) string {
	switch key {
	case models.FeatureKeyTask:
		return "Task access is controlled in Members with separate view, create, update, and bill permissions."
	case models.FeatureKeyEmployee:
		return "Employee access is controlled in Members, including a separate sensitive-data permission."
	case models.FeatureKeyPayroll:
		return "Payroll access is controlled in Members, with detail, run, finalize, export, and settings permissions split separately."
	case models.FeatureKeyCheque:
		return "Cheque access is controlled in Members, including separate print and bank-account management permissions."
	}
	return ""
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
